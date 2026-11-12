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

// An operator turns tracing on; a deployed service's container gets its
// token; spans sent with it list with `dock traces` and print as a tree
// with `dock traces show`.
func TestTracesCommands(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	if code, _, stderr := execStdin(t, srv.tokens[apitypes.ScopeAdmin]+"\n", "login", srv.url, "--token-stdin"); code != 0 {
		t.Fatal(stderr)
	}
	if st := ok[apitypes.TracingStatus](t, "tracing", "on"); !st.Enabled {
		t.Fatalf("tracing on = %+v", st)
	}
	ok[apitypes.Project](t, "project", "create", "shop")
	if code, _, stderr := exec(t, "deploy", "-p", "shop", "-e", "production", "-s", "web", "--image", "nginx:1"); code != 0 {
		t.Fatal(stderr)
	}
	spec, found := srv.rt.Spec("dock_p1_e1_s1_r1")
	tok, _ := tokenOf(spec.Env["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"])
	claims, err := traces.Parse(srv.rt.TraceKey(), tok)
	if !found || err != nil {
		t.Fatalf("the container's token %q: %v", tok, err)
	}
	start := time.Now().Add(-time.Minute).UnixMicro()
	for _, sp := range []analytics.Span{
		{ID: 1, Start: start, Duration: 1_284_000, Name: "POST /orders", Kind: analytics.SpanServer, Entry: true, Method: "POST", Route: "/orders", HTTPStatus: 500},
		{ID: 2, Parent: 1, Start: start + 72_000, Duration: 1_183_000, Name: "INSERT orders", Kind: analytics.SpanClient, Status: analytics.StatusError,
			Events: []byte(`[{"name":"exception","attrs":{"exception.type":"QueryFailedError","exception.message":"canceling statement"}}]`)},
	} {
		sp.Org, sp.Project, sp.Env, sp.Service, sp.Release, sp.Trace = claims.Org, claims.Project, claims.Env, claims.Service, claims.Release, analytics.TraceID{0: 0x4f, 15: 1}
		b, _ := json.Marshal(sp)
		srv.rt.AppendSpans(b)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if ts := srv.rt.TraceStore(); ts != nil && len(ts.Spans()) == 2 {
			break
		} else if time.Now().After(deadline) {
			t.Fatal("the spans didn't reach the trace store")
		}
	}

	list := ok[apitypes.TraceList](t, "traces", "-p", "shop", "-e", "production", "-s", "web", "--errors", "--slowest")
	if len(list.Traces) != 1 || list.Traces[0].Route != "/orders" || list.Traces[0].Release != 1 || list.Traces[0].DurationMS != 1284 {
		t.Fatalf("traces = %+v", list)
	}
	code, stdout, stderr := exec(t, "traces", "show", list.Traces[0].Trace, "-p", "shop", "-e", "production", "-s", "web")
	if code != 0 {
		t.Fatal(stderr)
	}
	for _, want := range []string{"1,284 ms", "POST /orders  web r1  ERROR 500", "  INSERT orders  web r1  ERROR", "! QueryFailedError: canceling statement", "+72 ms"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("show lacks %q:\n%s", want, stdout)
		}
	}
}

// tokenOf is the trace token in an endpoint Dock set:
// http://dock-otlp:80/t/<token>/v1/traces.
func tokenOf(endpoint string) (string, bool) {
	_, rest, ok := strings.Cut(endpoint, "/t/")
	tok, _, _ := strings.Cut(rest, "/")
	return tok, ok && tok != ""
}
