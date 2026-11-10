package pg

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

// Insight actions (ADR 0069): turning on query stats, resetting them, and
// cancelling a query or ending a session. They run as the owner on one
// uncached connection, as setup does: the app's sessions often run as a
// superuser, which pg_signal_backend can't signal, and creating an
// extension or resetting its stats needs the owner anyway.

// terminateWait is how long ending a session waits for it to go.
const terminateWait = 5 * time.Second

// pgssState reads whether the server loads pg_stat_statements and in
// which schema its extension is ("" when it isn't created).
func pgssState(ctx context.Context, pg *pgx.Conn) (loaded bool, schema string, err error) {
	var preload string
	var sch *string
	err = pg.QueryRow(ctx, `SELECT current_setting('shared_preload_libraries'),
  (SELECT n.nspname FROM pg_extension x JOIN pg_namespace n ON n.oid = x.extnamespace WHERE x.extname = 'pg_stat_statements')`).Scan(&preload, &sch)
	if err != nil {
		return false, "", err
	}
	st := dbStats{preload: preload, pgssSchema: sch}
	if sch != nil {
		schema = *sch
	}
	return st.queryStats() != apitypes.QueryStatsNotLoaded, schema, nil
}

// EnableQueryStats creates pg_stat_statements in the database (in its
// default schema) when the server loads it.
func (e *Engine) EnableQueryStats(ctx context.Context, t data.Target, owner data.Credentials) (apitypes.DataQueryStatsState, bool, error) {
	pg, _, err := e.ownerConn(ctx, t, owner)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = pg.Close(context.Background()) }()
	loaded, schema, err := pgssState(ctx, pg)
	switch {
	case err != nil:
		return "", false, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	case !loaded:
		return apitypes.QueryStatsNotLoaded, false, nil
	case schema != "":
		return apitypes.QueryStatsOn, false, nil
	}
	if err := simpleExec(ctx, pg, "CREATE EXTENSION IF NOT EXISTS pg_stat_statements"); err != nil {
		return apitypes.QueryStatsNoExtension, false, fmt.Errorf("create pg_stat_statements as %s: %w", owner.User, dbError(err))
	}
	return apitypes.QueryStatsOn, true, nil
}

// ResetQueryStats calls pg_stat_statements_reset() as the owner.
func (e *Engine) ResetQueryStats(ctx context.Context, t data.Target, owner data.Credentials) error {
	pg, _, err := e.ownerConn(ctx, t, owner)
	if err != nil {
		return err
	}
	defer func() { _ = pg.Close(context.Background()) }()
	loaded, schema, err := pgssState(ctx, pg)
	switch {
	case err != nil:
		return fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	case !loaded || schema == "":
		return fmt.Errorf("%w: query stats are off, so there is nothing to reset", data.ErrInvalid)
	}
	if err := simpleExec(ctx, pg, "SELECT "+pgx.Identifier{schema}.Sanitize()+".pg_stat_statements_reset()"); err != nil {
		return fmt.Errorf("reset query stats as %s: %w", owner.User, dbError(err))
	}
	return nil
}

// cancelSQL signals one client session of this database, matched by its
// process ID and backend start (so a reused process ID is never hit) and,
// when given, its query's start (so a later query is never cancelled).
const cancelSQL = `SELECT CASE WHEN $4 THEN pg_terminate_backend(pid, $5) ELSE pg_cancel_backend(pid) END,
  coalesce(usename, ''), coalesce(application_name, ''), left(coalesce(query, ''), ` + textMax + `),
  extract(epoch FROM now() - coalesce(CASE WHEN $4 THEN xact_start END, query_start, backend_start))::float8
FROM pg_stat_activity
WHERE pid = $1 AND backend_start = $2 AND ($3::timestamptz IS NULL OR query_start = $3)
  AND datname = current_database() AND backend_type = 'client backend' AND pid <> pg_backend_pid()`

// Cancel cancels a session's query or ends the session, as the owner.
func (e *Engine) Cancel(ctx context.Context, t data.Target, owner data.Credentials, session string, queryStartedAt *time.Time, terminate bool) (data.CancelResult, error) {
	pid, start, err := parseSessionID(session)
	if err != nil {
		return data.CancelResult{}, err
	}
	pg, _, err := e.ownerConn(ctx, t, owner)
	if err != nil {
		return data.CancelResult{}, err
	}
	defer func() { _ = pg.Close(context.Background()) }()
	var res data.CancelResult
	var signalled bool
	var secs float64
	err = pg.QueryRow(ctx, cancelSQL, pid, start, queryStartedAt, terminate, terminateWait.Milliseconds()).
		Scan(&signalled, &res.User, &res.Application, &res.Text, &secs)
	res.Running = time.Duration(secs * float64(time.Second))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return res, nil
	case err != nil:
		return res, fmt.Errorf("signal the session as %s: %w", owner.User, dbError(err))
	}
	res.Found = signalled
	return res, nil
}

// parseSessionID reads a sessionID: "pid.backend start in microseconds".
func parseSessionID(id string) (int64, time.Time, error) {
	p, s, ok := strings.Cut(id, ".")
	pid, perr := strconv.ParseInt(p, 10, 32)
	micros, serr := strconv.ParseInt(s, 10, 64)
	if !ok || perr != nil || serr != nil || pid <= 0 {
		return 0, time.Time{}, fmt.Errorf("%w: %q is not a session ID; take one from the sessions view", data.ErrInvalid, id)
	}
	return pid, time.UnixMicro(micros), nil
}
