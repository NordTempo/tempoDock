package core

import (
	"context"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// Domains carry their URL and what the last DNS check saw.
func TestDomainURLAndLastCheck(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.rt.SetDomainCheck(func(string) node.DomainCheck {
		return node.DomainCheck{ExpectedIPs: []string{"203.0.113.10"}, ResolvedIPs: []string{"198.51.100.7"}}
	})
	dc, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	// The background checker may already have run, so only the URL is fixed.
	if dc.Domain.URL != "https://shop.example.com" {
		t.Fatalf("new domain: %+v", dc.Domain)
	}

	ss, _ := h.c.serviceFor(h.ctx, testSystem, web, apitypes.ScopeRead, "testing")
	rows, _ := h.store.ListDomainsByService(h.ctx, ss.svc.ID)
	var dnsOK time.Time
	h.c.checkDomain(h.ctx, rows[0], &dnsOK)
	ds, err := h.c.ListDomains(h.ctx, testSystem, web)
	if err != nil {
		t.Fatal(err)
	}
	d := ds[0]
	if d.Status != apitypes.DomainWaitingForDNS || d.CheckedAt == nil || !slices.Equal(d.ResolvedIPs, []string{"198.51.100.7"}) {
		t.Fatalf("checked domain: %+v", d)
	}

	h.opts.Config.Caddy.DisableTLS = true
	h.restart()
	if ds, _ := h.c.ListDomains(h.ctx, testSystem, web); ds[0].URL != "http://shop.example.com" {
		t.Fatalf("URL without TLS: %s", ds[0].URL)
	}
}

// fakeDNS answers TXT lookups from a map, as a zone the test edits.
type fakeDNS struct {
	mu  sync.Mutex
	txt map[string][]string
	// ip answers LookupIP; a name without addresses is NXDOMAIN, so a
	// test gets auto uptime checks (ADR 0059) only by giving one.
	ip map[string][]netip.Addr
}

// setIP gives name the address addr.
func (f *fakeDNS) setIP(name, addr string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ip == nil {
		f.ip = map[string][]netip.Addr{}
	}
	f.ip[name] = []netip.Addr{netip.MustParseAddr(addr)}
}

// lookupIP is Options.LookupIP.
func (f *fakeDNS) lookupIP(_ context.Context, name string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ip, ok := f.ip[name]; ok {
		return ip, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

// set gives name one TXT record holding value.
func (f *fakeDNS) set(name, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.txt == nil {
		f.txt = map[string][]string{}
	}
	f.txt[name] = []string{value}
}

// lookupTXT is Options.LookupTXT: NXDOMAIN for a name without records.
func (f *fakeDNS) lookupTXT(_ context.Context, name string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if txt, ok := f.txt[name]; ok {
		return txt, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

// routed reports whether host is in the local server's routes.
func (h *harness) routed(host string) bool {
	for _, r := range h.rt.Routes() {
		if slices.Contains(r.Hostnames, host) {
			return true
		}
	}
	return false
}

// A custom domain an org member adds routes and gets a certificate only
// once its TXT record proves the org controls the hostname.
func TestCustomDomainVerifiesWithTXTRecord(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.deploy(web, "app:1")
	alice := h.in(h.member("alice@x", map[string]apitypes.Scope{"test": apitypes.ScopeDeploy}), "test")

	dc, err := h.c.AddDomain(h.ctx, alice, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	rec := dc.Domain.Verification
	if dc.Domain.Verified || rec == nil || rec.Type != "TXT" || rec.Name != "_dock-challenge.shop.example.com" || !strings.HasPrefix(rec.Value, "dock-verify=") {
		t.Fatalf("new domain: %+v", dc.Domain)
	}
	if dc.Release != nil { // publishing the port
		h.wait(web, dc.Release.Number)
	}
	if h.routed("shop.example.com") {
		t.Fatal("unverified domain is routed")
	}
	if ok, _ := h.c.AllowCertificate(h.ctx, "shop.example.com"); ok {
		t.Fatal("unverified domain gets a certificate")
	}
	d, err := h.c.CheckDomainNow(h.ctx, alice, web, "shop.example.com")
	if err != nil || d.Verified || d.Status != apitypes.DomainWaitingForDNS || !strings.Contains(d.StatusReason, rec.Value) {
		t.Fatalf("checked without the record: %+v, %v", d, err)
	}

	h.dns.set(rec.Name, rec.Value)
	if d, err = h.c.CheckDomainNow(h.ctx, alice, web, "shop.example.com"); err != nil || !d.Verified || d.Verification != nil {
		t.Fatalf("checked with the record: %+v, %v", d, err)
	}
	if !h.routed("shop.example.com") {
		t.Fatal("verified domain isn't routed")
	}
	if ok, _ := h.c.AllowCertificate(h.ctx, "shop.example.com"); !ok {
		t.Fatal("verified domain gets no certificate")
	}
}

// Unverified claims don't block another org's; the first to verify holds
// the hostname, the others lose, and no one can claim it after.
func TestVerifiedHostnameBelongsToOneOrg(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{}) // shop/web in every org
	alice := h.in(h.member("alice@x", map[string]apitypes.Scope{"test": apitypes.ScopeDeploy}), "test")
	bob := h.in(h.member("bob@x", map[string]apitypes.Scope{"acme": apitypes.ScopeAdmin}), "acme")
	carol := h.in(h.member("carol@x", map[string]apitypes.Scope{"other": apitypes.ScopeAdmin}), "other")
	for _, a := range []Actor{bob, carol} {
		if _, err := h.c.CreateProject(h.ctx, a, apitypes.CreateProjectRequest{Name: "shop"}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.c.CreateService(h.ctx, a, web.Env(), apitypes.CreateServiceRequest{Name: "web"}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := h.c.AddDomain(h.ctx, alice, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	dc, err := h.c.AddDomain(h.ctx, bob, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"})
	if err != nil {
		t.Fatalf("a second org's claim beside an unverified one: %v", err)
	}
	h.dns.set(dc.Domain.Verification.Name, dc.Domain.Verification.Value)
	if d, err := h.c.CheckDomainNow(h.ctx, bob, web, "shop.example.com"); err != nil || !d.Verified {
		t.Fatalf("bob verifies: %+v, %v", d, err)
	}

	if _, err := h.c.AddDomain(h.ctx, carol, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); CodeOf(err) != apitypes.CodeHostnameTaken {
		t.Fatalf("claiming a verified hostname: %v, want hostname_taken", err)
	}
	// Alice's claim lost, and her record doesn't help now.
	ds, err := h.c.ListDomains(h.ctx, alice, web)
	if err != nil || len(ds) != 1 || ds[0].Verification == nil {
		t.Fatalf("alice's domains: %+v, %v", ds, err)
	}
	h.dns.set(ds[0].Verification.Name, ds[0].Verification.Value)
	d, err := h.c.CheckDomainNow(h.ctx, alice, web, "shop.example.com")
	if err != nil || d.Verified || d.Status != apitypes.DomainError || !strings.Contains(d.StatusReason, "another organization") {
		t.Fatalf("alice's lost claim: %+v, %v", d, err)
	}
}

// Hostnames under the platform's base domain need no TXT record: generated
// ones, and custom ones an org member picks there.
func TestPlatformHostnamesSkipVerification(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	alice := h.in(h.member("alice@x", map[string]apitypes.Scope{"test": apitypes.ScopeDeploy}), "test")

	gen, err := h.c.GenerateDomain(h.ctx, alice, web)
	if err != nil || !gen.Domain.Verified || gen.Domain.Verification != nil {
		t.Fatalf("generated: %+v, %v", gen.Domain, err)
	}
	dc, err := h.c.AddDomain(h.ctx, alice, web, apitypes.AddDomainRequest{Hostname: "shop.apps.test"})
	if err != nil || !dc.Domain.Verified {
		t.Fatalf("under the base domain: %+v, %v", dc.Domain, err)
	}
}

// A domain added while the deploy publishing the service's port is in its
// cutover (routes rendered, release not active yet) is still routed: its
// AddDomain sees no published release and a deploy in flight, so it must
// apply routes itself after that cutover. It once was left out until the
// next routing change.
func TestDomainAddedDuringCutoverIsRouted(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "nginx:1") // nothing published yet
	upstreamFor := func(rs []node.Route, host string) string {
		for _, r := range rs {
			if slices.Contains(r.Hostnames, host) {
				return r.Upstream
			}
		}
		return ""
	}
	// Hold the cutover of the release that publishes the port.
	held, release := h.rt.HoldApplyRoutes(func(rs []node.Route) bool { return upstreamFor(rs, "a.example.com") != "" })
	defer release()
	add := func(host string) error {
		_, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: host})
		return err
	}
	if err := add("a.example.com"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("the publishing deploy never reached its cutover")
	}
	done := make(chan error, 1)
	go func() { done <- add("b.example.com") }()
	// With the fix it waits for the cutover; give it time to get past its
	// look at the active release, then let the cutover finish.
	select {
	case err := <-done:
		done <- err
	case <-time.After(300 * time.Millisecond):
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if r := h.wait(web, 2); r.Status != apitypes.ReleaseActive {
		t.Fatalf("release 2 is %s", r.Status)
	}
	h.until("b.example.com routed", func() bool { return upstreamFor(h.rt.Routes(), "b.example.com") != "" })
}

// generatedLike matches a generated hostname: prefix, a dash, the 7
// random base32 characters and base (ADR 0087).
func generatedLike(prefix, base string) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `-[a-z2-7]{7}\.` + regexp.QuoteMeta(base) + `$`)
}

// generatedHost is ref's generated hostname; it fails without exactly one.
func (h *harness) generatedHost(ref ServiceRef) string {
	h.t.Helper()
	doms, err := h.c.ListDomains(h.ctx, testSystem, ref)
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for _, d := range doms {
		if d.Generated {
			out = append(out, d.Hostname)
		}
	}
	if len(out) != 1 {
		h.t.Fatalf("%s has generated domains %v, want one", ref, out)
	}
	return out[0]
}

// A redirect that Dock's other redirects lead back from is refused,
// naming the one that closes the loop; one that ends somewhere is fine.
func TestRedirectLoopRefused(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "web:1", Port: 3000})
	add := func(host, to string) error {
		_, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: host, RedirectTo: to})
		return err
	}
	for _, d := range [][2]string{{"a.example.com", "b.example.com"}, {"b.example.com", "c.example.com"}} {
		if err := add(d[0], d[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := add("c.example.com", "a.example.com"); CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "b.example.com redirects back to c.example.com") {
		t.Fatalf("a loop through two redirects: %v", err)
	}
	if err := add("x.example.com", "a.example.com"); err != nil {
		t.Fatalf("a chain that ends: %v", err)
	}
}

// A generated domain without DNS names the one wildcard record that
// serves every generated domain, not a record of its own.
func TestGeneratedDomainNamesTheWildcard(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.rt.SetDomainCheck(func(host string) node.DomainCheck {
		return node.DomainCheck{ExpectedIPs: []string{"203.0.113.10"}, Error: host + " has no DNS records yet; add an A record pointing at this server"}
	})
	if _, err := h.c.GenerateDomain(h.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	ss, _ := h.c.serviceFor(h.ctx, testSystem, web, apitypes.ScopeRead, "testing")
	rows, _ := h.store.ListDomainsByService(h.ctx, ss.svc.ID)
	var dnsOK time.Time
	h.c.checkDomain(h.ctx, rows[0], &dnsOK)
	ds, err := h.c.ListDomains(h.ctx, testSystem, web)
	if err != nil || ds[0].Status != apitypes.DomainWaitingForDNS || !strings.Contains(ds[0].StatusReason, "add the wildcard record *.apps.test A 203.0.113.10") {
		t.Fatalf("generated domain: %v %+v", err, ds)
	}
}
