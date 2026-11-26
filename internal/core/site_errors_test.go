package core

import (
	"fmt"
	"testing"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
)

func TestSiteErrorsAndVitals(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 8080})
	h.deploy(web, "nginx:1")
	h.domain(web, "shop.example.com")
	if _, err := h.c.EnableServiceAnalytics(h.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	r, _ := analyticsRoute(h, "shop.example.com")
	var site, svc, rel int64
	_, _ = fmt.Sscanf(r.Tag, "%d.%d.%d", &site, &svc, &rel)
	ms := now().UnixMilli()
	h.rt.AppendSpool(
		fmt.Appendf(nil, `{"t":%d,"s":%d,"sv":%d,"rl":%d,"k":4,"p":"/cart","n":"boom","v":1,"b":"Firefox","er":{"ty":"TypeError","fr":"f (/app.js)","g":255}}`, ms-900, site, svc, rel),
		fmt.Appendf(nil, `{"t":%d,"s":%d,"sv":%d,"rl":%d,"k":4,"p":"/cart","n":"boom","v":2,"er":{"ty":"TypeError","fr":"f (/app.js)","g":255}}`, ms-800, site, svc, rel),
		fmt.Appendf(nil, `{"t":%d,"s":%d,"sv":%d,"rl":%d,"k":5,"p":"/","v":1,"vt":{"lcp":1200,"inp":-1,"cls":0,"fcp":800,"ttfb":90}}`, ms-700, site, svc, rel),
	)
	var errs apitypes.SiteErrors
	eventually(t, "the error group", func() bool {
		var err error
		errs, err = h.c.SiteErrors(h.ctx, testSystem, "shop", "shop.example.com", StatsQuery{Range: "24h"}, 50)
		return err == nil && errs.StoreOnline && len(errs.Groups) == 1
	})
	g := errs.Groups[0]
	if !errs.Collecting || g.Group != "00000000000000ff" || g.Count != 2 || g.Message != "boom" || g.FirstRelease != "web r1" {
		t.Errorf("errors = %+v", errs)
	}
	d, err := h.c.SiteError(h.ctx, testSystem, "shop", "shop.example.com", g.Group, StatsQuery{Range: "24h"})
	if err != nil || len(d.Recent) != 2 || d.Recent[0].Release != "web r1" {
		t.Errorf("error group = %+v, %v", d, err)
	}
	// Errors filter by what they record, not by a session's source.
	bad := StatsQuery{Range: "24h", Filters: []analytics.Filter{{Dimension: analytics.DimChannel, Op: analytics.OpIs, Value: "search"}}}
	if _, err := h.c.SiteErrors(h.ctx, testSystem, "shop", "shop.example.com", bad, 50); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("errors filtered by channel: %v", err)
	}
	v, err := h.c.SiteVitals(h.ctx, testSystem, "shop", "shop.example.com",
		VitalsQuery{Dimensions: []analytics.Dimension{analytics.DimPage}, Limit: 10, Metric: analytics.VitalLCP}, StatsQuery{Range: "24h"})
	if err != nil || v.All.Loads != 1 || v.All.LCP.P75 != 1200 || v.Thresholds["lcp"].Good != 2500 {
		t.Errorf("vitals = %+v, %v", v, err)
	}
	// Errors and vitals aren't page views or events.
	if st, err := h.c.SiteStats(h.ctx, testSystem, "shop", "shop.example.com", StatsQuery{Range: "24h"}); err != nil || st.Overview.Events != 0 || st.Overview.Pageviews != 0 {
		t.Errorf("dashboard counted errors or vitals: %+v, %v", st.Overview, err)
	}

	// Turning them off reaches every server's ingest.
	off := false
	if _, err := h.c.UpdateSite(h.ctx, testSystem, "shop", "shop.example.com", apitypes.UpdateSiteRequest{Settings: apitypes.SiteSettings{ErrorsAndVitals: &off}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "errors off at the ingest", func() bool { return h.rt.AnalyticsSiteRules()[site].ErrorsOff })
	if errs, err := h.c.SiteErrors(h.ctx, testSystem, "shop", "shop.example.com", StatsQuery{Range: "24h"}, 50); err != nil || errs.Collecting {
		t.Errorf("collecting after turning it off: %+v, %v", errs, err)
	}

	outsider := Actor{Name: "other", Kind: "token", Scope: apitypes.ScopeAdmin, OrgID: 2}
	if _, err := h.c.SiteErrors(h.ctx, outsider, "shop", "shop.example.com", StatsQuery{}, 50); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("another org read the errors: %v", err)
	}
}
