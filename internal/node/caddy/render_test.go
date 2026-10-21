package caddy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/NordTempo/platform/internal/node"
)

func testOptions() Options {
	return Options{
		AdminSocket: "/run/platform/caddy-admin.sock",
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

	if cfg.Admin.Listen != "unix//run/platform/caddy-admin.sock|0660" || !cfg.Admin.Config.Persist {
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

func assertNotFound(t *testing.T, r route) {
	t.Helper()
	if len(r.Match) != 0 {
		t.Errorf("catch-all route has matchers %+v", r.Match)
	}
	if h := r.Handle[0]; h.Handler != "static_response" || h.StatusCode != 404 || h.Body != notFoundPage {
		t.Errorf("catch-all handler = %s %d", h.Handler, h.StatusCode)
	}
}
