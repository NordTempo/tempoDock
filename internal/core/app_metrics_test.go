package core

import (
	"encoding/json/v2"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// A service that pinned nothing has Dock's default charts of the runtime
// metrics it sends; pinning is a service setting (deploy scope), saved
// whole and audited, and with tracing off there is nothing to read.
func TestAppMetricsDashboard(t *testing.T) {
	h := newHarness(t)
	ref := h.service("web", apitypes.CreateServiceRequest{})
	if _, err := h.c.EnableTraces(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	rel := h.wait(ref, h.deploy(ref, "nginx:1").Number)
	scopes, err := h.store.ListServerServiceScopes(h.ctx, h.c.localID)
	if err != nil || len(scopes) != 1 {
		t.Fatalf("services on the server = %v, %v", scopes, err)
	}
	sc := scopes[0]
	point := func(name string, kind analytics.MetricKind, v float64) []byte {
		t.Helper()
		b, err := json.Marshal(analytics.MetricPoint{Org: sc.OrgID, Project: sc.ProjectID, Env: sc.EnvironmentID, Service: sc.ID, Release: rel.ID,
			Name: name, Kind: kind, Monotonic: kind == analytics.MetricSum, Time: now().Unix(), Value: v})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	h.rt.AppendMetrics(point("v8js.memory.heap.used", analytics.MetricGauge, 148e6), point("orders.created", analytics.MetricSum, 38))
	eventually(t, "the points in the trace store", func() bool { return h.rt.Acked(node.SpoolMetrics) == 2 })

	m, err := h.c.AppMetrics(h.ctx, testSystem, ref, apitypes.AppMetrics1h)
	if err != nil {
		t.Fatal(err)
	}
	if m.Saved || len(m.Charts) != 1 || m.Charts[0].Name != "v8js.memory.heap.used" || !m.Charts[0].Dock || len(m.Charts[0].Lines) != 1 {
		t.Fatalf("defaults = %+v", m)
	}
	if len(m.Metrics) != 2 || m.StepSeconds != 60 || m.MaxSeries != 2000 {
		t.Errorf("metrics = %+v", m)
	}

	reader := Actor{Kind: "token", OrgID: 1, Name: "ro", TokenID: 1, Scope: apitypes.ScopeRead}
	pins := apitypes.SetMetricChartsRequest{Charts: []apitypes.MetricChartSpec{{Name: "orders.created"}, {Name: "v8js.memory.heap.used"}}}
	if _, err := h.c.SetMetricCharts(h.ctx, reader, ref, pins); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("a read-only token pinned charts: %v", err)
	}
	if _, err := h.c.SetMetricCharts(h.ctx, testSystem, ref, apitypes.SetMetricChartsRequest{Charts: []apitypes.MetricChartSpec{{Name: "x", Agg: "median"}}}); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("an unknown aggregation: %v", err)
	}
	if _, err := h.c.SetMetricCharts(h.ctx, testSystem, ref, pins); err != nil {
		t.Fatal(err)
	}
	m, err = h.c.AppMetrics(h.ctx, testSystem, ref, apitypes.AppMetrics1h)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Saved || len(m.Charts) != 2 || m.Charts[0].Name != "orders.created" || m.Charts[0].Dock || m.Charts[0].Shape != "counter" || m.Charts[0].Agg != "" {
		t.Errorf("pinned = %+v", m.Charts)
	}
	events, err := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Project: "shop", Environment: "production", Service: "web"})
	if err != nil || len(events) == 0 || events[0].Action != "service.charts" || !strings.Contains(events[0].Summary, "pinned orders.created") {
		t.Errorf("audit = %+v, %v", events, err)
	}

	// A level's p99 isn't pinned; one saved before that check costs its
	// own chart, not the dashboard, and unpinning it works.
	bad := apitypes.MetricChartSpec{Name: "v8js.memory.heap.used", Agg: "p99"}
	if _, err := h.c.SetMetricCharts(h.ctx, testSystem, ref, apitypes.SetMetricChartsRequest{Charts: append(pins.Charts, bad)}); CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "is a level: use sum, max, min or avg") {
		t.Errorf("a level's p99: %v", err)
	}
	raw, _ := json.Marshal(append(pins.Charts, bad))
	if err := h.store.SetMetricDashboard(h.ctx, store.SetMetricDashboardParams{ServiceID: h.serviceID(ref), Charts: string(raw), UpdatedBy: "x", UpdatedAt: now()}); err != nil {
		t.Fatal(err)
	}
	m, err = h.c.AppMetrics(h.ctx, testSystem, ref, apitypes.AppMetrics1h)
	if err != nil || len(m.Charts) != 3 || m.Charts[2].Error == "" || len(m.Charts[2].Lines) != 0 || m.Charts[1].Error != "" || len(m.Charts[1].Lines) != 1 {
		t.Fatalf("a dashboard with a bad chart = %+v, %v", m.Charts, err)
	}
	if _, err := h.c.SetMetricCharts(h.ctx, testSystem, ref, pins); err != nil {
		t.Errorf("unpinning the bad chart: %v", err)
	}

	if _, err := h.c.DisableTraces(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	if m, err := h.c.AppMetrics(h.ctx, testSystem, ref, apitypes.AppMetrics1h); err != nil || !m.TracingOff || !m.Saved {
		t.Errorf("tracing off = %+v, %v", m, err)
	}
}
