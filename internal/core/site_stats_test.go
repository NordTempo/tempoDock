package core

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// A dashboard is one request to the store; a store from before
// DashboardPath, which an update leaves running for a while, gets the
// separate requests instead and gives the same dashboard.
func TestSiteStatsAsksOnce(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 8080})
	h.deploy(web, "nginx:1")
	h.domain(web, "shop.example.com", "")
	if _, err := h.c.EnableHostnameAnalytics(h.ctx, testSystem, "shop.example.com", apitypes.EnableAnalyticsRequest{}); err != nil {
		t.Fatal(err)
	}
	r, _ := analyticsRoute(h, "shop.example.com")
	var siteID, svcID, relID int64
	_, _ = fmt.Sscanf(r.Tag, "%d.%d.%d", &siteID, &svcID, &relID)
	ms := now().UnixMilli()
	h.rt.AppendSpool(fmt.Appendf(nil, `{"t":%d,"s":%d,"sv":%d,"rl":%d,"k":1,"p":"/","v":1,"b":"Firefox"}`, ms-1000, siteID, svcID, relID))
	q := StatsQuery{Range: "24h"}
	var st apitypes.SiteStats
	eventually(t, "the dashboard to count the visitor", func() bool {
		var err error
		st, err = h.c.SiteStats(h.ctx, testSystem, "shop", "shop.example.com", q)
		return err == nil && st.StoreOnline && st.Overview.Visitors == 1
	})

	fs := h.rt.AnalyticsStore()
	before := fs.Calls()
	if _, err := h.c.SiteStats(h.ctx, testSystem, "shop", "shop.example.com", q); err != nil {
		t.Fatal(err)
	}
	if got := diffCalls(before, fs.Calls()); !reflect.DeepEqual(got, map[string]int{analytics.DashboardPath: 1}) {
		t.Errorf("requests for a dashboard = %v, want one to %s", got, analytics.DashboardPath)
	}

	fs.SetOld(true)
	before = fs.Calls()
	old, err := h.c.SiteStats(h.ctx, testSystem, "shop", "shop.example.com", q)
	if err != nil || !old.StoreOnline {
		t.Fatalf("from an older store: %+v, %v", old, err)
	}
	want := map[string]int{analytics.DashboardPath: 1, analytics.OverviewPath: 2, analytics.SeriesPath: 1, analytics.BreakdownPath: len(dashboardDimensions)}
	if got := diffCalls(before, fs.Calls()); !reflect.DeepEqual(got, want) {
		t.Errorf("requests to an older store = %v, want %v", got, want)
	}
	old.From, old.To = st.From, st.To // now moved on
	if !reflect.DeepEqual(old, st) {
		t.Errorf("an older store's dashboard = %+v, want %+v", old, st)
	}
}

// diffCalls is the dashboard's requests per path between two
// fakestore.Calls; the forwarder's writes don't count.
func diffCalls(before, after map[string]int) map[string]int {
	out := map[string]int{}
	for _, p := range []string{analytics.DashboardPath, analytics.OverviewPath, analytics.SeriesPath, analytics.BreakdownPath} {
		if n := after[p] - before[p]; n > 0 {
			out[p] = n
		}
	}
	return out
}
