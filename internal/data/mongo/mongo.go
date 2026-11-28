// Package mongo is the MongoDB engine of the database panel (ADR 0110): a
// data.Engine and data.Tables on Dock's own client
// (internal/data/mongo/wire).
//
// A collection is a table ("database.collection"), its documents' top-level
// fields are the columns (typed bson:<type>), and _id is the key. Every
// cell is the field's value as canonical Extended JSON text (a missing
// field is null), so a
// value goes out and comes back without losing its type; filters,
// keys and changes are Extended JSON text too, typed as in the shell.
//
// Dock's users live in the admin database with built-in roles:
// readAnyDatabase for the reader, readWriteAnyDatabase for the writer and
// clusterMonitor for the monitor. MongoDB can't deny a role one field, so
// sensitive fields are masked by the engine (data.MasksItself), and the
// panel keeps masked callers off the console while any field is marked.
// A single server has no transactions: a changeset changes one document.
//
// Connections are cached by internal/data/conncache like the other
// engines', closed data.IdleTimeout after their last use.
package mongo

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
	"github.com/NordTempo/tempoDock/internal/data/mongo/wire"
)

const (
	// connectTimeout bounds dialing plus the login.
	connectTimeout = 10 * time.Second
	// callTimeout bounds one call's commands when ctx has no deadline.
	callTimeout = 30 * time.Second
	// authDB is where Dock's users and the template's root live.
	authDB = "admin"
)

// systemDBs are the server's own databases, never browsed.
var systemDBs = map[string]bool{"admin": true, "config": true, "local": true}

// Engine is the MongoDB data.Engine. Create it with New; it is safe for
// concurrent use.
type Engine struct {
	conns *conncache.Cache[*conn]
	roles data.Roles
	app   string

	mu      sync.Mutex
	schemas map[int64]*catalog
	// gen goes up on Forget, so a catalog read that started before one
	// isn't kept.
	gen    uint64
	closed bool
}

// Options configure an engine; the zero Options is Tempo Dock's.
type Options struct {
	// Roles are the user names; zero means data.DockRoles.
	Roles data.Roles
	// AppName names Dock's connections (the hello's application); ""
	// means "dock".
	AppName string
}

var (
	_ data.Engine      = (*Engine)(nil)
	_ data.Tables      = (*Engine)(nil)
	_ data.MasksItself = (*Engine)(nil)
)

// New returns an engine. It opens nothing until the first call.
func New(o Options) *Engine {
	if o.Roles == (data.Roles{}) {
		o.Roles = data.DockRoles
	}
	if o.AppName == "" {
		o.AppName = "dock"
	}
	e := &Engine{roles: o.Roles, app: o.AppName, schemas: map[int64]*catalog{}}
	e.conns = conncache.New(conncache.Options[*conn]{
		Reusable: func(c *conn) bool { return !c.Broken() },
		Close:    func(c *conn, _ bool) { _ = c.Close() },
		Emptied: func(id int64) {
			// Called with the cache's lock held; e.mu is never held
			// around the cache.
			e.mu.Lock()
			delete(e.schemas, id)
			e.mu.Unlock()
		},
	})
	return e
}

// Kind is MongoDB.
func (e *Engine) Kind() apitypes.DataEngine { return apitypes.EngineMongoDB }

// MasksItself: MongoDB has no field privileges.
func (e *Engine) MasksItself() {}

// conn is one logged-in connection.
type conn struct {
	*wire.Conn
	info data.ServerInfo
}

type cached = conncache.Conn[*conn]

// acquire hands out a connection to t as role. Release it with release.
func (e *Engine) acquire(ctx context.Context, t data.Target, role data.Role) (*cached, error) {
	cred, err := credentials(t, role)
	if err != nil {
		return nil, err
	}
	cn, err := e.conns.Acquire(ctx, conncache.Key{ID: t.ServiceID, Role: role}, func(ctx context.Context) (*conn, error) {
		return e.open(ctx, t, cred)
	})
	if err != nil {
		return nil, loginError(ctx, err, data.ErrNotSetUp)
	}
	return cn, nil
}

func (e *Engine) release(cn *cached) { e.conns.Release(cn) }

// credentials picks the login for one of the engine's roles. The masked
// reader logs in as the reader: masking is the engine's.
func credentials(t data.Target, role data.Role) (data.Credentials, error) {
	var cred data.Credentials
	switch role {
	case data.RoleReader, data.RoleReaderMasked:
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

// open dials t and logs in: hello, SCRAM-SHA-256 and buildInfo.
func (e *Engine) open(ctx context.Context, t data.Target, cred data.Credentials) (*conn, error) {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	nc, err := t.Dial(ctx)
	if err != nil {
		return nil, err
	}
	deadline, _ := ctx.Deadline()
	_ = nc.SetDeadline(deadline)
	// A caller that gives up cuts the login short too.
	stop := context.AfterFunc(ctx, func() { _ = nc.SetDeadline(time.Now()) })
	c := &conn{Conn: wire.New(nc)}
	hello, err := c.Handshake(e.app, cred.User, cred.Password, authDB)
	if !stop() && err == nil {
		err = ctx.Err() // the cut may land after the deadline is cleared below
	}
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	c.info = serverInfo(hello.Version)
	_ = nc.SetDeadline(time.Time{})
	return c, nil
}

// serverInfo is "MongoDB 8.0.38", numbered 80038.
func serverInfo(version string) data.ServerInfo {
	num := 0
	for i, part := range strings.SplitN(version, ".", 3) {
		n, _ := strconv.Atoi(strings.TrimFunc(part, func(r rune) bool { return r < '0' || r > '9' }))
		num += n * []int{10000, 100, 1}[i]
	}
	return data.ServerInfo{Version: "MongoDB " + version, VersionNum: num}
}

// loginError maps a failed dial or login: a refused login is loginErr.
func loginError(ctx context.Context, err, loginErr error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, conncache.ErrClosed) || errors.Is(err, data.ErrNotSetUp) || errors.Is(err, data.ErrUnsupported) {
		return err
	}
	if se, ok := errors.AsType[*wire.ServerError](err); ok {
		switch se.Code {
		case wire.CodeAuthFailed, wire.CodeUserNotFound:
			return fmt.Errorf("%w: %s", loginErr, se.Msg)
		}
		return fmt.Errorf("%w: %s", data.ErrUnreachable, se.Msg)
	}
	if strings.Contains(err.Error(), "wire version") {
		return fmt.Errorf("%w: %w", data.ErrUnsupported, err)
	}
	return fmt.Errorf("%w: %w", data.ErrUnreachable, err)
}

// run runs one command on cn with a deadline from ctx (callTimeout
// without one), marking the connection broken when it failed on the
// wire.
func run(ctx context.Context, cn *cached, db string, cmd wire.D) (wire.D, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(callTimeout)
	}
	nc := cn.Conn.NetConn()
	_ = nc.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = nc.SetDeadline(time.Now()) })
	reply, err := cn.Conn.Run(db, cmd)
	// A cancel that came as the command ended may still set its deadline
	// after this one clears it: the connection isn't reused then.
	if !stop() {
		cn.Broken = true
	}
	if cn.Conn.Broken() {
		cn.Broken = true
	} else {
		_ = nc.SetDeadline(time.Time{})
	}
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return reply, err
}

// owner logs in as the owner for one call; owner connections are never
// cached.
func (e *Engine) owner(ctx context.Context, t data.Target, owner data.Credentials) (*cached, error) {
	if t.Dial == nil {
		return nil, fmt.Errorf("%w: no way to reach the database", data.ErrUnreachable)
	}
	c, err := e.open(ctx, t, owner)
	if err != nil {
		return nil, loginError(ctx, err, data.ErrLoginFailed)
	}
	return &cached{Conn: c}, nil
}

// minVersion is MongoDB 6.0.
const minVersion = 60000

// Check logs in as the owner and checks the server and that the owner
// may create users (root, or userAdmin of admin).
func (e *Engine) Check(ctx context.Context, t data.Target, owner data.Credentials) (data.ServerInfo, error) {
	cn, err := e.owner(ctx, t, owner)
	if err != nil {
		return data.ServerInfo{}, err
	}
	defer func() { _ = cn.Conn.Close() }()
	info := cn.Conn.info
	if info.VersionNum < minVersion {
		return info, fmt.Errorf("%w: %s; the data panel needs MongoDB 6.0 or later", data.ErrUnsupported, info.Version)
	}
	status, err := run(ctx, cn, authDB, wire.D{{Key: "connectionStatus", Value: int32(1)}, {Key: "showPrivileges", Value: true}})
	if err != nil {
		return info, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}
	if !canCreateUsers(status) {
		return info, fmt.Errorf("%w: %s can't create users (it needs root or userAdminAnyDatabase)", data.ErrCannotSetUp, owner.User)
	}
	return info, nil
}

// canCreateUsers reads connectionStatus' privileges (showPrivileges), so
// a role that inherits them counts: setup creates and updates users in
// admin and grants them built-in roles there.
func canCreateUsers(status wire.D) bool {
	auth, _ := status.Get("authInfo")
	privs, _ := asDoc(auth).Get("authenticatedUserPrivileges")
	// updateUser, for users that exist: a new password and their roles.
	need := map[string]bool{"createUser": false, "grantRole": false, "revokeRole": false, "changePassword": false}
	for _, p := range asArray(privs) {
		pd := asDoc(p)
		res := asDoc(must(pd.Get("resource")))
		db, hasDB := res.Get("db")
		coll, _ := res.Get("collection")
		any, _ := res.Get("anyResource")
		onAdmin := hasDB && (db == authDB || db == "") && coll == ""
		if any != true && !onAdmin {
			continue
		}
		acts, _ := pd.Get("actions")
		for _, a := range asArray(acts) {
			if s, ok := a.(string); ok {
				if _, wanted := need[s]; wanted {
					need[s] = true
				}
			}
		}
	}
	for _, ok := range need {
		if !ok {
			return false
		}
	}
	return true
}

// Status logs in as the reader.
func (e *Engine) Status(ctx context.Context, t data.Target) (data.ServerInfo, error) {
	cn, err := e.acquire(ctx, t, data.RoleReader)
	if err != nil {
		return data.ServerInfo{}, err
	}
	defer e.release(cn)
	info := cn.Conn.info
	if info.VersionNum < minVersion {
		return info, fmt.Errorf("%w: %s", data.ErrUnsupported, info.Version)
	}
	return info, nil
}

// userRoles are each of Dock's users' built-in roles.
func (e *Engine) userRoles(role data.Role) wire.A {
	name := map[data.Role]string{
		e.roles.Reader: "readAnyDatabase", e.roles.Writer: "readWriteAnyDatabase", e.roles.Monitor: "clusterMonitor",
	}[role]
	return wire.A{wire.D{{Key: "role", Value: name}, {Key: "db", Value: authDB}}}
}

// Setup creates Dock's reader and writer users, or sets their passwords
// and roles when they exist, and drops cached connections.
func (e *Engine) Setup(ctx context.Context, t data.Target, owner, reader, writer data.Credentials) (data.ServerInfo, error) {
	info, err := e.Check(ctx, t, owner)
	if err != nil {
		return info, err
	}
	cn, err := e.owner(ctx, t, owner)
	if err != nil {
		return info, err
	}
	defer func() { _ = cn.Conn.Close() }()
	for _, u := range []data.Credentials{reader, writer} {
		if err := e.ensureUser(ctx, cn, data.Role(u.User), u.Password); err != nil {
			return info, err
		}
	}
	e.Forget(t.ServiceID)
	return info, nil
}

// ensureUser creates user with its role, or updates its password and
// roles.
func (e *Engine) ensureUser(ctx context.Context, cn *cached, user data.Role, password string) error {
	roles := e.userRoles(user)
	_, err := run(ctx, cn, authDB, wire.D{
		{Key: "createUser", Value: string(user)}, {Key: "pwd", Value: password}, {Key: "roles", Value: roles},
		{Key: "mechanisms", Value: wire.A{"SCRAM-SHA-256"}},
	})
	if se, ok := errors.AsType[*wire.ServerError](err); ok && se.Code == wire.CodeDuplicateUser {
		_, err = run(ctx, cn, authDB, wire.D{
			{Key: "updateUser", Value: string(user)}, {Key: "pwd", Value: password}, {Key: "roles", Value: roles},
			{Key: "mechanisms", Value: wire.A{"SCRAM-SHA-256"}},
		})
	}
	if err != nil {
		return fmt.Errorf("set up the user %s: %w", user, err)
	}
	return nil
}

// EnsureMonitor creates or updates the monitor user (clusterMonitor).
func (e *Engine) EnsureMonitor(ctx context.Context, t data.Target, owner data.Credentials) error {
	if t.Monitor.User == "" {
		return fmt.Errorf("%w: no monitor credentials", data.ErrNotSetUp)
	}
	cn, err := e.owner(ctx, t, owner)
	if err != nil {
		return err
	}
	defer func() { _ = cn.Conn.Close() }()
	return e.ensureUser(ctx, cn, data.Role(t.Monitor.User), t.Monitor.Password)
}

// SyncGrants has nothing to grant: the users' roles cover every database,
// and masking is the engine's. It returns the hash of the schema, so the
// panel sees the grants as current.
func (e *Engine) SyncGrants(ctx context.Context, t data.Target, _ data.Credentials, sensitive []apitypes.DataColumnRef) (string, error) {
	s, err := e.Schema(ctx, t, data.Call{}, sensitive)
	if err != nil {
		return "", err
	}
	return data.GrantsHash(s.Tables), nil
}

// RereadSchema drops a cached schema older than data.SchemaReread.
func (e *Engine) RereadSchema(serviceID int64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if cat := e.schemas[serviceID]; cat != nil && time.Since(cat.readAt) < data.SchemaReread {
		return false
	}
	delete(e.schemas, serviceID)
	return true
}

// Forget closes the database's idle connections and drops its schema.
func (e *Engine) Forget(id int64) {
	e.conns.Drop(func(k conncache.Key) bool { return k.ID == id })
	e.mu.Lock()
	delete(e.schemas, id)
	e.gen++
	e.mu.Unlock()
}

// Close closes every connection; later calls fail.
func (e *Engine) Close() {
	e.conns.Close()
	e.mu.Lock()
	e.closed = true
	clear(e.schemas)
	e.mu.Unlock()
}

// EnableQueryStats: MongoDB's profiler isn't turned on by Dock.
func (e *Engine) EnableQueryStats(context.Context, data.Target, data.Credentials) (apitypes.DataQueryStatsState, bool, error) {
	return apitypes.QueryStatsNotLoaded, false, nil
}

// ResetQueryStats: there are none.
func (e *Engine) ResetQueryStats(context.Context, data.Target, data.Credentials) error {
	return fmt.Errorf("%w: MongoDB has no query stats in the panel", data.ErrInvalid)
}
