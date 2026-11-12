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

// "The slowest traces since release 1" is one traces_list call, and
// trace_get says where the time went and what failed.
func TestTraceTools(t *testing.T) {
	f := newFixture(t)
	if _, err := f.core.EnableTraces(context.Background(), sys); err != nil {
		t.Fatal(err)
	}
	s := f.connect("shop-agent", nil)
	call(t, s, "deploy_image", map[string]any{"service": "web", "image": "nginx:1"})
	// The release's container carries its token; the spans come with it.
	var claims traces.Claims
	deadline := time.Now().Add(5 * time.Second)
	for {
		if spec, ok := f.rt.Spec("dock_p1_e3_s1_r1"); ok {
			tok, _ := tokenOf(spec.Env["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"])
			var err error
			if claims, err = traces.Parse(f.rt.TraceKey(), tok); err == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("no traced container")
		}
		time.Sleep(20 * time.Millisecond)
	}
	start := time.Now().Add(-time.Minute).UnixMicro()
	for _, sp := range []analytics.Span{
		{ID: 1, Start: start, Duration: 900_000, Name: "POST /orders", Kind: analytics.SpanServer, Entry: true, Method: "POST", Route: "/orders", HTTPStatus: 500, Status: analytics.StatusError, Kept: analytics.KeptError},
		{ID: 2, Parent: 1, Start: start + 1000, Duration: 850_000, Name: "INSERT orders", Kind: analytics.SpanClient, Status: analytics.StatusError,
			Events: []byte(`[{"name":"exception","attrs":{"exception.type":"QueryFailedError","exception.message":"statement timeout"}}]`)},
	} {
		sp.Org, sp.Project, sp.Env, sp.Service, sp.Release, sp.Trace = claims.Org, claims.Project, claims.Env, claims.Service, claims.Release, analytics.TraceID{15: 7}
		b, _ := json.Marshal(sp)
		f.rt.AppendSpans(b)
	}
	var res called
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		res = call(t, s, "traces_list", map[string]any{"service": "web", "since_release": 1, "slowest": true})
		if strings.Contains(res.summary, "1 traces") || time.Now().After(deadline) {
			break
		}
	}
	if !strings.Contains(res.summary, "POST /orders 900 ms (r1, error)") {
		t.Fatalf("traces_list = %q", res.summary)
	}
	got := call(t, s, "trace_get", map[string]any{"service": "web", "trace": analytics.TraceID{15: 7}.String()})
	if !strings.Contains(got.summary, "Slowest call: INSERT orders in web (850 ms)") || !strings.Contains(got.summary, "Failed: INSERT orders in web r1: QueryFailedError statement timeout") {
		t.Fatalf("trace_get = %q", got.summary)
	}
}

// tokenOf is the trace token in an endpoint Dock set:
// http://dock-otlp:80/t/<token>/v1/traces.
func tokenOf(endpoint string) (string, bool) {
	_, rest, ok := strings.Cut(endpoint, "/t/")
	tok, _, _ := strings.Cut(rest, "/")
	return tok, ok && tok != ""
}
