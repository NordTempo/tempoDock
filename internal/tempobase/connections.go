package tempobase

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbstore"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
)

// A connection is a saved Postgres URL (ADR 0024 section 2). The full URL
// is sealed with the key file; host, port, database, user and TLS mode are
// kept in the clear for listing. Responses never carry the password, and
// neither do errors or audit summaries.

// dialTimeout bounds opening a TCP connection to a database.
const dialTimeout = 10 * time.Second

// connName is a connection's name: used in URLs.
var connName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// connURL is a parsed connection URL.
type connURL struct {
	Host     string
	Port     int
	Database string
	User     string
	Password string
	TLS      tbtypes.TLSMode
}

// parseConnURL parses a postgres:// or postgresql:// URL. Its errors
// never contain the URL: it holds a password.
func parseConnURL(raw string) (connURL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return connURL{}, invalid("the URL is not valid (%v); use postgres://user:password@host:5432/database", err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return connURL{}, invalid("the URL must start with postgres:// or postgresql://")
	}
	if u.Opaque != "" || u.Fragment != "" {
		return connURL{}, invalid("the URL is not valid; use postgres://user:password@host:5432/database")
	}
	c := connURL{Host: u.Hostname(), Port: 5432, TLS: tbtypes.TLSPrefer}
	switch {
	case c.Host == "":
		return connURL{}, invalid("the URL needs a host; Unix sockets are not supported")
	case strings.HasPrefix(c.Host, "/") || strings.Contains(u.Host, ","):
		return connURL{}, invalid("the URL must name one host; Unix sockets and host lists are not supported")
	}
	if p := u.Port(); p != "" {
		if c.Port, err = strconv.Atoi(p); err != nil || c.Port < 1 || c.Port > 65535 {
			return connURL{}, invalid("the URL's port must be 1-65535")
		}
	}
	if u.User != nil {
		c.User = u.User.Username()
		c.Password, _ = u.User.Password()
	}
	if c.User == "" {
		return connURL{}, invalid("the URL needs a user: postgres://user:password@host/database")
	}
	c.Database = strings.TrimPrefix(u.Path, "/")
	if strings.Contains(c.Database, "/") {
		return connURL{}, invalid("the URL's path must be the database name alone")
	}
	if c.Database == "" {
		c.Database = c.User // libpq's default
	}
	for k, v := range u.Query() {
		if k != "sslmode" {
			return connURL{}, invalid("URL parameter %q is not supported; the only one is sslmode (disable, prefer, require, verify-ca, verify-full)", k)
		}
		switch m := tbtypes.TLSMode(v[len(v)-1]); m {
		case tbtypes.TLSDisable, tbtypes.TLSPrefer, tbtypes.TLSRequire, tbtypes.TLSVerifyCA, tbtypes.TLSVerifyFull:
			c.TLS = m
		default:
			return connURL{}, invalid("sslmode %q must be disable, prefer, require, verify-ca or verify-full", v[len(v)-1])
		}
	}
	return c, nil
}

// address is host:port.
func (c connURL) address() string { return net.JoinHostPort(c.Host, strconv.Itoa(c.Port)) }

// tlsConfig is how to speak TLS to host in mode, as libpq's sslmode:
// prefer and require encrypt without verifying (require with a CA
// verifies like verify-ca), verify-ca checks the chain, verify-full also
// the host name. CAs come from caPEM, else the system roots. optional
// falls back to plaintext when the server refuses TLS (prefer).
func tlsConfig(mode tbtypes.TLSMode, host, caPEM string) (cfg *tls.Config, optional bool, err error) {
	var roots *x509.CertPool
	if caPEM != "" {
		if mode == tbtypes.TLSDisable || mode == tbtypes.TLSPrefer {
			return nil, false, invalid("a CA certificate needs sslmode require, verify-ca or verify-full")
		}
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM([]byte(caPEM)) {
			return nil, false, invalid("the CA certificate is not valid PEM; paste the -----BEGIN CERTIFICATE----- block")
		}
		if mode == tbtypes.TLSRequire {
			mode = tbtypes.TLSVerifyCA
		}
	}
	cfg = &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	switch mode {
	case tbtypes.TLSDisable:
		return nil, false, nil
	case tbtypes.TLSPrefer, tbtypes.TLSRequire:
		cfg.InsecureSkipVerify = true // libpq: encrypt, don't verify
		return cfg, mode == tbtypes.TLSPrefer, nil
	case tbtypes.TLSVerifyCA:
		// Verify the chain but not the name: Go has no such mode, so skip
		// its check and verify by hand.
		cfg.InsecureSkipVerify = true
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("the server sent no certificate")
			}
			opts := x509.VerifyOptions{Roots: roots, Intermediates: x509.NewCertPool()}
			for _, c := range cs.PeerCertificates[1:] {
				opts.Intermediates.AddCert(c)
			}
			_, err := cs.PeerCertificates[0].Verify(opts)
			return err
		}
		return cfg, false, nil
	}
	cfg.RootCAs = roots
	return cfg, false, nil
}

// redactedURL is a connection's URL without its password.
func redactedURL(c tbstore.Connection) string {
	u := url.URL{
		Scheme: "postgres", User: url.User(c.Username), Host: net.JoinHostPort(c.Host, strconv.FormatInt(c.Port, 10)),
		Path: "/" + c.Database, RawQuery: "sslmode=" + c.TlsMode,
	}
	return u.String()
}

// describe names a connection's target for messages: user@host:port/db.
func (c connURL) describe() string {
	return c.User + "@" + c.address() + "/" + c.Database
}

// conn opens and parses a connection's sealed URL and says how to reach
// it: the datapanel.Conn that Host.Connect returns.
func (a *App) conn(c *tbstore.Connection) (datapanel.Conn, error) {
	raw, err := a.box.Open(c.UrlSecret, sealedConnURL.AAD(c.Name))
	if err != nil {
		return datapanel.Conn{}, fmt.Errorf("connection %s: %w", c.Name, err)
	}
	u, err := parseConnURL(string(raw))
	if err != nil {
		return datapanel.Conn{}, err
	}
	return dialConn(u, c.CaPem)
}

// dialConn is how to reach u: a TCP dial to its host and port, TLS from
// its mode, and its user as the owner.
func dialConn(u connURL, caPEM string) (datapanel.Conn, error) {
	cfg, optional, err := tlsConfig(u.TLS, u.Host, caPEM)
	if err != nil {
		return datapanel.Conn{}, err
	}
	addr := u.address()
	return datapanel.Conn{
		Dial: func(ctx context.Context) (net.Conn, error) {
			d := net.Dialer{Timeout: dialTimeout}
			return d.DialContext(ctx, "tcp", addr)
		},
		TLS: cfg, TLSOptional: optional, Database: u.Database,
		Owner: data.Credentials{User: u.User, Password: u.Password},
	}, nil
}

// ListConnections returns the connections act may see, with its access on
// each and, for admins, their open changesets. No database I/O: two or
// three queries.
func (a *App) ListConnections(ctx context.Context, act Actor) ([]tbtypes.Connection, error) {
	conns, err := a.store.ListConnections(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := a.store.ListAllPolicyRules(ctx)
	if err != nil {
		return nil, err
	}
	pending := map[int64]int{}
	if act.IsAdmin() {
		counts, err := a.store.CountPendingChangesets(ctx, a.now())
		if err != nil {
			return nil, err
		}
		for _, c := range counts {
			pending[c.ConnectionID] = int(c.Pending)
		}
	}
	out := make([]tbtypes.Connection, 0, len(conns))
	for _, c := range conns {
		perm := permission(act, c, rulesFor(rules, c.ID))
		if visible(act, c, perm) {
			out = append(out, toConnection(c, perm, pending[c.ID]))
		}
	}
	return out, nil
}

// GetConnection returns one connection act may see.
func (a *App) GetConnection(ctx context.Context, act Actor, name string) (tbtypes.Connection, error) {
	c, perm, err := a.connFor(ctx, act, name)
	if err != nil {
		return tbtypes.Connection{}, err
	}
	return toConnection(c, perm, 0), nil
}

// CreateConnection saves a database. Admin only.
func (a *App) CreateConnection(ctx context.Context, act Actor, req tbtypes.CreateConnectionRequest) (tbtypes.Connection, error) {
	if err := act.requireAdmin("adding connections"); err != nil {
		return tbtypes.Connection{}, err
	}
	if !connName.MatchString(req.Name) {
		return tbtypes.Connection{}, invalid("connection name %q must be 1-63 lowercase letters, digits and dashes", req.Name)
	}
	u, err := parseConnURL(req.URL)
	if err != nil {
		return tbtypes.Connection{}, err
	}
	if _, _, err := tlsConfig(u.TLS, u.Host, req.CAPEM); err != nil {
		return tbtypes.Connection{}, err
	}
	sealed, err := a.box.Seal([]byte(req.URL), sealedConnURL.AAD(req.Name))
	if err != nil {
		return tbtypes.Connection{}, err
	}
	c, err := a.store.CreateConnection(ctx, tbstore.CreateConnectionParams{
		Name: req.Name, Engine: string(apitypes.EnginePostgres), Host: u.Host, Port: int64(u.Port), Database: u.Database,
		Username: u.User, TlsMode: string(u.TLS), CaPem: req.CAPEM, UrlSecret: sealed, Production: boolInt(req.Production),
		CreatedAt: a.now(), CreatedBy: act.Name,
	})
	if tbstore.IsUnique(err) {
		return tbtypes.Connection{}, conflict("a connection named %q already exists; choose another name", req.Name)
	}
	if err != nil {
		return tbtypes.Connection{}, err
	}
	a.audit(ctx, act, "connection.create", &c.ID, "connection "+c.Name,
		fmt.Sprintf("added connection %s: %s, sslmode %s, %s", c.Name, u.describe(), u.TLS, datapanel.KindWords(kindOf(c))))
	return toConnection(c, permission(act, c, nil), 0), nil
}

// UpdateConnection changes a connection's name, URL, production flag or
// CA. It applies at once: the panel drops its cached connections and
// schema. Admin only.
func (a *App) UpdateConnection(ctx context.Context, act Actor, name string, req tbtypes.UpdateConnectionRequest) (tbtypes.Connection, error) {
	if err := act.requireAdmin("changing connections"); err != nil {
		return tbtypes.Connection{}, err
	}
	c, _, err := a.connFor(ctx, act, name)
	if err != nil {
		return tbtypes.Connection{}, err
	}
	var changes []string
	if req.Name != nil && *req.Name != c.Name {
		if !connName.MatchString(*req.Name) {
			return tbtypes.Connection{}, invalid("connection name %q must be 1-63 lowercase letters, digits and dashes", *req.Name)
		}
		changes = append(changes, "renamed to "+*req.Name)
		// The URL is bound to the name (ADR 0041): seal it for the new one.
		raw, err := a.box.Open(c.UrlSecret, sealedConnURL.AAD(c.Name))
		if err != nil {
			return tbtypes.Connection{}, fmt.Errorf("connection %s: %w", c.Name, err)
		}
		c.Name = *req.Name
		if c.UrlSecret, err = a.box.Seal(raw, sealedConnURL.AAD(c.Name)); err != nil {
			return tbtypes.Connection{}, err
		}
	}
	if req.URL != nil {
		u, err := parseConnURL(*req.URL)
		if err != nil {
			return tbtypes.Connection{}, err
		}
		if c.UrlSecret, err = a.box.Seal([]byte(*req.URL), sealedConnURL.AAD(c.Name)); err != nil {
			return tbtypes.Connection{}, err
		}
		c.Host, c.Port, c.Database, c.Username, c.TlsMode = u.Host, int64(u.Port), u.Database, u.User, string(u.TLS)
		changes = append(changes, fmt.Sprintf("URL: %s, sslmode %s", u.describe(), u.TLS))
	}
	if req.CAPEM != nil && *req.CAPEM != c.CaPem {
		c.CaPem = *req.CAPEM
		changes = append(changes, "CA certificate")
	}
	if req.Production != nil && boolInt(*req.Production) != c.Production {
		c.Production = boolInt(*req.Production)
		changes = append(changes, datapanel.KindWords(kindOf(c)))
	}
	if _, _, err := tlsConfig(tbtypes.TLSMode(c.TlsMode), c.Host, c.CaPem); err != nil {
		return tbtypes.Connection{}, err
	}
	if len(changes) > 0 {
		c, err = a.store.UpdateConnection(ctx, tbstore.UpdateConnectionParams{
			Name: c.Name, Host: c.Host, Port: c.Port, Database: c.Database, Username: c.Username, TlsMode: c.TlsMode,
			CaPem: c.CaPem, UrlSecret: c.UrlSecret, Production: c.Production, ID: c.ID,
		})
		if tbstore.IsUnique(err) {
			return tbtypes.Connection{}, conflict("a connection named %q already exists; choose another name", c.Name)
		}
		if err != nil {
			return tbtypes.Connection{}, err
		}
		// Cached connections and schema may be of the old URL or database.
		a.panel.Forget(c.ID)
		a.audit(ctx, act, "connection.update", &c.ID, "connection "+c.Name, "changed connection "+name+": "+strings.Join(changes, "; "))
	}
	return a.GetConnection(ctx, act, c.Name)
}

// DeleteConnection removes a connection and everything tempoBase keeps
// about it but its audit events. tempoBase's roles stay in the database.
// Admin only.
func (a *App) DeleteConnection(ctx context.Context, act Actor, name string) error {
	if err := act.requireAdmin("removing connections"); err != nil {
		return err
	}
	c, _, err := a.connFor(ctx, act, name)
	if err != nil {
		return err
	}
	if err := a.store.DeleteConnection(ctx, c.ID); err != nil {
		return err
	}
	a.panel.Forget(c.ID)
	a.audit(ctx, act, "connection.delete", &c.ID, "connection "+c.Name,
		"removed connection "+c.Name+" ("+c.Username+"@"+c.Host+"/"+c.Database+"); tempoBase's roles stay in the database")
	return nil
}

// TestConnection dials a URL and logs in as its user without saving or
// changing anything: the server version and whether setup could create
// tempoBase's roles, or what failed. Admin only.
func (a *App) TestConnection(ctx context.Context, act Actor, req tbtypes.TestConnectionRequest) (tbtypes.TestConnectionResult, error) {
	if err := act.requireAdmin("testing connections"); err != nil {
		return tbtypes.TestConnectionResult{}, err
	}
	u, err := parseConnURL(req.URL)
	if err != nil {
		return tbtypes.TestConnectionResult{}, err
	}
	conn, err := dialConn(u, req.CAPEM)
	if err != nil {
		return tbtypes.TestConnectionResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*dialTimeout)
	defer cancel()
	info, err := a.engine.Check(ctx, target(0, conn), conn.Owner)
	res := tbtypes.TestConnectionResult{ServerVersion: info.Version, OK: err == nil, CanSetUp: err == nil}
	if errors.Is(err, data.ErrCannotSetUp) {
		// The login works; only setup would fail.
		res.OK = true
	}
	if err != nil {
		res.Error = err.Error()
	}
	return res, nil
}

// target is a datapanel.Conn as the engine takes it, for database id (0:
// not saved yet, so nothing is cached for it).
func target(id int64, c datapanel.Conn) data.Target {
	return data.Target{ServiceID: id, Dial: c.Dial, TLS: c.TLS, TLSOptional: c.TLSOptional, Database: c.Database}
}

func toConnection(c tbstore.Connection, perm apitypes.DataPermission, pending int) tbtypes.Connection {
	return tbtypes.Connection{
		Name: c.Name, Engine: apitypes.DataEngine(c.Engine), Host: c.Host, Port: int(c.Port), Database: c.Database,
		User: c.Username, TLS: tbtypes.TLSMode(c.TlsMode), CustomCA: c.CaPem != "", Production: c.Production != 0,
		URL: redactedURL(c), CreatedAt: c.CreatedAt, CreatedBy: c.CreatedBy, Access: perm, Pending: pending,
	}
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
