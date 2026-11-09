package core

import (
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cloudflare"
	"github.com/NordTempo/tempoDock/internal/cloudflare/cloudflaretest"
)

// settledRedirect waits until the worker is done with the level's
// redirect for host and returns it; ok is false once it is gone.
func (p *pool) settledRedirect(a Actor, platform bool, host string) (r apitypes.CloudflareRedirect, ok bool) {
	p.t.Helper()
	waitFor(p.t, func() bool {
		list, err := p.c.ListCloudflareRedirects(p.ctx, a, platform)
		if err != nil {
			p.t.Fatal(err)
		}
		i := slices.IndexFunc(list, func(r apitypes.CloudflareRedirect) bool { return r.Hostname == host })
		if i < 0 {
			r, ok = apitypes.CloudflareRedirect{}, false
			return true
		}
		r, ok = list[i], true
		return r.Status == apitypes.RedirectLive || r.Status == apitypes.RedirectError
	})
	return r, ok
}

// dockRules are the redirect rules this control plane made in a zone.
func dockRules(p *pool, cf *cloudflaretest.Server, zone string) []cloudflare.Rule {
	return slices.DeleteFunc(cf.Rules(zone), func(r cloudflare.Rule) bool { return !strings.HasPrefix(r.Description, p.c.recordComment()) })
}

// A redirect is made in Cloudflare as a proxied placeholder record and a
// rule next to the rules made in the dashboard, which Dock never
// touches; changing it changes the rule, and removing it deletes both.
func TestCloudflareRedirect(t *testing.T) {
	p, cf := cfPool(t, "tempo.run")
	theirs := cf.AddRule("tempo.run", cloudflare.Rule{Description: "by hand", Expression: `(http.host eq "old.tempo.run")`, Action: "redirect",
		ActionParameters: &cloudflare.RuleParameters{FromValue: &cloudflare.RedirectTarget{TargetURL: cloudflare.TargetURL{Value: "https://new.example"}, StatusCode: 301}}})

	if _, err := p.c.AddCloudflareRedirect(p.ctx, testSystem, true, apitypes.CreateCloudflareRedirectRequest{Hostname: "ig.tempo.run", Target: "https://ig.tempo.run/x"}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("a redirect to itself: %v", err)
	}
	if _, err := p.c.AddCloudflareRedirect(p.ctx, testSystem, true, apitypes.CreateCloudflareRedirectRequest{Hostname: "ig.tempo.run", Target: `https://x.com/"a"`}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("a target with quotes: %v", err)
	}
	if _, err := p.c.AddCloudflareRedirect(p.ctx, testSystem, true, apitypes.CreateCloudflareRedirectRequest{Hostname: "ig.other.dev", Target: "https://x.com"}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("a hostname in no zone: %v", err)
	}
	r, err := p.c.AddCloudflareRedirect(p.ctx, testSystem, true, apitypes.CreateCloudflareRedirectRequest{Hostname: "IG.tempo.run", Target: "https://instagram.com/tempo"})
	if err != nil || r.Hostname != "ig.tempo.run" || r.StatusCode != 302 || r.Zone != "tempo.run" || r.Connection != "home" {
		t.Fatalf("added = %+v, %v", r, err)
	}
	if r, _ := p.settledRedirect(testSystem, true, "ig.tempo.run"); r.Status != apitypes.RedirectLive {
		t.Fatalf("redirect = %+v", r)
	}
	recs := cf.Named("ig.tempo.run")
	if len(recs) != 1 || recs[0].Type != "AAAA" || recs[0].Content != "100::" || !recs[0].Proxied || recs[0].Comment != p.c.recordComment() {
		t.Fatalf("records = %+v", recs)
	}
	mine := dockRules(p, cf, "tempo.run")
	if len(mine) != 1 || mine[0].Expression != `(http.host eq "ig.tempo.run")` {
		t.Fatalf("Dock's rules = %+v", mine)
	}
	if to := mine[0].ActionParameters.FromValue; to.TargetURL.Value != "https://instagram.com/tempo" || to.StatusCode != 302 || to.PreserveQueryString {
		t.Fatalf("rule goes to %+v", to)
	}

	code, keep := 301, true
	if _, err := p.c.UpdateCloudflareRedirect(p.ctx, testSystem, true, "ig.tempo.run", apitypes.UpdateCloudflareRedirectRequest{StatusCode: &code, KeepPath: &keep}); err != nil {
		t.Fatal(err)
	}
	if r, _ := p.settledRedirect(testSystem, true, "ig.tempo.run"); r.Status != apitypes.RedirectLive || r.StatusCode != 301 || !r.KeepPath {
		t.Fatalf("changed = %+v", r)
	}
	mine = dockRules(p, cf, "tempo.run")
	if to := mine[0].ActionParameters.FromValue; len(mine) != 1 || to.TargetURL.Expression != `concat("https://instagram.com/tempo", http.request.uri.path)` || to.StatusCode != 301 || !to.PreserveQueryString {
		t.Fatalf("changed rules = %+v", mine)
	}

	if err := p.c.RemoveCloudflareRedirect(p.ctx, testSystem, true, "ig.tempo.run"); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.settledRedirect(testSystem, true, "ig.tempo.run"); ok {
		t.Fatal("the redirect is still listed")
	}
	if got := cf.Rules("tempo.run"); len(got) != 1 || got[0].ID != theirs.ID {
		t.Fatalf("rules left = %+v; want only the one made by hand", got)
	}
	if recs := cf.Named("ig.tempo.run"); len(recs) != 0 {
		t.Fatalf("records left = %+v", recs)
	}
}

// Redirects are on the level a zone is added (ADR 0068): an org can't
// add one in a platform zone it only uses through sharing, adds them in
// its own zones, and no other org sees them. A hostname is a domain or a
// redirect, never both, and a record Dock didn't make refuses it. A
// zone the connection's token loses fails its redirects.
func TestRedirectLevels(t *testing.T) {
	p, cf, admin := cfOrgPool(t, "tempo.run", "acme.dev")
	other := p.in(p.member("bob@x", map[string]apitypes.Scope{"acme": apitypes.ScopeAdmin}), "acme")
	deployer := p.in(p.member("dev@x", map[string]apitypes.Scope{"test": apitypes.ScopeDeploy}), "test")
	if _, err := p.addCF(cf, testSystem, true, apitypes.CreateCloudflareRequest{Name: "edge", Shared: true}, "tempo.run"); err != nil {
		t.Fatal(err)
	}
	add := func(a Actor, platform bool, host string) error {
		_, err := p.c.AddCloudflareRedirect(p.ctx, a, platform, apitypes.CreateCloudflareRedirectRequest{Hostname: host, Target: "https://example.com"})
		return err
	}
	if err := add(admin, false, "ig.tempo.run"); CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "level its zone is added") {
		t.Fatalf("an org added a redirect in a shared platform zone: %v", err)
	}
	if err := add(admin, true, "ig.tempo.run"); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("an org admin added a platform redirect: %v", err)
	}
	if _, err := p.addCF(cf, admin, false, apitypes.CreateCloudflareRequest{Name: "own"}, "acme.dev"); err != nil {
		t.Fatal(err)
	}
	if err := add(deployer, false, "ig.acme.dev"); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a deployer added a redirect: %v", err)
	}
	if err := add(admin, false, "ig.acme.dev"); err != nil {
		t.Fatal(err)
	}
	if r, _ := p.settledRedirect(admin, false, "ig.acme.dev"); r.Status != apitypes.RedirectLive || r.Connection != "own" {
		t.Fatalf("org redirect = %+v", r)
	}
	if list, err := p.c.ListCloudflareRedirects(p.ctx, other, false); err != nil || len(list) != 0 {
		t.Fatalf("another org's redirects = %+v, %v", list, err)
	}
	if list, err := p.c.ListCloudflareRedirects(p.ctx, testSystem, true); err != nil || len(list) != 0 {
		t.Fatalf("the platform's redirects = %+v, %v", list, err)
	}
	if err := p.c.RemoveCloudflareRedirect(p.ctx, other, false, "ig.acme.dev"); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("another org removed the redirect: %v", err)
	}

	web := p.createService("staging", "web", apitypes.CreateServiceRequest{})
	if _, err := p.c.AddDomain(p.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "ig.acme.dev"}); CodeOf(err) != apitypes.CodeHostnameTaken {
		t.Fatalf("a domain took a redirect's hostname: %v", err)
	}
	if _, err := p.c.AddDomain(p.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "shop.acme.dev"}); err != nil {
		t.Fatal(err)
	}
	if err := add(admin, false, "shop.acme.dev"); CodeOf(err) != apitypes.CodeHostnameTaken {
		t.Fatalf("a redirect took a domain's hostname: %v", err)
	}
	cf.AddRecord("acme.dev", cloudflare.Record{Type: "A", Name: "www.acme.dev", Content: "192.0.2.1"})
	if err := add(admin, false, "www.acme.dev"); CodeOf(err) != apitypes.CodeHostnameTaken || !strings.Contains(err.Error(), "an A record to 192.0.2.1") {
		t.Fatalf("a redirect over someone's record: %v", err)
	}

	// The token loses the zone: the redirect stays the org's, failed.
	undo := reach(cf)
	_, err := p.c.RefreshCloudflare(p.ctx, admin, false, "own")
	undo()
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := p.settledRedirect(admin, false, "ig.acme.dev"); r.Status != apitypes.RedirectError || !strings.Contains(r.Reason, "no longer reaches this redirect's zone") {
		t.Fatalf("stranded redirect = %+v", r)
	}
}

// A token without Single Redirect Edit fails the redirect saying what it
// needs; once fixed, a retry makes it.
func TestRedirectWithoutPermission(t *testing.T) {
	p, cf := cfPool(t, "tempo.run")
	cf.Mu.Lock()
	cf.NoRedirects = true
	cf.Mu.Unlock()
	if _, err := p.c.AddCloudflareRedirect(p.ctx, testSystem, true, apitypes.CreateCloudflareRedirectRequest{Hostname: "ig.tempo.run", Target: "https://example.com"}); err != nil {
		t.Fatal(err)
	}
	if r, _ := p.settledRedirect(testSystem, true, "ig.tempo.run"); r.Status != apitypes.RedirectError || !strings.Contains(r.Reason, "Zone › Single Redirect › Edit") {
		t.Fatalf("redirect = %+v", r)
	}
	cf.Mu.Lock()
	cf.NoRedirects = false
	cf.Mu.Unlock()
	if _, err := p.c.RetryCloudflareRedirect(p.ctx, testSystem, true, "ig.tempo.run"); err != nil {
		t.Fatal(err)
	}
	if r, _ := p.settledRedirect(testSystem, true, "ig.tempo.run"); r.Status != apitypes.RedirectLive {
		t.Fatalf("retried = %+v", r)
	}
}

// A connection with redirects isn't removed unless forced; forced, its
// redirects are deleted in Cloudflare first.
func TestRemoveConnectionWithRedirects(t *testing.T) {
	p, cf := cfPool(t, "tempo.run")
	for _, host := range []string{"a.tempo.run", "b.tempo.run"} {
		if _, err := p.c.AddCloudflareRedirect(p.ctx, testSystem, true, apitypes.CreateCloudflareRedirectRequest{Hostname: host, Target: "https://example.com"}); err != nil {
			t.Fatal(err)
		}
		p.settledRedirect(testSystem, true, host)
	}
	err := p.c.RemoveCloudflare(p.ctx, testSystem, true, "home", false)
	if CodeOf(err) != apitypes.CodeConflict || !strings.Contains(err.Error(), "2 redirects (a.tempo.run, b.tempo.run)") {
		t.Fatalf("removed with redirects: %v", err)
	}
	if err := p.c.RemoveCloudflare(p.ctx, testSystem, true, "home", true); err != nil {
		t.Fatal(err)
	}
	if rules := dockRules(p, cf, "tempo.run"); len(rules) != 0 {
		t.Fatalf("rules left = %+v", rules)
	}
	if recs := cf.Named("a.tempo.run"); len(recs) != 0 {
		t.Fatalf("records left = %+v", recs)
	}
	if list, err := p.c.ListCloudflare(p.ctx, testSystem, true); err != nil || len(list) != 0 {
		t.Fatalf("connections = %+v, %v", list, err)
	}
}
