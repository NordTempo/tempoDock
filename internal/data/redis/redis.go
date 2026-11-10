// Package redis is the Redis and Valkey engine of the database panel (ADR
// 0072): a data.Engine and data.Keys on Dock's own RESP client
// (internal/data/redis/resp).
//
// Redis runs one command at a time for every client, so a slow command
// stalls the app. Every command the engine sends is O(1) or bounded by a
// count it chose: SCAN with COUNT, values a page at a time, MEMORY USAGE
// with 5 samples, never KEYS. The console checks a command against the
// reader's list and refuses whole-value reads of big keys.
//
// Dock's users are ACL users with command lists compiled in here
// (aclReader, ...), not categories, which shift between versions. Redis
// keeps ACL users in memory, so a restart drops them: a login refused for
// one of them is ErrNoPrivilege, and the panel's grants sync (SyncGrants)
// creates them again from the stored passwords.
//
// Connections are cached by internal/data/conncache like the other
// engines', closed data.IdleTimeout after their last use.
package redis

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/conncache"
	"github.com/NordTempo/tempoDock/internal/data/redis/resp"
)

const (
	// connectTimeout bounds dialing plus the login.
	connectTimeout = 10 * time.Second
	// callTimeout bounds one call's commands when ctx has no deadline.
	callTimeout = 10 * time.Second
	// minVersion is the oldest supported server: 7.0 (ACL selectors and
	// key permissions; Valkey starts at 7.2).
	minVersion = 70000
)

// Engine is the Redis and Valkey data.Engine. Create it with New; it is
// safe for concurrent use.
type Engine struct {
	conns *conncache.Cache[*conn]
	roles data.Roles
	app   string
}

// Options configure an engine; the zero Options is Tempo Dock's.
type Options struct {
	// Roles are the ACL user names; zero means data.DockRoles.
	Roles data.Roles
	// AppName names Dock's connections (CLIENT SETNAME); "" means "dock".
	AppName string
}

var (
	_ data.Engine = (*Engine)(nil)
	_ data.Keys   = (*Engine)(nil)
)

// New returns an engine. It opens nothing until the first call.
func New(o Options) *Engine {
	if o.Roles == (data.Roles{}) {
		o.Roles = data.DockRoles
	}
	if o.AppName == "" {
		o.AppName = "dock"
	}
	return &Engine{
		roles: o.Roles, app: o.AppName,
		conns: conncache.New(conncache.Options[*conn]{
			Reusable: func(c *conn) bool { return !c.Broken() && c.Pending() == 0 },
			Close:    func(c *conn, _ bool) { _ = c.Close() },
		}),
	}
}

// Kind is Redis (Valkey included).
func (e *Engine) Kind() apitypes.DataEngine { return apitypes.EngineRedis }

// conn is one logged-in connection.
type conn struct {
	*resp.Conn
	info data.ServerInfo
	// db is the selected logical database.
	db int
}

// cached is one connection held from the cache.
type cached = conncache.Conn[*conn]

// acquire hands out a connection to t as role. Release it with release.
func (e *Engine) acquire(ctx context.Context, t data.Target, role data.Role) (*cached, error) {
	cred, err := credentials(t, role)
	if err != nil {
		return nil, err
	}
	cn, err := e.conns.Acquire(ctx, conncache.Key{ID: t.ServiceID, Role: role}, func(ctx context.Context) (*conn, error) {
		return e.open(ctx, t, cred, role == data.RoleReader)
	})
	if err != nil {
		// Dock's users vanish when Redis restarts: sync them back.
		return nil, loginError(ctx, err, data.ErrNoPrivilege)
	}
	return cn, nil
}

func (e *Engine) release(cn *cached) { e.conns.Release(cn) }

// credentials picks the login for one of the engine's roles.
func credentials(t data.Target, role data.Role) (data.Credentials, error) {
	var cred data.Credentials
	switch role {
	case data.RoleReader:
		cred = t.Reader
	case data.RoleWriter:
		cred = t.Writer
	case data.RoleMonitor:
		cred = t.Monitor
	}
	if cred.User == "" || t.Dial == nil {
		return cred, fmt.Errorf("%w: no credentials for %s", data.ErrNotSetUp, role)
	}
	return cred, nil
}

// open dials t and logs in with HELLO (which also says the server's
// version and mode), names the connection and, for the reader, turns on
// CLIENT NO-TOUCH so browsing never changes the keys' LRU or LFU data
// (Redis 7.2+; ignored where unknown). One round trip after the dial.
func (e *Engine) open(ctx context.Context, t data.Target, cred data.Credentials, noTouch bool) (*conn, error) {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	nc, err := t.Dial(ctx)
	if err != nil {
		return nil, err
	}
	deadline, _ := ctx.Deadline()
	_ = nc.SetDeadline(deadline)
	c := &conn{Conn: resp.New(nc)}
	hello := []string{"HELLO", "2", "AUTH", cmp.Or(cred.User, "default"), cred.Password, "SETNAME", e.app}
	if cred.Password == "" && cmp.Or(cred.User, "default") == "default" {
		// A server without requirepass: the default user needs no AUTH.
		hello = []string{"HELLO", "2", "SETNAME", e.app}
	}
	cmds := [][]string{hello}
	if noTouch {
		cmds = append(cmds, []string{"CLIENT", "NO-TOUCH", "ON"})
	}
	replies, err := c.Pipeline(cmds...)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	if err := replies[0].Err(); err != nil {
		_ = c.Close()
		return nil, err
	}
	c.info, err = helloInfo(replies[0])
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	_ = nc.SetDeadline(time.Time{})
	return c, nil
}

// helloInfo reads the server and version from a HELLO reply (a flat
// list of names and values), and refuses cluster mode.
func helloInfo(v resp.Value) (data.ServerInfo, error) {
	fields := map[string]string{}
	for i := 0; i+1 < len(v.Arr); i += 2 {
		fields[v.Arr[i].Str] = v.Arr[i+1].Str
	}
	info := serverInfo(fields["server"], fields["version"])
	if fields["mode"] == "cluster" {
		return info, fmt.Errorf("%w: %s runs in cluster mode; the data panel works with standalone servers", data.ErrUnsupported, info.Version)
	}
	return info, nil
}

// serverInfo is "Redis 8.2.1" or "Valkey 8.1.1", numbered 80201.
func serverInfo(server, version string) data.ServerInfo {
	num := 0
	for i, part := range strings.SplitN(version, ".", 3) {
		n, _ := strconv.Atoi(part)
		num += n * []int{10000, 100, 1}[i]
	}
	name := "Redis"
	if strings.EqualFold(server, "valkey") {
		name = "Valkey"
	}
	return data.ServerInfo{Version: name + " " + version, VersionNum: num}
}

// unsupported is ErrUnsupported for a server older than 7.0, or nil.
func unsupported(info data.ServerInfo) error {
	if info.VersionNum < minVersion {
		return fmt.Errorf("%w: %s is older than 7.0; the data panel needs Redis 7.0 or Valkey 7.2 or later", data.ErrUnsupported, info.Version)
	}
	return nil
}

// loginError maps a connect error: a refused login becomes loginErr
// (ErrNoPrivilege for Dock's users, which a sync creates again;
// ErrLoginFailed for the owner), a full server ErrFull, anything else
// ErrUnreachable.
func loginError(ctx context.Context, err, loginErr error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, conncache.ErrClosed) || errors.Is(err, data.ErrNotSetUp) || errors.Is(err, data.ErrUnsupported) {
		return err
	}
	if se, ok := errors.AsType[*resp.ServerError](err); ok {
		switch {
		case se.Code() == "WRONGPASS" || se.Code() == "NOAUTH" || se.Code() == "NOPERM":
			return fmt.Errorf("%w: %s", loginErr, se.Msg)
		case strings.Contains(se.Msg, "max number of clients"):
			return fmt.Errorf("%w: %s", data.ErrFull, se.Msg)
		}
		return fmt.Errorf("%w: %s", data.ErrUnreachable, se.Msg)
	}
	return fmt.Errorf("%w: %w", data.ErrUnreachable, err)
}

// call runs fn on cn with a deadline from ctx (callTimeout without one),
// marking the connection broken when fn fails on the wire.
func call[T any](ctx context.Context, cn *cached, fn func(c *conn) (T, error)) (T, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(callTimeout)
	}
	_ = cn.Conn.NetConn().SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = cn.Conn.NetConn().SetDeadline(time.Now()) })
	out, err := fn(cn.Conn)
	if !stop() || cn.Conn.Broken() {
		cn.Broken = true
	}
	_ = cn.Conn.NetConn().SetDeadline(time.Time{})
	return out, err
}

// selectDB selects db on c when it isn't selected.
func (c *conn) selectDB(db int) error {
	if c.db == db {
		return nil
	}
	v, err := c.Do("SELECT", strconv.Itoa(db))
	if err != nil {
		return err
	}
	if err := v.Err(); err != nil {
		return fmt.Errorf("%w: %w", data.ErrInvalid, err)
	}
	c.db = db
	return nil
}

// Status logs in as the reader and checks the version.
func (e *Engine) Status(ctx context.Context, t data.Target) (data.ServerInfo, error) {
	cn, err := e.acquire(ctx, t, data.RoleReader)
	if err != nil {
		return data.ServerInfo{}, err
	}
	defer e.release(cn)
	return cn.Conn.info, unsupported(cn.Conn.info)
}

// ownerConn opens an uncached connection as the owner (the default user
// with requirepass, or the URL's user) and checks the version.
func (e *Engine) ownerConn(ctx context.Context, t data.Target, owner data.Credentials) (*conn, data.ServerInfo, error) {
	if t.Dial == nil {
		return nil, data.ServerInfo{}, fmt.Errorf("%w: no way to reach the database", data.ErrUnreachable)
	}
	c, err := e.open(ctx, t, owner, false)
	if err != nil {
		var info data.ServerInfo
		if errors.Is(err, data.ErrUnsupported) {
			return nil, info, err
		}
		return nil, info, loginError(ctx, err, data.ErrLoginFailed)
	}
	if err := unsupported(c.info); err != nil {
		_ = c.Close()
		return nil, c.info, err
	}
	_ = c.NetConn().SetDeadline(time.Now().Add(callTimeout))
	return c, c.info, nil
}

// Check logs in as the owner, checks the version and that the owner may
// manage ACL users, and changes nothing.
func (e *Engine) Check(ctx context.Context, t data.Target, owner data.Credentials) (data.ServerInfo, error) {
	c, info, err := e.ownerConn(ctx, t, owner)
	if err != nil {
		return info, err
	}
	defer func() { _ = c.Close() }()
	v, err := c.Do("ACL", "DRYRUN", cmp.Or(owner.User, "default"), "ACL", "SETUSER", "x")
	if err != nil {
		return info, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}
	if v.Kind != resp.Status {
		return info, fmt.Errorf("%w: %s may not manage ACL users (%s)", data.ErrCannotSetUp, cmp.Or(owner.User, "default"), v.Str)
	}
	return info, nil
}

// Command lists of Dock's ACL users. Each starts from nothing (-@all) and
// names every command, subcommands included, so a server version that
// moves commands between categories changes nothing. No KEYS, scripting,
// pub/sub, blocking commands, MONITOR, DEBUG or CONFIG.
var (
	aclConnection = []string{"hello", "ping", "select", "client|setname", "client|no-touch", "client|id"}
	// aclReader reads: the browser's bounded commands and the console's
	// read commands (whole-value ones are checked first, see query.go).
	aclReader = []string{
		"info", "dbsize", "scan", "type", "ttl", "pttl", "exists", "memory|usage", "object|encoding",
		"get", "getrange", "mget", "strlen", "getbit", "bitcount", "bitpos",
		"hget", "hmget", "hlen", "hstrlen", "hexists", "hscan", "hgetall", "hkeys", "hvals", "hrandfield",
		"lindex", "llen", "lrange", "lpos",
		"scard", "sismember", "smismember", "sscan", "smembers", "srandmember",
		"zcard", "zscore", "zmscore", "zrank", "zrevrank", "zcount", "zlexcount", "zscan", "zrange", "zrangebyscore", "zrevrange", "zrevrangebyscore",
		"xlen", "xrange", "xrevrange", "xinfo|stream", "xinfo|groups",
	}
	// aclWriter commits changesets (ADR 0072 phase 5): reads to compare
	// old values, the writes changesets render, and WATCH/MULTI/EXEC.
	aclWriter = []string{
		"type", "pttl", "exists", "get", "strlen", "hget", "hlen", "llen", "lindex", "scard", "sismember", "zcard", "zscore", "xlen",
		"set", "unlink", "pexpire", "persist", "hset", "hdel", "lset", "lpush", "rpush", "lrem", "sadd", "srem", "zadd", "zrem",
		"watch", "unwatch", "multi", "exec", "discard",
	}
	// aclMonitor reads the server's statistics, never keys (resetkeys):
	// no CONFIG GET, which would return requirepass.
	aclMonitor = []string{"info", "slowlog|get", "slowlog|len", "client|list", "client|info", "memory|stats", "dbsize"}
)

// aclRules renders ACL SETUSER for one user: reset, on, one password,
// keys (none for the monitor), and the commands.
func aclRules(name data.Role, password string, keys bool, cmds ...[]string) []string {
	out := []string{"ACL", "SETUSER", string(name), "reset", "on", ">" + password, "resetchannels"}
	if keys {
		out = append(out, "~*")
	}
	out = append(out, "-@all")
	for _, list := range cmds {
		for _, c := range list {
			out = append(out, "+"+c)
		}
	}
	return out
}

// users are the ACL users of t with their rules (the monitor's when t
// has its login).
func (e *Engine) users(t data.Target) [][]string {
	out := [][]string{
		aclRules(e.roles.Reader, t.Reader.Password, true, aclConnection, aclReader),
		aclRules(e.roles.Writer, t.Writer.Password, true, aclConnection, aclWriter),
	}
	if t.Monitor.User != "" {
		out = append(out, aclRules(e.roles.Monitor, t.Monitor.Password, false, aclConnection, aclMonitor))
	}
	return out
}

// applyUsers sends ACL SETUSER for each user as the owner.
func (e *Engine) applyUsers(c *conn, users [][]string) error {
	replies, err := c.Pipeline(users...)
	if err != nil {
		return fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}
	for i, r := range replies {
		if err := r.Err(); err != nil {
			return fmt.Errorf("create %s: %w", users[i][2], err)
		}
	}
	return nil
}

// Setup creates Dock's ACL users as the owner, or resets them with new
// passwords, and drops cached connections.
func (e *Engine) Setup(ctx context.Context, t data.Target, owner, reader, writer data.Credentials) (data.ServerInfo, error) {
	c, info, err := e.ownerConn(ctx, t, owner)
	if err != nil {
		return info, err
	}
	defer func() { _ = c.Close() }()
	t.Reader, t.Writer = reader, writer
	if err := e.applyUsers(c, e.users(t)); err != nil {
		return info, err
	}
	e.Forget(t.ServiceID)
	return info, nil
}

// SyncGrants applies Dock's ACL users again as the owner: after a
// restart (which drops them) or an ACL LOAD, and whenever the panel
// syncs. Redis has no per-column grants; the hash is of an empty schema.
func (e *Engine) SyncGrants(ctx context.Context, t data.Target, owner data.Credentials, _ []apitypes.DataColumnRef) (string, error) {
	c, _, err := e.ownerConn(ctx, t, owner)
	if err != nil {
		return "", err
	}
	defer func() { _ = c.Close() }()
	if err := e.applyUsers(c, e.users(t)); err != nil {
		return "", err
	}
	e.Forget(t.ServiceID)
	return data.GrantsHash(nil), nil
}

// EnsureMonitor creates or updates the monitor user as the owner.
func (e *Engine) EnsureMonitor(ctx context.Context, t data.Target, owner data.Credentials) error {
	c, _, err := e.ownerConn(ctx, t, owner)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if err := e.applyUsers(c, [][]string{aclRules(e.roles.Monitor, t.Monitor.Password, false, aclConnection, aclMonitor)}); err != nil {
		return err
	}
	e.conns.Drop(func(k conncache.Key) bool { return k.ID == t.ServiceID && k.Role == data.RoleMonitor })
	return nil
}

// Forget closes the database's idle connections (busy ones close when
// released).
func (e *Engine) Forget(id int64) { e.conns.Drop(func(k conncache.Key) bool { return k.ID == id }) }

// Close closes every connection.
func (e *Engine) Close() { e.conns.Close() }
