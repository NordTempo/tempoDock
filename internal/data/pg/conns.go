package pg

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/conncache"
)

const (
	// connectTimeout bounds dialing plus the startup handshake.
	connectTimeout = 10 * time.Second
	// cancelGrace is how long a canceled query may take to stop after the
	// cancel request before its connection is cut.
	cancelGrace = 2 * time.Second
	// closeTimeout bounds a polite close (Terminate) of a connection.
	closeTimeout = time.Second
	// stmtCacheSize bounds pgx's per-connection statement and description
	// caches (table pages and catalog queries; ad-hoc SQL isn't cached).
	stmtCacheSize = 64
	// maxMessageBytes bounds one message from the database, which the
	// client reads whole before the panel's own caps (a cell, a result)
	// see any of it: a row. pgx's own limit is 1 GiB, more than the
	// daemon has; one `SELECT repeat('x', 900000000)` would be read into
	// memory to show 8 KB of it. 16 MiB is far above the rows the panel
	// is for, and a row past it fails with tooLargeText.
	maxMessageBytes = 16 << 20
)

// tooLargeText is what a statement whose row is past maxMessageBytes
// answers with.
const tooLargeText = "a row of this result is larger than 16 MB, more than the Data panel reads at once; select fewer or shorter columns, for example left(column, 1000)"

// errClosed is what acquire returns after Close.
var errClosed = conncache.ErrClosed

// conn is one cached connection (internal/data/conncache): Conn is the
// pgx connection, used only by the caller holding it; Broken tells
// release to close it.
type conn = conncache.Conn[*pgx.Conn]

// newCache returns the engine's connection cache; zero limits are
// package data's. Closing a database's last connection drops its cached
// schema (under the cache's lock: e.mu is never held while calling the
// cache).
func (e *Engine) newCache(idle time.Duration, maxConns int) *conncache.Cache[*pgx.Conn] {
	return conncache.New(conncache.Options[*pgx.Conn]{
		IdleTimeout: idle,
		Max:         maxConns,
		Reusable:    func(pg *pgx.Conn) bool { return !pg.IsClosed() && pg.PgConn().TxStatus() == 'I' },
		Close:       closeConn,
		Emptied: func(id int64) {
			e.mu.Lock()
			delete(e.schemas, id)
			e.mu.Unlock()
		},
	})
}

// acquire hands out a connection to t as role from the cache (an idle
// one, a new one within the limits, or the next one released, waiting
// until ctx ends). Release it with release.
func (e *Engine) acquire(ctx context.Context, t data.Target, role data.Role) (*conn, error) {
	cred, err := credentials(t, role)
	if err != nil {
		return nil, err
	}
	cn, err := e.conns.Acquire(ctx, conncache.Key{ID: t.ServiceID, Role: role}, func(ctx context.Context) (*pgx.Conn, error) {
		return e.connect(ctx, t, cred)
	})
	if err != nil {
		return nil, loginError(ctx, err, data.ErrNotSetUp)
	}
	return cn, nil
}

// release returns cn to the cache, or closes it when it is broken or not
// idle (open transaction, closed by the server).
func (e *Engine) release(cn *conn) { e.conns.Release(cn) }

// closeConn closes a connection. A broken one gets a short deadline first
// so a stuck socket can't hold the caller.
func closeConn(pg *pgx.Conn, broken bool) {
	if broken {
		_ = pg.PgConn().Conn().SetDeadline(time.Now().Add(closeTimeout))
	}
	// Background: a canceled context would make pgx send a cancel request.
	_ = pg.Close(context.Background())
}

// credentials picks the login for one of the engine's roles.
func credentials(t data.Target, role data.Role) (data.Credentials, error) {
	var cred data.Credentials
	switch role {
	case data.RoleReader:
		cred = t.Reader
	case data.RoleWriter:
		cred = t.Writer
	case data.RoleReaderMasked:
		cred = t.ReaderMasked
	case data.RoleMonitor:
		cred = t.Monitor
	}
	if cred.User == "" || t.Dial == nil {
		return cred, fmt.Errorf("%w: no credentials for %s", data.ErrNotSetUp, role)
	}
	return cred, nil
}

// connect opens one connection to t through t.Dial, with TLS when t.TLS
// is set (falling back to plaintext with t.TLSOptional). Nothing comes
// from the environment (PG* variables): host, TLS, timeouts and session
// settings are fixed here. Results come back in text format, so the
// session settings pin the text output that apitypes.DataRow promises
// (ISO dates in UTC, hex bytea).
func (e *Engine) connect(ctx context.Context, t data.Target, cred data.Credentials) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig("host=dock port=5432 user=dock sslmode=disable")
	if err != nil {
		return nil, err
	}
	cfg.User, cfg.Password, cfg.Database = cred.User, cred.Password, t.Database
	cfg.TLSConfig, cfg.Fallbacks, cfg.ValidateConnect, cfg.AfterConnect = nil, nil, nil, nil
	if t.TLS != nil {
		cfg.TLSConfig = t.TLS
		if t.TLSOptional {
			cfg.Fallbacks = []*pgconn.FallbackConfig{{Host: cfg.Host, Port: cfg.Port}}
		}
	}
	cfg.ConnectTimeout = connectTimeout
	dial := t.Dial
	cfg.DialFunc = func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) }
	cfg.LookupFunc = func(_ context.Context, host string) ([]string, error) { return []string{host}, nil }
	cfg.RuntimeParams = map[string]string{
		"application_name": e.app,
		"DateStyle":        "ISO, MDY",
		"IntervalStyle":    "postgres",
		"TimeZone":         "UTC",
		"bytea_output":     "hex",
	}
	// On a canceled context, ask the server to cancel the query (through
	// Dial again) rather than cutting the connection.
	cfg.BuildContextWatcherHandler = func(pc *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: pc, DeadlineDelay: cancelGrace}
	}
	cfg.StatementCacheCapacity = stmtCacheSize
	cfg.DescriptionCacheCapacity = stmtCacheSize
	cfg.MaxProtocolMessageBodyLen = maxMessageBytes
	return pgx.ConnectConfig(ctx, cfg)
}

// loginError maps a connect error: rejected logins become loginErr
// (ErrNotSetUp for Dock's roles, ErrLoginFailed for the owner), everything
// else ErrUnreachable.
func loginError(ctx context.Context, err error, loginErr error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, errClosed) {
		return err
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		if pgErr.Code == "53300" {
			return fmt.Errorf("%w: %s", data.ErrFull, pgErr.Message)
		}
		if strings.HasPrefix(pgErr.Code, "28") {
			return fmt.Errorf("%w: %s", loginErr, pgErr.Message)
		}
		return fmt.Errorf("%w: %s", data.ErrUnreachable, pgErr.Message)
	}
	return fmt.Errorf("%w: %w", data.ErrUnreachable, err)
}

// simpleExec runs sql (no parameters, possibly several statements) with the
// simple protocol and discards results.
func simpleExec(ctx context.Context, pg *pgx.Conn, sql string) error {
	return pg.PgConn().Exec(ctx, sql).Close()
}

// beginSQL is the statement that opens a panel transaction with its
// limits; app is application_name (appName).
func beginSQL(readOnly bool, statement, lock time.Duration, idle time.Duration, app string) string {
	var b strings.Builder
	if readOnly {
		b.WriteString("BEGIN READ ONLY")
	} else {
		b.WriteString("BEGIN")
	}
	b.WriteString("; SET LOCAL statement_timeout = ")
	b.WriteString(strconv.FormatInt(statement.Milliseconds(), 10))
	b.WriteString("; SET LOCAL lock_timeout = ")
	b.WriteString(strconv.FormatInt(lock.Milliseconds(), 10))
	if idle > 0 {
		b.WriteString("; SET LOCAL idle_in_transaction_session_timeout = ")
		b.WriteString(strconv.FormatInt(idle.Milliseconds(), 10))
	}
	b.WriteString("; SET LOCAL application_name = '")
	b.WriteString(app)
	b.WriteString("'")
	return b.String()
}

// read runs fn in a READ ONLY transaction as role (the reader or the
// masked reader) with the given statement timeout, then rolls back: reads
// change nothing, and rolling back also undoes any session setting a
// query changed (set_config), so the next caller gets a clean connection.
func (e *Engine) read(ctx context.Context, t data.Target, role data.Role, c data.Call, timeout time.Duration, fn func(cn *conn) error) error {
	return e.readLocked(ctx, t, role, c, timeout, data.ReadLockTimeout, fn)
}

// readLocked is read with its own lock_timeout.
func (e *Engine) readLocked(ctx context.Context, t data.Target, role data.Role, c data.Call, timeout, lock time.Duration, fn func(cn *conn) error) error {
	cn, err := e.acquire(ctx, t, role)
	if err != nil {
		return err
	}
	defer e.release(cn)
	if err := simpleExec(ctx, cn.Conn, beginSQL(true, timeout, lock, 0, appName(e.app, c.Actor))); err != nil {
		cn.Broken = true
		return dbError(err)
	}
	ferr := fn(cn)
	if ctx.Err() != nil {
		cn.Broken = true
		if ferr != nil {
			return ferr
		}
		return ctx.Err()
	}
	if err := simpleExec(ctx, cn.Conn, "ROLLBACK"); err != nil {
		cn.Broken = true
	}
	return ferr
}

// dbError maps a database error for structured reads: a missing
// privilege (a relation created since the last grants sync) is
// ErrNoPrivilege, other errors in the request (bad value for a type,
// unknown operator for a type, ...) are ErrInvalid with Postgres' message;
// anything else passes through.
func dbError(err error) error {
	if err == nil {
		return nil
	}
	if tooLarge(err) {
		return fmt.Errorf("%w: %s", data.ErrInvalid, tooLargeText)
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		if pgErr.Code == "42501" {
			return fmt.Errorf("%w: %s", data.ErrNoPrivilege, pgErr.Message)
		}
		switch pgErr.Code[:2] {
		case "22", "42":
			return fmt.Errorf("%w: %s", data.ErrInvalid, pgErr.Message)
		}
	}
	return err
}

// tooLarge reports whether err is the client refusing a message past
// maxMessageBytes. The connection is unusable after it: the message was
// not read.
func tooLarge(err error) bool {
	_, ok := errors.AsType[*pgproto3.ExceededMaxBodyLenErr](err)
	return ok
}
