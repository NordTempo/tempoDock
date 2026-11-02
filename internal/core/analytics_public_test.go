package core

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

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
	if len(site.PublicURLs) != 2 || site.PublicURLs[0] != "https://stats-shop-example-com.dock.example.net" || site.PublicURLs[1] != "https://stats.example.com" {
		t.Fatalf("public URLs = %v", site.PublicURLs)
	}
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
		a.DNSRecords[0] != (apitypes.DNSRecord{Type: "CNAME", Name: "stats.example.com", Value: "stats-shop-example-com.dock.example.net"}) {
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
