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

	"github.com/NordTempo/tempoDock/internal/data"
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
)

var errClosed = errors.New("data engine closed")

// connKey identifies a set of cached connections: one service, one role.
type connKey struct {
	service int64
	role    data.Role
}

// conn is one cached connection. busy, stale, removed, idleAt and timer are
// guarded by Engine.mu; pg is used only by the caller holding the conn
// (busy) or, once removed, by whoever closes it.
type conn struct {
	key connKey
	pg  *pgx.Conn
	// busy: handed out (or still connecting, with pg nil).
	busy bool
	// stale: Forget or Close ran while busy; close it on release.
	stale bool
	// broken: the holder saw the connection in an unknown state (a
	// result abandoned mid-stream, a canceled context); close it on
	// release.
	broken  bool
	removed bool
	idleAt  time.Time
	// timer closes the connection IdleTimeout after its last release. It
	// is created on the first release and re-armed (Reset) on each one, so
	// an idle engine has no goroutines and only these timers pending.
	timer *time.Timer
}

// acquire hands out a connection to t as role: an idle cached one, a new
// one within the limits, or (at the limits) the next one released, waiting
// until ctx ends. Release it with release.
func (e *Engine) acquire(ctx context.Context, t data.Target, role data.Role) (*conn, error) {
	cred, err := credentials(t, role)
	if err != nil {
		return nil, err
	}
	k := connKey{t.ServiceID, role}
	for {
		e.mu.Lock()
		if e.closed {
			e.mu.Unlock()
			return nil, errClosed
		}
		list := e.conns[k]
		for _, c := range list {
			if !c.busy {
				c.busy = true
				if c.timer != nil {
					c.timer.Stop()
				}
				e.mu.Unlock()
				return c, nil
			}
		}
		if len(list) < e.maxPerKey && e.total >= e.maxConns {
			if victim := e.evictIdleLocked(); victim != nil {
				e.mu.Unlock()
				victim.close()
				continue
			}
		}
		if len(list) < e.maxPerKey && e.total < e.maxConns {
			c := &conn{key: k, busy: true}
			e.conns[k] = append(list, c)
			e.total++
			e.mu.Unlock()
			pc, err := e.connect(ctx, t, cred)
			if err != nil {
				e.release(c)
				return nil, loginError(ctx, err, data.ErrNotSetUp)
			}
			c.pg = pc
			return c, nil
		}
		e.waiters++
		wake := e.wake
		e.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
		}
		e.mu.Lock()
		e.waiters--
		e.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
}

// release returns c to the cache, or closes it when it is broken, stale or
// not idle (open transaction, closed by the server).
func (e *Engine) release(c *conn) {
	discard := c.broken || c.pg == nil || c.pg.IsClosed() || c.pg.PgConn().TxStatus() != 'I'
	e.mu.Lock()
	if discard || c.stale || e.closed {
		e.removeLocked(c)
		e.mu.Unlock()
		c.close()
		return
	}
	c.busy = false
	c.idleAt = time.Now()
	if c.timer == nil {
		c.timer = time.AfterFunc(e.idleTimeout, func() { e.closeIdle(c) })
	} else {
		c.timer.Reset(e.idleTimeout)
	}
	e.broadcastLocked()
	e.mu.Unlock()
}

// closeIdle is c's idle timer: it closes c if it is still idle, and drops
// the service's cached schema with its last connection.
func (e *Engine) closeIdle(c *conn) {
	e.mu.Lock()
	if c.busy || c.removed || time.Since(c.idleAt) < e.idleTimeout {
		e.mu.Unlock()
		return
	}
	e.removeLocked(c)
	if !e.hasConnsLocked(c.key.service) {
		delete(e.schemas, c.key.service)
	}
	e.mu.Unlock()
	c.close()
}

// evictIdleLocked removes the longest-idle connection to make room under
// MaxConns, or returns nil when every connection is busy.
func (e *Engine) evictIdleLocked() *conn {
	var victim *conn
	for _, list := range e.conns {
		for _, c := range list {
			if !c.busy && (victim == nil || c.idleAt.Before(victim.idleAt)) {
				victim = c
			}
		}
	}
	if victim != nil {
		e.removeLocked(victim)
	}
	return victim
}

// removeLocked takes c out of the cache and wakes waiters.
func (e *Engine) removeLocked(c *conn) {
	if c.removed {
		return
	}
	c.removed = true
	list := e.conns[c.key]
	for i, x := range list {
		if x == c {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(e.conns, c.key)
	} else {
		e.conns[c.key] = list
	}
	e.total--
	e.broadcastLocked()
}

// hasConnsLocked reports whether any connection of service is cached.
func (e *Engine) hasConnsLocked(service int64) bool {
	for k := range e.conns {
		if k.service == service {
			return true
		}
	}
	return false
}

// broadcastLocked wakes everyone waiting in acquire.
func (e *Engine) broadcastLocked() {
	if e.waiters > 0 {
		close(e.wake)
		e.wake = make(chan struct{})
	}
}

// dropLocked removes the connections whose key matches: idle ones are
// returned for closing, busy ones are marked stale and closed on release.
func (e *Engine) dropLocked(match func(connKey) bool) []*conn {
	var idle []*conn
	for k, list := range e.conns {
		if !match(k) {
			continue
		}
		for _, c := range list {
			if c.busy {
				c.stale = true
			} else {
				idle = append(idle, c)
			}
		}
	}
	for _, c := range idle {
		e.removeLocked(c)
	}
	return idle
}

// close stops c's timer and closes its connection. A broken connection
// gets a short deadline first so a stuck socket can't hold the caller.
func (c *conn) close() {
	if c.timer != nil {
		c.timer.Stop()
	}
	if c.pg == nil {
		return
	}
	if c.broken {
		_ = c.pg.PgConn().Conn().SetDeadline(time.Now().Add(closeTimeout))
	}
	// Background: a canceled context would make pgx send a cancel request.
	_ = c.pg.Close(context.Background())
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
	cn, err := e.acquire(ctx, t, role)
	if err != nil {
		return err
	}
	defer e.release(cn)
	if err := simpleExec(ctx, cn.pg, beginSQL(true, timeout, data.ReadLockTimeout, 0, appName(e.app, c.Actor))); err != nil {
		cn.broken = true
		return dbError(err)
	}
	ferr := fn(cn)
	if ctx.Err() != nil {
		cn.broken = true
		if ferr != nil {
			return ferr
		}
		return ctx.Err()
	}
	if err := simpleExec(ctx, cn.pg, "ROLLBACK"); err != nil {
		cn.broken = true
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
