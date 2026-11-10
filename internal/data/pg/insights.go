package pg

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/conncache"
)

// Insights (ADR 0069): fixed, read-only statistics queries as
// the monitor role. They are cheap on a busy server: each runs under
// insightTimeout and insightLockTimeout, caps its rows in SQL, never calls
// pg_blocking_pids for sessions that aren't waiting on a lock, and skips
// size functions for relations an exclusive lock is held or queued on
// (they would wait in that lock's queue and hold up everyone behind).

const (
	// insightTimeout is statement_timeout of insight reads.
	insightTimeout = 2 * time.Second
	// insightLockTimeout is their lock_timeout: a size function that still
	// meets a lock gives up quickly instead of queueing.
	insightLockTimeout = 200 * time.Millisecond
	// monitorStatementTimeout caps the monitor role's statements (ALTER
	// ROLE ... SET), in case a call ever runs outside insightTimeout.
	monitorStatementTimeout = 5 * time.Second
	// insufficientPrivilege is what Postgres shows for another role's
	// statement text to a role without pg_read_all_stats.
	insufficientPrivilege = "<insufficient privilege>"
)

// pressureSQL counts connections against the limit. The monitor's own
// backend counts as used but not in the states or the oldest transaction.
const pressureSQL = `SELECT count(*) FILTER (WHERE backend_type = 'client backend'),
  current_setting('max_connections')::int,
  current_setting('superuser_reserved_connections')::int + coalesce(current_setting('reserved_connections', true)::int, 0),
  count(*) FILTER (WHERE backend_type = 'client backend' AND pid <> pg_backend_pid() AND state = 'active'),
  count(*) FILTER (WHERE backend_type = 'client backend' AND state = 'idle'),
  count(*) FILTER (WHERE backend_type = 'client backend' AND state IN ('idle in transaction', 'idle in transaction (aborted)')),
  count(*) FILTER (WHERE wait_event_type = 'Lock'),
  min(xact_start) FILTER (WHERE backend_type = 'client backend' AND pid <> pg_backend_pid())
FROM pg_stat_activity`

// statsSQL reads the database's counters and whether pg_stat_statements
// is loaded and created (and in which schema).
const statsSQL = `SELECT d.blks_hit, d.blks_read, d.deadlocks, d.temp_bytes, d.stats_reset, pg_postmaster_start_time(),
  age(db.datfrozenxid)::int8, current_setting('shared_preload_libraries'),
  (SELECT n.nspname FROM pg_extension x JOIN pg_namespace n ON n.oid = x.extnamespace WHERE x.extname = 'pg_stat_statements')
FROM pg_stat_database d JOIN pg_database db ON db.oid = d.datid
WHERE d.datname = current_database()`

// sizeSQL is the database's size: it stats every file of the database,
// so only the overview reads it.
const sizeSQL = `SELECT pg_database_size(current_database())`

// sessionsSQL lists the database's sessions, those not idle first.
const sessionsSQL = `SELECT pid, backend_start, coalesce(usename, ''), coalesce(application_name, ''),
  coalesce(host(client_addr), ''), coalesce(backend_type, ''), coalesce(state, ''),
  xact_start, query_start, state_change, coalesce(wait_event_type, ''), coalesce(wait_event, ''),
  CASE WHEN wait_event_type = 'Lock' THEN pg_blocking_pids(pid) END,
  left(coalesce(query, ''), ` + textMax + `)
FROM pg_stat_activity
WHERE datname = current_database() AND pid <> pg_backend_pid()
ORDER BY coalesce(state, '') = 'idle', xact_start NULLS LAST, backend_start
LIMIT +$1`

// textMax is apitypes.DataInsightTextMaxBytes as SQL (a test keeps them
// equal).
const textMax = "4096"

// lockedSQL is the relations an ACCESS EXCLUSIVE lock is held or queued
// on in this database, as a CTE for the size queries.
const lockedSQL = `WITH locked AS (
  SELECT DISTINCT relation FROM pg_locks
  WHERE locktype = 'relation' AND mode = 'AccessExclusiveLock'
    AND database = (SELECT oid FROM pg_database WHERE datname = current_database()))
`

// tablesSQL lists user tables, largest first by the catalog's page count.
const tablesSQL = lockedSQL + `SELECT s.schemaname, s.relname, c.reltuples::int8, s.n_live_tup, s.n_dead_tup,
  coalesce(s.seq_scan, 0), coalesce(s.seq_tup_read, 0), coalesce(s.idx_scan, 0),
  greatest(s.last_vacuum, s.last_autovacuum), greatest(s.last_analyze, s.last_autoanalyze),
  io.heap_blks_hit::float8 / nullif(io.heap_blks_hit + io.heap_blks_read, 0),
  k.locked,
  CASE WHEN k.locked THEN NULL ELSE pg_total_relation_size(c.oid) END,
  CASE WHEN k.locked THEN NULL ELSE pg_indexes_size(c.oid) END,
  c.relpages::int8 * current_setting('block_size')::int8
FROM pg_stat_user_tables s
JOIN pg_class c ON c.oid = s.relid
LEFT JOIN pg_statio_user_tables io ON io.relid = s.relid
CROSS JOIN LATERAL (SELECT c.oid IN (SELECT relation FROM locked) AS locked) k
ORDER BY c.relpages DESC, 1, 2
LIMIT +$1`

// indexesSQL lists user indexes, largest first. last_idx_scan (Postgres
// 16+) is read through to_jsonb, so the query works on 14 and 15 too.
// dup is the same key for indexes with the same definition.
const indexesSQL = lockedSQL + `SELECT s.schemaname, s.relname, s.indexrelname,
  CASE WHEN k.locked THEN NULL ELSE pg_relation_size(s.indexrelid) END,
  coalesce(s.idx_scan, 0), (to_jsonb(s) ->> 'last_idx_scan')::timestamptz,
  i.indisunique, i.indisprimary, i.indisvalid,
  EXISTS (SELECT 1 FROM pg_constraint con WHERE con.conindid = s.indexrelid),
  count(*) OVER (PARTITION BY i.indrelid, i.indkey::text, i.indclass::text, coalesce(i.indexprs::text, ''), coalesce(i.indpred::text, '')) > 1,
  md5(i.indrelid::text || i.indkey::text || i.indclass::text || coalesce(i.indexprs::text, '') || coalesce(i.indpred::text, '')),
  pg_get_indexdef(s.indexrelid)
FROM pg_stat_user_indexes s
JOIN pg_index i ON i.indexrelid = s.indexrelid
JOIN pg_class ic ON ic.oid = s.indexrelid
CROSS JOIN LATERAL (SELECT s.indexrelid IN (SELECT relation FROM locked) OR s.relid IN (SELECT relation FROM locked) AS locked) k
ORDER BY ic.relpages DESC, 1, 2, 3
LIMIT +$1`

// statementsSQL lists top-level statements of this database, Dock's
// monitor's own left out; %[1]s is the extension's schema, %[2]s the
// order and %[3]s textMax.
const statementsSQL = `SELECT coalesce(s.queryid::text, ''), left(s.query, %[3]s), coalesce(r.rolname, ''),
  s.calls, s.total_exec_time, s.mean_exec_time, s.max_exec_time, s.rows,
  s.shared_blks_hit::float8 / nullif(s.shared_blks_hit + s.shared_blks_read, 0),
  s.temp_blks_written * current_setting('block_size')::int8,
  count(*) OVER ()
FROM %[1]s.pg_stat_statements s
LEFT JOIN pg_roles r ON r.oid = s.userid
WHERE s.dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
  AND s.toplevel AND s.userid <> (SELECT oid FROM pg_roles WHERE rolname = current_user)
ORDER BY %[2]s DESC NULLS LAST
LIMIT +$1`

// statementOrders are the queries view's orders as columns.
var statementOrders = map[apitypes.DataStatementOrder]string{
	apitypes.OrderTotal: "s.total_exec_time",
	apitypes.OrderMean:  "s.mean_exec_time",
	apitypes.OrderCalls: "s.calls",
	apitypes.OrderRows:  "s.rows",
}

// dbStats is statsSQL's row.
type dbStats struct {
	blksHit, blksRead, deadlocks, tempBytes int64
	statsReset                              *time.Time
	started                                 time.Time
	xidAge                                  int64
	preload                                 string
	pgssSchema                              *string
}

// queryStats says whether pg_stat_statements can be read.
func (s dbStats) queryStats() apitypes.DataQueryStatsState {
	loaded := false
	for _, lib := range strings.Split(s.preload, ",") {
		if strings.Trim(strings.TrimSpace(lib), `"`) == "pg_stat_statements" {
			loaded = true
		}
	}
	switch {
	case !loaded:
		return apitypes.QueryStatsNotLoaded
	case s.pgssSchema == nil:
		return apitypes.QueryStatsNoExtension
	}
	return apitypes.QueryStatsOn
}

// Insights reads the requested views as the monitor in one READ ONLY
// transaction, then derives the hints. A view that fails on its own (a
// statement timeout, a lock it met) is reported in Unavailable and the
// others still return. Views are read in DataInsightViews order.
func (e *Engine) Insights(ctx context.Context, t data.Target, c data.Call, req apitypes.DataInsightsRequest) (apitypes.DataInsights, error) {
	out := apitypes.DataInsights{Engine: apitypes.EnginePostgres, Hints: []apitypes.DataHint{}, Unavailable: []apitypes.DataUnavailable{}}
	want := func(v apitypes.DataInsightView) bool { return slices.Contains(req.Views, v) }
	var ev evidence
	err := e.readLocked(ctx, t, data.RoleMonitor, c, insightTimeout, insightLockTimeout, func(cn *conn) error {
		out.ReadAt = time.Now().UTC()
		var st dbStats
		if err := cn.Conn.QueryRow(ctx, statsSQL).Scan(&st.blksHit, &st.blksRead, &st.deadlocks, &st.tempBytes, &st.statsReset,
			&st.started, &st.xidAge, &st.preload, &st.pgssSchema); err != nil {
			return fmt.Errorf("%w: %w", data.ErrUnreachable, err)
		}
		since, reason := st.started.UTC(), "start"
		if st.statsReset != nil {
			since, reason = st.statsReset.UTC(), "reset"
		}
		out.Since, out.SinceReason = &since, reason
		ev.blksRead = st.blksRead
		// Each view runs in a savepoint, so one that errors (a timeout)
		// leaves the transaction usable for the next.
		view := func(v apitypes.DataInsightView, fn func() error) error {
			if !want(v) {
				return nil
			}
			if _, err := cn.Conn.Exec(ctx, "SAVEPOINT v"); err != nil {
				return err
			}
			err := fn()
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if _, rerr := cn.Conn.Exec(ctx, "ROLLBACK TO SAVEPOINT v"); rerr != nil {
					return rerr
				}
				out.Unavailable = append(out.Unavailable, apitypes.DataUnavailable{View: v, Reason: "couldn't read it: " + dbError(err).Error()})
				return nil
			}
			_, err = cn.Conn.Exec(ctx, "RELEASE SAVEPOINT v")
			return err
		}
		readers := map[apitypes.DataInsightView]func() error{
			apitypes.InsightOverview: func() error { return readOverview(ctx, cn.Conn, st, &out) },
			apitypes.InsightQueries:  func() error { return readStatements(ctx, cn.Conn, st, req, &out, &ev) },
			apitypes.InsightSessions: func() error { return e.readSessions(ctx, cn.Conn, &out) },
			apitypes.InsightTables:   func() error { return readTables(ctx, cn.Conn, &out) },
			apitypes.InsightIndexes:  func() error { return readIndexes(ctx, cn.Conn, &out, &ev) },
		}
		for _, v := range apitypes.DataInsightViews {
			if err := view(v, readers[v]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return apitypes.DataInsights{}, err
	}
	out.Hints = e.hints(&out, ev, out.ReadAt)
	return out, nil
}

// evidence is what the hints need beyond the API's fields.
type evidence struct {
	blksRead int64
	// dupGroups maps a duplicate index to its definition's key.
	dupGroups map[string]string
	// dealloc is pg_stat_statements_info.dealloc: statements dropped for
	// lack of room.
	dealloc int64
}

// readOverview reads the connections, counters and size.
func readOverview(ctx context.Context, pg *pgx.Conn, st dbStats, out *apitypes.DataInsights) error {
	p, err := scanPressure(ctx, pg)
	if err != nil {
		return err
	}
	ov := apitypes.DataOverview{
		Connections: p, Deadlocks: st.deadlocks, TempBytes: st.tempBytes, QueryStats: st.queryStats(),
		CacheHit: ratio(st.blksHit, st.blksRead),
	}
	age, started := st.xidAge, st.started.UTC()
	ov.TransactionIDAge, ov.StartedAt = &age, &started
	if err := pg.QueryRow(ctx, sizeSQL).Scan(&ov.Bytes); err != nil {
		return err
	}
	out.Overview = &ov
	return nil
}

// queryStatsOff says why the queries view is empty.
func queryStatsOff(s apitypes.DataQueryStatsState) apitypes.DataUnavailable {
	reason := "the server doesn't load pg_stat_statements: add it to shared_preload_libraries and restart the database"
	if s == apitypes.QueryStatsNoExtension {
		reason = "pg_stat_statements is loaded but not created in this database: run CREATE EXTENSION pg_stat_statements as the owner"
	}
	return apitypes.DataUnavailable{View: apitypes.InsightQueries, Reason: reason}
}

// appendOnce appends u unless an entry for its view with the same reason
// is there.
func appendOnce(list []apitypes.DataUnavailable, u apitypes.DataUnavailable) []apitypes.DataUnavailable {
	if slices.Contains(list, u) {
		return list
	}
	return append(list, u)
}

// ratio is hit / (hit + read), nil without reads.
func ratio(hit, read int64) *float64 {
	if hit+read == 0 {
		return nil
	}
	r := float64(hit) / float64(hit+read)
	return &r
}

// scanPressure runs pressureSQL.
func scanPressure(ctx context.Context, pg *pgx.Conn) (apitypes.DataPressure, error) {
	var p apitypes.DataPressure
	err := pg.QueryRow(ctx, pressureSQL).Scan(&p.Used, &p.Max, &p.Reserved, &p.Active, &p.Idle, &p.IdleInTransaction, &p.Waiting, &p.OldestTransactionAt)
	if p.OldestTransactionAt != nil {
		at := p.OldestTransactionAt.UTC()
		p.OldestTransactionAt = &at
	}
	return p, err
}

// Pressure reads the connection counts as the monitor. A login refused
// for too many connections is Full.
func (e *Engine) Pressure(ctx context.Context, t data.Target) (apitypes.DataPressure, error) {
	var p apitypes.DataPressure
	err := e.readLocked(ctx, t, data.RoleMonitor, data.Call{Actor: "pressure"}, insightTimeout, insightLockTimeout, func(cn *conn) (err error) {
		p, err = scanPressure(ctx, cn.Conn)
		return err
	})
	if errors.Is(err, data.ErrFull) {
		return apitypes.DataPressure{Full: true}, nil
	}
	return p, err
}

// readStatements reads the queries view from pg_stat_statements.
func readStatements(ctx context.Context, pg *pgx.Conn, st dbStats, req apitypes.DataInsightsRequest, out *apitypes.DataInsights, ev *evidence) error {
	if s := st.queryStats(); s != apitypes.QueryStatsOn {
		out.Unavailable = appendOnce(out.Unavailable, queryStatsOff(s))
		return nil
	}
	order, ok := statementOrders[req.Order]
	if !ok {
		order = statementOrders[apitypes.OrderTotal]
	}
	schema := pgx.Identifier{*st.pgssSchema}.Sanitize()
	rows, err := pg.Query(ctx, fmt.Sprintf(statementsSQL, schema, order, textMax), req.Limit)
	if err != nil {
		return err
	}
	hidden := false
	out.Queries = []apitypes.DataStatement{}
	for rows.Next() {
		var s apitypes.DataStatement
		if err := rows.Scan(&s.ID, &s.Text, &s.User, &s.Calls, &s.TotalMs, &s.MeanMs, &s.MaxMs, &s.Rows, &s.CacheHit, &s.TempBytes, &out.QueriesTracked); err != nil {
			rows.Close()
			return err
		}
		if s.Text == insufficientPrivilege {
			s.Text, hidden = "", true
		}
		out.Queries = append(out.Queries, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if hidden {
		out.Unavailable = append(out.Unavailable, textsHidden(apitypes.InsightQueries))
	}
	return pg.QueryRow(ctx, "SELECT dealloc FROM "+schema+".pg_stat_statements_info").Scan(&ev.dealloc)
}

// textsHidden is the partial note when the monitor can't read other
// roles' statement texts.
func textsHidden(v apitypes.DataInsightView) apitypes.DataUnavailable {
	return apitypes.DataUnavailable{View: v, Partial: true,
		Reason: "statement texts of other roles are hidden: the monitor role lacks pg_read_all_stats; a superuser can run GRANT pg_monitor TO the monitor role"}
}

// readSessions reads the sessions view and links blocked sessions to the
// IDs of their blockers.
func (e *Engine) readSessions(ctx context.Context, pg *pgx.Conn, out *apitypes.DataInsights) error {
	rows, err := pg.Query(ctx, sessionsSQL, apitypes.DataInsightSessionsLimit)
	if err != nil {
		return err
	}
	type blocked struct {
		i    int
		pids []int32
	}
	var waits []blocked
	ids := map[int64]string{}
	hidden := false
	out.Sessions = []apitypes.DataSession{}
	for rows.Next() {
		var s apitypes.DataSession
		var start time.Time
		var backendType, state, waitType, wait string
		var blockers []int32
		if err := rows.Scan(&s.PID, &start, &s.User, &s.Application, &s.Client, &backendType, &state,
			&s.TransactionAt, &s.QueryAt, &s.StateAt, &waitType, &wait, &blockers, &s.Text); err != nil {
			rows.Close()
			return err
		}
		s.ID = sessionID(s.PID, start)
		ids[s.PID] = s.ID
		s.Kind = e.sessionKind(backendType, s.Application)
		s.State = sessionState(state)
		// Client and Activity waits are what idle sessions and background
		// processes do; only other waits say something.
		if waitType != "" && waitType != "Client" && waitType != "Activity" {
			s.Wait = waitType + ": " + wait
		}
		if s.Text == insufficientPrivilege {
			s.Text, hidden = "", true
		}
		utc(s.TransactionAt, s.QueryAt, s.StateAt)
		if len(blockers) > 0 {
			waits = append(waits, blocked{len(out.Sessions), blockers})
		}
		out.Sessions = append(out.Sessions, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, w := range waits {
		for _, pid := range w.pids {
			id, ok := ids[int64(pid)]
			if !ok {
				id = strconv.Itoa(int(pid))
			}
			out.Sessions[w.i].BlockedBy = append(out.Sessions[w.i].BlockedBy, id)
		}
	}
	if hidden {
		out.Unavailable = append(out.Unavailable, textsHidden(apitypes.InsightSessions))
	}
	return nil
}

// sessionID binds a process ID to its backend's start, since Postgres
// reuses process IDs: "4121.1731248400123456".
func sessionID(pid int64, start time.Time) string {
	return strconv.FormatInt(pid, 10) + "." + strconv.FormatInt(start.UnixMicro(), 10)
}

// sessionKind maps backend_type, telling Dock's own sessions apart by
// application_name.
func (e *Engine) sessionKind(backendType, app string) string {
	switch {
	case backendType == "client backend" && strings.HasPrefix(app, e.app+":"):
		return "dock"
	case backendType == "client backend":
		return "client"
	case backendType == "autovacuum worker":
		return "autovacuum"
	}
	return "background"
}

// sessionState maps pg_stat_activity.state.
func sessionState(s string) string {
	switch s {
	case "active", "idle":
		return s
	case "idle in transaction", "idle in transaction (aborted)":
		return "idle_in_transaction"
	}
	return "other"
}

// utc converts timestamps to UTC in place.
func utc(ts ...*time.Time) {
	for _, t := range ts {
		if t != nil {
			*t = t.UTC()
		}
	}
}

// readTables reads the tables view.
func readTables(ctx context.Context, pg *pgx.Conn, out *apitypes.DataInsights) error {
	rows, err := pg.Query(ctx, tablesSQL, apitypes.DataInsightTablesLimit)
	if err != nil {
		return err
	}
	out.Tables = []apitypes.DataTableStats{}
	for rows.Next() {
		var t apitypes.DataTableStats
		var live int64
		if err := rows.Scan(&t.Schema, &t.Name, &t.EstimatedRows, &live, &t.DeadRows, &t.SeqScans, &t.SeqRowsRead, &t.IndexScans,
			&t.VacuumedAt, &t.AnalyzedAt, &t.CacheHit, &t.Locked, &t.TotalBytes, &t.IndexBytes, &t.EstimatedBytes); err != nil {
			rows.Close()
			return err
		}
		if t.EstimatedRows < 0 {
			// Never analysed (reltuples -1): the statistics' live count.
			t.EstimatedRows = live
		}
		utc(t.VacuumedAt, t.AnalyzedAt)
		out.Tables = append(out.Tables, t)
	}
	return rows.Err()
}

// readIndexes reads the indexes view.
func readIndexes(ctx context.Context, pg *pgx.Conn, out *apitypes.DataInsights, ev *evidence) error {
	rows, err := pg.Query(ctx, indexesSQL, apitypes.DataInsightIndexesLimit)
	if err != nil {
		return err
	}
	out.Indexes = []apitypes.DataIndexStats{}
	ev.dupGroups = map[string]string{}
	for rows.Next() {
		var x apitypes.DataIndexStats
		var group string
		if err := rows.Scan(&x.Schema, &x.Table, &x.Name, &x.Bytes, &x.Scans, &x.LastScanAt, &x.Unique, &x.Primary, &x.Valid,
			&x.Constraint, &x.Duplicate, &group, &x.Definition); err != nil {
			rows.Close()
			return err
		}
		utc(x.LastScanAt)
		if x.Duplicate {
			ev.dupGroups[x.Schema+"."+x.Name] = group
		}
		out.Indexes = append(out.Indexes, x)
	}
	return rows.Err()
}

// EnsureMonitor creates the monitor role, or updates its password and
// settings, as the owner, then tries to grant it pg_monitor. The grant is
// separate: a server that refuses it (a managed service whose owner isn't
// a superuser) still gets a monitor that reads sizes, scans and counts.
// It drops the monitor's cached connections, whose password may be old.
func (e *Engine) EnsureMonitor(ctx context.Context, t data.Target, owner data.Credentials) error {
	if t.Monitor.User == "" {
		return fmt.Errorf("%w: no credentials for %s", data.ErrNotSetUp, e.roles.Monitor)
	}
	pg, _, err := e.ownerConn(ctx, t, owner)
	if err != nil {
		return err
	}
	defer func() { _ = pg.Close(context.Background()) }()
	if err := ensureMonitor(ctx, pg, e.roles.Monitor, t.Monitor.Password, t.Database); err != nil {
		return err
	}
	e.conns.Drop(func(k conncache.Key) bool { return k.ID == t.ServiceID && k.Role == data.RoleMonitor })
	return nil
}

// ensureMonitor brings the monitor role to spec on an owner connection:
// LOGIN, nothing privileged, read-only by default, a statement timeout,
// CONNECT on the database, then (best effort) pg_monitor.
func ensureMonitor(ctx context.Context, pg *pgx.Conn, name data.Role, password, database string) error {
	verifier, err := scramVerifier(password)
	if err != nil {
		return fmt.Errorf("monitor password: %w", err)
	}
	var exists bool
	if err := pg.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", string(name)).Scan(&exists); err != nil {
		return err
	}
	role := pgx.Identifier{string(name)}.Sanitize()
	verb := "ALTER"
	if !exists {
		verb = "CREATE"
	}
	stmts := []string{
		verb + " ROLE " + role + " WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT PASSWORD " + quoteLiteral(verifier),
		"ALTER ROLE " + role + " SET default_transaction_read_only = 'on'",
		"ALTER ROLE " + role + " SET statement_timeout = " + quoteLiteral(strconv.FormatInt(monitorStatementTimeout.Milliseconds(), 10)),
		"GRANT CONNECT ON DATABASE " + pgx.Identifier{database}.Sanitize() + " TO " + role,
	}
	if err := simpleExec(ctx, pg, "BEGIN;\n"+strings.Join(stmts, ";\n")+";\nCOMMIT"); err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	_ = simpleExec(ctx, pg, "GRANT pg_monitor TO "+role)
	return nil
}
