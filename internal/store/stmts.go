package store

import (
	"context"
	"database/sql"
	"errors"
	"sync"
)

// stmts is the DBTX the store's queries run on: each query is prepared the
// first time it runs and the statement is reused after, so SQLite parses
// its SQL once (database/sql then prepares it once per connection it runs
// on). Preparing every query up front cost an idle daemon about 4 MB for
// the hundreds it never runs (docs/performance.md). The map is bounded:
// its keys are the generated queries' SQL constants.
type stmts struct {
	db *sql.DB
	// mu guards the maps only: nothing holding it calls into
	// database/sql. Closing a statement takes the lock of each connection
	// it was prepared on; a BEGIN waiting for SQLite's write lock holds
	// its connection's, and the writer holding the write lock may be
	// waiting for mu (warm, from txStmts). Holding mu there deadlocks
	// until busy_timeout, and the BEGIN fails with SQLITE_BUSY.
	mu sync.RWMutex
	m  map[string]*sql.Stmt
	// warming are queries being prepared in the background (warm).
	warming map[string]bool
	// closed is set by Close: nothing is prepared after it. warms counts
	// the background prepares and stop cancels them, so Close returns
	// with none of them holding or opening a connection.
	closed bool
	warms  sync.WaitGroup
	ctx    context.Context
	stop   context.CancelFunc
}

// errStmtsClosed is a query prepared after Close.
var errStmtsClosed = errors.New("sql: database is closed")

// newStmts returns an empty statement cache over db.
func newStmts(db *sql.DB) *stmts {
	s := &stmts{db: db, m: map[string]*sql.Stmt{}, warming: map[string]bool{}}
	s.ctx, s.stop = context.WithCancel(context.Background())
	return s
}

// cached returns query's statement if it was prepared already.
func (s *stmts) cached(query string) *sql.Stmt {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.m[query]
}

// warm prepares query in the background, once, for a transaction that
// had to run it unprepared (txStmts).
func (s *stmts) warm(query string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.m[query] != nil || s.warming[query] {
		return
	}
	s.warming[query] = true
	s.warms.Go(func() {
		_, _ = s.get(s.ctx, query)
		s.mu.Lock()
		delete(s.warming, query)
		s.mu.Unlock()
	})
}

// get returns query's statement, preparing it on first use. Two callers
// that prepare it at once keep the first one stored; the other's is
// closed outside mu, as is one prepared while Close ran.
func (s *stmts) get(ctx context.Context, query string) (*sql.Stmt, error) {
	if st := s.cached(query); st != nil {
		return st, nil
	}
	st, err := s.db.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	prev, closed := s.m[query], s.closed
	if prev == nil && !closed {
		s.m[query] = st
	}
	s.mu.Unlock()
	switch {
	case closed:
		_ = st.Close()
		return nil, errStmtsClosed
	case prev != nil:
		_ = st.Close()
		return prev, nil
	}
	return st, nil
}

// ExecContext implements DBTX.
func (s *stmts) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	st, err := s.get(ctx, query)
	if err != nil {
		return nil, err
	}
	return st.ExecContext(ctx, args...)
}

// QueryContext implements DBTX.
func (s *stmts) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	st, err := s.get(ctx, query)
	if err != nil {
		return nil, err
	}
	return st.QueryContext(ctx, args...)
}

// QueryRowContext implements DBTX. A query that doesn't prepare runs
// unprepared, so the *sql.Row carries its error.
func (s *stmts) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	st, err := s.get(ctx, query)
	if err != nil {
		return s.db.QueryRowContext(ctx, query, args...)
	}
	return st.QueryRowContext(ctx, args...)
}

// PrepareContext implements DBTX.
func (s *stmts) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return s.get(ctx, query)
}

// Close closes every statement prepared so far, after the background
// prepares have ended. One left running took a connection of its own,
// which database/sql's Close doesn't wait for: the connection was opened
// after the database closed and made SQLite's -wal and -shm files again,
// in a directory the caller thought it was done with.
func (s *stmts) Close() error {
	s.mu.Lock()
	s.closed = true
	all := s.m
	s.m = map[string]*sql.Stmt{}
	s.mu.Unlock()
	s.stop()
	s.warms.Wait()
	var errs []error
	for _, st := range all {
		errs = append(errs, st.Close())
	}
	return errors.Join(errs...)
}

// txStmts is the DBTX of a transaction (Store.Tx): the cached statements,
// bound to the transaction's connection. A query the cache doesn't have
// yet runs unprepared in the transaction and is prepared in the
// background for next time: preparing it first would need a second
// connection, and the others may all be held by transactions waiting for
// the write lock this one holds (BEGIN IMMEDIATE), which only ends at
// busy_timeout.
type txStmts struct {
	tx *sql.Tx
	s  *stmts
}

// ExecContext implements DBTX.
func (t txStmts) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	st := t.s.cached(query)
	if st == nil {
		t.s.warm(query)
		return t.tx.ExecContext(ctx, query, args...)
	}
	return t.tx.StmtContext(ctx, st).ExecContext(ctx, args...)
}

// QueryContext implements DBTX.
func (t txStmts) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	st := t.s.cached(query)
	if st == nil {
		t.s.warm(query)
		return t.tx.QueryContext(ctx, query, args...)
	}
	return t.tx.StmtContext(ctx, st).QueryContext(ctx, args...)
}

// QueryRowContext implements DBTX.
func (t txStmts) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	st := t.s.cached(query)
	if st == nil {
		t.s.warm(query)
		return t.tx.QueryRowContext(ctx, query, args...)
	}
	return t.tx.StmtContext(ctx, st).QueryRowContext(ctx, args...)
}

// PrepareContext implements DBTX: a statement of the transaction's own.
func (t txStmts) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	if st := t.s.cached(query); st != nil {
		return t.tx.StmtContext(ctx, st), nil
	}
	return t.tx.PrepareContext(ctx, query)
}
