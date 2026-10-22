package caddy

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// The structs below are the small subset of Caddy's JSON config we use.
// See https://caddyserver.com/docs/json/.

type caddyConfig struct {
	Admin admin `json:"admin"`
	Apps  apps  `json:"apps"`
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
	Host []string `json:"host"`
	Path []string `json:"path,omitempty"`
}

// handler covers the handlers we use: reverse_proxy, static_response and
// headers.
type handler struct {
	Handler    string              `json:"handler"`
	Upstreams  []upstream          `json:"upstreams,omitempty"`
	StatusCode int                 `json:"status_code,omitempty"`
	Headers    map[string][]string `json:"headers,omitempty"`
	Body       string              `json:"body,omitempty"`
	// Response edits response headers (headers handler).
	Response *headerOps `json:"response,omitempty"`
	// StreamCloseDelay keeps upgraded connections (websockets) open across
	// config reloads, so deploying one app doesn't disconnect every other
	// app's websockets.
	StreamCloseDelay string `json:"stream_close_delay,omitempty"`
}

type upstream struct {
	Dial string `json:"dial"`
}

type headerOps struct {
	Set map[string][]string `json:"set"`
	// Deferred applies the change when the response is written, so it
	// overrides the same header from an upstream.
	Deferred bool `json:"deferred"`
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
	if err := validateRoutes(routes); err != nil {
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

	var main []route
	if opts.ServerMarker != "" {
		main = append(main, markerRoute(opts.ServerMarker))
	}
	var hostnames []string
	for _, r := range routes {
		hostnames = append(hostnames, r.Hostnames...)
		h := handler{
			Handler:          "reverse_proxy",
			Upstreams:        []upstream{{Dial: r.Upstream}},
			StreamCloseDelay: streamCloseDelay.String(),
		}
		if r.RedirectTo != "" {
			h = redirect(scheme + "://" + r.RedirectTo + portSuffix(scheme, port) + "{http.request.uri}")
		}
		m := match{Host: r.Hostnames}
		if r.PathPrefix != "" {
			m.Path = []string{r.PathPrefix + "*"}
		}
		main = append(main, route{Match: []match{m}, Handle: []handler{h}, Terminal: true})
	}
	main = append(main, notFoundRoute())

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

	if opts.plainHTTP() {
		srv := server{
			Listen:         []string{listenAddr(opts.HTTPPort)},
			Routes:         main,
			AutomaticHTTPS: automaticHTTPS{Disable: true},
		}
		if opts.tunnel() {
			// Only the local cloudflared connects; nothing is public.
			srv.Listen = []string{net.JoinHostPort(cmp.Or(opts.tunnelListenHost, "127.0.0.1"), strconv.Itoa(opts.HTTPPort))}
			srv.TrustedProxies = &ipRanges{Source: "static", Ranges: loopback}
			srv.ClientIPHeaders = []string{"CF-Connecting-IP", "X-Forwarded-For"}
		}
		cfg.Apps.HTTP.Servers = map[string]server{"http": srv}
		return json.Marshal(cfg)
	}

	var plain []route
	if opts.ServerMarker != "" {
		plain = append(plain, markerRoute(opts.ServerMarker))
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
		},
		"http": {
			Listen:         []string{listenAddr(opts.HTTPPort)},
			Routes:         plain,
			AutomaticHTTPS: automaticHTTPS{Disable: true},
		},
	}

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

func redirect(location string) handler {
	return handler{
		Handler:    "static_response",
		StatusCode: 308,
		Headers:    map[string][]string{"Location": {location}},
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

func validateRoutes(routes []node.Route) error {
	seen := map[string]bool{}
	for i, r := range routes {
		if len(r.Hostnames) == 0 {
			return fmt.Errorf("route %d has no hostnames", i)
		}
		if r.Upstream == "" && r.RedirectTo == "" {
			return fmt.Errorf("route for %s needs an upstream or a redirect target", r.Hostnames[0])
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
