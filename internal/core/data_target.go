package core

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// defaultPostgresPort is where Postgres listens unless the URL or the
// service's port setting say otherwise.
const defaultPostgresPort = 5432

// postgresImages are the image repositories detected as Postgres (ADR 0016
// section 1); timescale/timescaledb matches as a prefix.
var postgresImages = map[string]bool{
	"postgres": true, "postgis/postgis": true, "pgvector/pgvector": true,
	"bitnami/postgresql": true, "supabase/postgres": true,
}

// detectDatabase says whether a service is a database and how that was
// decided: the database setting, else the image name. Engine "" means not
// a database.
func detectDatabase(s store.Service) (apitypes.DataEngine, apitypes.DataDetection) {
	switch apitypes.DatabaseSetting(s.Database) {
	case apitypes.DatabaseNone:
		return "", ""
	case apitypes.DatabasePostgres:
		return apitypes.EnginePostgres, apitypes.DetectedBySetting
	}
	repo := imageRepository(s.Image)
	if postgresImages[repo] || strings.HasPrefix(repo, "timescale/timescaledb") {
		return apitypes.EnginePostgres, apitypes.DetectedByImage
	}
	return "", ""
}

// imageRepository reduces an image reference to its repository on Docker
// Hub terms: no registry host, tag, digest or "library/" prefix.
// "docker.io/library/postgres:17@sha256:..." is "postgres".
func imageRepository(ref string) string {
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndexByte(ref, ':'); i > strings.LastIndexByte(ref, '/') {
		ref = ref[:i]
	}
	if first, rest, ok := strings.Cut(ref, "/"); ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		ref = rest
	}
	return strings.TrimPrefix(strings.ToLower(ref), "library/")
}

// dbConn is how the panel reaches a database service: its running
// container and port, the database name and the owner's credentials (used
// only by setup and masked-role syncs).
type dbConn struct {
	serverID    int64
	containerID string
	port        int
	database    string
	owner       data.Credentials
}

// errNotRunning means the service has no running container to dial.
var errNotRunning = errors.New("not running")

// dbConnection derives a database service's connection details from its
// active release's resolved variables (what the container runs with), per
// ADR 0016 section 1: the database_url_variable setting's variable, else
// the service's own DATABASE_URL, else POSTGRES_USER (default postgres),
// POSTGRES_PASSWORD and POSTGRES_DB (default: the user). The port is the
// URL's, else the service's port setting, else 5432. A service without a
// running container is errNotRunning; details that can't be derived are a
// validation error saying what to fix.
func (c *Core) dbConnection(ctx context.Context, ss svcScope) (dbConn, error) {
	rel, err := c.store.GetActiveRelease(ctx, ss.svc.ID)
	if store.IsNotFound(err) || err == nil && rel.ContainerID == "" {
		return dbConn{}, errNotRunning
	}
	if err != nil {
		return dbConn{}, err
	}
	if c.getStatus(ss.svc.ID).State != apitypes.ServiceRunning {
		return dbConn{}, errNotRunning
	}
	cfg, err := c.openConfig(rel.Config)
	if err != nil {
		return dbConn{}, fmt.Errorf("read release %d config: %w", rel.Number, err)
	}
	conn := dbConn{serverID: ss.env.ServerID, containerID: rel.ContainerID, port: int(ss.svc.Port)}
	if conn.port == 0 {
		conn.port = defaultPostgresPort
	}

	name, raw := "", ""
	switch v := ss.svc.DatabaseUrlVariable; {
	case v != "":
		name = v
		if raw, err = c.variableValue(ctx, ss, cfg.Env, v); err != nil {
			return dbConn{}, err
		}
	case cfg.Env["DATABASE_URL"] != "":
		name, raw = "DATABASE_URL", cfg.Env["DATABASE_URL"]
	default:
		conn.owner = data.Credentials{User: cfg.Env["POSTGRES_USER"], Password: cfg.Env["POSTGRES_PASSWORD"]}
		if conn.owner.User == "" {
			conn.owner.User = "postgres"
		}
		conn.database = cfg.Env["POSTGRES_DB"]
		if conn.database == "" {
			conn.database = conn.owner.User
		}
		return conn, nil
	}
	if err := parseDatabaseURL(raw, ss.svc.Name, &conn); err != nil {
		return dbConn{}, invalid("%s: %v; set database_url_variable to a variable with this database's postgres:// URL", name, err)
	}
	return conn, nil
}

// variableValue resolves the database_url_variable setting: KEY is the
// service's own resolved variable; service.KEY (or shared.KEY) is resolved
// from the environment's live variables.
func (c *Core) variableValue(ctx context.Context, ss svcScope, own map[string]string, name string) (string, error) {
	scope, key, other := strings.Cut(name, ".")
	if !other || scope == ss.svc.Name {
		if !other {
			key = name
		}
		if v, ok := own[key]; ok {
			return v, nil
		}
		return "", invalid("database_url_variable is %s, but %s has no variable %s in its active release; set it, deploy, or change the setting", name, ss.svc.Name, key)
	}
	svcs, err := c.store.ListServices(ctx, ss.env.ID)
	if err != nil {
		return "", err
	}
	ev, err := loadEnvVars(ctx, c.store.Queries, ss.envScope, svcs, nil)
	if err != nil {
		return "", err
	}
	r, err := c.envResolver(ev, 0, false)
	if err != nil {
		return "", err
	}
	if _, ok := r.vars[scope][key]; !ok {
		return "", invalid("database_url_variable is %s, but %s has no variable %s; set it or change the setting", name, scope, key)
	}
	return r.value(scope, key)
}

// parseDatabaseURL reads user, password, database and port from a
// postgres:// URL into conn. The host must be this service: the panel
// always dials the service's own container.
func parseDatabaseURL(raw, service string, conn *dbConn) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return errors.New("not a postgres:// URL")
	}
	switch h := u.Hostname(); h {
	case service, "localhost", "127.0.0.1", "::1", "":
	default:
		return fmt.Errorf("host %s isn't this service; external databases aren't supported", h)
	}
	if u.User == nil || u.User.Username() == "" {
		return errors.New("the URL has no user")
	}
	conn.owner.User = u.User.Username()
	conn.owner.Password, _ = u.User.Password()
	conn.database = strings.TrimPrefix(u.Path, "/")
	if conn.database == "" {
		conn.database = conn.owner.User
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			return fmt.Errorf("port %q is not valid", p)
		}
		conn.port = n
	}
	return nil
}

// dial returns a Target.Dial for the connection: the node runtime of the
// service's server dials its container on the environment network.
func (c *Core) dial(conn dbConn) func(ctx context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		rt, err := c.runtimeFor(conn.serverID)
		if err != nil {
			return nil, err
		}
		return rt.Dial(ctx, node.DialRequest{ContainerID: conn.containerID, Port: conn.port})
	}
}

// newRolePassword generates a Dock role's password: 32 random bytes as
// URL-safe base64, printable ASCII as the engine requires.
func newRolePassword() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// maskedPassword derives the masked reader's password from the reader's,
// so it needs no column of its own and rotates with it. HMAC keeps the
// reader's password from being recovered from it.
func maskedPassword(readerPassword string) string {
	m := hmac.New(sha256.New, []byte(readerPassword))
	m.Write([]byte(data.RoleReaderMasked))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// dbTarget is a database service resolved for one call: who asks, what
// they may do, and how the engine reaches it.
type dbTarget struct {
	ss     svcScope
	engine apitypes.DataEngine
	by     apitypes.DataDetection
	perm   apitypes.DataPermission
	// access is the setup record; nil before setup.
	access *store.DataAccess
	// conn and t are set by connect.
	conn dbConn
	t    data.Target
	// sensitive are the marked columns (loaded by connect).
	sensitive []apitypes.DataColumnRef
}

// dataService loads a database service the actor can see, with its
// detection and the actor's permission. Services that aren't databases
// are not_found.
func (c *Core) dataService(ctx context.Context, a Actor, ref ServiceRef) (*dbTarget, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "using the database panel")
	if err != nil {
		return nil, err
	}
	engine, by := detectDatabase(ss.svc)
	if engine == "" {
		return nil, notFound("%s is not a database; if it is Postgres, set its database setting to postgres", ss)
	}
	perm, err := c.dataPermission(ctx, a, ss.envScope)
	if err != nil {
		return nil, err
	}
	dt := &dbTarget{ss: ss, engine: engine, by: by, perm: perm}
	acc, err := c.store.GetDataAccess(ctx, ss.svc.ID)
	switch {
	case err == nil:
		dt.access = &acc
	case !store.IsNotFound(err):
		return nil, err
	}
	return dt, nil
}

// dataTarget is dataService plus a level check, a set-up check and
// connect: everything an engine call needs.
func (c *Core) dataTarget(ctx context.Context, a Actor, ref ServiceRef, need apitypes.DataLevel, action string) (*dbTarget, error) {
	dt, err := c.dataService(ctx, a, ref)
	if err != nil {
		return nil, err
	}
	if err := requireLevel(dt.perm, need, action); err != nil {
		return nil, err
	}
	if dt.access == nil {
		return nil, errNotSetUp(dt)
	}
	return dt, c.connect(ctx, dt)
}

// errNotSetUp says data access isn't set up and who can fix it.
func errNotSetUp(dt *dbTarget) error {
	return errf(apitypes.CodeConflict, "data access is not set up for %s; an admin can run `dock db setup`", dt.ss)
}

// connect derives the connection, opens the role passwords and loads the
// sensitive columns.
func (c *Core) connect(ctx context.Context, dt *dbTarget) error {
	conn, err := c.dbConnection(ctx, dt.ss)
	if errors.Is(err, errNotRunning) {
		return errf(apitypes.CodeConflict, "%s is not running; deploy or restart it to use its data", dt.ss)
	}
	if err != nil {
		return err
	}
	if err := c.useConn(ctx, dt, conn); err != nil {
		return err
	}
	return c.loadSensitive(ctx, dt)
}

// useConn builds the engine target for a derived connection, with the
// role passwords once set up.
func (c *Core) useConn(_ context.Context, dt *dbTarget, conn dbConn) error {
	dt.conn = conn
	dt.t = data.Target{ServiceID: dt.ss.svc.ID, Dial: c.dial(conn), Database: conn.database}
	if dt.access != nil {
		reader, err := c.box.Open(dt.access.ReaderSecret)
		if err != nil {
			return fmt.Errorf("open reader password: %w", err)
		}
		writer, err := c.box.Open(dt.access.WriterSecret)
		if err != nil {
			return fmt.Errorf("open writer password: %w", err)
		}
		dt.t.Reader = data.Credentials{User: string(data.RoleReader), Password: string(reader)}
		dt.t.Writer = data.Credentials{User: string(data.RoleWriter), Password: string(writer)}
		dt.t.ReaderMasked = data.Credentials{User: string(data.RoleReaderMasked), Password: maskedPassword(string(reader))}
	}
	return nil
}

// loadSensitive loads the database's sensitive columns.
func (c *Core) loadSensitive(ctx context.Context, dt *dbTarget) error {
	rows, err := c.store.ListSensitiveColumns(ctx, dt.ss.svc.ID)
	if err != nil {
		return err
	}
	dt.sensitive = make([]apitypes.DataColumnRef, len(rows))
	for i, r := range rows {
		dt.sensitive[i] = apitypes.DataColumnRef{Schema: r.SchemaName, Table: r.TableName, Column: r.ColumnName}
	}
	return nil
}

// call is the engine call context for the actor. Sensitive columns are
// masked unless the actor may see them (sensitive visible) and reveals
// them: explicitly for pages (DataPageRequest.Reveal, audited as
// data.reveal), implicitly for SQL and changesets, whose every statement
// is audited with its text.
func (dt *dbTarget) call(a Actor, reveal bool) data.Call {
	return data.Call{Actor: a.Name, Masked: !reveal || dt.perm.Sensitive != apitypes.SensitiveVisible, Sensitive: dt.sensitive}
}

// dataError maps an engine error to what the caller should do. Errors
// that aren't the engine's pass through.
func dataError(dt *dbTarget, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, data.ErrNotSetUp):
		return errf(apitypes.CodeConflict, "Dock's roles in %s are missing or their passwords changed (%v); an admin can run `dock db setup` again", dt.ss, err)
	case errors.Is(err, data.ErrUnsupported):
		return invalid("%v; the database panel needs Postgres 14 or later", err)
	case errors.Is(err, data.ErrLoginFailed):
		return invalid("%v. If POSTGRES_PASSWORD changed after the database was created, set database_url_variable to a variable with the current postgres:// URL", err)
	case errors.Is(err, data.ErrUnreachable):
		return errf(apitypes.CodeConflict, "can't reach %s: %v; check that it is running and healthy", dt.ss, err)
	case errors.Is(err, data.ErrNoTable):
		return notFound("%v in %s", err, dt.ss)
	case errors.Is(err, data.ErrInvalid):
		return invalid("%v", err)
	}
	return err
}
