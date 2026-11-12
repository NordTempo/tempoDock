package cli

import (
	"encoding/json/v2"
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
}
