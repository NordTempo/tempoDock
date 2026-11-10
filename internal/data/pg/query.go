package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

const (
	// maxReadTimeout is the longest statement timeout a read may ask for;
	// setup also makes it dock_reader's default.
	maxReadTimeout = apitypes.DataQueryMaxTimeout
	// frameMaxBytes ends a rows frame early when its encoded rows reach
	// this size, so wide rows don't make a 500-row frame huge.
	frameMaxBytes = 1 << 20
	// queryGrace is how long past its statement timeout a query may take
	// before the engine gives up on the connection (a hung link).
	queryGrace = 5 * time.Second
)

// textFormats asks for every result column in text format.
var textFormats = []int16{pgx.TextFormatCode}

// Query runs one read statement as the reader and streams its result.
//
// The statement is checked before connecting (one statement, a read
// keyword, no function whose effects outlive the transaction), then
// prepared unnamed in a READ ONLY transaction with the
// request's statement timeout and data.ReadLockTimeout, executed with text
// results and encoded row by row into frames of at most
// apitypes.DataFrameRows rows (and about frameMaxBytes). Hitting the row or
// byte cap ends the result with Truncated: the connection is then cut
// rather than drained, which also stops the server, and is not reused.
// The transaction is rolled back afterwards and session advisory locks
// released (endQuerySQL). Masked calls run as the masked
// reader, which can't select sensitive columns; without its credentials
// they can't run SQL.
func (e *Engine) Query(ctx context.Context, t data.Target, c data.Call, req apitypes.DataQueryRequest, send func(apitypes.DataQueryFrame) error) (data.QueryStats, error) {
	start := time.Now()
	stats := data.QueryStats{Outcome: apitypes.OutcomeOK}
	role := data.RoleReader
	if c.Masked {
		if t.ReaderMasked.User == "" {
			return stats, fmt.Errorf("%w: SQL isn't available while sensitive columns are masked for you", data.ErrInvalid)
		}
		role = data.RoleReaderMasked
	}
	if len(req.SQL) > apitypes.DataSQLMaxBytes {
		return stats, fmt.Errorf("%w: SQL is longer than %d bytes", data.ErrInvalid, apitypes.DataSQLMaxBytes)
	}
	if err := checkReadStatement(req.SQL); err != nil {
		return stats, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = apitypes.DataQueryLimit
	}
	limit = min(limit, apitypes.DataQueryMaxLimit)
	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = apitypes.DataQueryTimeout
	}
	timeout = min(timeout, maxReadTimeout)

	qctx, cancel := context.WithTimeout(ctx, timeout+queryGrace)
	defer cancel()
	cn, err := e.acquire(qctx, t, role)
	if err != nil {
		return stats, err
	}
	defer e.release(cn)
	if err := simpleExec(qctx, cn.Conn, beginSQL(true, timeout, data.ReadLockTimeout, 0, appName(e.app, c.Actor))); err != nil {
		cn.Broken = true
		return stats, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}

	s := stream{send: send, limit: int64(limit)}
	qerr := s.run(qctx, cn, req.SQL)
	stats.Rows = s.rows
	stats.Duration = time.Since(start)
	switch {
	case s.sendErr != nil:
		stats.Outcome = apitypes.OutcomeCanceled
		return stats, nil
	case qerr != nil:
		stats.Outcome = outcome(ctx, qerr)
		_ = send(errorFrame(qerr))
	default:
		_ = send(apitypes.DataQueryFrame{
			Type:       apitypes.FrameEnd,
			RowCount:   s.rows,
			Truncated:  s.truncated,
			Command:    s.command,
			DurationMS: stats.Duration.Milliseconds(),
		})
	}
	// A canceled context leaves the connection in an unknown state; one
	// that broke on the wire is closed already (release drops both).
	if qctx.Err() != nil || cn.Conn.IsClosed() {
		cn.Broken = true
	}
	if !cn.Broken {
		if err := simpleExec(qctx, cn.Conn, endQuerySQL); err != nil {
			cn.Broken = true
		}
	}
	return stats, nil
}

// endQuerySQL ends a query's transaction and undoes what a rollback
// doesn't: session-level advisory locks, which would otherwise stay held
// on the cached connection (blocking the app's migrations or leader
// election). checkReadStatement already refuses the lock functions; this
// covers calls it can't see (a view or function that takes one). Both
// statements go in one round trip.
const endQuerySQL = "ROLLBACK; SELECT pg_advisory_unlock_all()"

// stream is one query's result on its way to send. Its row buffer and
// frame are reused for every frame.
type stream struct {
	send      func(apitypes.DataQueryFrame) error
	limit     int64
	rows      int64
	bytes     int
	truncated bool
	command   string
	sendErr   error

	buf  []byte
	ends []int
	out  []apitypes.DataRow
}

// run prepares and executes sql on cn, sending the header and row frames.
// Database errors are returned for the caller's error frame.
func (s *stream) run(ctx context.Context, cn *conn, sql string) error {
	pc := cn.Conn.PgConn()
	sd, err := pc.Prepare(ctx, "", sql, nil)
	if err != nil {
		return err
	}
	if len(sd.ParamOIDs) > 0 {
		return errors.New("query parameters ($1, ...) aren't supported: write the values into the SQL")
	}
	cols, kinds, err := resultColumns(ctx, cn.Conn, sd.Fields)
	if err != nil {
		return err
	}
	if s.sendErr = s.send(apitypes.DataQueryFrame{Type: apitypes.FrameHeader, Columns: cols}); s.sendErr != nil {
		cn.Broken = true
		return nil
	}
	src := make([]int, len(kinds))
	for i := range src {
		src[i] = i
	}
	s.ends = make([]int, 0, apitypes.DataFrameRows)
	s.out = make([]apitypes.DataRow, 0, apitypes.DataFrameRows)

	rr := pc.ExecPrepared(ctx, "", nil, nil, textFormats)
	for rr.NextRow() {
		if s.rows == s.limit {
			s.truncated = true
			break
		}
		before := len(s.buf)
		s.buf = appendRow(s.buf, rr.Values(), src, kinds, false)
		if s.bytes+len(s.buf)-before > apitypes.DataQueryMaxBytes {
			s.buf = s.buf[:before]
			s.truncated = true
			break
		}
		s.bytes += len(s.buf) - before
		s.ends = append(s.ends, len(s.buf))
		s.rows++
		if len(s.ends) == apitypes.DataFrameRows || len(s.buf) >= frameMaxBytes {
			if !s.flush() {
				break
			}
		}
	}
	if s.truncated || s.sendErr != nil {
		// Cut the connection instead of draining the rest of the result;
		// the server stops when its next write fails.
		cn.Broken = true
		_ = pc.Conn().Close()
		_, _ = rr.Close()
		if s.sendErr == nil {
			s.flush()
		}
		return nil
	}
	tag, err := rr.Close()
	if !s.flush() || err != nil {
		return err
	}
	s.command = tag.String()
	return nil
}

// flush sends the buffered rows as one frame and resets the buffer. It
// reports false when send failed.
func (s *stream) flush() bool {
	if len(s.ends) == 0 || s.sendErr != nil {
		return s.sendErr == nil
	}
	s.out = s.out[:0]
	start := 0
	for _, end := range s.ends {
		s.out = append(s.out, s.buf[start:end:end])
		start = end
	}
	s.sendErr = s.send(apitypes.DataQueryFrame{Type: apitypes.FrameRows, Rows: s.out})
	s.buf = s.buf[:0]
	s.ends = s.ends[:0]
	return s.sendErr == nil
}

// resultColumns names the result's columns and types and picks their cell
// kinds. Types pgx doesn't know (enums, extensions) are looked up in one
// query.
func resultColumns(ctx context.Context, pg *pgx.Conn, fields []pgconn.FieldDescription) ([]apitypes.DataResultColumn, []data.CellKind, error) {
	cols := make([]apitypes.DataResultColumn, len(fields))
	kinds := make([]data.CellKind, len(fields))
	var unknown []uint32
	for i, f := range fields {
		cols[i].Name = f.Name
		kinds[i] = kindOfOID(f.DataTypeOID)
		if typ, ok := pg.TypeMap().TypeForOID(f.DataTypeOID); ok {
			cols[i].TypeName = typ.Name
		} else if !containsOID(unknown, f.DataTypeOID) {
			unknown = append(unknown, f.DataTypeOID)
		}
	}
	if len(unknown) == 0 {
		return cols, kinds, nil
	}
	rows, err := pg.Query(ctx, "SELECT oid, typname FROM pg_type WHERE oid = ANY($1)", unknown)
	if err != nil {
		return nil, nil, err
	}
	names := make(map[uint32]string, len(unknown))
	for rows.Next() {
		var oid uint32
		var name string
		if err := rows.Scan(&oid, &name); err != nil {
			return nil, nil, err
		}
		names[oid] = name
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	for i, f := range fields {
		if cols[i].TypeName == "" {
			cols[i].TypeName = names[f.DataTypeOID]
		}
	}
	return cols, kinds, nil
}

func containsOID(list []uint32, oid uint32) bool {
	for _, x := range list {
		if x == oid {
			return true
		}
	}
	return false
}

// outcome classifies a query error for the audit detail.
func outcome(parent context.Context, err error) apitypes.DataOutcome {
	if parent.Err() != nil {
		return apitypes.OutcomeCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return apitypes.OutcomeTimeout
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && (pgErr.Code == "57014" || pgErr.Code == "55P03") {
		return apitypes.OutcomeTimeout
	}
	return apitypes.OutcomeError
}

// errorFrame reports err, with Postgres' fields when it is a database
// error.
func errorFrame(err error) apitypes.DataQueryFrame {
	qe := &apitypes.DataQueryError{Message: err.Error()}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		qe = &apitypes.DataQueryError{
			Message:  pgErr.Message,
			SQLState: pgErr.Code,
			Detail:   pgErr.Detail,
			Hint:     pgErr.Hint,
			Position: int(pgErr.Position),
		}
	}
	return apitypes.DataQueryFrame{Type: apitypes.FrameError, Error: qe}
}
