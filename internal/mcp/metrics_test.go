package mcp_test

import (
	"context"
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/traces"
)

// "Is web's memory growing?" is one metrics call: Dock's heap chart's
// latest, lowest and highest; with name, one metric.
func TestMetricTools(t *testing.T) {
	f := newFixture(t)
	s := f.connect("shop-agent", nil)
	if got := call(t, s, "metrics", map[string]any{"service": "web"}); !strings.Contains(got.summary, "Tracing is off") {
		t.Fatalf("tracing off = %q", got.summary)
	}
	if _, err := f.core.EnableTraces(context.Background(), sys); err != nil {
		t.Fatal(err)
	}
	call(t, s, "deploy_image", map[string]any{"service": "web", "image": "nginx:1"})
	var claims traces.Claims
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if spec, ok := f.rt.Spec("dock_p1_e3_s1_r1"); ok {
			tok, _ := tokenOf(spec.Env["OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"])
			var err error
			if claims, err = traces.Parse(f.rt.TraceKey(), tok); err == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("no traced container")
		}
	}
	now := time.Now()
	for i, mb := range []float64{120, 160, 212} {
		b, _ := json.Marshal(analytics.MetricPoint{
			Org: claims.Org, Project: claims.Project, Env: claims.Env, Service: claims.Service, Release: claims.Release,
			Name: "v8js.memory.heap.used", Unit: "By", Kind: analytics.MetricGauge, Time: now.Add(time.Duration(i-3) * time.Minute).Unix(), Value: mb * (1 << 20),
		})
		f.rt.AppendMetrics(b)
	}
	var got called
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		got = call(t, s, "metrics", map[string]any{"service": "web"})
		if strings.Contains(got.summary, "212 MB now") || time.Now().After(deadline) {
			break
		}
	}
	if !strings.Contains(got.summary, "v8js.memory.heap.used sum 212 MB now (min 120 MB, max 212 MB at") || !strings.Contains(got.summary, "1 metrics sent") {
		t.Fatalf("metrics = %q", got.summary)
	}
	one := call(t, s, "metrics", map[string]any{"service": "web", "name": "v8js.memory.heap.used", "agg": "max"})
	if !strings.Contains(one.summary, "v8js.memory.heap.used max 212 MB now") {
		t.Fatalf("metrics with name = %q", one.summary)
	}
}
