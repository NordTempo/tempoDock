package core

import (
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// traceSecret is a secret value JSON escapes, a quote and a backslash in
// it, so masking must work on decoded values.
const traceSecret = `sk-live-"0123\\456789`

// tracedService deploys shop/production/<name> with a secret variable and
// returns the IDs its spans carry: org, project, environment, service and
// release 1.
func tracedService(t *testing.T, h *harness, name string) analytics.Span {
	t.Helper()
	ref := h.service(name, apitypes.CreateServiceRequest{})
	if _, err := h.c.SetVariables(h.ctx, testSystem, EnvRef{"shop", "production"}, apitypes.SetVariablesRequest{Service: name, Set: map[string]string{"API_KEY": traceSecret}, Apply: true}); err != nil {
		t.Fatal(err)
	}
	h.wait(ref, h.deploy(ref, "nginx:1").Number)
	p, err := h.store.GetProjectByName(h.ctx, store.GetProjectByNameParams{OrgID: 1, Name: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	e, err := h.store.GetEnvironmentByName(h.ctx, store.GetEnvironmentByNameParams{ProjectID: p.ID, Name: "production"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := h.store.GetServiceByName(h.ctx, store.GetServiceByNameParams{EnvironmentID: e.ID, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	r, err := h.store.GetReleaseByNumber(h.ctx, store.GetReleaseByNumberParams{ServiceID: s.ID, Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	return analytics.Span{Org: 1, Project: p.ID, Env: e.ID, Service: s.ID, Release: r.ID}
}

// spool appends spans to the server's spans spool, as the ingest does.
func spool(t *testing.T, h *harness, spans ...analytics.Span) {
	t.Helper()
	for _, sp := range spans {
		b, err := json.Marshal(sp)
		if err != nil {
			t.Fatal(err)
		}
		h.rt.AppendSpans(b)
	}
}

func TestReadingTraces(t *testing.T) {
	h := newHarness(t)
	ref := ServiceRef{"shop", "production", "web"}
	if _, err := h.c.ListTraces(h.ctx, testSystem, ref, TraceFilter{}); CodeOf(err) != apitypes.CodeNotFound && CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("before anything: %v", err)
	}
	web := tracedService(t, h, "web")
	api := tracedService(t, h, "api")
	if _, err := h.c.ListTraces(h.ctx, testSystem, ref, TraceFilter{}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("tracing off: %v", err)
	}
	if _, err := h.c.EnableTraces(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	start := now().Add(-time.Minute).UnixMicro()
	// web's request calls api, which fails with the secret in its message.
	entry := web
	entry.Trace, entry.ID, entry.Start, entry.Duration, entry.Name, entry.Kind, entry.Entry = analytics.TraceID{15: 1}, 1, start, 300_000, "POST /orders", analytics.SpanServer, true
	entry.Method, entry.Route, entry.HTTPStatus = "POST", "/orders", 500
	call := api
	call.Trace, call.ID, call.Parent, call.Start, call.Duration, call.Name, call.Kind, call.Entry, call.Status = entry.Trace, 2, 1, start+1000, 200_000, "INSERT orders", analytics.SpanServer, true, analytics.StatusError
	call.Attrs, _ = json.Marshal(map[string]string{"db.statement": "INSERT ... key=" + traceSecret})
	call.Events, _ = json.Marshal([]map[string]any{{"name": "exception", "at": 150000, "attrs": map[string]any{"exception.type": "QueryFailedError", "exception.message": "bad key " + traceSecret}}})
	quick := web
	quick.Trace, quick.ID, quick.Start, quick.Duration, quick.Name, quick.Kind, quick.Entry = analytics.TraceID{15: 2}, 3, start+5000, 2_000, "GET /", analytics.SpanServer, true
	spool(t, h, entry, call, quick)
	eventually(t, "the spans in the trace store", func() bool { ts := h.rt.TraceStore(); return ts != nil && len(ts.Spans()) == 3 })

	list, err := h.c.ListTraces(h.ctx, testSystem, ref, TraceFilter{Slowest: true})
	if err != nil || len(list.Traces) != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if first := list.Traces[0]; first.Name != "POST /orders" || first.Release != 1 || first.DurationMS != 300 || !first.Error || first.Kind != "server" {
		t.Errorf("slowest first = %+v", first)
	}
	if l, _ := h.c.ListTraces(h.ctx, testSystem, ref, TraceFilter{Errors: true}); len(l.Traces) != 1 {
		t.Errorf("errors only: %+v", l.Traces)
	}
	if l, _ := h.c.ListTraces(h.ctx, testSystem, ref, TraceFilter{SinceRelease: 1, SlowerThan: 100 * time.Millisecond}); len(l.Traces) != 1 {
		t.Errorf("since release 1, slower than 100 ms: %+v", l.Traces)
	}
	if _, err := h.c.ListTraces(h.ctx, testSystem, ref, TraceFilter{Range: "1y"}); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("a year: %v", err)
	}

	// An admin sees the trace as stored; a reader who can't reveal secret
	// values gets them masked, as in logs.
	id := entry.Trace.String()
	tr, err := h.c.GetTrace(h.ctx, testSystem, ref, id, time.UnixMicro(start))
	if err != nil || len(tr.Spans) != 2 || tr.DurationMS != 300 {
		t.Fatalf("trace = %+v, %v", tr, err)
	}
	if sp := tr.Spans[1]; sp.Service != "api" || sp.Parent != tr.Spans[0].Span || sp.Status != "error" || sp.Attrs["db.statement"] != "INSERT ... key="+traceSecret ||
		len(sp.Events) != 1 || sp.Events[0].AtMS != 150 {
		t.Errorf("api's span = %+v", sp)
	}
	reader := Actor{Name: "reader", Kind: "token", Scope: apitypes.ScopeRead, OrgID: 1}
	tr, err = h.c.GetTrace(h.ctx, reader, ref, id, time.Time{})
	if err != nil || tr.Spans[1].Attrs["db.statement"] != "INSERT ... key=*****" || tr.Spans[1].Events[0].Attrs["exception.message"] != "bad key *****" {
		t.Errorf("a reader's trace = %+v, %v", tr.Spans, err)
	}
	// A token limited to another environment sees neither the service
	// nor, through another, its spans.
	other := Actor{Name: "limited", Kind: "token", Scope: apitypes.ScopeRead, OrgID: 1, ProjectID: web.Project, EnvironmentID: web.Env + 1000}
	if _, err := h.c.GetTrace(h.ctx, other, ref, id, time.Time{}); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("a token of another environment: %v", err)
	}
	// Through a service that isn't in it, the trace isn't there.
	if _, err := h.c.GetTrace(h.ctx, testSystem, ServiceRef{"shop", "production", "api"}, quick.Trace.String(), time.Time{}); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("another service's trace: %v", err)
	}
	if _, err := h.c.GetTrace(h.ctx, testSystem, ref, "nope", time.Time{}); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("a bad ID: %v", err)
	}
}

// A trace of big spans opens: the trace store answers a page at a time,
// so 600 spans of 15 KB, 9 MB in all, never meet its 8 MB answer cap.
func TestBigTracesOpen(t *testing.T) {
	h := newHarness(t)
	web := tracedService(t, h, "web")
	if _, err := h.c.EnableTraces(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	attrs, _ := json.Marshal(map[string]string{"db.statement": strings.Repeat("x", 15<<10)})
	start := now().Add(-time.Minute).UnixMicro()
	spans := make([]analytics.Span, 600)
	for i := range spans {
		sp := web
		sp.Trace, sp.ID, sp.Start, sp.Duration, sp.Name, sp.Attrs = analytics.TraceID{15: 9}, analytics.SpanID(i+1), start+int64(i), 10, "SELECT", attrs
		sp.Entry = i == 0
		spans[i] = sp
	}
	spool(t, h, spans...)
	eventually(t, "the spans in the trace store", func() bool { ts := h.rt.TraceStore(); return ts != nil && len(ts.Spans()) == 600 })
	tr, err := h.c.GetTrace(h.ctx, testSystem, ServiceRef{"shop", "production", "web"}, spans[0].Trace.String(), time.UnixMicro(start))
	if err != nil || len(tr.Spans) != 600 || tr.Truncated || tr.Spans[599].Span != analytics.SpanID(600).String() {
		t.Fatalf("trace: %d spans, truncated %v, %v", len(tr.Spans), tr.Truncated, err)
	}
}
