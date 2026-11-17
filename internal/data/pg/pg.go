// Package pg is the Postgres engine of the database panel (ADR 0016): a
// data.Engine on pgx v5.
//
// Connections are dialed through data.Target.Dial (never a TCP address)
// and cached by internal/data/conncache per (service, role), at most
// data.MaxConnsPerKey per key and data.MaxConns in total, each closed by its
// own timer data.IdleTimeout after its last use. There is no pool
// goroutine: an idle engine holds nothing but the timers of connections
// still open.
//
// Every read runs as the reader role (Options.Roles; Dock's dock_reader,
// masked references and SQL as dock_reader_masked) in a READ ONLY
// transaction that is rolled back afterwards; commits run as the writer
// (dock_writer) in one transaction. The roles hold
// explicit grants on the user schemas only, never the system catalogs
// (SyncGrants). Results are
// requested in Postgres' text format and encoded to JSON cells straight from
// the wire bytes (apitypes.DataRow), without decoding into Go values.
//
// Query reuses its buffers between frames: send must be done with a frame
// (encode or copy it) before it returns.
package pg

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/conncache"
)

// Engine is the Postgres data.Engine. Create it with New; it is safe for
// concurrent use.
type Engine struct {
	// conns are the cached connections (internal/data/conncache).
	conns *conncache.Cache[*pgx.Conn]
	// mu guards schemas and closed.
	mu     sync.Mutex
	closed bool
	// schemas caches introspection per service for schemaTTL; a service's
	// entry is dropped with its last connection or by Forget.
	schemas   map[int64]*catalog
	schemaTTL time.Duration
	// countTimeout bounds each exact count of a never-analysed table
	// (countUnanalysed).
	countTimeout time.Duration

	// roles are the role names the engine creates and logs in as; app
	// is application_name's prefix (Options).
	roles data.Roles
	app   string
}

// Options configure an engine; the zero Options is Tempo Dock's.
type Options struct {
	// Roles are the role names in the user's database; zero means
	// data.DockRoles. tempoBase passes data.TempoBaseRoles, so both
	// products can manage one database.
	Roles data.Roles
	// AppName prefixes application_name ("<AppName>:<actor>"); "" means
	// "dock".
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
	e := &Engine{
		schemas:      map[int64]*catalog{},
		schemaTTL:    data.SchemaTTL,
		countTimeout: exactCountTimeout,
		roles:        o.Roles,
		app:          o.AppName,
	}
	e.conns = e.newCache(0, 0)
	return e
}

// Kind is Postgres.
func (e *Engine) Kind() apitypes.DataEngine { return apitypes.EnginePostgres }

// Redact replaces sql's literal values with ? and drops its comments.
func (e *Engine) Redact(sql string) string { return redact(sql) }

// Status logs in as the reader and checks the server version, that the
// role isn't a member of pg_read_all_data (older setups granted it; it
// also reads the system catalogs, password hashes included) and, with
// t.ReaderMasked set, that the masked reader exists (databases set up
// before it need setup again).
func (e *Engine) Status(ctx context.Context, t data.Target) (data.ServerInfo, error) {
	cn, err := e.acquire(ctx, t, data.RoleReader)
	if err != nil {
		return data.ServerInfo{}, err
	}
	defer e.release(cn)
	var num int
	var readsAll, masked bool
	err = cn.Conn.QueryRow(ctx, "SELECT current_setting('server_version_num')::int, pg_has_role(current_user, 'pg_read_all_data', 'MEMBER'), to_regrole($1) IS NOT NULL",
		string(e.roles.ReaderMasked)).Scan(&num, &readsAll, &masked)
	if err != nil {
		cn.Broken = true
		return data.ServerInfo{}, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}
	info := serverInfo(cn.Conn, num)
	if num < data.MinPostgresVersion {
		return info, unsupported(info)
	}
	if readsAll {
		return info, fmt.Errorf("%w: %s still has pg_read_all_data from an older setup, which also reads password hashes; run setup again", data.ErrNotSetUp, e.roles.Reader)
	}
	if t.ReaderMasked.User != "" && !masked {
		return info, fmt.Errorf("%w: %s is missing; run setup again", data.ErrNotSetUp, e.roles.ReaderMasked)
	}
	return info, nil
}

// Check logs in as owner, checks the server version and that the owner
// is a superuser or has CREATEROLE, and changes nothing.
func (e *Engine) Check(ctx context.Context, t data.Target, owner data.Credentials) (data.ServerInfo, error) {
	pg, info, err := e.ownerConn(ctx, t, owner)
	if err != nil {
		return info, err
	}
	defer func() { _ = pg.Close(context.Background()) }()
	var canCreate bool
	if err := pg.QueryRow(ctx, "SELECT rolsuper OR rolcreaterole FROM pg_roles WHERE rolname = current_user").Scan(&canCreate); err != nil {
		return info, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}
	if !canCreate {
		return info, fmt.Errorf("%w: %s is neither a superuser nor has CREATEROLE", data.ErrCannotSetUp, owner.User)
	}
	return info, nil
}

// serverInfo builds ServerInfo from server_version_num and the connection's
// server_version parameter ("17.2 (Debian ...)" becomes "17.2").
func serverInfo(pg *pgx.Conn, num int) data.ServerInfo {
	v := pg.PgConn().ParameterStatus("server_version")
	if i := strings.IndexByte(v, ' '); i > 0 {
		v = v[:i]
	}
	return data.ServerInfo{Version: v, VersionNum: num}
}

func unsupported(info data.ServerInfo) error {
	return fmt.Errorf("%w: Postgres %s is older than 14; the data panel needs Postgres 14 or later", data.ErrUnsupported, info.Version)
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

// Forget closes the service's idle connections (busy ones close when
// released) and drops its cached schema.
func (e *Engine) Forget(serviceID int64) {
	e.conns.Drop(func(k conncache.Key) bool { return k.ID == serviceID })
	e.mu.Lock()
	delete(e.schemas, serviceID)
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
