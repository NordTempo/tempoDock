package core

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cloudflare"
	"github.com/NordTempo/tempoDock/internal/cloudflare/cloudflaretest"
	"github.com/NordTempo/tempoDock/internal/node/remote"
)

// cfPool is labPool with a fake Cloudflare holding zones and a connection
// "home" to it. The control plane's server ("cp") is direct; homelab is
// in tunnel mode, without a tunnel.
func cfPool(t *testing.T, zones ...string) (*pool, *cloudflaretest.Server) {
	t.Helper()
	p, _ := labPool(t)
	cf := cloudflaretest.New(t, zones...)
	p.opts.CloudflareAPI = cf.URL
	p.restart()
	p.tunnelMode("")
	if _, err := p.c.AddCloudflare(p.ctx, testSystem, apitypes.CreateCloudflareRequest{Name: "home", Token: cloudflaretest.Token}); err != nil {
		t.Fatal(err)
	}
	return p, cf
}

// tunnelMode has homelab (re)connect in tunnel mode, reporting the tunnel
// hostname it runs ("" for none).
func (p *pool) tunnelMode(tunnelHostname string) {
	lab := p.ids["homelab"]
	p.nodes.add(lab, p.rts[lab], remote.Hello{Protocol: remote.Protocol, Facts: remote.Facts{Ingress: remote.IngressCloudflareTunnel, TunnelHostname: tunnelHostname}})
	p.c.NodeStatus(p.nodes.State(lab))
}

// fromCloudflare adds host to ref from Cloudflare.
func (p *pool) fromCloudflare(ref ServiceRef, host string) error {
	_, err := p.c.AddDomain(p.ctx, testSystem, ref, apitypes.AddDomainRequest{Hostname: host, Cloudflare: true})
	return err
}

// record waits until the domain Dock routes as host is no longer pending
// and returns it.
func (p *pool) record(host string) apitypes.CloudflareRecord {
	p.t.Helper()
	var got apitypes.CloudflareRecord
	waitFor(p.t, func() bool {
		list, err := p.c.ListCloudflareRecords(p.ctx, testSystem)
		if err != nil {
			p.t.Fatal(err)
		}
		for _, r := range list {
			if r.Hostname == host && r.Status != apitypes.DNSPending && r.Status != apitypes.DNSDeleting {
				got = r
				return true
			}
		}
		return false
	})
	return got
}

// TestCloudflareConnection checks a token before saving it: a bad token
// or one without tunnel permission is refused with what to fix, and zones
// it can't read DNS records of are left out. Only operators manage
// connections; anyone who can add domains sees the zones. A connection
// that runs a tunnel can't be removed.
func TestCloudflareConnection(t *testing.T) {
	p, _ := labPool(t)
	cf := cloudflaretest.New(t, "tempo.run", "private.dev")
	p.opts.CloudflareAPI = cf.URL
	p.restart()
	add := func(token string) (apitypes.Cloudflare, error) {
		return p.c.AddCloudflare(p.ctx, testSystem, apitypes.CreateCloudflareRequest{Name: "home", Token: token})
	}
	if _, err := add("wrong"); CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "refused the token") {
		t.Fatalf("bad token: %v", err)
	}
	cf.Mu.Lock()
	cf.NoTunnels = true
	cf.DeniedZones[cf.Zones[1].ID] = true
	cf.Mu.Unlock()
	if _, err := add(cloudflaretest.Token); err == nil || !strings.Contains(err.Error(), "Cloudflare Tunnel › Edit") {
		t.Fatalf("no tunnel permission: %v", err)
	}
	cf.Mu.Lock()
	cf.NoTunnels = false
	cf.Mu.Unlock()
	got, err := add(cloudflaretest.Token)
	if err != nil || got.AccountID != cloudflaretest.AccountID || len(got.Zones) != 1 || got.Zones[0] != "tempo.run" || got.Status != apitypes.CloudflareOK {
		t.Fatalf("add = %+v, %v", got, err)
	}
	deployer := Actor{Name: "ci", Kind: "token", Scope: apitypes.ScopeDeploy, OrgID: 1}
	if _, err := p.c.ListCloudflare(p.ctx, deployer); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("non-operator lists connections: %v", err)
	}
	if zones, err := p.c.ListCloudflareZones(p.ctx, deployer); err != nil || len(zones) != 1 || zones[0] != (apitypes.CloudflareZone{Name: "tempo.run", Connection: "home"}) {
		t.Fatalf("zones = %+v, %v", zones, err)
	}
	p.tunnelMode("")
	if _, err := p.c.SetServerTunnel(p.ctx, testSystem, "homelab", apitypes.ServerTunnelRequest{Action: "create"}); err != nil {
		t.Fatal(err)
	}
	if err := p.c.RemoveCloudflare(p.ctx, testSystem, "home"); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("remove a connection in use: %v", err)
	}
}

// TestCloudflareDomainCreatesTunnel adds a domain from Cloudflare to a
// tunnel server that has no tunnel: Dock creates one routed to the
// server's Caddy (found by name after a crash, not made twice), has the
// server run it and creates the domain's proxied CNAME. A generated domain
// under a managed base domain gets the same, a typed one nothing, and
// there is no wildcard. All of it goes with the domain, the service and
// the server.
func TestCloudflareDomainCreatesTunnel(t *testing.T) {
	p, cf := cfPool(t, "tempo.run")
	lab := p.ids["homelab"]
	early := cf.AddTunnel(p.c.tunnelName("homelab", 1), "cloudflare") // a crash after Cloudflare made it
	web := p.createService("staging", "web", apitypes.CreateServiceRequest{})
	if err := p.fromCloudflare(web, "shop.tempo.run"); err != nil {
		t.Fatal(err)
	}
	if r := p.record("shop.tempo.run"); r.Status != apitypes.DNSReady || r.Tunnel != early.Hostname() {
		t.Fatalf("record = %+v", r)
	}
	if n := cf.CallCount("POST /accounts"); n != 0 {
		t.Errorf("created %d tunnels; the one made before the crash is reused", n)
	}
	waitFor(t, func() bool { got := p.rts[lab].Tunnel(); return got != nil && got.Token == early.Token })
	cf.Mu.Lock()
	cfg := cf.Tunnels[early.ID].Config
	cf.Mu.Unlock()
	if cfg == nil || len(cfg.Ingress) != 1 || cfg.Ingress[0] != (cloudflare.IngressRule{Service: "http://127.0.0.1:80"}) {
		t.Fatalf("tunnel routes = %+v", cfg)
	}
	recs := cf.Named("shop.tempo.run")
	if len(recs) != 1 || recs[0].Type != "CNAME" || recs[0].Content != early.Hostname() || !recs[0].Proxied || recs[0].Comment != "dock:"+p.c.install {
		t.Fatalf("Cloudflare has %+v", recs)
	}

	base := "tempo.run"
	if _, err := p.c.UpdateServer(p.ctx, testSystem, "homelab", apitypes.UpdateServerRequest{BaseDomain: &base}); err != nil {
		t.Fatal(err)
	}
	gen, err := p.c.GenerateDomain(p.ctx, testSystem, web)
	if err != nil {
		t.Fatal(err)
	}
	p.record(gen.Domain.Hostname)
	if _, err := p.c.AddDomain(p.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "typed.tempo.run"}); err != nil {
		t.Fatal(err)
	}
	doms, err := p.c.ListDomains(p.ctx, testSystem, web)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range doms {
		if (d.Hostname == "typed.tempo.run") != (d.DNS == nil) {
			t.Errorf("domain %s: DNS %+v", d.Hostname, d.DNS)
		}
	}
	if len(cf.Named("*.tempo.run")) != 0 || len(cf.Named("typed.tempo.run")) != 0 {
		t.Error("Dock made a wildcard, or a record for a typed domain")
	}

	if err := p.c.RemoveDomain(p.ctx, testSystem, web, "shop.tempo.run"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(cf.Named("shop.tempo.run")) == 0 })
	if _, err := p.c.ArchiveProject(p.ctx, testSystem, "shop"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(cf.Named(gen.Domain.Hostname)) == 0 })
	if err := p.c.RemoveServer(p.ctx, testSystem, "homelab"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		cf.Mu.Lock()
		defer cf.Mu.Unlock()
		return cf.Tunnels[early.ID].DeletedAt != nil
	})
	if p.rts[lab].Tunnel() != nil {
		t.Error("the removed server still runs the tunnel")
	}
}

// TestCloudflareDomainAdoptsTunnel reuses the tunnel a server runs
// already: Dock adopts it without touching its routes, adds a route for
// each of its domains ahead of them (the catch-all doesn't go to Caddy),
// and takes those away with the domains. A CNAME made by hand that
// already points at the tunnel is left alone, and an adopted tunnel isn't
// deleted with its server. A locally configured tunnel can't be adopted.
func TestCloudflareDomainAdoptsTunnel(t *testing.T) {
	p, cf := cfPool(t, "tempo.run")
	lab := p.ids["homelab"]
	web := p.createService("staging", "web", apitypes.CreateServiceRequest{})
	local := cf.AddTunnel("old-lab", "local")
	p.tunnelMode(local.Hostname())
	if err := p.fromCloudflare(web, "shop.tempo.run"); err == nil || !strings.Contains(err.Error(), "Migrate") {
		t.Fatalf("locally configured tunnel: %v", err)
	}
	dash := cf.AddTunnel("homelab", "cloudflare")
	theirs := []cloudflare.IngressRule{{Hostname: "app.tempo.run", Service: "http://localhost:3000"}, {Service: "http_status:404"}}
	cf.Mu.Lock()
	dash.Config = &cloudflare.TunnelConfig{Ingress: theirs}
	cf.Mu.Unlock()
	cf.AddRecord("tempo.run", cloudflare.Record{Type: "CNAME", Name: "hand.tempo.run", Content: dash.Hostname(), Proxied: true, Comment: "by hand"})
	p.tunnelMode(dash.Hostname())
	for _, host := range []string{"shop.tempo.run", "hand.tempo.run"} {
		if err := p.fromCloudflare(web, host); err != nil {
			t.Fatalf("%s: %v", host, err)
		}
	}
	waitFor(t, func() bool { got := p.rts[lab].Tunnel(); return got != nil && got.Token == dash.Token })
	if r := p.record("shop.tempo.run"); r.Status != apitypes.DNSReady {
		t.Fatalf("shop = %+v", r)
	}
	if r := p.record("hand.tempo.run"); r.Status != apitypes.DNSExternal {
		t.Fatalf("hand-made CNAME = %+v", r)
	}
	routes := func() []cloudflare.IngressRule {
		cf.Mu.Lock()
		defer cf.Mu.Unlock()
		return append([]cloudflare.IngressRule(nil), dash.Config.Ingress...)
	}
	got := routes()
	if len(got) != 4 || !sameRules(got[2:], theirs) || !hasRule(got[:2], "shop.tempo.run") || !hasRule(got[:2], "hand.tempo.run") {
		t.Fatalf("routes = %+v", got)
	}
	for _, host := range []string{"shop.tempo.run", "hand.tempo.run"} {
		if err := p.c.RemoveDomain(p.ctx, testSystem, web, host); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool { return len(cf.Named("shop.tempo.run")) == 0 && sameRules(routes(), theirs) })
	if len(cf.Named("hand.tempo.run")) != 1 {
		t.Fatal("Dock deleted a CNAME it didn't make")
	}
	if _, err := p.c.ArchiveProject(p.ctx, testSystem, "shop"); err != nil {
		t.Fatal(err)
	}
	if err := p.c.RemoveServer(p.ctx, testSystem, "homelab"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		list, _ := p.c.ListCloudflare(p.ctx, testSystem)
		return len(list) == 1 && len(list[0].UsedBy) == 0
	})
	cf.Mu.Lock()
	kept := dash.DeletedAt == nil
	cf.Mu.Unlock()
	if !kept {
		t.Fatal("Dock deleted a tunnel it adopted")
	}
}

func sameRules(a, b []cloudflare.IngressRule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// hasRule reports whether rules send host to the server's Caddy.
func hasRule(rules []cloudflare.IngressRule, host string) bool {
	for _, r := range rules {
		if r == (cloudflare.IngressRule{Hostname: host, Service: "http://127.0.0.1:80"}) {
			return true
		}
	}
	return false
}

// TestCloudflareDomainRefused refuses a name Cloudflare already has a
// record for that Dock didn't make, a name outside the connected zones,
// and a direct server without a tunnel listener. Nothing is added then.
func TestCloudflareDomainRefused(t *testing.T) {
	p, cf := cfPool(t, "tempo.run")
	p.opts.Config.Caddy.TunnelPort = 0 // the control plane's server has no tunnel listener
	p.restart()
	cf.AddRecord("tempo.run", cloudflare.Record{Type: "A", Name: "www.tempo.run", Content: "198.51.100.1"})
	web := p.createService("staging", "web", apitypes.CreateServiceRequest{})
	if err := p.fromCloudflare(web, "www.tempo.run"); CodeOf(err) != apitypes.CodeHostnameTaken || !strings.Contains(err.Error(), "198.51.100.1") {
		t.Fatalf("taken name: %v", err)
	}
	if err := p.fromCloudflare(web, "shop.example.com"); CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "tempo.run") {
		t.Fatalf("name outside the zones: %v", err)
	}
	direct := p.createService("production", "api", apitypes.CreateServiceRequest{})
	if err := p.fromCloudflare(direct, "api.tempo.run"); CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "tunnel_port") {
		t.Fatalf("direct server: %v", err)
	}
	if doms, _ := p.c.ListDomains(p.ctx, testSystem, web); len(doms) != 0 {
		t.Fatalf("domains = %+v", doms)
	}
	if list, _ := p.c.ListCloudflare(p.ctx, testSystem); len(list[0].UsedBy) != 0 {
		t.Fatal("a refused domain created a tunnel")
	}
}

// TestCloudflareTokenRevoked pauses work while Cloudflare refuses the
// token, without retrying, and resumes it once the connection gets a
// working one.
func TestCloudflareTokenRevoked(t *testing.T) {
	p, cf := cfPool(t, "tempo.run")
	web := p.createService("staging", "web", apitypes.CreateServiceRequest{})
	if err := p.fromCloudflare(web, "shop.tempo.run"); err != nil {
		t.Fatal(err)
	}
	p.record("shop.tempo.run")
	cf.Revoke()
	if err := p.c.RemoveDomain(p.ctx, testSystem, web, "shop.tempo.run"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		list, _ := p.c.ListCloudflare(p.ctx, testSystem)
		return len(list) == 1 && list[0].Status == apitypes.CloudflareInvalid
	})
	calls := cf.CallCount("")
	p.c.kickCloudflare()
	time.Sleep(50 * time.Millisecond)
	if n := cf.CallCount(""); n != calls {
		t.Fatalf("%d more calls with a refused token", n-calls)
	}
	cf.Mu.Lock()
	cf.Token = "new-token"
	cf.Mu.Unlock()
	if _, err := p.c.UpdateCloudflare(p.ctx, testSystem, "home", apitypes.UpdateCloudflareRequest{Token: "new-token"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(cf.Named("shop.tempo.run")) == 0 })
}

// TestCloudflareRateLimit stops calling Cloudflare until its Retry-After
// has passed, with the domain still pending, not given up.
func TestCloudflareRateLimit(t *testing.T) {
	p, cf := cfPool(t, "tempo.run")
	cf.Mu.Lock()
	cf.Fail = func(r *http.Request) (int, int) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/dns_records") {
			return 429, 600
		}
		return 0, 0
	}
	cf.Mu.Unlock()
	web := p.createService("staging", "web", apitypes.CreateServiceRequest{})
	if err := p.fromCloudflare(web, "shop.tempo.run"); err != nil {
		t.Fatal(err)
	}
	var row apitypes.CloudflareRecord
	waitFor(t, func() bool {
		list, _ := p.c.ListCloudflareRecords(p.ctx, testSystem)
		if len(list) == 1 && strings.Contains(list[0].Reason, "rate limiting") {
			row = list[0]
			return true
		}
		return false
	})
	if row.Status != apitypes.DNSPending {
		t.Fatalf("record = %+v", row)
	}
	calls := cf.CallCount("")
	p.c.kickCloudflare()
	time.Sleep(50 * time.Millisecond)
	if n := cf.CallCount(""); n != calls {
		t.Fatalf("%d calls during the rate limit", n-calls)
	}
}

// TestCloudflareRemovedWhileCreating deletes the record of a domain removed
// while Dock was creating it: the worker's result doesn't overwrite the
// removal.
func TestCloudflareRemovedWhileCreating(t *testing.T) {
	p, cf := cfPool(t, "tempo.run")
	creating, release := make(chan struct{}), make(chan struct{})
	cf.Mu.Lock()
	cf.Fail = func(r *http.Request) (int, int) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/dns_records") {
			cf.Fail = nil
			close(creating)
			<-release // the domain is removed meanwhile
		}
		return 0, 0
	}
	cf.Mu.Unlock()
	web := p.createService("staging", "web", apitypes.CreateServiceRequest{})
	if err := p.fromCloudflare(web, "shop.tempo.run"); err != nil {
		t.Fatal(err)
	}
	<-creating
	if err := p.c.RemoveDomain(p.ctx, testSystem, web, "shop.tempo.run"); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitFor(t, func() bool {
		list, _ := p.c.ListCloudflareRecords(p.ctx, testSystem)
		return len(list) == 0 && len(cf.Named("shop.tempo.run")) == 0
	})
}

// TestCloudflareDirectServerTunnel serves a direct server's domains both
// ways (ADR 0036): one added from Cloudflare goes through a tunnel Dock
// creates there, routed to the tunnel listener only and never given a
// certificate of ours, while a typed one stays on the public IP.
func TestCloudflareDirectServerTunnel(t *testing.T) {
	p, cf := cfPool(t, "tempo.run")
	api := p.createService("production", "api", apitypes.CreateServiceRequest{})
	p.deploy(api, "api:1")
	if err := p.fromCloudflare(api, "api.tempo.run"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.c.AddDomain(p.ctx, testSystem, api, apitypes.AddDomainRequest{Hostname: "api.example.com"}); err != nil {
		t.Fatal(err)
	}
	r := p.record("api.tempo.run")
	if r.Status != apitypes.DNSReady {
		t.Fatalf("record = %+v", r)
	}
	waitFor(t, func() bool { return p.rt.Tunnel() != nil })
	cf.Mu.Lock()
	var routes []cloudflare.IngressRule
	for _, tun := range cf.Tunnels {
		if tun.Hostname() == r.Tunnel && tun.Config != nil {
			routes = tun.Config.Ingress
		}
	}
	cf.Mu.Unlock()
	if len(routes) != 1 || routes[0].Service != "http://127.0.0.1:7075" {
		t.Fatalf("tunnel routes = %+v; want everything to the tunnel listener", routes)
	}
	for host, want := range map[string]bool{"api.tempo.run": false, "api.example.com": true} {
		if ok, err := p.c.AllowCertificate(p.ctx, host); err != nil || ok != want {
			t.Errorf("certificate for %s: %v, %v", host, ok, err)
		}
	}
	tunnelOnly := map[string]bool{}
	for _, rt := range p.rt.Routes() {
		for _, h := range rt.Hostnames {
			tunnelOnly[h] = rt.TunnelOnly
		}
	}
	if only, ok := tunnelOnly["api.tempo.run"]; !ok || !only || tunnelOnly["api.example.com"] {
		t.Fatalf("tunnel-only routes = %v", tunnelOnly)
	}
}
