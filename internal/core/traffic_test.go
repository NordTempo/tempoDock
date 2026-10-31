package core

import (
	"fmt"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// trafficTick runs the tick's traffic step on what the local server
// drained (set directly, so the harness's own collector can't drain it
// first). Times are in the future, like the stats tests'.
func (h *harness) trafficTick(col *collector, at time.Time, hosts ...node.HostTraffic) {
	h.t.Helper()
	srv, err := h.store.GetServer(h.ctx, h.c.local.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	col.samples = []serverSample{{srv: srv, traffic: hosts}}
	h.c.trafficTick(h.ctx, col, at, at.Truncate(time.Minute).Unix()-statsMinute)
}

// Service routes are counted; the tick adds each minute's counts of a
// service's hostnames to its minute and hour points, and
// serves the range with its top paths. Unknown hostnames are ignored.
func TestTrafficTickStoresAndServes(t *testing.T) {
	h := newHarness(t)
	ref := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	for _, host := range []string{"shop.example.com", "www.shop.example.com"} {
		if _, err := h.c.AddDomain(h.ctx, testSystem, ref, apitypes.AddDomainRequest{Hostname: host}); err != nil {
			t.Fatal(err)
		}
	}
	h.deploy(ref, "app:1")
	if rs := h.rt.Routes(); len(rs) == 0 || !rs[0].Traffic {
		t.Fatalf("routes = %+v, want the service's counted", rs)
	}

	base := time.Date(2031, 1, 1, 10, 1, 0, 0, time.UTC)
	col := newCollector()
	h.trafficTick(col, base,
		node.HostTraffic{
			Host: "shop.example.com", Requests: 10, Status: [4]uint64{7, 1, 1, 1}, Bytes: 5000, Duration: 100 * time.Millisecond,
			Paths: []node.PathCount{{Method: "GET", Path: "/", Requests: 6}, {Method: "GET", Path: "/items/:id", Requests: 4}},
		},
		node.HostTraffic{Host: "www.shop.example.com", Requests: 2, Status: [4]uint64{2}, Paths: []node.PathCount{{Method: "GET", Path: "/", Requests: 2}}},
		node.HostTraffic{Host: "elsewhere.example.com", Requests: 99},
	)
	h.trafficTick(col, base.Add(time.Minute),
		node.HostTraffic{Host: "shop.example.com", Requests: 3, Status: [4]uint64{3}, Paths: []node.PathCount{{Method: "POST", Path: "/cart", Requests: 3}}},
	)

	got, err := h.c.ServiceTraffic(h.ctx, testSystem, ref, apitypes.TrafficRange24h)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Points) != 2 || got.ResolutionSeconds != 60 || !got.Points[0].Time.Equal(base.Add(-time.Minute)) {
		t.Fatalf("points = %+v", got.Points)
	}
	if p := got.Points[0]; p.Requests != 12 || p.Status2xx != 9 || p.Status5xx != 1 || p.Bytes != 5000 || p.AvgDurationMS != 100.0/12 {
		t.Errorf("first minute = %+v", p)
	}
	if tot := got.Totals; tot.Requests != 15 || tot.Status2xx != 12 {
		t.Errorf("totals = %+v; want 15 requests", tot)
	}
	want := []apitypes.TrafficPath{{Method: "GET", Path: "/", Requests: 8}, {Method: "GET", Path: "/items/:id", Requests: 4}, {Method: "POST", Path: "/cart", Requests: 3}}
	if fmt.Sprint(got.Paths) != fmt.Sprint(want) {
		t.Errorf("paths = %+v, want %+v", got.Paths, want)
	}
	if week, _ := h.c.ServiceTraffic(h.ctx, testSystem, ref, apitypes.TrafficRange7d); len(week.Points) != 1 || week.Points[0].Requests != 15 {
		t.Errorf("7d points = %+v; want the hour so far", week.Points)
	}

	other := Actor{Kind: "token", OrgID: 1, Name: "blog", Scope: apitypes.ScopeRead, ProjectID: 99, ProjectName: "blog"}
	if _, err := h.c.ServiceTraffic(h.ctx, other, ref, apitypes.TrafficRange24h); err == nil {
		t.Error("a token for another project read the service's traffic")
	}
	if _, err := h.c.ServiceTraffic(h.ctx, testSystem, ref, "1y"); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("range 1y: %v, want a validation error", err)
	}
}

// Once an hour is over its paths are trimmed to the top 100, and a month
// later everything is gone.
func TestTrafficHoursTrimAndPrune(t *testing.T) {
	h := newHarness(t)
	ref := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	if _, err := h.c.AddDomain(h.ctx, testSystem, ref, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	h.deploy(ref, "app:1")
	svc := h.serviceID(ref)

	base := time.Date(2031, 1, 1, 10, 1, 0, 0, time.UTC)
	paths := make([]node.PathCount, 150)
	for i := range paths {
		paths[i] = node.PathCount{Method: "GET", Path: fmt.Sprintf("/p%d", i), Requests: uint64(1000 - i)}
	}
	// Three drains of 50 paths (a drain carries at most DrainPaths).
	col := newCollector()
	for i := range 3 {
		h.trafficTick(col, base.Add(time.Duration(i)*time.Minute), node.HostTraffic{Host: "shop.example.com", Requests: 1, Paths: paths[50*i : 50*(i+1)]})
	}
	hour := base.Truncate(time.Hour).Unix()

	if all, _ := h.store.TopTrafficPaths(h.ctx, store.TopTrafficPathsParams{ServiceID: svc, At: hour, Limit: 1000}); len(all) != 150 {
		t.Fatalf("paths during the hour = %d, want 150", len(all))
	}
	h.trafficTick(col, base.Add(time.Hour))
	top, _ := h.store.TopTrafficPaths(h.ctx, store.TopTrafficPathsParams{ServiceID: svc, At: hour, Limit: 1000})
	if len(top) != 100 || top[0].Path != "/p0" || top[99].Path != "/p99" {
		t.Fatalf("paths after the hour = %d, want the top 100", len(top))
	}

	h.trafficTick(col, base.Add(31*24*time.Hour))
	if pts, _ := h.store.ListTrafficStats(h.ctx, store.ListTrafficStatsParams{ServiceID: svc, Res: trafficHour, At: 0}); len(pts) != 0 {
		t.Errorf("hour points after a month: %+v", pts)
	}
	if top, _ := h.store.TopTrafficPaths(h.ctx, store.TopTrafficPathsParams{ServiceID: svc, At: 0, Limit: 1}); len(top) != 0 {
		t.Errorf("paths after a month: %+v", top)
	}
}
