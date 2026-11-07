package core

import (
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cloudflare/cloudflaretest"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store"
)

// cfOrgPool is labPool with a fake Cloudflare holding zones and no
// connection yet (homelab in tunnel mode, without a tunnel), and an
// unlimited admin of org 1 ("test").
func cfOrgPool(t *testing.T, zones ...string) (*pool, *cloudflaretest.Server, Actor) {
	t.Helper()
	p, _ := labPool(t)
	cf := cloudflaretest.New(t, zones...)
	p.opts.CloudflareAPI = cf.URL
	p.restart()
	p.tunnelMode("")
	return p, cf, p.in(p.member("ann@x", map[string]apitypes.Scope{"test": apitypes.ScopeAdmin}), "test")
}

// reach has the fake's token reach only the zones named, as Dock checks
// it: the others refuse their DNS records, until undo.
func reach(cf *cloudflaretest.Server, zones ...string) (undo func()) {
	cf.Mu.Lock()
	defer cf.Mu.Unlock()
	for _, z := range cf.Zones {
		cf.DeniedZones[z.ID] = !slices.Contains(zones, z.Name)
	}
	return func() {
		cf.Mu.Lock()
		defer cf.Mu.Unlock()
		clear(cf.DeniedZones)
	}
}

// addCF adds a connection whose token reaches only zones.
func (p *pool) addCF(cf *cloudflaretest.Server, a Actor, platform bool, req apitypes.CreateCloudflareRequest, zones ...string) (apitypes.Cloudflare, error) {
	defer reach(cf, zones...)()
	req.Token = cloudflaretest.Token
	return p.c.AddCloudflare(p.ctx, a, platform, req)
}

// dnsOf is how Dock routes one of ref's domains through Cloudflare.
func (p *pool) dnsOf(ref ServiceRef, host string) *apitypes.DomainDNS {
	p.t.Helper()
	doms, err := p.c.ListDomains(p.ctx, testSystem, ref)
	if err != nil {
		p.t.Fatal(err)
	}
	for _, d := range doms {
		if d.Hostname == host {
			return d.DNS
		}
	}
	return nil
}

// An org's admins add its own Cloudflare connection; its zones serve that
// org's domains only. Another org can't see it, use its zones or take
// them, and members below admin can't add one (ADR 0062). The server's
// tunnel for the org's domain is made with the platform's connection.
func TestOrgCloudflareConnections(t *testing.T) {
	p, cf, admin := cfOrgPool(t, "tempo.run", "acme.dev")
	deployer := p.in(p.member("dev@x", map[string]apitypes.Scope{"test": apitypes.ScopeDeploy}), "test")
	other := p.in(p.member("bob@x", map[string]apitypes.Scope{"acme": apitypes.ScopeAdmin}), "acme")
	if _, err := p.addCF(cf, testSystem, true, apitypes.CreateCloudflareRequest{Name: "edge", Shared: true}, "tempo.run"); err != nil {
		t.Fatal(err)
	}

	if _, err := p.addCF(cf, deployer, false, apitypes.CreateCloudflareRequest{Name: "own"}, "acme.dev"); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a deployer added a connection: %v", err)
	}
	if _, err := p.addCF(cf, admin, false, apitypes.CreateCloudflareRequest{Name: "own", Shared: true}, "acme.dev"); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("an org shared its connection: %v", err)
	}
	own, err := p.addCF(cf, admin, false, apitypes.CreateCloudflareRequest{Name: "own"}, "acme.dev")
	if err != nil || own.Shared || !slices.Equal(own.Zones, []string{"acme.dev"}) {
		t.Fatalf("own = %+v, %v", own, err)
	}
	if _, err := p.addCF(cf, other, false, apitypes.CreateCloudflareRequest{Name: "mine"}, "acme.dev"); CodeOf(err) != apitypes.CodeConflict || !strings.Contains(err.Error(), "acme.dev is already connected") {
		t.Fatalf("another org took the zone: %v", err)
	}
	if _, err := p.addCF(cf, other, false, apitypes.CreateCloudflareRequest{Name: "mine"}); err != nil {
		t.Fatal(err)
	}

	if zones, err := p.c.ListCloudflareZones(p.ctx, admin); err != nil || !slices.Equal(zones, []apitypes.CloudflareZone{
		{Name: "acme.dev", Connection: "own"}, {Name: "tempo.run", Connection: "edge", Platform: true},
	}) {
		t.Fatalf("org 1's zones = %+v, %v", zones, err)
	}
	if zones, err := p.c.ListCloudflareZones(p.ctx, other); err != nil || len(zones) != 1 || zones[0].Name != "tempo.run" {
		t.Fatalf("another org's zones = %+v, %v; want the platform's shared one only", zones, err)
	}
	if list, err := p.c.ListCloudflare(p.ctx, other, false); err != nil || len(list) != 1 || list[0].Name != "mine" {
		t.Fatalf("another org's connections = %+v, %v", list, err)
	}
	if _, err := p.c.ListCloudflare(p.ctx, other, true); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("an org admin listed the platform's connections: %v", err)
	}
	if err := p.c.RemoveCloudflare(p.ctx, other, false, "own"); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("another org removed org 1's connection: %v", err)
	}
	lab, err := p.store.GetServer(p.ctx, p.ids["homelab"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.c.prepareCloudflareDomain(p.ctx, other, lab, other.OrgID, "shop.acme.dev"); CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "isn't in a Cloudflare zone this organization may use") {
		t.Fatalf("another org used org 1's zone: %v", err)
	}

	web := p.createService("staging", "web", apitypes.CreateServiceRequest{})
	if err := p.fromCloudflare(web, "shop.acme.dev"); err != nil {
		t.Fatal(err)
	}
	if r := p.record("shop.acme.dev"); r.Status != apitypes.DNSReady || r.Connection != "own" {
		t.Fatalf("record = %+v", r)
	}
	edge, err := p.store.GetCloudflareConnection(p.ctx, store.GetCloudflareConnectionParams{Org: 0, Name: "edge"})
	if err != nil {
		t.Fatal(err)
	}
	if tun, err := p.store.GetCloudflareTunnelByServer(p.ctx, &lab.ID); err != nil || tun.ConnectionID != edge.ID {
		t.Fatalf("homelab's tunnel = %+v, %v; want it made with the platform's edge", tun, err)
	}
}

// A shared platform connection's zones serve every org's domains; once
// it stops being shared new ones can't use them, and those already there
// stay. The org's own zone comes first, even when a platform zone is the
// closer match.
func TestSharedPlatformZones(t *testing.T) {
	p, cf, admin := cfOrgPool(t, "tempo.run", "eu.tempo.run")
	if _, err := p.addCF(cf, testSystem, true, apitypes.CreateCloudflareRequest{Name: "edge"}, "eu.tempo.run"); err != nil {
		t.Fatal(err)
	}
	web := p.createService("staging", "web", apitypes.CreateServiceRequest{})
	if err := p.fromCloudflare(web, "a.eu.tempo.run"); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("a domain in a platform zone that isn't shared: %v", err)
	}
	share := func(on bool) {
		t.Helper()
		if got, err := p.c.UpdateCloudflare(p.ctx, testSystem, true, "edge", apitypes.UpdateCloudflareRequest{Shared: &on}); err != nil || got.Shared != on {
			t.Fatalf("share %v = %+v, %v", on, got, err)
		}
	}
	share(true)
	if err := p.fromCloudflare(web, "a.eu.tempo.run"); err != nil {
		t.Fatal(err)
	}
	share(false)
	if err := p.fromCloudflare(web, "b.eu.tempo.run"); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("a domain in a zone no longer shared: %v", err)
	}
	if r := p.record("a.eu.tempo.run"); r.Status != apitypes.DNSReady {
		t.Fatalf("the domain already there = %+v", r)
	}
	if zones, _ := p.c.ListCloudflareZones(p.ctx, admin); len(zones) != 0 {
		t.Fatalf("zones = %+v; want none", zones)
	}

	share(true)
	if _, err := p.addCF(cf, admin, false, apitypes.CreateCloudflareRequest{Name: "own"}, "tempo.run"); err != nil {
		t.Fatal(err)
	}
	if err := p.fromCloudflare(web, "c.eu.tempo.run"); err != nil {
		t.Fatal(err)
	}
	if dns := p.dnsOf(web, "c.eu.tempo.run"); dns == nil || dns.Connection != "own" || dns.Zone != "tempo.run" {
		t.Fatalf("c.eu.tempo.run goes through %+v; want the org's own zone", dns)
	}
}

// An org's own connection failing is the org's alert, naming the
// connection, not the platform's (ADR 0062).
func TestOrgConnectionFailureIsTheOrgsAlert(t *testing.T) {
	p, cf, admin := cfOrgPool(t, "tempo.run", "acme.dev")
	if _, err := p.addCF(cf, testSystem, true, apitypes.CreateCloudflareRequest{Name: "edge"}, "tempo.run"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.addCF(cf, admin, false, apitypes.CreateCloudflareRequest{Name: "own"}, "acme.dev"); err != nil {
		t.Fatal(err)
	}
	web := p.createService("staging", "web", apitypes.CreateServiceRequest{})
	if err := p.fromCloudflare(web, "shop.acme.dev"); err != nil {
		t.Fatal(err)
	}
	p.record("shop.acme.dev")
	defer reach(cf, "tempo.run")() // the token lost DNS Edit on acme.dev
	if err := p.c.RemoveDomain(p.ctx, testSystem, web, "shop.acme.dev"); err != nil {
		t.Fatal(err)
	}
	connAlerts := func(a Actor, platform bool) []apitypes.Alert {
		open, _ := p.c.ListAlerts(p.ctx, a, platform, false, 0, 0)
		return slices.DeleteFunc(open, func(a apitypes.Alert) bool { return a.Kind != apitypes.AlertConnection })
	}
	waitFor(t, func() bool { return len(connAlerts(admin, false)) == 1 })
	got := connAlerts(admin, false)[0]
	if got.Target == nil || got.Target.Connection != "cloudflare:own" || got.Command != "dock cloudflare refresh own" {
		t.Fatalf("org alert = %+v", got)
	}
	if open := connAlerts(testSystem, true); len(open) != 0 {
		t.Fatalf("platform alerts = %+v; the org's connection isn't the platform's", open)
	}
}

// Server tunnels are the platform's: an org's connection can't be picked
// for one, and an org's zone doesn't get a tunnel made without a
// platform connection in its account.
func TestServerTunnelsArePlatformOnly(t *testing.T) {
	p, cf, admin := cfOrgPool(t, "acme.dev")
	if _, err := p.addCF(cf, admin, false, apitypes.CreateCloudflareRequest{Name: "own"}, "acme.dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.c.SetServerTunnel(p.ctx, testSystem, "homelab", apitypes.ServerTunnelRequest{Action: "create", Cloudflare: "own"}); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("a tunnel with an org's connection: %v", err)
	}
	if _, err := p.c.SetServerTunnel(p.ctx, testSystem, "homelab", apitypes.ServerTunnelRequest{Action: "create"}); CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "the platform has no Cloudflare connection") {
		t.Fatalf("a tunnel with the only connection, an org's: %v", err)
	}
	web := p.createService("staging", "web", apitypes.CreateServiceRequest{})
	if err := p.fromCloudflare(web, "shop.acme.dev"); CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "no connection in account") {
		t.Fatalf("a domain in the org's zone on a server without a tunnel: %v", err)
	}
}

// A platform connection keeps the AAD its token had before org
// connections (its name), an org's is bound to "<org>/<name>", and
// resealing (ADR 0041) binds both the same way: each token still opens.
func TestCloudflareTokensOpen(t *testing.T) {
	p, cf, admin := cfOrgPool(t, "tempo.run", "acme.dev")
	sealed, err := p.c.box.Seal([]byte(cloudflaretest.Token), secrets.Bind("cloudflare_connections.token", "home"))
	if err != nil {
		t.Fatal(err)
	}
	p.exec(`INSERT INTO cloudflare_connections (name, shared, account_id, token, created_at, updated_at, updated_by) VALUES ('home', 1, ?, ?, '2026-01-01', '2026-01-01', 'admin')`,
		cloudflaretest.AccountID, sealed)
	if _, err := p.addCF(cf, admin, false, apitypes.CreateCloudflareRequest{Name: "own"}, "acme.dev"); err != nil {
		t.Fatal(err)
	}
	refresh := func() {
		t.Helper()
		undo := reach(cf, "tempo.run")
		home, err := p.c.RefreshCloudflare(p.ctx, testSystem, true, "home")
		undo()
		if err != nil || !home.Shared || !slices.Equal(home.Zones, []string{"tempo.run"}) {
			t.Fatalf("refresh home = %+v, %v", home, err)
		}
		undo = reach(cf, "acme.dev")
		_, err = p.c.RefreshCloudflare(p.ctx, admin, false, "own")
		undo()
		if err != nil {
			t.Fatalf("refresh own: %v", err)
		}
	}
	refresh()

	p.unbind(sealedCloudflareToken)
	p.exec(`UPDATE install SET secrets_format = 1`)
	p.restart()
	refresh()
}

// An org's admins install the platform's GitHub App for their own org
// and unassign their own installation, never another org's; operators
// still assign and move any (ADR 0062).
func TestGitHubInstallByOrgAdmin(t *testing.T) {
	h, _, _ := appHarness(t) // installation 7 is org 1's
	admin := h.in(h.member("ann@x", map[string]apitypes.Scope{"test": apitypes.ScopeAdmin}), "test")
	deployer := h.in(h.member("dev@x", map[string]apitypes.Scope{"test": apitypes.ScopeDeploy}), "test")
	other := h.in(h.member("bob@x", map[string]apitypes.Scope{"acme": apitypes.ScopeAdmin}), "acme")
	install := func(a Actor, org string) error {
		t.Helper()
		link, err := h.c.StartGitHubInstall(h.ctx, a, apitypes.GitHubOrgRequest{Org: org})
		if err != nil {
			return err
		}
		u, _ := url.Parse(link.URL)
		_, err = h.c.CompleteGitHubInstall(h.ctx, fakeInstall, "install", u.Query().Get("state"))
		return err
	}
	assignedTo := func() int64 {
		t.Helper()
		inst, err := h.store.GetGitHubInstallation(h.ctx, fakeInstall)
		if store.IsNotFound(err) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return inst.OrgID
	}

	st, err := h.c.GitHubOrgStatus(h.ctx, admin)
	if err != nil || st.App == nil || len(st.Installations) != 1 || st.Installations[0].Account != "acme" || st.Installations[0].Repositories != "all" || st.Installations[0].Missing {
		t.Fatalf("org 1's GitHub = %+v, %v", st, err)
	}
	if st, err := h.c.GitHubOrgStatus(h.ctx, other); err != nil || st.App == nil || len(st.Installations) != 0 {
		t.Fatalf("another org's GitHub = %+v, %v", st, err)
	}
	if _, err := h.c.GitHubOrgStatus(h.ctx, deployer); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a deployer read the org's installations: %v", err)
	}
	if _, err := h.c.StartGitHubInstall(h.ctx, deployer, apitypes.GitHubOrgRequest{}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a deployer started an install: %v", err)
	}
	if _, err := h.c.StartGitHubInstall(h.ctx, other, apitypes.GitHubOrgRequest{Org: "test"}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("an org admin started an install for another org: %v", err)
	}
	if err := h.c.UnassignGitHubInstallation(h.ctx, other, fakeInstall); CodeOf(err) != apitypes.CodeNotFound || assignedTo() != 1 {
		t.Fatalf("another org unassigned org 1's installation: %v", err)
	}
	if err := install(other, ""); CodeOf(err) != apitypes.CodeConflict || assignedTo() != 1 {
		t.Fatalf("another org's install took org 1's installation: %v", err)
	}

	if err := h.c.UnassignGitHubInstallation(h.ctx, admin, fakeInstall); err != nil || assignedTo() != 0 {
		t.Fatalf("unassign own: %v", err)
	}
	if err := install(admin, ""); err != nil || assignedTo() != 1 {
		t.Fatalf("install for own org: %v", err)
	}
	if err := h.c.AssignGitHubInstallation(h.ctx, testSystem, fakeInstall, apitypes.GitHubOrgRequest{Org: "acme"}); err != nil || assignedTo() != other.OrgID {
		t.Fatalf("operator moves the installation: %v", err)
	}
}
