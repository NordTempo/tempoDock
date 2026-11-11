package caddy

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/NordTempo/tempoDock/internal/access"
	"github.com/NordTempo/tempoDock/internal/node"
)

// The structs below are the small subset of Caddy's JSON config we use.
// See https://caddyserver.com/docs/json/.

type caddyConfig struct {
	Admin   admin    `json:"admin"`
	Logging *logging `json:"logging,omitempty"`
	Apps    apps     `json:"apps"`
}

// logging sends the access log of counted routes to the node's traffic
// counter (trafficLogging).
type logging struct {
	Logs map[string]logConfig `json:"logs"`
}

type logConfig struct {
	Writer  *logWriter  `json:"writer,omitempty"`
	Encoder *logEncoder `json:"encoder,omitempty"`
	Include []string    `json:"include,omitempty"`
	Exclude []string    `json:"exclude,omitempty"`
}

type logWriter struct {
	Output  string `json:"output"`
	Address string `json:"address"`
	// SoftStart lets Caddy start while the address can't be dialed.
	SoftStart bool `json:"soft_start"`
}

// logEncoder is the filter encoder around JSON: Fields are deleted before
// encoding, so Caddy doesn't spend time on what isn't counted.
type logEncoder struct {
	Format string                 `json:"format"`
	Wrap   map[string]string      `json:"wrap"`
	Fields map[string]fieldFilter `json:"fields"`
}

type fieldFilter struct {
	Filter string `json:"filter"`
}

// serverLogs turns on a server's access log for the hostnames in
// LoggerNames only.
type serverLogs struct {
	LoggerNames       map[string][]string `json:"logger_names"`
	SkipUnmappedHosts bool                `json:"skip_unmapped_hosts"`
}

type admin struct {
	// Listen is "unix/<path>|<mode>".
	Listen string      `json:"listen"`
	Config adminConfig `json:"config"`
}

type adminConfig struct {
	// Persist saves every loaded config so `caddy run --resume` restores it.
	Persist bool `json:"persist"`
}

type apps struct {
	HTTP httpApp `json:"http"`
	TLS  *tlsApp `json:"tls,omitempty"`
	PKI  *pkiApp `json:"pki,omitempty"`
}

type httpApp struct {
	HTTPPort  int `json:"http_port"`
	HTTPSPort int `json:"https_port,omitempty"`
	// GracePeriod is a Go duration string; see defaultGracePeriod.
	GracePeriod string            `json:"grace_period"`
	Servers     map[string]server `json:"servers"`
}

type server struct {
	Listen []string `json:"listen"`
	Routes []route  `json:"routes"`
	// TLSConnectionPolicies turns on TLS for the server. One empty policy
	// means "any SNI, certificates from the tls app".
	TLSConnectionPolicies []struct{}     `json:"tls_connection_policies,omitempty"`
	AutomaticHTTPS        automaticHTTPS `json:"automatic_https"`
	// TrustedProxies are the peers whose X-Forwarded-* headers Caddy keeps
	// and extends instead of replacing.
	TrustedProxies *ipRanges `json:"trusted_proxies,omitempty"`
	// ClientIPHeaders are read, first present wins, for the client IP of
	// requests from a trusted proxy (logs and matchers).
	ClientIPHeaders []string `json:"client_ip_headers,omitempty"`
	// Logs logs the requests of counted routes (ADR 0033).
	Logs *serverLogs `json:"logs,omitempty"`
}

// ipRanges is Caddy's static IP source module.
type ipRanges struct {
	Source string   `json:"source"`
	Ranges []string `json:"ranges"`
}

// loopback is where cloudflared connects from on a tunnel server.
var loopback = []string{"127.0.0.1/32", "::1/128"}

// automaticHTTPS is always disabled: we render redirects and TLS explicitly,
// and certificates come from the on-demand automation policy.
type automaticHTTPS struct {
	Disable bool `json:"disable"`
}

type route struct {
	Match    []match   `json:"match,omitempty"`
	Handle   []handler `json:"handle"`
	Terminal bool      `json:"terminal,omitempty"`
}

type match struct {
	Host     []string            `json:"host"`
	Path     []string            `json:"path,omitempty"`
	Header   map[string][]string `json:"header,omitempty"`
	RemoteIP *remoteIP           `json:"remote_ip,omitempty"`
}

// remoteIP matches the immediate peer's address.
type remoteIP struct {
	Ranges []string `json:"ranges"`
}

// handler covers the handlers we use: reverse_proxy, static_response,
// headers and authentication.
type handler struct {
	Handler    string              `json:"handler"`
	Upstreams  []upstream          `json:"upstreams,omitempty"`
	StatusCode int                 `json:"status_code,omitempty"`
	Headers    map[string][]string `json:"headers,omitempty"`
	Body       string              `json:"body,omitempty"`
	// Request and Response edit request and response headers (headers
	// handler).
	Request  *headerOps `json:"request,omitempty"`
	Response *headerOps `json:"response,omitempty"`
	// StreamCloseDelay keeps upgraded connections (websockets) open across
	// config reloads, so deploying one app doesn't disconnect every other
	// app's websockets.
	StreamCloseDelay string `json:"stream_close_delay,omitempty"`
	// Rewrite and HandleResponse turn a reverse_proxy into a forward-auth
	// check (loginAccess).
	Rewrite        *rewrite          `json:"rewrite,omitempty"`
	HandleResponse []responseHandler `json:"handle_response,omitempty"`
	// Providers configure the authentication handler (passwordAccess).
	Providers *authProviders `json:"providers,omitempty"`
	// LogSkip keeps the request out of the access log (vars handler).
	LogSkip bool `json:"log_skip,omitempty"`
	// LoadBalancing holds requests while the one upstream is down
	// (Route.Hold).
	LoadBalancing *loadBalancing `json:"load_balancing,omitempty"`
	// Span names the tracing handler's spans (edgeTrace).
	Span string `json:"span,omitempty"`
}

// loadBalancing is reverse_proxy's retry settings: Caddy tries the
// upstream again every TryInterval for TryDuration after a refused
// connection, which is always safe to retry.
type loadBalancing struct {
	TryDuration string `json:"try_duration"`
	TryInterval string `json:"try_interval"`
}

// holdInterval is how often a held request tries its upstream again.
const holdInterval = 250 * time.Millisecond

// rewrite changes the request a reverse_proxy sends upstream. With method
// GET Caddy also leaves the request body out.
type rewrite struct {
	Method string `json:"method"`
	URI    string `json:"uri"`
}

// responseHandler runs Routes for upstream responses that match. Routes
// that write nothing let the request go on down the handler chain.
type responseHandler struct {
	Match  responseMatch `json:"match"`
	Routes []route       `json:"routes"`
}

type responseMatch struct {
	// StatusCode entries below 10 match a class: 2 is any 2xx.
	StatusCode []int `json:"status_code"`
}

type authProviders struct {
	HTTPBasic httpBasic `json:"http_basic"`
}

type httpBasic struct {
	Accounts []account `json:"accounts"`
	Hash     hashAlg   `json:"hash"`
	// HashCache remembers checked passwords in memory, so a page's
	// requests don't each pay for a bcrypt comparison.
	HashCache *struct{} `json:"hash_cache,omitempty"`
	Realm     string    `json:"realm"`
}

type account struct {
	Username string `json:"username"`
	// Password is a bcrypt hash; Caddy takes "$2…" hashes as they are.
	Password string `json:"password"`
}

type hashAlg struct {
	Algorithm string `json:"algorithm"`
}

type upstream struct {
	Dial string `json:"dial"`
}

type headerOps struct {
	Set    map[string][]string `json:"set,omitempty"`
	Delete []string            `json:"delete,omitempty"`
	// Deferred applies the change when the response is written, so it
	// overrides the same header from an upstream. Response edits only.
	Deferred bool `json:"deferred,omitempty"`
}

type tlsApp struct {
	Automation automation `json:"automation"`
}

type automation struct {
	Policies []automationPolicy `json:"policies"`
	OnDemand onDemand           `json:"on_demand"`
}

type automationPolicy struct {
	Issuers  []issuer `json:"issuers"`
	OnDemand bool     `json:"on_demand"`
}

type issuer struct {
	Module string `json:"module"`
	Email  string `json:"email,omitempty"`
}

type onDemand struct {
	Permission permission `json:"permission"`
}

// permission is Caddy's `http` permission module: before issuing a
// certificate it GETs Endpoint?domain=<hostname> and only proceeds on 2xx.
type permission struct {
	Module   string `json:"module"`
	Endpoint string `json:"endpoint"`
}

// pkiApp is only rendered for the test-only internal CA, so Caddy doesn't
// try to install its root into the system trust store.
type pkiApp struct {
	CertificateAuthorities map[string]pkiCA `json:"certificate_authorities"`
}

type pkiCA struct {
	InstallTrust bool `json:"install_trust"`
}

// defaultGracePeriod bounds how long a replaced config's servers wait for
// open connections to finish. It does not close connections on reload (Go's
// Shutdown just stops waiting), but it does bound how long Caddy takes to
// exit: without it, one HTTP/2 connection that missed its GOAWAY keeps
// `systemctl stop caddy` hanging until systemd kills it.
const defaultGracePeriod = 10 * time.Second

// streamCloseDelay is how long websockets survive a reload. Streams to a
// container that gets replaced still end when that container stops.
const streamCloseDelay = time.Hour

// notFoundPage is served for hostnames that have no route.
const notFoundPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>404</title>
<style>
html,body{margin:0;height:100%;background:#000;color:#fff;font:14px/1.5 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
body{display:flex;align-items:center;justify-content:center}
</style>
</head>
<body>
<p>404. Nothing is deployed at this address.</p>
</body>
</html>
`

// stoppedPage is served for the hostnames of a stopped service (ADR 0050);
// %s is the service's name.
const stoppedPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>503</title>
<style>
html,body{margin:0;height:100%%;background:#000;color:#fff;font:14px/1.5 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
body{display:flex;align-items:center;justify-content:center}
</style>
</head>
<body>
<p>503. %s is stopped.</p>
</body>
</html>
`

// BootstrapConfig is the config the install script writes before the daemon
// has ever run: admin on the Unix socket and the 404 page for every host.
func BootstrapConfig(opts Options) ([]byte, error) {
	return Render(opts, nil)
}

// Render builds the complete Caddy JSON config for routes. It is a pure
// function; the same input always gives the same output.
//
// In direct mode with TLS there are two servers: "https" serves routes with
// on-demand certificates, and "http" redirects known hostnames to HTTPS.
// With TLS disabled a single "http" server serves routes. In tunnel mode a
// single "http" server on 127.0.0.1 serves routes, trusts forwarding
// headers from loopback only, and redirects to https:// because the public
// side is Cloudflare's edge. Every server starts by setting the marker
// header (when there is one) and ends with a catch-all 404 page.
//
// Hostnames must be lower-case ASCII (punycode for IDNs) and unique across
// routes.
func Render(opts Options, routes []node.Route) ([]byte, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	if err := validateRoutes(opts, routes); err != nil {
		return nil, err
	}

	// scheme and port are what clients use, for redirects.
	scheme, port := "https", opts.HTTPSPort
	switch {
	case opts.tunnel():
		port = 443
	case opts.DisableTLS:
		scheme, port = "http", opts.HTTPPort
	}

	// main are the routes of the public listeners (all of them in tunnel
	// mode); viaTunnel those of a direct server's tunnel listener, every
	// route, TunnelOnly ones included.
	// It exists only while some route is tunnel-only: a direct server that
	// runs no tunnel gets no extra listener.
	tunnelListener := !opts.tunnel() && opts.TunnelPort > 0 &&
		slices.ContainsFunc(routes, func(r node.Route) bool { return r.TunnelOnly })
	var main, viaTunnel []route
	if opts.ServerMarker != "" {
		main = append(main, markerRoute(opts.ServerMarker))
		viaTunnel = append(viaTunnel, markerRoute(opts.ServerMarker))
	}
	var hostnames []string
	var counted map[string][]string // hostname → access logger, for counted routes
	for _, r := range routes {
		public := !r.TunnelOnly || opts.tunnel()
		if public {
			hostnames = append(hostnames, r.Hostnames...)
		}
		h := handler{
			Handler:          "reverse_proxy",
			Upstreams:        []upstream{{Dial: r.Upstream}},
			StreamCloseDelay: streamCloseDelay.String(),
		}
		if r.Hold {
			// A refused connection is tried again, every request held
			// until the upstream answers or HoldFor passes (ADR 0063).
			h.LoadBalancing = &loadBalancing{TryDuration: node.HoldFor.String(), TryInterval: holdInterval.String()}
		}
		// Through the tunnel, visitors are on https://, port 443, at
		// Cloudflare's edge, whatever this server's own scheme and port.
		ht := h
		switch {
		case r.RedirectTo != "":
			h = redirect(scheme + "://" + r.RedirectTo + portSuffix(scheme, port) + "{http.request.uri}")
			ht = redirect("https://" + r.RedirectTo + "{http.request.uri}")
		case r.Stopped != "":
			h = stoppedResponse(r.Stopped)
			ht = h
		}
		m := match{Host: r.Hostnames}
		if r.PathPrefix != "" {
			m.Path = []string{r.PathPrefix + "*"}
		}
		var handle []handler
		if r.Traffic && r.RedirectTo == "" && opts.TrafficAddr != "" {
			if counted == nil {
				counted = map[string][]string{}
			}
			for _, h := range r.Hostnames {
				counted[h] = []string{trafficLogger}
			}
		}
		if r.ClientIPHeader != "" && r.RedirectTo == "" {
			// The client's address as this server determined it: the peer,
			// or CF-Connecting-IP from the local cloudflared on a tunnel.
			set := &headerOps{Set: map[string][]string{r.ClientIPHeader: {"{http.vars.client_ip}"}}}
			handle = append(handle, handler{Handler: "headers", Request: set})
		}
		if r.Access != nil && r.RedirectTo == "" {
			if r.Access.Login() {
				handle = append(handle, loginAccess(opts.accessCheck, r.Access)...)
			} else {
				handle = append(handle, passwordAccess(r.Access)...)
			}
		}
		if r.Wake != "" && r.RedirectTo == "" && r.Stopped == "" {
			// After the access check: a stranger at a protected route
			// never wakes it.
			handle = append(handle, wakeCheck(opts.wakeCheck, r.Wake)...)
		}
		if r.Trace != "" && r.RedirectTo == "" && r.Stopped == "" {
			// After the access and wake checks: refused and held requests
			// make no trace.
			handle = append(handle, edgeTrace()...)
		}
		if r.QuietProbes && r.RedirectTo == "" {
			// Ahead of the route and not terminal: Dock's uptime probes go
			// on to it, without an access log line.
			q := route{Match: []match{{Host: r.Hostnames, Header: map[string][]string{"User-Agent": {uptimeAgent}}}}, Handle: []handler{{Handler: "vars", LogSkip: true}}}
			if public {
				main = append(main, q)
			}
			if tunnelListener {
				viaTunnel = append(viaTunnel, q)
			}
		}
		// The analytics route goes ahead of the service's, on every listener
		// that serves the route: tunnel visitors are counted too.
		if r.RedirectTo == "" && opts.AnalyticsAddr != "" {
			for _, an := range r.Analytics {
				if public {
					main = append(main, analyticsRoute(an, opts.AnalyticsAddr))
				}
				if tunnelListener {
					viaTunnel = append(viaTunnel, analyticsRoute(an, opts.AnalyticsAddr))
				}
			}
		}
		if public {
			main = append(main, route{Match: []match{m}, Handle: append(slices.Clone(handle), h), Terminal: true})
		}
		if tunnelListener {
			viaTunnel = append(viaTunnel, route{Match: []match{m}, Handle: append(slices.Clone(handle), ht), Terminal: true})
		}
	}
	// Apps' OTLP exports, plain HTTP from the server's own containers:
	// on the public listener of a plain-HTTP server, or the HTTP one next
	// to HTTPS (below). Not in tunnel mode, where nothing listens where
	// containers can reach.
	otlp := opts.TraceIngestAddr != "" && !opts.tunnel()
	if otlp && opts.plainHTTP() {
		main = append(main, otlpRoute(opts.TraceIngestAddr))
	}
	main = append(main, notFoundRoute())
	viaTunnel = append(viaTunnel, notFoundRoute())

	cfg := caddyConfig{
		Admin: admin{
			// 0660 lets the daemon connect through a shared group without
			// opening the socket to other local users.
			Listen: "unix/" + opts.AdminSocket + "|0660",
			Config: adminConfig{Persist: true},
		},
		Apps: apps{HTTP: httpApp{
			HTTPPort:    opts.HTTPPort,
			HTTPSPort:   opts.HTTPSPort,
			GracePeriod: cmp.Or(opts.gracePeriod, defaultGracePeriod).String(),
		}},
	}

	var logs *serverLogs
	if counted != nil {
		cfg.Logging = trafficLogging(opts.TrafficAddr)
		logs = &serverLogs{LoggerNames: counted, SkipUnmappedHosts: true}
	}

	if opts.plainHTTP() {
		srv := server{
			Listen:         []string{listenAddr(opts.HTTPPort)},
			Routes:         main,
			AutomaticHTTPS: automaticHTTPS{Disable: true},
			Logs:           logs,
		}
		if opts.tunnel() {
			// Only the local cloudflared connects; nothing is public.
			srv.Listen = []string{net.JoinHostPort(cmp.Or(opts.tunnelListenHost, "127.0.0.1"), strconv.Itoa(opts.HTTPPort))}
			srv.TrustedProxies = &ipRanges{Source: "static", Ranges: loopback}
			srv.ClientIPHeaders = []string{"CF-Connecting-IP", "X-Forwarded-For"}
		}
		cfg.Apps.HTTP.Servers = map[string]server{"http": srv}
		addTunnelServer(cfg.Apps.HTTP.Servers, opts, tunnelListener, viaTunnel, logs)
		return json.Marshal(cfg)
	}

	var plain []route
	if opts.ServerMarker != "" {
		plain = append(plain, markerRoute(opts.ServerMarker))
	}
	if otlp {
		plain = append(plain, otlpRoute(opts.TraceIngestAddr))
	}
	if len(hostnames) > 0 {
		plain = append(plain, route{
			Match:    []match{{Host: hostnames}},
			Handle:   []handler{redirect("https://{http.request.host}" + portSuffix("https", opts.HTTPSPort) + "{http.request.uri}")},
			Terminal: true,
		})
	}
	plain = append(plain, notFoundRoute())

	cfg.Apps.HTTP.Servers = map[string]server{
		"https": {
			Listen:                []string{listenAddr(opts.HTTPSPort)},
			Routes:                main,
			TLSConnectionPolicies: []struct{}{{}},
			AutomaticHTTPS:        automaticHTTPS{Disable: true},
			Logs:                  logs,
		},
		"http": {
			Listen:         []string{listenAddr(opts.HTTPPort)},
			Routes:         plain,
			AutomaticHTTPS: automaticHTTPS{Disable: true},
		},
	}

	addTunnelServer(cfg.Apps.HTTP.Servers, opts, tunnelListener, viaTunnel, logs)

	iss := issuer{Module: "acme", Email: opts.ACMEEmail}
	if opts.internalCA {
		iss = issuer{Module: "internal"}
		cfg.Apps.PKI = &pkiApp{CertificateAuthorities: map[string]pkiCA{"local": {InstallTrust: false}}}
	}
	cfg.Apps.TLS = &tlsApp{Automation: automation{
		Policies: []automationPolicy{{Issuers: []issuer{iss}, OnDemand: true}},
		OnDemand: onDemand{Permission: permission{Module: "http", Endpoint: opts.AskURL}},
	}}
	return json.Marshal(cfg)
}

// tunnelServer names a direct server's tunnel listener in Caddy's config.
const tunnelServer = "tunnel"

// addTunnelServer adds a direct server's tunnel listener (Options.
// TunnelPort) when on is set: plain HTTP on loopback, where only the local
// cloudflared connects, trusting its forwarding headers.
func addTunnelServer(servers map[string]server, opts Options, on bool, routes []route, logs *serverLogs) {
	if !on {
		return
	}
	servers[tunnelServer] = server{
		Listen:          []string{net.JoinHostPort(cmp.Or(opts.tunnelListenHost, "127.0.0.1"), strconv.Itoa(opts.TunnelPort))},
		Routes:          routes,
		AutomaticHTTPS:  automaticHTTPS{Disable: true},
		Logs:            logs,
		TrustedProxies:  &ipRanges{Source: "static", Ranges: loopback},
		ClientIPHeaders: []string{"CF-Connecting-IP", "X-Forwarded-For"},
	}
}

// trafficLogger names the access logger of counted routes; Caddy calls it
// http.log.access.dock.
const trafficLogger = "dock"

// trafficLogging sends the access log of counted routes to addr over UDP,
// one line per datagram, and keeps it out of Caddy's own log. Only what
// the traffic counter reads is encoded: the request line, status, size and
// duration, never who sent the request. While nothing listens at addr
// (the node restarting), Caddy writes the lines it can't send to its
// stderr, the journal.
func trafficLogging(addr string) *logging {
	const access = "http.log.access." + trafficLogger
	drop := fieldFilter{Filter: "delete"}
	return &logging{Logs: map[string]logConfig{
		"default": {Exclude: []string{access}},
		trafficLogger: {
			Writer: &logWriter{Output: "net", Address: "udp/" + addr, SoftStart: true},
			Encoder: &logEncoder{Format: "filter", Wrap: map[string]string{"format": "json"}, Fields: map[string]fieldFilter{
				"request>headers": drop, "request>tls": drop, "request>remote_ip": drop, "request>remote_port": drop,
				"request>client_ip": drop, "request>proto": drop,
				"resp_headers": drop, "user_id": drop, "bytes_read": drop,
			}},
			Include: []string{access},
		},
	}}
}

// Headers the analytics ingest reads (internal/analytics/ingest), set from
// the routing table and replacing any a visitor sent.
const (
	analyticsTagHeader = "X-Dock-Analytics"
	analyticsIPHeader  = "X-Dock-Client-Ip"
)

// analyticsRoute sends /_dock/a/* on a group's hostnames to the ingest
// (ADR 0037), ahead of the service's own route. The requests stay out of
// the access log, so traffic statistics don't count them, and carry the
// group's tag and the client's address as this server determined it.
func analyticsRoute(a node.AnalyticsHosts, addr string) route {
	return route{
		Match: []match{{Host: a.Hostnames, Path: []string{"/_dock/a/*"}}},
		Handle: []handler{
			{Handler: "vars", LogSkip: true},
			{Handler: "headers", Request: &headerOps{Set: map[string][]string{
				analyticsTagHeader: {a.Tag},
				analyticsIPHeader:  {"{http.vars.client_ip}"},
			}}},
			{Handler: "reverse_proxy", Upstreams: []upstream{{Dial: addr}}},
		},
		Terminal: true,
	}
}

// privateRanges are where apps' containers reach the server from: their
// Docker networks are private. Anyone else asking for dock-otlp gets the
// catch-all 404; the ingest checks every export's token besides.
var privateRanges = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "fc00::/7"}

// otlpRoute forwards OTLP exports for node.TraceIngestHost, from private
// addresses, to the trace ingest at addr, out of the access log so
// traffic statistics don't count them.
func otlpRoute(addr string) route {
	return route{
		Match: []match{{Host: []string{node.TraceIngestHost}, RemoteIP: &remoteIP{Ranges: privateRanges}}},
		Handle: []handler{
			{Handler: "vars", LogSkip: true},
			{Handler: "reverse_proxy", Upstreams: []upstream{{Dial: addr}}},
		},
		Terminal: true,
	}
}

// Request headers the access check reads, set by Caddy from the route
// (AccessHandler).
const (
	accessKeyHeader   = "X-Dock-Access-Key"
	accessLoginHeader = "X-Dock-Access-Login"
	forwardedMethod   = "X-Forwarded-Method"
	forwardedURI      = "X-Forwarded-Uri"
)

// passwordAccess asks for HTTP basic auth and tells the app who signed in
// (access.UserHeader), replacing any value the client sent.
func passwordAccess(a *node.Access) []handler {
	return []handler{
		{Handler: "authentication", Providers: &authProviders{HTTPBasic: httpBasic{
			Accounts:  []account{{Username: a.Username, Password: a.PasswordHash}},
			Hash:      hashAlg{Algorithm: "bcrypt"},
			HashCache: &struct{}{},
			Realm:     "restricted",
		}}},
		{Handler: "headers", Request: &headerOps{Set: map[string][]string{access.UserHeader: {"{http.auth.user.id}"}}}},
	}
}

// loginAccess is Caddy's forward_auth, spelled out: the request's method,
// URI, cookies and the route's key go to the access check (a GET without
// the body). A 2xx lets the request on to the app with the visitor's name
// in access.UserHeader and the check's headers removed; any other answer
// (a redirect to sign in, the cookie being set, a 401) goes back to the
// client as it is.
func loginAccess(check accessCheck, a *node.Access) []handler {
	return []handler{
		{Handler: "headers", Request: &headerOps{Set: map[string][]string{
			accessKeyHeader:   {base64.RawURLEncoding.EncodeToString(a.Key)},
			accessLoginHeader: {a.LoginURL},
			forwardedMethod:   {"{http.request.method}"},
			forwardedURI:      {"{http.request.uri}"},
		}}},
		{
			Handler:   "reverse_proxy",
			Upstreams: []upstream{{Dial: check.dial}},
			Rewrite:   &rewrite{Method: http.MethodGet, URI: check.path},
			HandleResponse: []responseHandler{{
				Match: responseMatch{StatusCode: []int{2}},
				Routes: []route{{Handle: []handler{{Handler: "headers", Request: &headerOps{
					Delete: []string{accessKeyHeader, accessLoginHeader, forwardedMethod, forwardedURI},
					Set:    map[string][]string{access.UserHeader: {"{http.reverse_proxy.header." + access.UserHeader + "}"}},
				}}}}},
			}},
		},
	}
}

// wakeKeyHeader carries a route's environment key to the wake check,
// set by Caddy from the route and replacing any a client sent.
const wakeKeyHeader = "X-Dock-Wake"

// uptimeAgent matches the User-Agent of Dock's uptime probes
// (internal/node/uptime), which never count as an environment's traffic.
const uptimeAgent = "dock-uptime/*"

// wakeCheck asks the server's wake check before an asleep environment's
// route proxies, the way loginAccess asks the access check: the request's
// method, URI and headers go along (a GET without the body). The check
// holds the request while the environment wakes; a 2xx lets it on to the
// app, which is running then, and anything else (the waking page, a 503)
// goes back to the client as it is.
func wakeCheck(check accessCheck, key string) []handler {
	return []handler{
		{Handler: "headers", Request: &headerOps{Set: map[string][]string{
			wakeKeyHeader:   {key},
			forwardedMethod: {"{http.request.method}"},
			forwardedURI:    {"{http.request.uri}"},
		}}},
		{
			Handler:   "reverse_proxy",
			Upstreams: []upstream{{Dial: check.dial}},
			Rewrite:   &rewrite{Method: http.MethodGet, URI: check.path},
			HandleResponse: []responseHandler{{
				Match: responseMatch{StatusCode: []int{2}},
				Routes: []route{{Handle: []handler{{Handler: "headers", Request: &headerOps{
					Delete: []string{wakeKeyHeader, forwardedMethod, forwardedURI},
				}}}}},
			}},
		},
	}
}

func redirect(location string) handler {
	return handler{
		Handler:    "static_response",
		StatusCode: 308,
		Headers:    map[string][]string{"Location": {location}},
	}
}

// edgeTrace starts a trace at the edge (ADR 0075): the visitor's trace
// headers are dropped, so the internet can't pick trace IDs or force
// sampling, then Caddy's tracing handler starts a span, passes its
// traceparent to the app and puts the trace and span IDs in the request's
// access log line, from which the server's trace ingest makes the edge
// span. Caddy exports nothing itself: dock-caddy.service sets
// OTEL_TRACES_EXPORTER=none.
func edgeTrace() []handler {
	return []handler{
		{Handler: "headers", Request: &headerOps{Delete: []string{"traceparent", "tracestate", "baggage"}}},
		{Handler: "tracing", Span: "edge"},
	}
}

// markerRoute sets the marker header on every response of a server. It has
// no matcher and isn't terminal, so the routes after it still run.
func markerRoute(marker string) route {
	return route{Handle: []handler{{
		Handler:  "headers",
		Response: &headerOps{Set: map[string][]string{MarkerHeader: {marker}}, Deferred: true},
	}}}
}

// stoppedResponse is the 503 page of a stopped service's hostnames, never
// cached: the service may start again any moment.
func stoppedResponse(service string) handler {
	return handler{
		Handler:    "static_response",
		StatusCode: 503,
		Headers:    map[string][]string{"Content-Type": {"text/html; charset=utf-8"}, "Cache-Control": {"no-store"}},
		Body:       fmt.Sprintf(stoppedPage, service),
	}
}

func notFoundRoute() route {
	return route{Handle: []handler{{
		Handler:    "static_response",
		StatusCode: 404,
		Headers:    map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
		Body:       notFoundPage,
	}}}
}

func listenAddr(port int) string { return ":" + strconv.Itoa(port) }

// portSuffix is ":<port>" unless port is the scheme's default.
func portSuffix(scheme string, port int) string {
	if (scheme == "https" && port == 443) || (scheme == "http" && port == 80) {
		return ""
	}
	return ":" + strconv.Itoa(port)
}

func validateRoutes(opts Options, routes []node.Route) error {
	seen := map[string]bool{}
	for i, r := range routes {
		if len(r.Hostnames) == 0 {
			return fmt.Errorf("route %d has no hostnames", i)
		}
		if err := validateAccess(opts, r.Access); err != nil {
			return fmt.Errorf("route for %s: %w", r.Hostnames[0], err)
		}
		for _, a := range r.Analytics {
			if !validTag(a.Tag) {
				return fmt.Errorf("route for %s: analytics tag %q must be three numbers like 3.40.981", r.Hostnames[0], a.Tag)
			}
			for _, h := range a.Hostnames {
				if !slices.Contains(r.Hostnames, h) {
					return fmt.Errorf("route for %s: analytics hostname %s isn't one of the route's", r.Hostnames[0], h)
				}
			}
		}
		if r.Upstream == "" && r.RedirectTo == "" && r.Stopped == "" {
			return fmt.Errorf("route for %s needs an upstream or a redirect target", r.Hostnames[0])
		}
		if r.Wake != "" && (opts.wakeCheck.dial == "" || !validWakeKey(r.Wake)) {
			return fmt.Errorf("route for %s: a wake key needs the wake check URL (Options.WakeURL) and must be digits, not %q", r.Hostnames[0], r.Wake)
		}
		if r.Stopped != "" && !validHostname(r.Stopped) {
			// A service name; it goes into the page, where Caddy would
			// expand a placeholder.
			return fmt.Errorf("route for %s: stopped service name %q is not valid", r.Hostnames[0], r.Stopped)
		}
		if r.RedirectTo != "" && !validHostname(r.RedirectTo) {
			return fmt.Errorf("redirect target %q is not a valid hostname", r.RedirectTo)
		}
		if r.PathPrefix != "" && !validPathPrefix(r.PathPrefix) {
			return fmt.Errorf("path prefix %q must look like /a/b/", r.PathPrefix)
		}
		for _, h := range r.Hostnames {
			if !validHostname(h) {
				return fmt.Errorf("%q is not a valid hostname", h)
			}
			if seen[h] {
				return fmt.Errorf("%s is used by more than one route", h)
			}
			seen[h] = true
		}
	}
	return nil
}

// validWakeKey reports whether key is an environment key: digits only, so
// Caddy never expands it as a placeholder.
func validWakeKey(key string) bool {
	if key == "" || len(key) > 20 {
		return false
	}
	for _, c := range key {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// validateAccess keeps an access setting complete and free of anything
// Caddy would expand as a placeholder.
func validateAccess(opts Options, a *node.Access) error {
	switch {
	case a == nil:
		return nil
	case a.Login() && a.PasswordHash != "":
		return errors.New("access needs a password or a login, not both")
	case a.Login() && opts.accessCheck.dial == "":
		return errors.New("login access needs the access check URL (Options.AccessURL)")
	case a.Login() && (a.LoginURL == "" || strings.ContainsAny(a.LoginURL, "{} \n")):
		return fmt.Errorf("login URL %q is not valid", a.LoginURL)
	case a.Login():
		return nil
	case !strings.HasPrefix(a.PasswordHash, "$2") || strings.ContainsAny(a.PasswordHash, "{}"):
		return errors.New("access needs a bcrypt password hash or a login key")
	case a.Username == "" || strings.ContainsAny(a.Username, "{}:") || strings.ContainsFunc(a.Username, unicode.IsControl):
		return fmt.Errorf("username %q is not valid", a.Username)
	}
	return nil
}

// validTag accepts "site.service.release": three decimal numbers.
func validTag(t string) bool {
	parts := strings.Split(t, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" || len(p) > 19 || strings.Trim(p, "0123456789") != "" {
			return false
		}
	}
	return true
}

// validPathPrefix accepts "/" plus lower-case segments, each ending in
// "/", so no Caddy placeholder or wildcard can sneak in.
func validPathPrefix(p string) bool {
	if len(p) < 2 || p[0] != '/' || p[len(p)-1] != '/' || strings.Contains(p, "//") {
		return false
	}
	for _, c := range p {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '/' {
			return false
		}
	}
	return true
}

// validHostname accepts lower-case DNS names. It mainly keeps characters
// like "{" out of the config, where Caddy would expand them as placeholders.
func validHostname(h string) bool {
	if h == "" || len(h) > 253 || h[0] == '.' || h[0] == '-' || h[len(h)-1] == '.' {
		return false
	}
	for _, c := range h {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '.' {
			return false
		}
	}
	return true
}
