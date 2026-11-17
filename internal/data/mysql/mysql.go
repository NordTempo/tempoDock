// Package mysql is the MySQL and MariaDB engine of the database panel
// (ADR 0072): a data.Engine and data.Tables on Dock's own wire client
// (internal/data/mysql/wire).
//
// Connections are dialed through data.Target.Dial and cached by
// internal/data/conncache like Postgres': at most data.MaxConnsPerKey per
// (database, role), closed data.IdleTimeout after their last use, no
// goroutines. Dock's users also carry MAX_USER_CONNECTIONS (the cached
// ones plus one to kill a query), so the server enforces the bound too.
//
// Reads run as the reader, whose only privileges are SELECT and SHOW VIEW
// on the user databases, with a statement timeout (MySQL's
// max_execution_time, MariaDB's max_statement_time) and 1 s lock waits;
// a context that ends sends KILL QUERY from a second connection. SQL a
// caller wrote runs in a READ ONLY transaction. Commits run as the
// writer (DML only) in one transaction. Everything goes over the text
// protocol: values are rendered as literals by one function (literal),
// which is also what the changeset review shows.
package mysql

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/conncache"
	"github.com/NordTempo/tempoDock/internal/data/mysql/wire"
)

const (
	// connectTimeout bounds dialing plus the login.
	connectTimeout = 10 * time.Second
	// queryGrace is how long past its statement timeout a statement may
	// take before the engine kills it and gives up on the connection.
	queryGrace = 5 * time.Second
	// killTimeout bounds the connection that sends KILL QUERY.
	killTimeout = 5 * time.Second
	// readLockWait and writeLockWait are innodb_lock_wait_timeout and
	// lock_wait_timeout (metadata locks), in seconds: the server's
	// minimum is 1.
	readLockWait  = 1
	writeLockWait = 2
	// minMySQL and minMariaDB are the oldest supported versions.
	minMySQL   = 80000
	minMariaDB = 100600
)

// Engine is the MySQL and MariaDB data.Engine. Create it with New; it is
// safe for concurrent use.
type Engine struct {
	conns *conncache.Cache[*conn]
	// mu guards schemas and closed.
	mu     sync.Mutex
	closed bool
	// schemas caches introspection per database for schemaTTL; an entry
	// is dropped with its last connection or by Forget.
	schemas   map[int64]*catalog
	schemaTTL time.Duration

	roles data.Roles
	app   string
}

// Options configure an engine; the zero Options is Tempo Dock's.
type Options struct {
	// Roles are the user names in the server; zero means data.DockRoles.
	Roles data.Roles
	// AppName names Dock in connection attributes and statement comments;
	// "" means "dock".
	AppName string
}

var (
	_ data.Engine = (*Engine)(nil)
	_ data.Tables = (*Engine)(nil)
)

// New returns an engine with the limits of package data. It opens nothing
// until the first call.
func New(o Options) *Engine {
	if o.Roles == (data.Roles{}) {
		o.Roles = data.DockRoles
	}
	if o.AppName == "" {
		o.AppName = "dock"
	}
	e := &Engine{schemas: map[int64]*catalog{}, schemaTTL: data.SchemaTTL, roles: o.Roles, app: o.AppName}
	e.conns = e.newCache(0, 0)
	return e
}

// Kind is MySQL (MariaDB included).
func (e *Engine) Kind() apitypes.DataEngine { return apitypes.EngineMySQL }

// Redact replaces sql's literal values with ? and drops its comments.
func (e *Engine) Redact(sql string) string { return redact(sql) }

// conn is one connection with what the engine set on its session.
type conn struct {
	*wire.Conn
	// mariadb picks MariaDB's variants of statements.
	mariadb bool
	// timeout is the session's statement timeout now.
	timeout time.Duration
	// dial and cred open the connection that kills this one's query.
	dial func(context.Context) (*wire.Conn, error)
}

// cached is one connection held from the cache.
type cached = conncache.Conn[*conn]

// newCache returns the engine's connection cache; zero limits are package
// data's. Closing a database's last connection drops its cached schema
// (under the cache's lock: e.mu is never held while calling the cache).
func (e *Engine) newCache(idle time.Duration, maxConns int) *conncache.Cache[*conn] {
	return conncache.New(conncache.Options[*conn]{
		IdleTimeout: idle,
		Max:         maxConns,
		Reusable:    func(c *conn) bool { return !c.Broken() && !c.InTransaction() },
		Close:       func(c *conn, _ bool) { _ = c.Close() },
		Emptied: func(id int64) {
			e.mu.Lock()
			delete(e.schemas, id)
			e.mu.Unlock()
		},
	})
}

// acquire hands out a connection to t as role. Release it with release.
func (e *Engine) acquire(ctx context.Context, t data.Target, role data.Role) (*cached, error) {
	cred, err := credentials(t, role)
	if err != nil {
		return nil, err
	}
	cn, err := e.conns.Acquire(ctx, conncache.Key{ID: t.ServiceID, Role: role}, func(ctx context.Context) (*conn, error) {
		return e.open(ctx, t, cred, role)
	})
	if err != nil {
		return nil, loginError(ctx, err, data.ErrNotSetUp)
	}
	return cn, nil
}

// release returns cn to the cache (or closes it when broken).
func (e *Engine) release(cn *cached) { e.conns.Release(cn) }

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

// dialWire opens one logged-in wire connection to t as cred, with
// database as the default (none when empty).
func (e *Engine) dialWire(ctx context.Context, t data.Target, cred data.Credentials, database string) (*wire.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	nc, err := t.Dial(ctx)
	if err != nil {
		return nil, err
	}
	deadline, _ := ctx.Deadline()
	_ = nc.SetDeadline(deadline)
	c, err := wire.Connect(nc, wire.Config{
		User: cred.User, Password: cred.Password, Database: database,
		TLS: t.TLS, TLSOptional: t.TLSOptional,
		Attributes: map[string]string{"program_name": e.app},
	})
	if err != nil {
		return nil, err
	}
	_ = nc.SetDeadline(time.Time{})
	return c, nil
}

// open connects as role and sets the session up: UTC, lock waits, and
// for readers the statement timeout. One round trip after the login. The
// monitor has no default database: it may not use the app's.
func (e *Engine) open(ctx context.Context, t data.Target, cred data.Credentials, role data.Role) (*conn, error) {
	database := t.Database
	if role == data.RoleMonitor {
		database = ""
	}
	wc, err := e.dialWire(ctx, t, cred, database)
	if err != nil {
		return nil, err
	}
	c := &conn{Conn: wc, mariadb: isMariaDB(wc.Version)}
	c.dial = func(ctx context.Context) (*wire.Conn, error) { return e.dialWire(ctx, t, cred, database) }
	lock := readLockWait
	if role == data.RoleWriter {
		lock = writeLockWait
	}
	set := "SET time_zone = '+00:00', innodb_lock_wait_timeout = " + strconv.Itoa(lock) + ", lock_wait_timeout = " + strconv.Itoa(lock)
	if role != data.RoleWriter {
		c.timeout = apitypes.DataQueryTimeout
		set += ", " + timeoutVar(c.mariadb, c.timeout)
	}
	if err := c.exec(ctx, set); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// timeoutVar is the session statement timeout assignment.
func timeoutVar(mariadb bool, d time.Duration) string {
	if mariadb {
		return "max_statement_time = " + strconv.FormatFloat(d.Seconds(), 'f', -1, 64)
	}
	return "max_execution_time = " + strconv.FormatInt(d.Milliseconds(), 10)
}

// setTimeout sets the session's statement timeout to d when it differs.
func (c *conn) setTimeout(ctx context.Context, d time.Duration) error {
	if c.timeout == d {
		return nil
	}
	if err := c.exec(ctx, "SET "+timeoutVar(c.mariadb, d)); err != nil {
		return err
	}
	c.timeout = d
	return nil
}

// watch bounds a call on c by ctx: the connection's deadline is ctx's
// plus queryGrace, and a context that ends kills the running statement
// from a second connection (KILL QUERY, which a user may send for its own
// connections) and marks c broken. Call the returned stop when done.
func (c *conn) watch(ctx context.Context) (stop func()) {
	if d, ok := ctx.Deadline(); ok {
		_ = c.NetConn().SetDeadline(d.Add(queryGrace))
	}
	after := context.AfterFunc(ctx, func() {
		kctx, cancel := context.WithTimeout(context.Background(), killTimeout)
		defer cancel()
		if k, err := c.dial(kctx); err == nil {
			_ = k.NetConn().SetDeadline(time.Now().Add(killTimeout))
			_, _ = k.Exec("KILL QUERY " + strconv.FormatUint(uint64(c.ID), 10))
			_ = k.Close()
		}
		// Whatever the statement does now, give it queryGrace and no more.
		_ = c.NetConn().SetDeadline(time.Now().Add(queryGrace))
	})
	return func() {
		if !after() {
			// The kill ran: the connection's state is unknown.
			c.MarkBroken()
		}
		_ = c.NetConn().SetDeadline(time.Time{})
	}
}

// exec runs one statement without a result set, bounded by ctx.
func (c *conn) exec(ctx context.Context, sql string) error {
	stop := c.watch(ctx)
	defer stop()
	_, err := c.Exec(sql)
	return err
}

// isMariaDB reports whether a server version string is MariaDB's.
func isMariaDB(version string) bool { return strings.Contains(version, "MariaDB") }

// serverInfo parses a version string: "8.4.3" is 80403, MariaDB's
// "11.4.4-MariaDB-ubu2404" (or "5.5.5-10.6.18-MariaDB" from old
// handshakes) is shown as "MariaDB 11.4.4" and numbered 110404.
func serverInfo(version string) data.ServerInfo {
	v := version
	maria := isMariaDB(v)
	if maria {
		v = strings.TrimPrefix(v, "5.5.5-")
	}
	if i := strings.IndexAny(v, "-+ "); i > 0 {
		v = v[:i]
	}
	num := 0
	for i, part := range strings.SplitN(v, ".", 3) {
		n, _ := strconv.Atoi(part)
		num += n * []int{10000, 100, 1}[i]
	}
	info := data.ServerInfo{Version: v, VersionNum: num}
	if maria {
		info.Version = "MariaDB " + v
	}
	return info
}

// unsupported is ErrUnsupported for a server older than the engine
// supports, or nil.
func unsupported(info data.ServerInfo, maria bool) error {
	switch {
	case maria && info.VersionNum < minMariaDB:
		return fmt.Errorf("%w: %s is older than MariaDB 10.6; the data panel needs MariaDB 10.6 or later", data.ErrUnsupported, info.Version)
	case !maria && info.VersionNum < minMySQL:
		return fmt.Errorf("%w: MySQL %s is older than 8.0; the data panel needs MySQL 8.0 or later", data.ErrUnsupported, info.Version)
	}
	return nil
}

// Status logs in as the reader and checks the server version.
func (e *Engine) Status(ctx context.Context, t data.Target) (data.ServerInfo, error) {
	cn, err := e.acquire(ctx, t, data.RoleReader)
	if err != nil {
		return data.ServerInfo{}, err
	}
	defer e.release(cn)
	info := serverInfo(cn.Conn.Version)
	return info, unsupported(info, cn.Conn.mariadb)
}

// Check logs in as the owner, checks the version and that the owner may
// create users and grant (CREATE USER and GRANT OPTION), and changes
// nothing.
func (e *Engine) Check(ctx context.Context, t data.Target, owner data.Credentials) (data.ServerInfo, error) {
	c, info, err := e.ownerConn(ctx, t, owner)
	if err != nil {
		return info, err
	}
	defer func() { _ = c.Close() }()
	var can bool
	err = c.query(ctx, `SELECT COUNT(*) FROM information_schema.USER_PRIVILEGES
WHERE PRIVILEGE_TYPE = 'CREATE USER' AND IS_GRANTABLE = 'YES'
  AND GRANTEE = CONCAT('''', SUBSTRING_INDEX(CURRENT_USER(), '@', 1), '''@''', SUBSTRING_INDEX(CURRENT_USER(), '@', -1), '''')`, 1,
		func(row [][]byte) error { can = string(row[0]) != "0"; return nil })
	if err != nil {
		return info, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}
	if !can {
		return info, fmt.Errorf("%w: %s may not create users with grants (CREATE USER and GRANT OPTION)", data.ErrCannotSetUp, owner.User)
	}
	return info, nil
}

// ownerConn opens an uncached connection as the owner and checks the
// version.
func (e *Engine) ownerConn(ctx context.Context, t data.Target, owner data.Credentials) (*conn, data.ServerInfo, error) {
	if t.Dial == nil {
		return nil, data.ServerInfo{}, fmt.Errorf("%w: no way to reach the database", data.ErrUnreachable)
	}
	wc, err := e.dialWire(ctx, t, owner, t.Database)
	if err != nil {
		return nil, data.ServerInfo{}, loginError(ctx, err, data.ErrLoginFailed)
	}
	c := &conn{Conn: wc, mariadb: isMariaDB(wc.Version)}
	c.dial = func(ctx context.Context) (*wire.Conn, error) { return e.dialWire(ctx, t, owner, t.Database) }
	info := serverInfo(wc.Version)
	if err := unsupported(info, c.mariadb); err != nil {
		_ = c.Close()
		return nil, info, err
	}
	if err := c.exec(ctx, "SET time_zone = '+00:00', lock_wait_timeout = "+strconv.Itoa(writeLockWait)); err != nil {
		_ = c.Close()
		return nil, info, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}
	return c, info, nil
}

// query runs sql and calls row for each row (values valid during the
// call), bounded by ctx. The result must have at least cols columns, the
// ones row reads by position: a server that sends fewer gets an error, not
// an index past the row.
func (c *conn) query(ctx context.Context, sql string, cols int, row func([][]byte) error) error {
	stop := c.watch(ctx)
	defer stop()
	return c.Query(sql, func(got []wire.Column) error {
		if len(got) < cols {
			return fmt.Errorf("%w: the server sent %d columns, want %d", data.ErrUnreachable, len(got), cols)
		}
		return nil
	}, row, nil)
}

// RereadSchema implements data.Tables.
func (e *Engine) RereadSchema(serviceID int64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if cat := e.schemas[serviceID]; cat != nil && time.Since(cat.readAt) < data.SchemaReread {
		return false
	}
	delete(e.schemas, serviceID)
	return true
}

// Forget closes the database's idle connections (busy ones close when
// released) and drops its cached schema.
func (e *Engine) Forget(id int64) {
	e.conns.Drop(func(k conncache.Key) bool { return k.ID == id })
	e.mu.Lock()
	delete(e.schemas, id)
	e.mu.Unlock()
}

// Close closes every connection; busy ones close when released, and later
// calls fail.
func (e *Engine) Close() {
	e.conns.Close()
	e.mu.Lock()
	e.closed = true
	clear(e.schemas)
	e.mu.Unlock()
}

// Server error numbers the engine maps.
const (
	errAccessDenied       = 1045 // login refused
	errDBAccessDenied     = 1044
	errTooManyConns       = 1040
	errUserConns          = 1203 // MAX_USER_CONNECTIONS
	errTableAccessDenied  = 1142
	errColumnAccessDenied = 1143
	errNoSuchTable        = 1146
	errLockWait           = 1205
	errInterrupted        = 1317
	errMySQLTimeout       = 3024 // max_execution_time
	errMariaDBTimeout     = 1969 // max_statement_time
	errBadValue           = 1366 // incorrect value for a column (state HY000)
	errUnknownThread      = 1094 // KILL of a connection that is gone
)

// loginError maps a connect error: a refused login becomes loginErr
// (ErrNotSetUp for Dock's users, ErrLoginFailed for the owner), a full
// server ErrFull, anything else ErrUnreachable.
func loginError(ctx context.Context, err error, loginErr error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, conncache.ErrClosed) || errors.Is(err, data.ErrNotSetUp) {
		return err
	}
	if e, ok := errors.AsType[*wire.Error](err); ok {
		switch e.Code {
		case errTooManyConns, errUserConns:
			return fmt.Errorf("%w: %s", data.ErrFull, e.Message)
		case errAccessDenied, errDBAccessDenied:
			return fmt.Errorf("%w: %s", loginErr, e.Message)
		}
		return fmt.Errorf("%w: %s", data.ErrUnreachable, e.Message)
	}
	return fmt.Errorf("%w: %w", data.ErrUnreachable, err)
}

// dbError maps an error of a structured read or write: a missing
// privilege (a table created since the last grants sync) is
// ErrNoPrivilege, a missing table ErrNoTable, other errors in the request
// ErrInvalid with the server's message; anything else passes through.
func dbError(err error) error {
	e, ok := errors.AsType[*wire.Error](err)
	if !ok {
		return err
	}
	switch {
	case e.Code == errTableAccessDenied || e.Code == errColumnAccessDenied || e.Code == errDBAccessDenied:
		return fmt.Errorf("%w: %s", data.ErrNoPrivilege, e.Message)
	case e.Code == errNoSuchTable:
		return fmt.Errorf("%w: %s", data.ErrNoTable, e.Message)
	case strings.HasPrefix(e.State, "22") || strings.HasPrefix(e.State, "42") || e.Code == errBadValue:
		return fmt.Errorf("%w: %s", data.ErrInvalid, e.Message)
	}
	return err
}
