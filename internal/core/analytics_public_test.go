package core

import (
	"fmt"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// A public dashboard's address belongs to one site across every org and
// can't be guessed from its name (ADR 0087): another org's site with the
// same name gets another, a rename keeps it, and nobody can claim it, or
// the site's own domain, as a public domain.
func TestPublicAddressesAreUnique(t *testing.T) {
	h := newHarness(t)
	h.c.cfg.BaseDomain = "dock.example.net"
	countingSite(h)
	on, off := true, false
	org, err := h.store.CreateOrg(h.ctx, store.CreateOrgParams{Slug: "other", Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	them := System.InOrg(org.ID)
	if _, err := h.c.CreateProject(h.ctx, them, apitypes.CreateProjectRequest{Name: "theirs"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.CreateService(h.ctx, them, EnvRef{"theirs", "production"}, apitypes.CreateServiceRequest{Name: "web", Port: 8080}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.AddDomain(h.ctx, them, ServiceRef{"theirs", "production", "web"}, apitypes.AddDomainRequest{Hostname: "shop-example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.EnableServiceAnalytics(h.ctx, them, ServiceRef{"theirs", "production", "web"}); err != nil {
		t.Fatal(err)
	}
	address := func(a Actor, project, name string) string {
		t.Helper()
		site, err := h.c.UpdateSite(h.ctx, a, project, name, apitypes.UpdateSiteRequest{Settings: apitypes.SiteSettings{Public: &on}})
		if err != nil || len(site.PublicURLs) != 1 {
			t.Fatalf("%s: %v, %v", name, site.PublicURLs, err)
		}
		return strings.TrimPrefix(site.PublicURLs[0], "https://")
	}
	// Both names read shop-example-com; the random parts tell them apart.
	ours, theirs := address(testSystem, "shop", "shop.example.com"), address(them, "theirs", "shop-example.com")
	for _, host := range []string{ours, theirs} {
		if !generatedLike("stats-shop-example-com", "dock.example.net").MatchString(host) {
			t.Errorf("address %s, want stats-shop-example-com-<random>", host)
		}
	}
	if ours == theirs {
		t.Fatalf("two sites share %s", ours)
	}
	// Renamed, a site keeps its address.
	if _, err := h.c.UpdateSite(h.ctx, them, "theirs", "shop-example.com", apitypes.UpdateSiteRequest{Name: "blog"}); err != nil {
		t.Fatal(err)
	}
	if got := address(them, "theirs", "blog"); got != theirs {
		t.Errorf("renamed, the address moved from %s to %s", theirs, got)
	}
	// Claimed as a public domain: refused.
	if _, err := h.c.UpdateSite(h.ctx, them, "theirs", "blog", apitypes.UpdateSiteRequest{
		Settings: apitypes.SiteSettings{PublicDomain: &ours}}); CodeOf(err) != apitypes.CodeHostnameTaken {
		t.Errorf("another org took our address as its public domain: %v", err)
	}
	// Our own domain, set while our dashboard is off, stays ours.
	domain := "stats.shop.example"
	if _, err := h.c.UpdateSite(h.ctx, testSystem, "shop", "shop.example.com", apitypes.UpdateSiteRequest{
		Settings: apitypes.SiteSettings{Public: &off, PublicDomain: &domain}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.UpdateSite(h.ctx, them, "theirs", "blog", apitypes.UpdateSiteRequest{
		Settings: apitypes.SiteSettings{Public: &on, PublicDomain: &domain}}); CodeOf(err) != apitypes.CodeHostnameTaken {
		t.Errorf("another org took our public domain: %v", err)
	}
	if _, err := h.c.UpdateSite(h.ctx, testSystem, "shop", "shop.example.com", apitypes.UpdateSiteRequest{Settings: apitypes.SiteSettings{Public: &on}}); err != nil {
		t.Fatal(err)
	}

	eventually(t, "the store", func() bool { return h.rt.AnalyticsStore() != nil })
	for _, host := range []string{ours, "stats.shop.example"} {
		if st, err := h.c.PublicStats(h.ctx, host, ""); err != nil || st.Site != "shop.example.com" {
			t.Errorf("%s shows %q, %v", host, st.Site, err)
		}
	}
}

// Sites from before addresses were stored get theirs when Dock starts,
// the oldest first: it keeps an address that two names share.
func TestPublicAddressesAssignedOldestFirst(t *testing.T) {
	h := newHarness(t)
	h.c.cfg.BaseDomain = "dock.example.net"
	for i, host := range []string{"shop-example.com", "shop.example.com"} {
		svc := h.service(fmt.Sprint("web", i), apitypes.CreateServiceRequest{Port: 8080})
		h.domain(svc, host)
		if _, err := h.c.EnableServiceAnalytics(h.ctx, testSystem, svc); err != nil {
			t.Fatal(err)
		}
	}
	// As migration 0037 leaves them.
	if _, err := h.store.DB.ExecContext(h.ctx, `UPDATE sites SET public_slug = NULL`); err != nil {
		t.Fatal(err)
	}
	h.restart()
	h.c.cfg.BaseDomain = "dock.example.net"
	on := true
	for name, want := range map[string]string{"shop-example.com": "https://stats-shop-example-com.dock.example.net", "shop.example.com": "https://stats-shop-example-com-2.dock.example.net"} {
		site, err := h.c.UpdateSite(h.ctx, testSystem, "shop", name, apitypes.UpdateSiteRequest{Settings: apitypes.SiteSettings{Public: &on}})
		if err != nil || len(site.PublicURLs) != 1 || site.PublicURLs[0] != want {
			t.Errorf("%s: %v, %v; want %s", name, site.PublicURLs, err, want)
		}
	}
}

func TestPublicDashboards(t *testing.T) {
	h := newHarness(t)
	h.c.cfg.BaseDomain = "dock.example.net"
	countingSite(h)
	if got := publicSlug("Shop.Example.com"); got != "shop-example-com" {
		t.Errorf("slug = %q", got)
	}
	on := true
	domain := "Stats.Example.com"
	site, err := h.c.UpdateSite(h.ctx, testSystem, "shop", "shop.example.com", apitypes.UpdateSiteRequest{
		Settings: apitypes.SiteSettings{Public: &on, PublicDomain: &domain}})
	if err != nil {
		t.Fatal(err)
	}
	if len(site.PublicURLs) != 2 || !generatedLike("stats-shop-example-com", "dock.example.net").MatchString(strings.TrimPrefix(site.PublicURLs[0], "https://")) || site.PublicURLs[1] != "https://stats.example.com" {
		t.Fatalf("public URLs = %v", site.PublicURLs)
	}
	address := strings.TrimPrefix(site.PublicURLs[0], "https://")
	// Its own domain waits for DNS, which is best a CNAME to its Dock address.
	h.rt.SetDomainCheck(func(host string) node.DomainCheck {
		return node.DomainCheck{DNSOK: host != "stats.example.com", CertificateOK: true}
	})
	pub, err := h.c.SitePublic(h.ctx, testSystem, "shop", "shop.example.com")
	if err != nil || !pub.Public || len(pub.Addresses) != 2 {
		t.Fatalf("public = %+v, %v", pub, err)
	}
	if a := pub.Addresses[0]; a.Own || a.Status != apitypes.DomainActive {
		t.Errorf("Dock's address = %+v", a)
	}
	if a := pub.Addresses[1]; !a.Own || a.Status != apitypes.DomainWaitingForDNS || len(a.DNSRecords) == 0 ||
		a.DNSRecords[0] != (apitypes.DNSRecord{Type: "CNAME", Name: "stats.example.com", Value: address}) {
		t.Errorf("own domain = %+v", a)
	}
	// Routed on the control plane to the stats listener, with certificates.
	routed := false
	for _, r := range h.rt.Routes() {
		if r.Upstream == h.c.cfg.Analytics.StatsListen && len(r.Hostnames) == 2 && r.ClientIPHeader == ClientIPHeader {
			routed = true
		}
	}
	if !routed {
		t.Errorf("no route to the stats listener: %+v", h.rt.Routes())
	}
	if ok, err := h.c.AllowCertificate(h.ctx, "stats.example.com"); err != nil || !ok {
		t.Errorf("certificate for the public domain: %v, %v", ok, err)
	}
	// Nobody else may take its hostname, and it may not take an app's.
	web := ServiceRef{Project: "shop", Environment: "production", Service: "web"}
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "stats.example.com"}); CodeOf(err) != apitypes.CodeHostnameTaken {
		t.Errorf("a domain on a public dashboard's hostname: %v", err)
	}
	taken := "shop.example.com"
	if _, err := h.c.UpdateSite(h.ctx, testSystem, "shop", "shop.example.com", apitypes.UpdateSiteRequest{
		Settings: apitypes.SiteSettings{PublicDomain: &taken}}); CodeOf(err) != apitypes.CodeHostnameTaken {
		t.Errorf("a public domain on an app's hostname: %v", err)
	}

	eventually(t, "the store", func() bool { return h.rt.AnalyticsStore() != nil })
	st, err := h.c.PublicStats(h.ctx, "stats.example.com", "30d")
	if err != nil || st.Site != "shop.example.com" || st.ShowErrors || len(st.Breakdowns["page"]) != 0 {
		t.Fatalf("public stats = %+v, %v", st, err)
	}
	if _, ok := st.Breakdowns["service"]; ok {
		t.Error("the public dashboard shows services")
	}
	if _, err := h.c.PublicStats(h.ctx, "stats.example.com", "1y"); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("an unknown range: %v", err)
	}
	if _, err := h.c.PublicStats(h.ctx, "nope.example.com", ""); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("a hostname that isn't a dashboard: %v", err)
	}

	// Any change to its settings makes cached pages stale.
	gen, ok := h.c.PublicDashboard("stats.example.com")
	if _, err := h.c.UpdateSite(h.ctx, testSystem, "shop", "shop.example.com", apitypes.UpdateSiteRequest{Settings: apitypes.SiteSettings{PublicErrors: &on}}); err != nil {
		t.Fatal(err)
	}
	if now, _ := h.c.PublicDashboard("stats.example.com"); !ok || now == gen {
		t.Errorf("settings changed, generation %d still %d (a dashboard: %v)", gen, now, ok)
	}

	// Turned off: gone from the routes and the dashboard.
	off := false
	if _, err := h.c.UpdateSite(h.ctx, testSystem, "shop", "shop.example.com", apitypes.UpdateSiteRequest{Settings: apitypes.SiteSettings{Public: &off}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.PublicStats(h.ctx, "stats.example.com", ""); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("a dashboard turned off: %v", err)
	}
	if _, ok := h.c.PublicDashboard("stats.example.com"); ok {
		t.Error("a dashboard turned off is still one")
	}
	if ok, _ := h.c.AllowCertificate(h.ctx, "stats.example.com"); ok {
		t.Error("a certificate for a dashboard turned off")
	}
}
