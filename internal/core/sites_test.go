package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// analyticsRoute finds the analytics group of hostname in the fake's
// routes.
func analyticsRoute(h *harness, hostname string) (node.AnalyticsHosts, bool) {
	for _, r := range h.rt.Routes() {
		for _, a := range r.Analytics {
			if slices.Contains(a.Hostnames, hostname) {
				return a, true
			}
		}
	}
	return node.AnalyticsHosts{}, false
}

// domain adds hostname to a service.
func (h *harness) domain(ref ServiceRef, hostname string) {
	h.t.Helper()
	if _, err := h.c.AddDomain(h.ctx, testSystem, ref, apitypes.AddDomainRequest{Hostname: hostname}); err != nil {
		h.t.Fatal(err)
	}
}

// countVisits turns analytics on for a service and returns its site.
func (h *harness) countVisits(ref ServiceRef) apitypes.Site {
	h.t.Helper()
	site, err := h.c.EnableServiceAnalytics(h.ctx, testSystem, ref)
	if err != nil {
		h.t.Fatal(err)
	}
	return site
}

// A site's pages get the script from Caddy unless its auto_script is off,
// on the site or as the platform's default, or its consent is none (ADR
// 0106).
func TestSiteAutoScript(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 8080})
	h.deploy(web, "nginx:1")
	h.domain(web, "shop.example.com")
	site := h.countVisits(web)
	script := func() bool {
		r, ok := analyticsRoute(h, "shop.example.com")
		if !ok {
			t.Fatal("no analytics route")
		}
		return r.Script
	}
	if !script() || site.Settings.AutoScript == nil || !*site.Settings.AutoScript {
		t.Fatalf("on by default: route %t, settings %+v", script(), site.Settings)
	}
	off := false
	if _, err := h.c.UpdateSite(h.ctx, testSystem, "shop", site.Name, apitypes.UpdateSiteRequest{Settings: apitypes.SiteSettings{AutoScript: &off}}); err != nil {
		t.Fatal(err)
	}
	if script() {
		t.Error("the site turned it off; the route still adds the script")
	}
	if _, err := h.c.UpdateSite(h.ctx, testSystem, "shop", site.Name, apitypes.UpdateSiteRequest{Reset: []string{"auto_script"}}); err != nil {
		t.Fatal(err)
	}
	// A consent none site's pages load the script once the visitor agrees.
	none := apitypes.ConsentNone
	if _, err := h.c.UpdateSite(h.ctx, testSystem, "shop", site.Name, apitypes.UpdateSiteRequest{Settings: apitypes.SiteSettings{Consent: &none}}); err != nil {
		t.Fatal(err)
	}
	if script() {
		t.Error("consent none, and the route adds the script")
	}
	if _, err := h.c.UpdateSite(h.ctx, testSystem, "shop", site.Name, apitypes.UpdateSiteRequest{Reset: []string{"consent"}}); err != nil {
		t.Fatal(err)
	}
	if !script() {
		t.Error("consent back to the default, and no script")
	}
	if _, err := h.c.SetSiteDefaults(h.ctx, testSystem, apitypes.SiteSettings{AutoScript: &off}, nil); err != nil {
		t.Fatal(err)
	}
	if script() {
		t.Error("the platform's default turned it off; the route still adds the script")
	}
}

func TestSites(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 8080})
	rel := h.deploy(web, "nginx:1")
	h.domain(web, "shop.example.com")
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "www.shop.example.com", RedirectTo: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}

	// Named after its custom domain; the redirect isn't counted.
	site := h.countVisits(web)
	if site.Name != "shop.example.com" || site.Service != "web" || site.Environment != "production" ||
		len(site.Hostnames) != 1 || site.Hostnames[0].Hostname != "shop.example.com" {
		t.Fatalf("site = %+v", site)
	}
	// A domain added later counts without being asked.
	h.domain(web, "shop.example.org")
	r, ok := analyticsRoute(h, "shop.example.com")
	if !ok || !slices.Contains(r.Hostnames, "shop.example.org") || r.Tag == "" {
		t.Fatalf("analytics route = %+v", r)
	}
	if _, ok := analyticsRoute(h, "www.shop.example.com"); ok {
		t.Error("a redirect routes to the ingest")
	}
	var siteID, svcID, relID int64
	if _, err := fmt.Sscanf(r.Tag, "%d.%d.%d", &siteID, &svcID, &relID); err != nil || siteID == 0 || svcID == 0 || relID == 0 {
		t.Errorf("tag %q", r.Tag)
	}

	// A new deploy retags at the cutover.
	h.deploy(web, "nginx:2")
	if r2, _ := analyticsRoute(h, "shop.example.com"); r2.Tag == r.Tag {
		t.Errorf("the tag didn't follow release %d → the next", rel.Number)
	}

	// Settings reach every server's ingest.
	paths := []string{"/admin/*"}
	if _, err := h.c.UpdateSite(h.ctx, testSystem, "shop", "shop.example.com", apitypes.UpdateSiteRequest{Settings: apitypes.SiteSettings{ExcludedPaths: &paths}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the site's rules on the server", func() bool {
		rules := h.rt.AnalyticsSiteRules()[siteID]
		return len(rules.ExcludedPaths) == 1
	})
	if _, err := h.c.UpdateSite(h.ctx, testSystem, "shop", "shop.example.com", apitypes.UpdateSiteRequest{Reset: []string{"nope"}}); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("resetting an unknown setting: %v", err)
	}

	// Removing a domain leaves the site as it is.
	if err := h.c.RemoveDomain(h.ctx, testSystem, web, "shop.example.com"); err != nil {
		t.Fatal(err)
	}
	if site, err := h.c.GetSite(h.ctx, testSystem, "shop", "shop.example.com"); err != nil || site.Archived || len(site.Hostnames) != 1 {
		t.Errorf("after removing its name's domain: %+v, %v", site, err)
	}

	// Off: archived with its history. On again: the same site comes back.
	if err := h.c.DisableServiceAnalytics(h.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	sites, err := h.c.ListSites(h.ctx, testSystem, "shop")
	if err != nil || len(sites) != 1 || !sites[0].Archived {
		t.Errorf("sites after turning it off: %+v, %v", sites, err)
	}
	if _, ok := analyticsRoute(h, "shop.example.org"); ok {
		t.Error("an archived site's hostname still routes to the ingest")
	}
	if err := h.c.DisableServiceAnalytics(h.ctx, testSystem, web); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("turning it off twice: %v", err)
	}
	if back := h.countVisits(web); back.Name != "shop.example.com" || back.Archived {
		t.Errorf("on again = %+v, want the old site back", back)
	}
	if r, _ := analyticsRoute(h, "shop.example.org"); !strings.HasPrefix(r.Tag, fmt.Sprint(siteID, ".")) {
		t.Errorf("on again routes with tag %q, want site %d", r.Tag, siteID)
	}

	// Archiving the service archives its site.
	if _, err := h.c.ArchiveService(h.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	if sites, _ := h.c.ListSites(h.ctx, testSystem, "shop"); len(sites) != 1 || !sites[0].Archived {
		t.Errorf("sites after archiving the service: %+v", sites)
	}
}

// A site without a custom domain is named after its service.
func TestSiteNamedAfterItsService(t *testing.T) {
	h := newHarness(t)
	api := h.service("api", apitypes.CreateServiceRequest{Port: 8080})
	if site := h.countVisits(api); site.Name != "api" {
		t.Errorf("site = %q, want api", site.Name)
	}
}

// A site moves whole to a service that replaced its own; the old one
// stops counting, and a target that counts already is refused.
func TestSiteMove(t *testing.T) {
	h := newHarness(t)
	v1 := h.service("web", apitypes.CreateServiceRequest{Port: 8080})
	v2 := h.service("web-v2", apitypes.CreateServiceRequest{Port: 8080})
	h.deploy(v1, "nginx:1")
	h.deploy(v2, "nginx:1")
	h.domain(v1, "old.example.com")
	h.domain(v2, "shop.example.com")
	h.countVisits(v1)
	before, _ := analyticsRoute(h, "old.example.com")
	// web-v2 counted for a while too, so it has a site, newer than web's,
	// archived since: the site moved in must win over it.
	early := h.countVisits(v2)
	if err := h.c.DisableServiceAnalytics(h.ctx, testSystem, v2); err != nil {
		t.Fatal(err)
	}

	to := apitypes.MoveSiteRequest{Environment: "production", Service: "web-v2"}
	reader := Actor{Name: "reader", Kind: "token", Scope: apitypes.ScopeRead, OrgID: 1}
	if _, err := h.c.MoveSite(h.ctx, reader, "shop", "old.example.com", to); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("a reader moved a site: %v", err)
	}
	site, err := h.c.MoveSite(h.ctx, testSystem, "shop", "old.example.com", to)
	if err != nil || site.Name != "old.example.com" || site.Service != "web-v2" || len(site.Hostnames) != 1 || site.Hostnames[0].Hostname != "shop.example.com" {
		t.Fatalf("moved = %+v, %v", site, err)
	}
	after, ok := analyticsRoute(h, "shop.example.com")
	if !ok || strings.Split(after.Tag, ".")[0] != strings.Split(before.Tag, ".")[0] {
		t.Errorf("web-v2 routes with tag %q, want the site of %q", after.Tag, before.Tag)
	}
	if _, ok := analyticsRoute(h, "old.example.com"); ok {
		t.Error("the old service still counts")
	}
	stats, err := h.c.SiteStats(h.ctx, testSystem, "shop", "old.example.com", StatsQuery{Range: "24h"})
	if err != nil || !slices.ContainsFunc(stats.Markers, func(m apitypes.SiteMarker) bool { return m.Kind == "moved" }) {
		t.Errorf("markers = %+v, %v; want the move", stats.Markers, err)
	}
	// Counting on web-v2 again keeps the moved site, not its older one;
	// off and on brings back the one it counted last.
	if again := h.countVisits(v2); again.Name != "old.example.com" {
		t.Errorf("enable after the move = %s, want old.example.com (not %s)", again.Name, early.Name)
	}
	if err := h.c.DisableServiceAnalytics(h.ctx, testSystem, v2); err != nil {
		t.Fatal(err)
	}
	if back := h.countVisits(v2); back.Name != "old.example.com" {
		t.Errorf("on again = %s, want the site it counted last", back.Name)
	}

	// web counts again in a site of its own; moving into web-v2 now is refused.
	h.countVisits(v1)
	sites, _ := h.c.ListSites(h.ctx, testSystem, "shop")
	var web string
	for _, s := range sites {
		if s.Service == "web" && !s.Archived {
			web = s.Name
		}
	}
	if _, err := h.c.MoveSite(h.ctx, testSystem, "shop", web, to); CodeOf(err) != apitypes.CodeConflict {
		t.Errorf("moving into a service that counts: %v", err)
	}
	// Nor into an archived service.
	v3 := h.service("web-v3", apitypes.CreateServiceRequest{Port: 8080})
	if _, err := h.c.ArchiveService(h.ctx, testSystem, v3); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.MoveSite(h.ctx, testSystem, "shop", web, apitypes.MoveSiteRequest{Environment: "production", Service: "web-v3"}); CodeOf(err) != apitypes.CodeConflict {
		t.Errorf("moving into an archived service: %v", err)
	}
}

func TestSiteStats(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 8080})
	h.deploy(web, "nginx:1")
	h.domain(web, "shop.example.com")
	if _, err := h.c.EnableServiceAnalytics(h.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	r, _ := analyticsRoute(h, "shop.example.com")
	var siteID, svcID, relID int64
	_, _ = fmt.Sscanf(r.Tag, "%d.%d.%d", &siteID, &svcID, &relID)
	ms := now().UnixMilli()
	h.rt.AppendSpool(fmt.Appendf(nil, `{"t":%d,"s":%d,"sv":%d,"rl":%d,"k":1,"p":"/","v":1}`, ms-1000, siteID, svcID, relID),
		fmt.Appendf(nil, `{"t":%d,"s":%d,"sv":%d,"rl":%d,"k":1,"p":"/a","v":2}`, ms-500, siteID, svcID, relID))
	var st apitypes.SiteStats
	eventually(t, "the dashboard to count the visitors", func() bool {
		var err error
		st, err = h.c.SiteStats(h.ctx, testSystem, "shop", "shop.example.com", StatsQuery{Range: "24h"})
		return err == nil && st.StoreOnline && st.Overview.Visitors == 2 && st.Overview.Pageviews == 2 && st.Bucket == "hour"
	})
	// The organization's list carries the same 24 hours, with an hourly
	// series that has every hour; another org's actor sees none of it.
	list, err := h.c.ListOrgSites(h.ctx, testSystem, "24h")
	if err != nil || len(list) != 1 || list[0].Project != "shop" || list[0].Site.Name != "shop.example.com" ||
		list[0].Overview == nil || list[0].Overview.Visitors != 2 || list[0].Previous == nil || list[0].Previous.Visitors != 0 {
		t.Fatalf("org sites = %+v, %v", list, err)
	}
	var views int64
	for i, p := range list[0].Series {
		views += p.Pageviews
		if i > 0 && p.T-list[0].Series[i-1].T != time.Hour.Milliseconds() {
			t.Errorf("series step at %d = %d", i, p.T-list[0].Series[i-1].T)
		}
	}
	if n := len(list[0].Series); n < 24 || n > 25 || views != 2 {
		t.Errorf("series = %d points with %d page views, want 24-25 and 2", n, views)
	}
	if _, err := h.c.ListOrgSites(h.ctx, testSystem, "1y"); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("range 1y = %v, want a validation error", err)
	}
	if list, err := h.c.ListOrgSites(h.ctx, Actor{Name: "other-org", Kind: "token", Scope: apitypes.ScopeRead, OrgID: 2}, "24h"); err != nil || len(list) != 0 {
		t.Errorf("another org's sites = %+v, %v", list, err)
	}
	// Services and releases are labelled; their values stay what filters take.
	if rows := st.Breakdowns["service"]; len(rows) != 1 || rows[0].Label != "web" || rows[0].Value != fmt.Sprint(svcID) {
		t.Errorf("service breakdown = %+v", rows)
	}
	if b, err := h.c.SiteBreakdown(h.ctx, testSystem, "shop", "shop.example.com", analytics.DimRelease, StatsQuery{Range: "24h"}, 10); err != nil || len(b.Rows) != 1 || b.Rows[0].Label != "web r1" {
		t.Errorf("release breakdown = %+v, %v", b, err)
	}
	// A service filter may name the service.
	byName := StatsQuery{Range: "24h", Filters: []analytics.Filter{{Dimension: analytics.DimService, Op: analytics.OpIs, Value: "web"}}}
	if st, err := h.c.SiteStats(h.ctx, testSystem, "shop", "shop.example.com", byName); err != nil || st.Overview.Pageviews != 2 {
		t.Errorf("service:is:web = %+v, %v", st.Overview, err)
	}
	byName.Filters[0].Value = "nope"
	if _, err := h.c.SiteStats(h.ctx, testSystem, "shop", "shop.example.com", byName); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("an unknown service in a filter: %v", err)
	}
	byName.Filters[0] = analytics.Filter{Dimension: analytics.DimService, Op: analytics.OpContains, Value: "we"}
	if _, err := h.c.SiteStats(h.ctx, testSystem, "shop", "shop.example.com", byName); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("service:contains, which can't match IDs: %v", err)
	}
	if _, err := h.c.SiteStats(h.ctx, testSystem, "shop", "shop.example.com", StatsQuery{Range: "1y"}); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("an unknown range: %v", err)
	}
	if _, err := h.c.SiteStats(h.ctx, testSystem, "shop", "nope", StatsQuery{}); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("an unknown site: %v", err)
	}
}

func TestSitePermissions(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 8080})
	h.domain(web, "shop.example.com")
	reader := Actor{Name: "reader", Kind: "token", Scope: apitypes.ScopeRead, OrgID: 1}
	if _, err := h.c.EnableServiceAnalytics(h.ctx, reader, web); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("a reader turned analytics on: %v", err)
	}
	other := Actor{Name: "other-org", Kind: "token", Scope: apitypes.ScopeAdmin, OrgID: 2}
	if _, err := h.c.EnableServiceAnalytics(h.ctx, other, web); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("another org's actor: %v", err)
	}
	if _, err := h.c.EnableServiceAnalytics(h.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SiteStats(h.ctx, reader, "shop", "shop.example.com", StatsQuery{}); err != nil {
		t.Errorf("a reader reading stats: %v", err)
	}
	if _, err := h.c.UpdateSite(h.ctx, reader, "shop", "shop.example.com", apitypes.UpdateSiteRequest{Name: "x"}); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("a reader changed a site: %v", err)
	}
	if _, err := h.c.SetSiteDefaults(h.ctx, Actor{Name: "admin", Kind: "token", Scope: apitypes.ScopeAdmin, OrgID: 1}, apitypes.SiteSettings{}, nil); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("an org admin changed the install's defaults: %v", err)
	}
}

func TestSiteLive(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 8080})
	h.deploy(web, "nginx:1")
	h.domain(web, "shop.example.com")
	if _, err := h.c.EnableServiceAnalytics(h.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	r, _ := analyticsRoute(h, "shop.example.com")
	var siteID int64
	_, _ = fmt.Sscanf(r.Tag, "%d.", &siteID)
	ms := now().UnixMilli()
	// Already in the store before anyone watches: the view starts with it.
	h.rt.AppendSpool(fmt.Appendf(nil, `{"t":%d,"s":%d,"k":1,"p":"/","v":1}`, ms, siteID))
	eventually(t, "the first event in the store", func() bool { s := h.rt.AnalyticsStore(); return s != nil && len(s.Events()) == 1 })

	stream, err := h.c.SiteLive(h.ctx, testSystem, "shop", "shop.example.com")
	if err != nil {
		t.Fatal(err)
	}
	views := make(chan apitypes.SiteLive, 8)
	ctx, cancel := context.WithCancel(h.ctx)
	done := make(chan error, 1)
	go func() {
		done <- stream(ctx, func() {}, func(v ...apitypes.SiteLive) error {
			for _, x := range v {
				views <- x
			}
			return nil
		})
	}()
	if v := <-views; v.Visitors != 1 || len(v.Recent) != 1 || v.Recent[0].Path != "/" {
		t.Fatalf("first view = %+v", v)
	}
	// A new visitor arrives through the forwarder.
	h.rt.AppendSpool(fmt.Appendf(nil, `{"t":%d,"s":%d,"k":1,"p":"/pricing","v":2}`, ms+1, siteID))
	if v := <-views; v.Visitors != 2 || v.Recent[0].Path != "/pricing" {
		t.Errorf("after a new visitor = %+v", v)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("the stream ended with %v", err)
	}
	if h.c.analytics.live.watched.Load() != 0 {
		t.Error("the site is still watched after its only watcher left")
	}
}

func TestSiteRetention(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 8080})
	h.deploy(web, "nginx:1")
	h.domain(web, "shop.example.com")
	if _, err := h.c.EnableServiceAnalytics(h.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	days := 30
	if _, err := h.c.UpdateSite(h.ctx, testSystem, "shop", "shop.example.com", apitypes.UpdateSiteRequest{Settings: apitypes.SiteSettings{EventRetentionDays: &days}}); err != nil {
		t.Fatal(err)
	}
	r, _ := analyticsRoute(h, "shop.example.com")
	var siteID int64
	_, _ = fmt.Sscanf(r.Tag, "%d.", &siteID)
	old := now().AddDate(0, 0, -40).UnixMilli()
	h.rt.AppendSpool(fmt.Appendf(nil, `{"t":%d,"s":%d,"k":1,"p":"/","v":1}`, old, siteID), fmt.Appendf(nil, `{"t":%d,"s":%d,"k":1,"p":"/","v":2}`, now().UnixMilli(), siteID))
	eventually(t, "both events in the store", func() bool { return storeEvents(h) == 2 })
	h.c.rotateSalts(h.ctx, now().AddDate(0, 0, 1)) // the next day's pass
	eventually(t, "the old event pruned", func() bool { return storeEvents(h) == 1 })

	// Filters over the year see only the kept 30 days, and say so.
	filtered := StatsQuery{Range: "12m", Filters: []analytics.Filter{{Dimension: analytics.DimPage, Op: analytics.OpIs, Value: "/"}}}
	if st, err := h.c.SiteStats(h.ctx, testSystem, "shop", "shop.example.com", filtered); err != nil || !strings.Contains(st.Notice, "last 30 days") {
		t.Errorf("filtered year: notice %q, err %v", st.Notice, err)
	}
	if st, err := h.c.SiteStats(h.ctx, testSystem, "shop", "shop.example.com", StatsQuery{Range: "12m"}); err != nil || st.Notice != "" {
		t.Errorf("unfiltered year: notice %q, err %v", st.Notice, err)
	}
}
