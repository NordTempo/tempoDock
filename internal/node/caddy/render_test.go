package caddy

import (
	"encoding/json"
	"slices"
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
	b, err := Render(opts, routes, nil)
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

	// Plain HTTP sends every host to HTTPS, known or not (ADR 0087): one
	// route with no matcher. Caddy answers ACME HTTP-01 challenges before
	// any route, so certificates are still issued.
	plain := cfg.Apps.HTTP.Servers["http"]
	if len(plain.Routes) != 1 || len(plain.Routes[0].Match) != 0 || !plain.Routes[0].Terminal {
		t.Fatalf("http routes = %+v, want one redirect for every host", plain.Routes)
	}
	if h := plain.Routes[0].Handle[0]; h.StatusCode != 308 || h.Headers["Location"][0] != "https://{http.request.host}{http.request.uri}" {
		t.Errorf("http->https redirect = %+v", h)
	}

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
		if name == "http" {
			continue // the redirect to HTTPS (TestRenderTLS)
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
			if _, err := Render(opts, tc.routes, nil); err == nil {
				t.Error("Render succeeded, want an error")
			}
		})
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
		if _, err := Render(testOptions(), []node.Route{{Hostnames: []string{"a.com"}, Upstream: "127.0.0.1:1", PathPrefix: bad}}, nil); err == nil {
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
	// The spelling some app servers read as the same header never
	// arrives from the client.
	if !slices.Contains(pw[1].Request.Delete, "X_Dock_User") {
		t.Errorf("the password route keeps a client's X_Dock_User: %v", pw[1].Request.Delete)
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
	if ok.Match.StatusCode[0] != 2 || len(clean.Delete) != 5 || !slices.Contains(clean.Delete, "X_Dock_User") || clean.Set["X-Dock-User"][0] != "{http.reverse_proxy.header.X-Dock-User}" {
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
		if _, err := Render(opts, []node.Route{{Hostnames: []string{"a.com"}, Upstream: "127.0.0.1:1", Access: a}}, nil); err == nil {
			t.Errorf("%s: Render succeeded, want an error", name)
		}
	}
	login1 := []node.Route{{Hostnames: []string{"a.com"}, Upstream: "127.0.0.1:1", Access: &node.Access{Key: []byte{1}, LoginURL: "https://d/access"}}}
	if _, err := Render(testOptions(), login1, nil); err == nil {
		t.Error("login access without an access URL rendered")
	}
}

// An asleep environment's route asks the wake check after its access
// check and before proxying; Dock's uptime probes on a sleepable route
// skip the access log in a route of their own ahead of it.
func TestRenderWake(t *testing.T) {
	opts := testOptions()
	opts.AccessURL = "http://127.0.0.1:7071/internal/caddy/access"
	opts.WakeURL = "http://127.0.0.1:7071/internal/caddy/wake"
	cfg := render(t, opts, []node.Route{
		{Hostnames: []string{"pr-12.example.com"}, Upstream: "127.0.0.1:9001", Wake: "42", QuietProbes: true,
			Access: &node.Access{Key: []byte{1}, LoginURL: "https://dock.example.com/access"}},
	})
	routes := cfg.Apps.HTTP.Servers["https"].Routes
	if len(routes) != 3 {
		t.Fatalf("routes = %+v", routes)
	}
	quiet := routes[0]
	if quiet.Terminal || quiet.Match[0].Header["User-Agent"][0] != "dock-uptime/*" || !quiet.Handle[0].LogSkip {
		t.Errorf("probe route = %+v", quiet)
	}
	h := routes[1].Handle
	if len(h) != 5 || h[1].Rewrite.URI != "/internal/caddy/access" || h[3].Rewrite.URI != "/internal/caddy/wake" || h[4].Upstreams[0].Dial != "127.0.0.1:9001" {
		t.Fatalf("handlers = %+v", h)
	}
	if set := h[2].Request.Set; set[wakeKeyHeader][0] != "42" || set[forwardedURI][0] != "{http.request.uri}" {
		t.Errorf("wake headers = %v", set)
	}
	if ok := h[3].HandleResponse[0]; ok.Match.StatusCode[0] != 2 || len(ok.Routes[0].Handle[0].Request.Delete) != 3 {
		t.Errorf("2xx handling = %+v", ok)
	}
	for name, r := range map[string]node.Route{
		"no wake URL": {Hostnames: []string{"a.com"}, Upstream: "127.0.0.1:1", Wake: "1"},
		"placeholder": {Hostnames: []string{"a.com"}, Upstream: "127.0.0.1:1", Wake: "{env.X}"},
	} {
		o := testOptions()
		if name != "no wake URL" {
			o.WakeURL = opts.WakeURL
		}
		if _, err := Render(o, []node.Route{r}, nil); err == nil {
			t.Errorf("%s: Render succeeded, want an error", name)
		}
	}
}

// Counted routes get their access log sent to the traffic counter, without
// who sent each request; other hostnames aren't logged at all.
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
	if cfg.Apps.HTTP.Servers["http"].Logs != nil {
		t.Error("the redirecting http server logs")
	}
	dock := cfg.Logging.Logs["dock"]
	if dock.Writer.Address != "udp/127.0.0.1:7074" || !dock.Writer.SoftStart || dock.Include[0] != "http.log.access.dock" {
		t.Errorf("dock log = %+v", dock)
	}
	for _, f := range []string{"request>headers", "request>remote_ip", "request>client_ip"} {
		if _, ok := dock.Encoder.Fields[f]; !ok {
			t.Errorf("%s is encoded", f)
		}
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
	if got := strings.Join(hosts("https"), ","); got != "app.example.com" {
		t.Errorf("https listener serves %s", got)
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

func TestRenderAnalytics(t *testing.T) {
	opts := testOptions()
	opts.AnalyticsAddr = "127.0.0.1:7077"
	routes := []node.Route{{
		Hostnames: []string{"shop.example.com", "www.shop.example.com", "admin.example.com"}, Upstream: "127.0.0.1:20001",
		Analytics: []node.AnalyticsHosts{{Hostnames: []string{"shop.example.com", "www.shop.example.com"}, Tag: "3.40.981"}},
	}}
	rs := render(t, opts, routes).Apps.HTTP.Servers["https"].Routes
	// The analytics route comes before the service's, which is terminal.
	if len(rs) != 3 || rs[0].Match[0].Path[0] != "/_dock/a/*" || len(rs[0].Match[0].Host) != 2 || rs[1].Handle[len(rs[1].Handle)-1].Upstreams[0].Dial != "127.0.0.1:20001" {
		t.Fatalf("routes = %+v", rs)
	}
	hs := rs[0].Handle
	if !hs[0].LogSkip || hs[1].Request.Set["X-Dock-Analytics"][0] != "3.40.981" || hs[1].Request.Set["X-Dock-Client-Ip"][0] != "{http.vars.client_ip}" || hs[2].Upstreams[0].Dial != "127.0.0.1:7077" {
		t.Errorf("analytics handlers = %+v", hs)
	}

	// A tunnel-only route's visitors arrive on the tunnel listener, and
	// are counted there.
	opts.TunnelPort = 7075
	tunnelOnly := []node.Route{{
		Hostnames: []string{"shop.example.com"}, Upstream: "127.0.0.1:20001", TunnelOnly: true,
		Analytics: []node.AnalyticsHosts{{Hostnames: []string{"shop.example.com"}, Tag: "3.40.981"}},
	}}
	if rs := render(t, opts, tunnelOnly).Apps.HTTP.Servers[tunnelServer].Routes; len(rs) < 2 || len(rs[0].Match[0].Path) == 0 || rs[0].Match[0].Path[0] != "/_dock/a/*" {
		t.Errorf("tunnel listener routes = %+v", rs)
	}
	opts.TunnelPort = 0

	// Without an ingest address, nothing goes there.
	if rs := render(t, testOptions(), routes).Apps.HTTP.Servers["https"].Routes; len(rs) != 2 {
		t.Errorf("without an ingest: %d routes, want the service's and the 404", len(rs))
	}

	for name, a := range map[string]node.AnalyticsHosts{
		"a placeholder in the tag":    {Hostnames: []string{"shop.example.com"}, Tag: "{http.request.host}"},
		"two numbers":                 {Hostnames: []string{"shop.example.com"}, Tag: "3.40"},
		"a hostname of another route": {Hostnames: []string{"evil.example.com"}, Tag: "3.40.981"},
	} {
		bad := []node.Route{{Hostnames: []string{"shop.example.com"}, Upstream: "127.0.0.1:20001", Analytics: []node.AnalyticsHosts{a}}}
		if _, err := Render(opts, bad, nil); err == nil {
			t.Errorf("%s: rendered", name)
		}
	}
}

// Groups with Script get dock_inject for their hostnames, right before
// the upstream; redirects, stopped routes and a stock Caddy don't.
func TestRenderScriptInject(t *testing.T) {
	opts := testOptions()
	opts.AnalyticsAddr = "127.0.0.1:7077"
	route := node.Route{
		Hostnames: []string{"shop.example.com", "www.shop.example.com", "admin.example.com"}, Upstream: "127.0.0.1:20001",
		Analytics: []node.AnalyticsHosts{
			{Hostnames: []string{"shop.example.com", "www.shop.example.com"}, Tag: "3.40.981", Script: true},
			{Hostnames: []string{"admin.example.com"}, Tag: "4.40.981"},
		},
	}
	inject := func(o Options, r node.Route) *handler {
		rs := render(t, o, []node.Route{r}).Apps.HTTP.Servers["https"].Routes
		for _, r := range rs {
			for i, h := range r.Handle {
				if h.Handler == "dock_inject" {
					if i != len(r.Handle)-2 || r.Handle[i+1].Handler != "reverse_proxy" {
						t.Errorf("dock_inject at %d of %+v", i, r.Handle)
					}
					return &h
				}
			}
		}
		return nil
	}
	h := inject(opts, route)
	if h == nil || !slices.Equal(h.Hosts, []string{"shop.example.com", "www.shop.example.com"}) || h.Src != "/_dock/a/s.js" {
		t.Fatalf("dock_inject = %+v", h)
	}
	stopped := route
	stopped.Upstream, stopped.Stopped = "", "web"
	redirect := route
	redirect.Upstream, redirect.RedirectTo = "", "example.com"
	stock := opts
	stock.noInject = true
	for name, c := range map[string]struct {
		o Options
		r node.Route
	}{"stopped": {opts, stopped}, "redirect": {opts, redirect}, "stock Caddy": {stock, route}, "no ingest": {testOptions(), route}} {
		if h := inject(c.o, c.r); h != nil {
			t.Errorf("%s: %+v", name, h)
		}
	}
}

func TestRenderRefusesARouteWithoutHostnames(t *testing.T) {
	// Not a panic: the access check reads the first hostname for its
	// message.
	_, err := Render(testOptions(), []node.Route{{Upstream: "127.0.0.1:1", Access: &node.Access{Username: "x"}}}, nil)
	if err == nil {
		t.Error("rendered a route without hostnames")
	}
}

// Traced apps reach the trace ingest as dock-otlp on the server's HTTP
// port, from private addresses only; not in tunnel mode, where Caddy
// listens only on loopback.
func TestRenderTraceIngest(t *testing.T) {
	routes := []node.Route{{Hostnames: []string{"shop.example.com"}, Upstream: "127.0.0.1:20001"}}
	otlp := func(rs []route) *route {
		for i := range rs {
			if len(rs[i].Match) > 0 && len(rs[i].Match[0].Host) == 1 && rs[i].Match[0].Host[0] == node.TraceIngestHost {
				return &rs[i]
			}
		}
		return nil
	}
	opts := testOptions()
	opts.TraceIngestAddr = "127.0.0.1:4318"
	cfg := render(t, opts, routes)
	r := otlp(cfg.Apps.HTTP.Servers["http"].Routes)
	if r == nil || r.Match[0].RemoteIP == nil || len(r.Match[0].RemoteIP.Ranges) == 0 || r.Handle[len(r.Handle)-1].Upstreams[0].Dial != "127.0.0.1:4318" {
		t.Fatalf("http routes = %+v", cfg.Apps.HTTP.Servers["http"].Routes)
	}
	if otlp(cfg.Apps.HTTP.Servers["https"].Routes) != nil {
		t.Error("dock-otlp on the HTTPS listener")
	}
	opts.DisableTLS = true
	if otlp(render(t, opts, routes).Apps.HTTP.Servers["http"].Routes) == nil {
		t.Error("no dock-otlp on a plain-HTTP server")
	}
	opts.DisableTLS, opts.Ingress = false, IngressCloudflareTunnel
	opts.ServerMarker = "srv-1"
	for name, srv := range render(t, opts, routes).Apps.HTTP.Servers {
		if otlp(srv.Routes) != nil {
			t.Errorf("dock-otlp on %s in tunnel mode", name)
		}
	}
	opts = testOptions()
	opts.TraceIngestAddr = "0.0.0.0:4318"
	if _, err := Render(opts, routes, nil); err == nil {
		t.Error("a trace ingest that isn't on loopback was rendered")
	}
}

// A traced route drops the visitor's trace headers and starts a trace
// after its access check, just before proxying; a redirect or a stopped
// service isn't traced.
func TestRenderEdgeTrace(t *testing.T) {
	cfg := render(t, testOptions(), []node.Route{
		{Hostnames: []string{"shop.example.com"}, Upstream: "127.0.0.1:9001", Trace: "dtr1.x", Access: &node.Access{Username: "u", PasswordHash: "$2a$06$x"}},
		{Hostnames: []string{"old.example.com"}, RedirectTo: "shop.example.com", Trace: "dtr1.x"},
		{Hostnames: []string{"off.example.com"}, Upstream: "127.0.0.1:9002", Stopped: "web", Trace: "dtr1.x"},
	})
	routes := cfg.Apps.HTTP.Servers["https"].Routes
	h := routes[0].Handle
	n := len(h)
	if n < 4 || h[0].Handler != "authentication" || h[n-3].Handler != "headers" || h[n-2].Handler != "tracing" || h[n-2].Span != "edge" || h[n-1].Handler != "reverse_proxy" {
		t.Fatalf("traced handlers = %+v", h)
	}
	if del := h[n-3].Request.Delete; del[0] != "traceparent" || !slices.Contains(del, "X-B3-Sampled") || !slices.Contains(del, "uber-trace-id") {
		t.Errorf("trace headers dropped = %v", del)
	}
	for _, r := range routes[1:] {
		for _, x := range r.Handle {
			if x.Handler == "tracing" {
				t.Errorf("%v is traced: %+v", r.Match, r.Handle)
			}
		}
	}
}

// TestRenderSecurityHeaders: a route with SecurityHeaders (ADR 0107) has
// one deferred headers handler per header ahead of everything else, each
// required absent from the response so the app's own wins; HSTS only on
// HTTPS, and redirects and routes without the flag get none.
func TestRenderSecurityHeaders(t *testing.T) {
	routes := []node.Route{
		{Hostnames: []string{"shop.example.com"}, Upstream: "127.0.0.1:9001", SecurityHeaders: true, Access: &node.Access{Username: "u", PasswordHash: "$2a$06$x"}},
		{Hostnames: []string{"old.example.com"}, RedirectTo: "shop.example.com", SecurityHeaders: true},
		{Hostnames: []string{"plain.example.com"}, Upstream: "127.0.0.1:9002"},
	}
	// added lists the headers a route's deferred handlers add, in order.
	added := func(r route) []string {
		var out []string
		for i, h := range r.Handle {
			if h.Handler != "headers" || h.Response == nil || !h.Response.Deferred {
				continue
			}
			for name, v := range h.Response.Set {
				if req, ok := h.Response.Require.Headers[name]; !ok || req != nil || len(h.Response.Set) != 1 {
					t.Errorf("%s handler %+v overwrites the app's", name, *h.Response)
				}
				if i >= len(node.SecurityHeaders) {
					t.Errorf("%s set at handler %d, after the access check", name, i)
				}
				out = append(out, name+": "+v[0])
			}
		}
		return out
	}
	all := []string{
		"Strict-Transport-Security: max-age=31536000", "X-Content-Type-Options: nosniff",
		"Referrer-Policy: strict-origin-when-cross-origin", "X-Frame-Options: SAMEORIGIN",
	}

	https := render(t, testOptions(), routes).Apps.HTTP.Servers["https"].Routes
	if got := added(https[0]); !slices.Equal(got, all) {
		t.Errorf("headers on https = %v, want %v", got, all)
	}
	// Basic auth's 401 is written outside the route: the headers are set
	// before the check, and deleted once it passes.
	h := https[0].Handle[len(node.SecurityHeaders):]
	if len(h) < 3 || h[0].Response == nil || len(h[0].Response.Set) != len(all) || h[0].Response.Deferred ||
		h[1].Handler != "authentication" || h[2].Response == nil || len(h[2].Response.Delete) != len(all) {
		t.Errorf("around basic auth: %+v", h)
	}
	if got := https[0].Handle[len(https[0].Handle)-1].Handler; got != "reverse_proxy" {
		t.Errorf("last handler = %s, want the proxy", got)
	}
	for _, r := range https[1:3] {
		if got := added(r); len(got) > 0 {
			t.Errorf("%v gets %v", r.Match[0].Host, got)
		}
	}

	opts := testOptions()
	opts.DisableTLS, opts.AskURL = true, ""
	plain := render(t, opts, routes).Apps.HTTP.Servers["http"].Routes
	if got := added(plain[0]); !slices.Equal(got, all[1:]) {
		t.Errorf("headers on plain HTTP = %v, want %v", got, all[1:])
	}
}

// TestRenderCertificates: a loaded certificate (ADR 0090) is in the tls
// app with a tag, and the https server's policy for its names selects it
// ahead of the catch-all; bad names are refused.
func TestRenderCertificates(t *testing.T) {
	wild := node.Certificate{Names: []string{"*.apps.example.com", "apps.example.com"}, ChainPEM: []byte("CHAIN"), KeyPEM: []byte("KEY")}
	b, err := Render(testOptions(), nil, []node.Certificate{wild})
	if err != nil {
		t.Fatal(err)
	}
	var cfg caddyConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	loaded := cfg.Apps.TLS.Certificates.LoadPEM
	if len(loaded) != 1 || loaded[0].Certificate != "CHAIN" || loaded[0].Key != "KEY" || !slices.Equal(loaded[0].Tags, []string{"dock:*.apps.example.com"}) {
		t.Errorf("load_pem = %+v", loaded)
	}
	ps := cfg.Apps.HTTP.Servers["https"].TLSConnectionPolicies
	if len(ps) != 2 || !slices.Equal(ps[0].Match.SNI, wild.Names) || !slices.Equal(ps[0].CertificateSelection.AnyTag, loaded[0].Tags) || ps[1].Match != nil {
		t.Errorf("connection policies = %+v", ps)
	}

	for _, bad := range []node.Certificate{
		{Names: []string{"*.{http.request.host}"}, ChainPEM: []byte("C"), KeyPEM: []byte("K")},
		{Names: []string{"a.test"}, ChainPEM: []byte("C")},
	} {
		if _, err := Render(testOptions(), nil, []node.Certificate{bad}); err == nil {
			t.Errorf("Render accepted %+v", bad.Names)
		}
	}
	if _, err := Render(testOptions(), nil, []node.Certificate{wild, wild}); err == nil {
		t.Error("Render accepted two certificates for the same names")
	}
}

func TestCovers(t *testing.T) {
	for _, c := range []struct {
		name, host string
		want       bool
	}{
		{"*.apps.test", "web.apps.test", true},
		{"*.apps.test", "apps.test", false},
		{"*.apps.test", "a.web.apps.test", false},
		{"*.apps.test", "webapps.test", false},
		{"apps.test", "apps.test", true},
		{"apps.test", "web.apps.test", false},
	} {
		if got := covers(c.name, c.host); got != c.want {
			t.Errorf("covers(%q, %q) = %v", c.name, c.host, got)
		}
	}
}
