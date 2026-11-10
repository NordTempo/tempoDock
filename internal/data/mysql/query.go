package mysql

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/mysql/wire"
)

// frameMaxBytes ends a rows frame early when its encoded rows reach this
// size, so wide rows don't make a 500-row frame huge.
const frameMaxBytes = 1 << 20

// errCapped stops reading a result at a row or byte cap.
var errCapped = errors.New("result capped")

// Query runs one read statement as the reader and streams its result.
//
// The statement is checked first (checkReadStatement), then runs in a
// READ ONLY transaction with the request's statement timeout, encoded
// row by row into frames of at most apitypes.DataFrameRows rows (and about
// frameMaxBytes). Hitting the row or byte cap ends the result with
// Truncated: the connection is then closed rather than drained, which
// stops the server too. Afterwards the transaction is rolled back and
// named locks (GET_LOCK) released, so nothing outlives the call. Masked
// calls run as the masked reader, which can't select sensitive columns;
// without its credentials they can't run SQL.
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
	timeout = min(timeout, apitypes.DataQueryMaxTimeout)

	qctx, cancel := context.WithTimeout(ctx, timeout+queryGrace)
	defer cancel()
	cn, err := e.acquire(qctx, t, role)
	if err != nil {
		return stats, err
	}
	defer e.release(cn)
	c0 := cn.Conn
	if err := c0.setTimeout(qctx, timeout); err != nil {
		cn.Broken = true
		return stats, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}
	if err := c0.exec(qctx, "START TRANSACTION READ ONLY"); err != nil {
		cn.Broken = true
		return stats, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}

	s := stream{send: send, limit: int64(limit)}
	stop := c0.watch(qctx)
	qerr := c0.Query(actorComment(e.app, c.Actor)+req.SQL, s.header, s.row, nil)
	stop()
	if errors.Is(qerr, errCapped) {
		qerr = nil
	}
	if s.truncated || s.sendErr != nil {
		s.flush()
	}
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
		if !s.truncated && !s.flush() {
			stats.Outcome = apitypes.OutcomeCanceled
			return stats, nil
		}
		_ = send(apitypes.DataQueryFrame{
			Type: apitypes.FrameEnd, RowCount: s.rows, Truncated: s.truncated,
			Command: commandTag(req.SQL, s.rows), DurationMS: stats.Duration.Milliseconds(),
		})
	}
	if !c0.Broken() {
		if c0.exec(qctx, "ROLLBACK") != nil || c0.exec(qctx, "DO RELEASE_ALL_LOCKS()") != nil {
			cn.Broken = true
		}
	}
	return stats, nil
}

// commandTag is the end frame's command: the statement's first keyword
// and the row count, like Postgres' "SELECT 12".
func commandTag(sql string, rows int64) string {
	word := ""
	_ = scanSQL(sql, func(kind tokenKind, text string) {
		if word == "" && kind == tokWord {
			word = strings.ToUpper(text)
		}
	})
	return word + " " + strconv.FormatInt(rows, 10)
}

// stream is one query's result on its way to send. Its buffers and frame
// are reused for every frame.
type stream struct {
	send      func(apitypes.DataQueryFrame) error
	limit     int64
	rows      int64
	bytes     int
	truncated bool
	sendErr   error

	cols    []outCol
	buf     []byte
	scratch []byte
	ends    []int
	out     []apitypes.DataRow
}

// header sends the header frame for the result's columns.
func (s *stream) header(cols []wire.Column) error {
	out := make([]apitypes.DataResultColumn, len(cols))
	s.cols = make([]outCol, len(cols))
	for i, c := range cols {
		out[i], s.cols[i] = resultColumn(c)
		s.cols[i].src = i
	}
	s.ends = make([]int, 0, apitypes.DataFrameRows)
	s.out = make([]apitypes.DataRow, 0, apitypes.DataFrameRows)
	if s.sendErr = s.send(apitypes.DataQueryFrame{Type: apitypes.FrameHeader, Columns: out}); s.sendErr != nil {
		return s.sendErr
	}
	return nil
}

// row encodes one row, flushing a full frame; at a cap it stops the read.
func (s *stream) row(vals [][]byte) error {
	if s.rows == s.limit {
		s.truncated = true
		return errCapped
	}
	before := len(s.buf)
	s.buf = appendRow(s.buf, vals, s.cols, false, &s.scratch)
	if s.bytes+len(s.buf)-before > apitypes.DataQueryMaxBytes {
		s.buf = s.buf[:before]
		s.truncated = true
		return errCapped
	}
	s.bytes += len(s.buf) - before
	s.ends = append(s.ends, len(s.buf))
	s.rows++
	if len(s.ends) == apitypes.DataFrameRows || len(s.buf) >= frameMaxBytes {
		if !s.flush() {
			return s.sendErr
		}
	}
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

// outcome classifies a query error for the audit detail.
func outcome(parent context.Context, err error) apitypes.DataOutcome {
	if parent.Err() != nil {
		return apitypes.OutcomeCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return apitypes.OutcomeTimeout
	}
	switch wire.ErrorCode(err) {
	case errMySQLTimeout, errMariaDBTimeout, errLockWait, errInterrupted:
		return apitypes.OutcomeTimeout
	}
	return apitypes.OutcomeError
}

// errorFrame reports err, with the server's SQLSTATE and error number
// when it is a server error.
func errorFrame(err error) apitypes.DataQueryFrame {
	qe := &apitypes.DataQueryError{Message: err.Error()}
	if e, ok := errors.AsType[*wire.Error](err); ok {
		qe = &apitypes.DataQueryError{Message: e.Message, SQLState: e.State, Detail: "error " + strconv.Itoa(int(e.Code))}
	}
	return apitypes.DataQueryFrame{Type: apitypes.FrameError, Error: qe}
}
