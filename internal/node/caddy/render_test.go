package caddy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/node"
)

func testOptions() Options {
	return Options{
		AdminSocket: "/run/dock/caddy-admin.sock",
		HTTPPort:    80,
		HTTPSPort:   443,
		ACMEEmail:   "ops@example.com",
		AskURL:      "http://127.0.0.1:7071/internal/caddy/ask",
	}
}

func render(t *testing.T, opts Options, routes []node.Route) caddyConfig {
	t.Helper()
	b, err := Render(opts, routes)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var cfg caddyConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("rendered invalid JSON: %v", err)
	}
	return cfg
}

func TestRenderTLS(t *testing.T) {
	cfg := render(t, testOptions(), []node.Route{
		{Hostnames: []string{"example.com", "app.example.com"}, Upstream: "127.0.0.1:32768"},
		{Hostnames: []string{"www.example.com"}, RedirectTo: "example.com"},
	})

	if cfg.Admin.Listen != "unix//run/dock/caddy-admin.sock|0660" || !cfg.Admin.Config.Persist {
		t.Errorf("admin = %+v, want the Unix socket with persist on", cfg.Admin)
	}

	https := cfg.Apps.HTTP.Servers["https"]
	if len(https.Listen) != 1 || https.Listen[0] != ":443" || len(https.TLSConnectionPolicies) != 1 {
		t.Errorf("https server listen=%v tls policies=%d", https.Listen, len(https.TLSConnectionPolicies))
	}
	if len(https.Routes) != 3 {
		t.Fatalf("https server has %d routes, want 2 + catch-all", len(https.Routes))
	}
	proxy := https.Routes[0]
	if got := proxy.Match[0].Host; strings.Join(got, ",") != "example.com,app.example.com" {
		t.Errorf("proxy route hosts = %v", got)
	}
	if h := proxy.Handle[0]; h.Handler != "reverse_proxy" || h.Upstreams[0].Dial != "127.0.0.1:32768" {
		t.Errorf("proxy handler = %+v", h)
	}
	if h := https.Routes[1].Handle[0]; h.StatusCode != 308 || h.Headers["Location"][0] != "https://example.com{http.request.uri}" {
		t.Errorf("redirect handler = %+v", h)
	}
	assertNotFound(t, https.Routes[2])

	plain := cfg.Apps.HTTP.Servers["http"]
	if len(plain.Routes) != 2 {
		t.Fatalf("http server has %d routes, want redirect + catch-all", len(plain.Routes))
	}
	if got := plain.Routes[0].Match[0].Host; len(got) != 3 {
		t.Errorf("http->https redirect covers %v, want all 3 hostnames", got)
	}
	if loc := plain.Routes[0].Handle[0].Headers["Location"][0]; loc != "https://{http.request.host}{http.request.uri}" {
		t.Errorf("http->https Location = %q", loc)
	}
	assertNotFound(t, plain.Routes[1])

	tlsApp := cfg.Apps.TLS
	if tlsApp == nil || len(tlsApp.Automation.Policies) != 1 {
		t.Fatalf("tls app = %+v, want one automation policy", tlsApp)
	}
	pol := tlsApp.Automation.Policies[0]
	if !pol.OnDemand || pol.Issuers[0] != (issuer{Module: "acme", Email: "ops@example.com"}) {
		t.Errorf("automation policy = %+v", pol)
	}
	if p := tlsApp.Automation.OnDemand.Permission; p.Module != "http" || p.Endpoint != testOptions().AskURL {
		t.Errorf("on-demand permission = %+v", p)
	}
	if cfg.Apps.HTTP.GracePeriod != "10s" {
		t.Errorf("grace_period = %q, want a bounded shutdown", cfg.Apps.HTTP.GracePeriod)
	}
	if !https.AutomaticHTTPS.Disable || !plain.AutomaticHTTPS.Disable {
		t.Error("automatic HTTPS must be disabled; redirects and TLS are rendered explicitly")
	}
}

func TestRenderNonDefaultPortsInRedirects(t *testing.T) {
	opts := testOptions()
	opts.HTTPPort, opts.HTTPSPort = 18080, 18443
	cfg := render(t, opts, []node.Route{{Hostnames: []string{"www.example.com"}, RedirectTo: "example.com"}})

	if loc := cfg.Apps.HTTP.Servers["https"].Routes[0].Handle[0].Headers["Location"][0]; loc != "https://example.com:18443{http.request.uri}" {
		t.Errorf("redirect Location = %q", loc)
	}
	if loc := cfg.Apps.HTTP.Servers["http"].Routes[0].Handle[0].Headers["Location"][0]; loc != "https://{http.request.host}:18443{http.request.uri}" {
		t.Errorf("http->https Location = %q", loc)
	}
}

func TestRenderWithoutTLS(t *testing.T) {
	opts := testOptions()
	opts.DisableTLS, opts.AskURL = true, ""
	cfg := render(t, opts, []node.Route{
		{Hostnames: []string{"app.internal"}, Upstream: "127.0.0.1:9000"},
		{Hostnames: []string{"www.app.internal"}, RedirectTo: "app.internal"},
	})

	if cfg.Apps.TLS != nil || len(cfg.Apps.HTTP.Servers) != 1 {
		t.Fatalf("want a single plain HTTP server and no tls app, got %d servers, tls=%v", len(cfg.Apps.HTTP.Servers), cfg.Apps.TLS)
	}
	srv := cfg.Apps.HTTP.Servers["http"]
	if srv.Listen[0] != ":80" || len(srv.TLSConnectionPolicies) != 0 {
		t.Errorf("http server = %+v", srv)
	}
	if loc := srv.Routes[1].Handle[0].Headers["Location"][0]; loc != "http://app.internal{http.request.uri}" {
		t.Errorf("redirect Location = %q", loc)
	}
	assertNotFound(t, srv.Routes[2])
}

func TestRenderTunnel(t *testing.T) {
	opts := Options{AdminSocket: "/run/dock/caddy-admin.sock", Ingress: IngressCloudflareTunnel, HTTPPort: 80, ServerMarker: "srv-1"}
	cfg := render(t, opts, []node.Route{
		{Hostnames: []string{"web-shop-staging.up.example.dev"}, Upstream: "127.0.0.1:9000"},
		{Hostnames: []string{"www.shop.example.com"}, RedirectTo: "shop.example.com"},
	})

	if cfg.Apps.TLS != nil || len(cfg.Apps.HTTP.Servers) != 1 {
		t.Fatalf("want a single plain HTTP server and no tls app, got %d servers, tls=%v", len(cfg.Apps.HTTP.Servers), cfg.Apps.TLS)
	}
	srv := cfg.Apps.HTTP.Servers["http"]
	if len(srv.Listen) != 1 || srv.Listen[0] != "127.0.0.1:80" || len(srv.TLSConnectionPolicies) != 0 {
		t.Errorf("listen=%v tls policies=%d, want plain HTTP on loopback only", srv.Listen, len(srv.TLSConnectionPolicies))
	}
	if tp := srv.TrustedProxies; tp == nil || tp.Source != "static" || strings.Join(tp.Ranges, ",") != "127.0.0.1/32,::1/128" {
		t.Errorf("trusted_proxies = %+v, want loopback only", tp)
	}
	if got := strings.Join(srv.ClientIPHeaders, ","); got != "CF-Connecting-IP,X-Forwarded-For" {
		t.Errorf("client_ip_headers = %s", got)
	}
	if len(srv.Routes) != 4 {
		t.Fatalf("%d routes, want marker + 2 + catch-all", len(srv.Routes))
	}
	assertMarker(t, srv.Routes[0], "srv-1")
	if h := srv.Routes[1].Handle[0]; h.Handler != "reverse_proxy" || h.Upstreams[0].Dial != "127.0.0.1:9000" {
		t.Errorf("proxy handler = %+v", h)
	}
	// The public side is Cloudflare's HTTPS edge, not Caddy's port.
	if loc := srv.Routes[2].Handle[0].Headers["Location"][0]; loc != "https://shop.example.com{http.request.uri}" {
		t.Errorf("redirect Location = %q", loc)
	}
	assertNotFound(t, srv.Routes[3])
}

func TestRenderMarkerDirect(t *testing.T) {
	opts := testOptions()
	opts.ServerMarker = "srv-2"
	cfg := render(t, opts, []node.Route{{Hostnames: []string{"a.com"}, Upstream: "127.0.0.1:1"}})
	for name, srv := range cfg.Apps.HTTP.Servers {
		assertMarker(t, srv.Routes[0], "srv-2")
		if srv.TrustedProxies != nil || srv.Listen[0][0] != ':' {
			t.Errorf("%s server: direct mode listens publicly and trusts no proxies, got %v %+v", name, srv.Listen, srv.TrustedProxies)
		}
	}
	if cfg.Apps.HTTP.Servers["https"].Routes[1].Match[0].Host[0] != "a.com" {
		t.Error("routes must follow the marker route")
	}
}

func TestBootstrapConfig(t *testing.T) {
	b, err := BootstrapConfig(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	var cfg caddyConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	for name, srv := range cfg.Apps.HTTP.Servers {
		if len(srv.Routes) != 1 {
			t.Errorf("%s server has %d routes, want only the catch-all", name, len(srv.Routes))
			continue
		}
		assertNotFound(t, srv.Routes[0])
	}
	if cfg.Admin.Listen == "" || cfg.Apps.TLS == nil {
		t.Error("bootstrap config must set the admin socket and the gated on-demand TLS policy")
	}
}

func TestRenderRejectsBadInput(t *testing.T) {
	up := "127.0.0.1:1"
	for name, tc := range map[string]struct {
		opts   func(*Options)
		routes []node.Route
	}{
		"no hostnames":       {routes: []node.Route{{Upstream: up}}},
		"no target":          {routes: []node.Route{{Hostnames: []string{"a.com"}}}},
		"duplicate hostname": {routes: []node.Route{{Hostnames: []string{"a.com"}, Upstream: up}, {Hostnames: []string{"a.com"}, Upstream: up}}},
		"placeholder":        {routes: []node.Route{{Hostnames: []string{"{env.X}"}, Upstream: up}}},
		"upper case":         {routes: []node.Route{{Hostnames: []string{"A.com"}, Upstream: up}}},
		"bad redirect":       {routes: []node.Route{{Hostnames: []string{"a.com"}, RedirectTo: "b.com/{uri}"}}},
		"missing ask URL":    {opts: func(o *Options) { o.AskURL = "" }},
		"missing socket":     {opts: func(o *Options) { o.AdminSocket = "" }},
		"unknown ingress":    {opts: func(o *Options) { o.Ingress = "tailscale" }},
		"tunnel, no marker":  {opts: func(o *Options) { o.Ingress = IngressCloudflareTunnel }},
		"marker placeholder": {opts: func(o *Options) { o.ServerMarker = "{env.HOME}" }},
		"public traffic":     {opts: func(o *Options) { o.TrafficAddr = "0.0.0.0:7074" }},
		"traffic, no port":   {opts: func(o *Options) { o.TrafficAddr = "127.0.0.1" }},
	} {
		t.Run(name, func(t *testing.T) {
			opts := testOptions()
			if tc.opts != nil {
				tc.opts(&opts)
			}
			if _, err := Render(opts, tc.routes); err == nil {
				t.Error("Render succeeded, want an error")
			}
		})
	}
}

func TestNotFoundPage(t *testing.T) {
	if !strings.Contains(notFoundPage, "background:#000") || !strings.Contains(notFoundPage, "color:#fff") || strings.Contains(notFoundPage, "—") {
		t.Error("404 page should be white on true black with no em dashes")
	}
}

func assertMarker(t *testing.T, r route, marker string) {
	t.Helper()
	h := r.Handle[0]
	if len(r.Match) != 0 || r.Terminal || h.Handler != "headers" || h.Response == nil || !h.Response.Deferred ||
		len(h.Response.Set[MarkerHeader]) != 1 || h.Response.Set[MarkerHeader][0] != marker {
		t.Errorf("marker route = %+v %+v, want a deferred, non-terminal %s: %s for every request", r, h.Response, MarkerHeader, marker)
	}
}

func assertNotFound(t *testing.T, r route) {
	t.Helper()
	if len(r.Match) != 0 {
		t.Errorf("catch-all route has matchers %+v", r.Match)
	}
	if h := r.Handle[0]; h.Handler != "static_response" || h.StatusCode != 404 || h.Body != notFoundPage {
		t.Errorf("catch-all handler = %s %d", h.Handler, h.StatusCode)
	}
}

// A route with a path prefix proxies only that path; the host's other
// paths fall through to the 404 page.
func TestRenderPathPrefix(t *testing.T) {
	cfg := render(t, testOptions(), []node.Route{
		{Hostnames: []string{"hooks.example.com"}, Upstream: "127.0.0.1:7072", PathPrefix: "/hooks/github/"},
	})
	r := cfg.Apps.HTTP.Servers["https"].Routes[0]
	if m := r.Match[0]; len(m.Path) != 1 || m.Path[0] != "/hooks/github/*" || m.Host[0] != "hooks.example.com" {
		t.Fatalf("match = %+v", m)
	}
	assertNotFound(t, cfg.Apps.HTTP.Servers["https"].Routes[1])
	for _, bad := range []string{"/hooks/{uri}/", "hooks/", "/hooks", "/a//b/", "/*/"} {
		if _, err := Render(testOptions(), []node.Route{{Hostnames: []string{"a.com"}, Upstream: "127.0.0.1:1", PathPrefix: bad}}); err == nil {
			t.Errorf("path prefix %q: Render succeeded, want an error", bad)
		}
	}
}

func TestRenderClientIPHeader(t *testing.T) {
	cfg := render(t, testOptions(), []node.Route{
		{Hostnames: []string{"dock.example.com"}, Upstream: "127.0.0.1:7073", ClientIPHeader: "X-Dock-Client-Ip"},
	})
	hs := cfg.Apps.HTTP.Servers["https"].Routes[0].Handle
	if len(hs) != 2 || hs[0].Handler != "headers" || hs[0].Request == nil || hs[0].Response != nil || hs[1].Handler != "reverse_proxy" {
		t.Fatalf("handlers = %+v", hs)
	}
	if got := hs[0].Request.Set["X-Dock-Client-Ip"]; len(got) != 1 || got[0] != "{http.vars.client_ip}" {
		t.Fatalf("set = %v", hs[0].Request.Set)
	}
}

func TestRenderAccess(t *testing.T) {
	opts := testOptions()
	opts.AccessURL = "http://127.0.0.1:7071/internal/caddy/access"
	cfg := render(t, opts, []node.Route{
		{Hostnames: []string{"pw.example.com"}, Upstream: "127.0.0.1:9001", Access: &node.Access{Username: "team", PasswordHash: "$2a$06$abc"}},
		{Hostnames: []string{"login.example.com"}, Upstream: "127.0.0.1:9002", Access: &node.Access{Key: []byte{1, 2, 3}, LoginURL: "https://dock.example.com/access"}},
		{Hostnames: []string{"www.example.com"}, RedirectTo: "login.example.com", Access: &node.Access{Key: []byte{1}, LoginURL: "https://dock.example.com/access"}},
	})
	routes := cfg.Apps.HTTP.Servers["https"].Routes

	pw := routes[0].Handle
	if len(pw) != 3 || pw[0].Handler != "authentication" || pw[1].Handler != "headers" || pw[2].Handler != "reverse_proxy" {
		t.Fatalf("password handlers = %+v", pw)
	}
	if acc := pw[0].Providers.HTTPBasic; acc.Accounts[0] != (account{"team", "$2a$06$abc"}) || acc.Hash.Algorithm != "bcrypt" || acc.HashCache == nil {
		t.Errorf("basic auth = %+v", acc)
	}
	if got := pw[1].Request.Set["X-Dock-User"]; len(got) != 1 || got[0] != "{http.auth.user.id}" {
		t.Errorf("user header = %v", got)
	}

	login := routes[1].Handle
	if len(login) != 3 || login[0].Handler != "headers" || login[1].Handler != "reverse_proxy" || login[2].Upstreams[0].Dial != "127.0.0.1:9002" {
		t.Fatalf("login handlers = %+v", login)
	}
	if set := login[0].Request.Set; set[accessKeyHeader][0] != "AQID" || set[accessLoginHeader][0] != "https://dock.example.com/access" {
		t.Errorf("check headers = %v", set)
	}
	check := login[1]
	if check.Upstreams[0].Dial != "127.0.0.1:7071" || *check.Rewrite != (rewrite{"GET", "/internal/caddy/access"}) {
		t.Errorf("check = %+v %+v", check.Upstreams, check.Rewrite)
	}
	ok := check.HandleResponse[0]
	clean := ok.Routes[0].Handle[0].Request
	if ok.Match.StatusCode[0] != 2 || len(clean.Delete) != 4 || clean.Set["X-Dock-User"][0] != "{http.reverse_proxy.header.X-Dock-User}" {
		t.Errorf("2xx handling = %+v %+v", ok.Match, clean)
	}

	if h := routes[2].Handle; len(h) != 1 || h[0].StatusCode != 308 {
		t.Errorf("a redirect stays a plain redirect: %+v", h)
	}

	for name, a := range map[string]*node.Access{
		"both":            {Username: "u", PasswordHash: "$2a$06$abc", Key: []byte{1}, LoginURL: "https://d/access"},
		"plain password":  {Username: "u", PasswordHash: "secret"},
		"no username":     {PasswordHash: "$2a$06$abc"},
		"colon username":  {Username: "a:b", PasswordHash: "$2a$06$abc"},
		"brace username":  {Username: "{env.X}", PasswordHash: "$2a$06$abc"},
		"no login URL":    {Key: []byte{1}},
		"brace login URL": {Key: []byte{1}, LoginURL: "https://{host}/access"},
	} {
		if _, err := Render(opts, []node.Route{{Hostnames: []string{"a.com"}, Upstream: "127.0.0.1:1", Access: a}}); err == nil {
			t.Errorf("%s: Render succeeded, want an error", name)
		}
	}
	login1 := []node.Route{{Hostnames: []string{"a.com"}, Upstream: "127.0.0.1:1", Access: &node.Access{Key: []byte{1}, LoginURL: "https://d/access"}}}
	if _, err := Render(testOptions(), login1); err == nil {
		t.Error("login access without an access URL rendered")
	}
}

// Counted routes get their access log sent to the traffic counter, with
// the user agent appended; other hostnames aren't logged at all.
func TestRenderTraffic(t *testing.T) {
	opts := testOptions()
	opts.TrafficAddr = "127.0.0.1:7074"
	cfg := render(t, opts, []node.Route{
		{Hostnames: []string{"shop.example.com", "shop.test"}, Upstream: "127.0.0.1:1", Traffic: true},
		{Hostnames: []string{"www.shop.example.com"}, RedirectTo: "shop.example.com", Traffic: true},
		{Hostnames: []string{"dock.example.com"}, Upstream: "127.0.0.1:2"},
	})
	https := cfg.Apps.HTTP.Servers["https"]
	logs := https.Logs
	if logs == nil || !logs.SkipUnmappedHosts || len(logs.LoggerNames) != 2 || logs.LoggerNames["shop.test"][0] != "dock" {
		t.Fatalf("https logs = %+v; want the shop hostnames only", logs)
	}
	if h := https.Routes[0].Handle[0]; h.Handler != "log_append" || h.Key != "ua" || h.Value != "{http.request.header.User-Agent}" {
		t.Errorf("counted route starts with %+v, want log_append of the user agent", h)
	}
	if h := https.Routes[2].Handle[0]; h.Handler != "reverse_proxy" {
		t.Errorf("uncounted route starts with %+v", h)
	}
	if cfg.Apps.HTTP.Servers["http"].Logs != nil {
		t.Error("the redirecting http server logs")
	}
	dock := cfg.Logging.Logs["dock"]
	if dock.Writer.Address != "udp/127.0.0.1:7074" || !dock.Writer.SoftStart || dock.Include[0] != "http.log.access.dock" {
		t.Errorf("dock log = %+v", dock)
	}
	if _, ok := dock.Encoder.Fields["request>headers"]; !ok {
		t.Error("request headers are encoded")
	}
	if ex := cfg.Logging.Logs["default"].Exclude; len(ex) != 1 || ex[0] != "http.log.access.dock" {
		t.Errorf("default log excludes %v", ex)
	}

	// Without an address, or without counted routes, nothing is logged.
	for _, o := range []Options{testOptions(), opts} {
		cfg := render(t, o, []node.Route{{Hostnames: []string{"a.example.com"}, Upstream: "127.0.0.1:1", Traffic: o.TrafficAddr == ""}})
		if cfg.Logging != nil || cfg.Apps.HTTP.Servers["https"].Logs != nil {
			t.Errorf("logging rendered with %+v", o)
		}
	}
}

// TestRenderTunnelListener adds a direct server's tunnel listener (ADR
// 0036): every route on 127.0.0.1:TunnelPort, trusting forwarding headers
// from loopback, while the public listeners leave tunnel-only routes out,
// and so does the on-demand TLS check.
func TestRenderTunnelListener(t *testing.T) {
	opts := testOptions()
	opts.TunnelPort = 7075
	routes := []node.Route{
		{Hostnames: []string{"app.example.com"}, Upstream: "127.0.0.1:32768"},
		{Hostnames: []string{"app.tempo.run"}, Upstream: "127.0.0.1:32768", TunnelOnly: true},
	}
	cfg := render(t, opts, routes)
	hosts := func(name string) []string {
		var out []string
		for _, r := range cfg.Apps.HTTP.Servers[name].Routes {
			for _, m := range r.Match {
				out = append(out, m.Host...)
			}
		}
		return out
	}
	tunnel := cfg.Apps.HTTP.Servers[tunnelServer]
	if len(tunnel.Listen) != 1 || tunnel.Listen[0] != "127.0.0.1:7075" || tunnel.TrustedProxies == nil {
		t.Fatalf("tunnel server = %+v", tunnel)
	}
	if got := strings.Join(hosts(tunnelServer), ","); got != "app.example.com,app.tempo.run" {
		t.Errorf("tunnel listener serves %s", got)
	}
	for _, name := range []string{"https", "http"} {
		if got := strings.Join(hosts(name), ","); got != "app.example.com" {
			t.Errorf("%s listener serves %s", name, got)
		}
	}
	if s := routeHosts(routes); len(*s) != 1 {
		t.Errorf("certificates asked for %v", *s)
	}
	if _, ok := render(t, opts, routes[:1]).Apps.HTTP.Servers[tunnelServer]; ok {
		t.Error("a tunnel listener with no tunnel-only route")
	}
	opts.TunnelPort = 0
	if _, ok := render(t, opts, routes).Apps.HTTP.Servers[tunnelServer]; ok {
		t.Error("a tunnel listener without a tunnel port")
	}
}
