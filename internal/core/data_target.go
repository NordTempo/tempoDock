package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/datapanel"
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

// errNotRunning means the service has no running container to dial; it is
// the panel's datapanel.ErrNotRunning (dataHost.Connect).
var errNotRunning = datapanel.ErrNotRunning

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
		nc, err := rt.Dial(ctx, node.DialRequest{ContainerID: conn.containerID, Port: conn.port})
		return nc, c.nodeErr(conn.serverID, err)
	}
}
