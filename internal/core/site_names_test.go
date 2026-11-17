package core

import (
	"slices"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// lastMarker is the newest site marker's message.
func (h *harness) lastMarker() string {
	h.t.Helper()
	var msg string
	if err := h.store.DB.QueryRowContext(h.ctx, `SELECT message FROM site_markers ORDER BY id DESC LIMIT 1`).Scan(&msg); err != nil {
		h.t.Fatal(err)
	}
	return msg
}

// siteNames are the project's live sites' names.
func (h *harness) siteNames() []string {
	h.t.Helper()
	sites, err := h.c.ListSites(h.ctx, testSystem, "shop")
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for _, s := range sites {
		out = append(out, s.Name)
	}
	return out
}

// A new site is named after the service's custom domain even when
// analytics is turned on for its generated one; when the hostname a site
// is named after goes away, it takes another of its hostnames' names,
// custom first, but a name someone gave it stays.
func TestSiteNamesFollowLiveHostnames(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 8080})
	h.deploy(web, "nginx:1")
	gen, err := h.c.GenerateDomain(h.ctx, testSystem, web)
	if err != nil {
		t.Fatal(err)
	}
	generated := gen.Domain.Hostname
	h.domain(web, "shop.example.com", "")
	if _, err := h.c.EnableHostnameAnalytics(h.ctx, testSystem, generated, apitypes.EnableAnalyticsRequest{}); err != nil {
		t.Fatal(err)
	}
	if got := h.siteNames(); len(got) != 1 || got[0] != "shop.example.com" {
		t.Fatalf("new site named %v, want the custom domain", got)
	}

	// Named after its generated hostname (as sites made before were), it
	// takes the custom one's name once the generated one is removed.
	if _, err := h.c.UpdateSite(h.ctx, testSystem, "shop", "shop.example.com", apitypes.UpdateSiteRequest{Name: generated}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.EnableHostnameAnalytics(h.ctx, testSystem, "shop.example.com", apitypes.EnableAnalyticsRequest{}); err != nil {
		t.Fatal(err)
	}
	if err := h.c.RemoveDomain(h.ctx, testSystem, web, generated); err != nil {
		t.Fatal(err)
	}
	if got := h.siteNames(); len(got) != 1 || got[0] != "shop.example.com" {
		t.Fatalf("after removing %s: sites %v", generated, got)
	}
	if got, want := h.lastMarker(), generated+" removed; the site is now shop.example.com"; got != want {
		t.Errorf("marker %q, want %q", got, want)
	}

	// Turning analytics off for the hostname it is named after does too.
	h.domain(web, "www.shop.example.com", "")
	if _, err := h.c.EnableHostnameAnalytics(h.ctx, testSystem, "www.shop.example.com", apitypes.EnableAnalyticsRequest{}); err != nil {
		t.Fatal(err)
	}
	if err := h.c.DisableHostnameAnalytics(h.ctx, testSystem, "shop.example.com"); err != nil {
		t.Fatal(err)
	}
	if got := h.siteNames(); len(got) != 1 || got[0] != "www.shop.example.com" {
		t.Fatalf("after turning shop.example.com off: sites %v", got)
	}
	if got := h.lastMarker(); got != "shop.example.com removed; the site is now www.shop.example.com" {
		t.Errorf("marker %q", got)
	}

	// A name given by hand stays.
	if _, err := h.c.UpdateSite(h.ctx, testSystem, "shop", "www.shop.example.com", apitypes.UpdateSiteRequest{Name: "Shop"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.EnableHostnameAnalytics(h.ctx, testSystem, "shop.example.com", apitypes.EnableAnalyticsRequest{Site: "Shop"}); err != nil {
		t.Fatal(err)
	}
	if err := h.c.RemoveDomain(h.ctx, testSystem, web, "www.shop.example.com"); err != nil {
		t.Fatal(err)
	}
	if got := h.siteNames(); len(got) != 1 || got[0] != "Shop" {
		t.Fatalf("a hand-given name changed: %v", got)
	}
}

// A hostname that moves to a new site takes its name along; the site it
// left, named after it, takes another of its hostnames' names.
func TestSiteRenamedWhenItsHostnameMovesAway(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 8080})
	api := h.service("api", apitypes.CreateServiceRequest{Port: 8080})
	h.domain(web, "detoo.no", "")
	h.domain(web, "admin.detoo.no", "")
	for _, host := range []string{"detoo.no", "admin.detoo.no"} {
		if _, err := h.c.EnableHostnameAnalytics(h.ctx, testSystem, host, apitypes.EnableAnalyticsRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.c.RemoveDomain(h.ctx, testSystem, web, "detoo.no"); err != nil {
		t.Fatal(err)
	}
	// The removal renamed it; name it after detoo.no again to see a move.
	if _, err := h.c.UpdateSite(h.ctx, testSystem, "shop", "admin.detoo.no", apitypes.UpdateSiteRequest{Name: "detoo.no"}); err != nil {
		t.Fatal(err)
	}
	h.domain(api, "detoo.no", "new")
	got := h.siteNames()
	slices.Sort(got)
	if !slices.Equal(got, []string{"admin.detoo.no", "detoo.no"}) {
		t.Fatalf("sites %v, want the old one renamed and a new detoo.no", got)
	}
}

// A site named after a hostname removed before Dock renamed sites (like
// one named web-detoo.<base>) is renamed when Dock starts.
func TestSiteNamedAfterGoneHostnameRenamedAtStart(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 8080})
	h.deploy(web, "nginx:1")
	h.domain(web, "web-shop.apps.test", "")
	h.domain(web, "shop.example.com", "")
	for _, host := range []string{"web-shop.apps.test", "shop.example.com"} {
		if _, err := h.c.EnableHostnameAnalytics(h.ctx, testSystem, host, apitypes.EnableAnalyticsRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	// As a removal before this left it: the domain gone, the name kept.
	if _, err := h.store.DB.ExecContext(h.ctx, `DELETE FROM domains WHERE hostname = 'web-shop.apps.test'`); err != nil {
		t.Fatal(err)
	}
	if got := h.siteNames(); len(got) != 1 || got[0] != "web-shop.apps.test" {
		t.Fatalf("before restart: sites %v", got)
	}
	h.restart()
	if got := h.siteNames(); len(got) != 1 || got[0] != "shop.example.com" {
		t.Fatalf("after restart: sites %v", got)
	}
}
