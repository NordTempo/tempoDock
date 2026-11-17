package cli

import (
	"encoding/json/v2"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/traces"
)

// A traced app's metrics list with `dock metrics`, Dock's charts first;
// `dock metrics show` reads one, and pin and unpin change the charts.
func TestMetricsCommands(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	if code, _, stderr := execStdin(t, srv.tokens[apitypes.ScopeAdmin]+"\n", "login", srv.url, "--token-stdin"); code != 0 {
		t.Fatal(stderr)
	}
	ok[apitypes.TracingStatus](t, "tracing", "on")
	ok[apitypes.Project](t, "project", "create", "shop")
	if code, _, stderr := exec(t, "deploy", "-p", "shop", "-e", "production", "-s", "web", "--image", "nginx:1"); code != 0 {
		t.Fatal(stderr)
	}
	spec, _ := srv.rt.Spec("dock_p1_e1_s1_r1")
	tok, _ := tokenOf(spec.Env["OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"])
	claims, err := traces.Parse(srv.rt.TraceKey(), tok)
	if err != nil {
		t.Fatalf("the container's token %q: %v", tok, err)
	}
	for _, p := range []analytics.MetricPoint{
		{Name: "v8js.memory.heap.used", Unit: "By", Kind: analytics.MetricGauge, Value: 148 << 20},
		{Name: "orders.created", Kind: analytics.MetricSum, Monotonic: true, Value: 38},
	} {
		p.Org, p.Project, p.Env, p.Service, p.Release, p.Time = claims.Org, claims.Project, claims.Env, claims.Service, claims.Release, time.Now().Add(-time.Minute).Unix()
		b, _ := json.Marshal(p)
		srv.rt.AppendMetrics(b)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if ts := srv.rt.TraceStore(); ts != nil && len(ts.Points()) == 2 {
			break
		} else if time.Now().After(deadline) {
			t.Fatal("the points didn't reach the trace store")
		}
	}
	at := []string{"-p", "shop", "-e", "production", "-s", "web"}
	code, stdout, stderr := exec(t, append([]string{"metrics", "--range", "1h"}, at...)...)
	if code != 0 {
		t.Fatal(stderr)
	}
	for _, want := range []string{"Dock's charts (nothing pinned yet)", "v8js.memory.heap.used", "148.0 MB", "orders.created"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("metrics lacks %q:\n%s", want, stdout)
		}
	}
	charts := ok[apitypes.MetricCharts](t, append([]string{"metrics", "pin", "orders.created"}, at...)...)
	if len(charts.Charts) != 2 || charts.Charts[1].Name != "orders.created" {
		t.Fatalf("pinned = %+v", charts)
	}
	charts = ok[apitypes.MetricCharts](t, append([]string{"metrics", "unpin", "v8js.memory.heap.used"}, at...)...)
	if len(charts.Charts) != 1 || charts.Charts[0].Name != "orders.created" {
		t.Fatalf("unpinned = %+v", charts)
	}
	one := ok[apitypes.AppMetricChartResult](t, append([]string{"metrics", "show", "orders.created", "--range", "1h"}, at...)...)
	if one.Chart.Shape != "counter" || len(one.Chart.Lines) != 1 {
		t.Errorf("show = %+v", one)
	}

	// A level's p99 isn't pinned. Pinned before its metric sent points,
	// it costs only its own chart, and unpin takes it off.
	if code, _, stderr := exec(t, append([]string{"metrics", "pin", "v8js.memory.heap.used", "--agg", "p99"}, at...)...); code == 0 || !strings.Contains(stderr, "is a level") {
		t.Errorf("pin a level's p99: exit %d\n%s", code, stderr)
	}
	if code, _, stderr := exec(t, append([]string{"metrics", "pin", "queue.depth", "--agg", "p99"}, at...)...); code != 0 || !strings.Contains(stderr, "sent no queue.depth") {
		t.Fatalf("pin a metric not sent yet: exit %d\n%s", code, stderr)
	}
	b, _ := json.Marshal(analytics.MetricPoint{Org: claims.Org, Project: claims.Project, Env: claims.Env, Service: claims.Service, Release: claims.Release,
		Name: "queue.depth", Kind: analytics.MetricGauge, Time: time.Now().Add(-time.Minute).Unix(), Value: 4})
	srv.rt.AppendMetrics(b)
	for deadline := time.Now().Add(5 * time.Second); len(srv.rt.TraceStore().Points()) != 3; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the point didn't reach the trace store")
		}
	}
	code, stdout, stderr = exec(t, append([]string{"metrics", "--range", "1h"}, at...)...)
	if code != 0 || !strings.Contains(stdout, "queue.depth p99 can't be read: queue.depth is a level") || !strings.Contains(stdout, "dock metrics unpin queue.depth --agg p99") {
		t.Fatalf("metrics with a broken chart: exit %d\n%s%s", code, stdout, stderr)
	}
	if charts = ok[apitypes.MetricCharts](t, append([]string{"metrics", "unpin", "queue.depth", "--agg", "p99"}, at...)...); len(charts.Charts) != 1 {
		t.Errorf("unpinned the broken chart = %+v", charts)
	}
}

// A split chart's latest is its lines' sum only where lines add up: a
// sum of p95s is no latency.
func TestLineStats(t *testing.T) {
	v := func(x float64) []*float64 { return []*float64{&x} }
	lines := []apitypes.AppMetricLine{{Label: "/slow", Values: v(0.499)}, {Label: "/", Values: v(0.0078)}, {Label: "/fail", Values: v(0.0078)}}
	for _, tc := range []struct {
		agg  string
		aggs []string
		want float64
	}{
		{"p95", nil, 0.499},
		{"", []string{"p95", "p50"}, 0.499},
		{"min", nil, 0.0078},
		{"", []string{"rate", "sum"}, 0.5146},
	} {
		now, _, hi := lineStats(apitypes.AppMetricChart{MetricChartSpec: apitypes.MetricChartSpec{Agg: tc.agg}, Aggs: tc.aggs, Lines: lines})
		if now == nil || math.Abs(*now-tc.want) > 1e-9 || *hi != 0.499 {
			t.Errorf("%q %v: now %v, max %v; want now %v", tc.agg, tc.aggs, now, hi, tc.want)
		}
	}
}
