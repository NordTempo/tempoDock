package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
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

// engineConn is how core finds an engine's databases and their owner:
// image repositories (ADR 0016 section 1), URL schemes, the default port,
// the variables that may hold a URL, and the owner from the image's own
// variables when there is no URL.
type engineConn struct {
	images  map[string]bool
	schemes []string
	port    int
	urlVars []string
	// defaultUser is the owner when a URL names none (Redis' default
	// user); "" means a URL must name one.
	defaultUser string
	// owner reads the owner and database from the container's variables.
	owner func(env map[string]string) (data.Credentials, string)
}

// engineConns are the engines Dock detects, in detection order.
var engineConns = map[apitypes.DataEngine]engineConn{
	apitypes.EnginePostgres: {
		images: map[string]bool{
			"postgres": true, "postgis/postgis": true, "pgvector/pgvector": true,
			"bitnami/postgresql": true, "supabase/postgres": true,
		},
		schemes: []string{"postgres", "postgresql"},
		port:    defaultPostgresPort,
		urlVars: []string{"DATABASE_URL"},
		// POSTGRES_USER (default postgres), POSTGRES_PASSWORD, POSTGRES_DB
		// (default: the user).
		owner: func(env map[string]string) (data.Credentials, string) {
			user := cmp.Or(env["POSTGRES_USER"], "postgres")
			return data.Credentials{User: user, Password: env["POSTGRES_PASSWORD"]}, cmp.Or(env["POSTGRES_DB"], user)
		},
	},
	apitypes.EngineMySQL: {
		images: map[string]bool{
			"mysql": true, "mysql/mysql-server": true, "mariadb": true, "bitnami/mysql": true,
			"bitnami/mariadb": true, "percona": true, "percona/percona-server": true,
		},
		schemes: []string{"mysql", "mariadb"},
		port:    3306,
		urlVars: []string{"DATABASE_URL", "MYSQL_URL"},
		// root with MYSQL_ROOT_PASSWORD (MariaDB's images also read
		// MARIADB_*), MYSQL_DATABASE; no database means every user
		// database.
		owner: func(env map[string]string) (data.Credentials, string) {
			return data.Credentials{User: "root", Password: mysqlRootPassword(env)},
				cmp.Or(env["MYSQL_DATABASE"], env["MARIADB_DATABASE"])
		},
	},
	apitypes.EngineRedis: {
		images: map[string]bool{
			"redis": true, "valkey/valkey": true, "bitnami/redis": true, "bitnami/valkey": true,
			"redis/redis-stack-server": true,
		},
		schemes:     []string{"redis"},
		port:        6379,
		urlVars:     []string{"REDIS_URL"},
		defaultUser: "default",
		// The default user with REDIS_PASSWORD (the template's and
		// bitnami's), else no password; logical database 0. A URL's path
		// names another (/2).
		owner: func(env map[string]string) (data.Credentials, string) {
			return data.Credentials{User: "default", Password: env["REDIS_PASSWORD"]}, "0"
		},
	},
	apitypes.EngineMongoDB: {
		images: map[string]bool{
			"mongo": true, "bitnami/mongodb": true, "mongodb/mongodb-community-server": true,
		},
		schemes: []string{"mongodb"},
		port:    mongoPort,
		urlVars: []string{"MONGODB_URI", "MONGO_URL"},
		// The image's root user (MONGO_INITDB_ROOT_*), who lives in
		// admin; the console starts in the URL's database, else
		// MONGO_INITDB_DATABASE.
		owner: func(env map[string]string) (data.Credentials, string) {
			user, password := mongoRoot(env)
			return data.Credentials{User: user, Password: password}, env["MONGO_INITDB_DATABASE"]
		},
	},
}

// detectDatabase says whether a service is a database and how that was
// decided: the database setting, else the image name. Engine "" means not
// a database.
func detectDatabase(s store.Service) (apitypes.DataEngine, apitypes.DataDetection) {
	switch setting := apitypes.DatabaseSetting(s.Database); setting {
	case apitypes.DatabaseDetect:
	case apitypes.DatabaseNone:
		return "", ""
	default:
		if _, ok := engineConns[apitypes.DataEngine(setting)]; ok {
			return apitypes.DataEngine(setting), apitypes.DetectedBySetting
		}
		return "", ""
	}
	repo := imageRepository(s.Image)
	if strings.HasPrefix(repo, "timescale/timescaledb") {
		return apitypes.EnginePostgres, apitypes.DetectedByImage
	}
	for engine, ec := range engineConns {
		if ec.images[repo] {
			return engine, apitypes.DetectedByImage
		}
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
// the service's own URL variable (DATABASE_URL; MySQL also MYSQL_URL),
// else the image's own variables (engineConn.owner). The port is the
// URL's, else the service's port setting, else the engine's. A service
// without a running container is errNotRunning; details that can't be
// derived are a validation error saying what to fix.
func (c *Core) dbConnection(ctx context.Context, ss svcScope) (dbConn, error) {
	engine, _ := detectDatabase(ss.svc)
	ec, ok := engineConns[engine]
	if !ok {
		return dbConn{}, invalid("%s is not a database", ss)
	}
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
	cfg, err := c.openConfig(rel)
	if err != nil {
		return dbConn{}, fmt.Errorf("read release %d config: %w", rel.Number, err)
	}
	conn := dbConn{serverID: ss.env.ServerID, containerID: rel.ContainerID, port: int(ss.svc.Port)}
	if conn.port == 0 {
		conn.port = ec.port
	}

	name, raw := ss.svc.DatabaseUrlVariable, ""
	if name != "" {
		if raw, err = c.variableValue(ctx, ss, cfg.Env, name); err != nil {
			return dbConn{}, err
		}
	} else {
		for _, v := range ec.urlVars {
			if cfg.Env[v] != "" {
				name, raw = v, cfg.Env[v]
				break
			}
		}
	}
	if name == "" {
		conn.owner, conn.database = ec.owner(cfg.Env)
		return conn, nil
	}
	if err := parseDatabaseURL(raw, ss.svc.Name, ec, &conn); err != nil {
		return dbConn{}, invalid("%s: %v; set database_url_variable to a variable with this database's %s:// URL", name, err, ec.schemes[0])
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

// parseDatabaseURL reads user, password, database and port from a URL
// with one of ec's schemes into conn. The host must be this service: the
// panel always dials the service's own container. Postgres URLs without a
// database name the user's; MySQL ones mean every user database; Redis
// ones database 0, and the default user when they name none.
func parseDatabaseURL(raw, service string, ec engineConn, conn *dbConn) error {
	schemes := ec.schemes
	u, err := url.Parse(raw)
	if err != nil || !slices.Contains(schemes, u.Scheme) {
		return fmt.Errorf("not a %s:// URL", schemes[0])
	}
	switch h := u.Hostname(); h {
	case service, "localhost", "127.0.0.1", "::1", "":
	default:
		return fmt.Errorf("host %s isn't this service; external databases aren't supported", h)
	}
	if (u.User == nil || u.User.Username() == "") && ec.defaultUser == "" {
		return errors.New("the URL has no user")
	}
	conn.owner.User = cmp.Or(u.User.Username(), ec.defaultUser)
	conn.owner.Password, _ = u.User.Password()
	conn.database = strings.TrimPrefix(u.Path, "/")
	switch {
	case conn.database == "" && slices.Contains(schemes, "postgres"):
		conn.database = conn.owner.User
	case slices.Contains(schemes, "redis"):
		if conn.database == "" {
			conn.database = "0"
		}
		if n, err := strconv.Atoi(conn.database); err != nil || n < 0 {
			return fmt.Errorf("database %q isn't a number", conn.database)
		}
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
