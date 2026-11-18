package core

import (
	"encoding/json/v2"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// releaseSpan is the IDs spans of release 1 of shop's service carry.
func releaseSpan(t *testing.T, h *harness, env, name string) analytics.Span {
	t.Helper()
	p, err := h.store.GetProjectByName(h.ctx, store.GetProjectByNameParams{OrgID: 1, Name: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	e, err := h.store.GetEnvironmentByName(h.ctx, store.GetEnvironmentByNameParams{ProjectID: p.ID, Name: env})
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

// A trace that crosses environments shows a span only where the reader
// may read logs: seeing that an environment exists is not enough.
func TestTraceSpansNeedLogsReadWhereTheyAre(t *testing.T) {
	h := newHarness(t)
	h.enforce()
	prodRef := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.wait(prodRef, h.deploy(prodRef, "app:1").Number)
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	stagingRef := h.service2("web", "staging")
	h.wait(stagingRef, h.deploy(stagingRef, "app:1").Number)
	if _, err := h.c.EnableTraces(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	admin := h.user("ada@example.com", apitypes.ScopeAdmin)
	viewer := grantedPerson(h, admin, "vw@example.com",
		apitypes.GrantRequest{Role: "deploy:view"},
		apitypes.GrantRequest{Role: "logs:view", Project: "shop", Environment: "staging"})

	prod, staging := releaseSpan(t, h, "production", "web"), releaseSpan(t, h, "staging", "web")
	start := now().Add(-time.Minute).UnixMicro()
	trace := analytics.TraceID{15: 0x42}
	prod.Trace, prod.ID, prod.Start, prod.Duration, prod.Name, prod.Kind, prod.Entry = trace, 1, start, 50_000, "POST /checkout", analytics.SpanServer, true
	prod.Attrs, _ = json.Marshal(map[string]string{"db.query.text": "SELECT 1"})
	staging.Trace, staging.ID, staging.Start, staging.Duration, staging.Name = trace, 2, start+10, 10, "call"
	spool(t, h, prod, staging)
	eventually(t, "the spans in the trace store", func() bool { ts := h.rt.TraceStore(); return ts != nil && len(ts.Spans()) == 2 })

	tr, err := h.c.GetTrace(h.ctx, viewer, stagingRef, trace.String(), time.Time{})
	if err != nil || len(tr.Spans) != 2 {
		t.Fatalf("the trace through staging = %+v, %v", tr.Spans, err)
	}
	for _, sp := range tr.Spans {
		switch sp.Span {
		case analytics.SpanID(1).String():
			if !sp.Hidden || sp.Name != "" || sp.Environment != "" || sp.Attrs != nil {
				t.Errorf("production's span, without logs.read there = %+v, want hidden", sp)
			}
		case analytics.SpanID(2).String():
			if sp.Hidden || sp.Name != "call" || sp.Environment != "staging" {
				t.Errorf("staging's span = %+v, want shown", sp)
			}
		}
	}
	// Someone with logs.read in both reads both.
	if tr, err := h.c.GetTrace(h.ctx, admin, stagingRef, trace.String(), time.Time{}); err != nil || len(tr.Spans) != 2 || tr.Spans[0].Hidden || tr.Spans[1].Hidden {
		t.Errorf("an admin's trace = %+v, %v", tr.Spans, err)
	}
}

// A span outlives its release in the trace store. Once the release is
// gone there are no values to mask it with, so a reader who gets spans
// masked gets all of that span's text masked.
func TestSpansOfGoneReleasesAreMaskedWhole(t *testing.T) {
	h := newHarness(t)
	web := tracedService(t, h, "web")
	if _, err := h.c.CreateProject(h.ctx, testSystem, apitypes.CreateProjectRequest{Name: "old"}); err != nil {
		t.Fatal(err)
	}
	oldRef := ServiceRef{"old", "production", "api"}
	if _, err := h.c.CreateService(h.ctx, testSystem, EnvRef{"old", "production"}, apitypes.CreateServiceRequest{Name: "api"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SetVariables(h.ctx, testSystem, EnvRef{"old", "production"}, apitypes.SetVariablesRequest{Service: "api", Set: map[string]string{"API_KEY": traceSecret}, Apply: true}); err != nil {
		t.Fatal(err)
	}
	h.wait(oldRef, h.deploy(oldRef, "nginx:1").Number)
	ss, err := h.c.serviceFor(h.ctx, testSystem, oldRef, apitypes.ScopeRead, "reading traces")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := h.store.GetReleaseByNumber(h.ctx, store.GetReleaseByNumberParams{ServiceID: ss.svc.ID, Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.EnableTraces(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	start := now().Add(-time.Minute).UnixMicro()
	trace := analytics.TraceID{15: 0x77}
	entry := web
	entry.Trace, entry.ID, entry.Start, entry.Duration, entry.Name, entry.Kind, entry.Entry = trace, 1, start, 1000, "GET /", analytics.SpanServer, true
	call := analytics.Span{Org: 1, Project: ss.project.ID, Env: ss.env.ID, Service: ss.svc.ID, Release: rel.ID, Trace: trace, ID: 2, Parent: 1, Start: start + 10, Duration: 10, Name: "call"}
	call.Attrs, _ = json.Marshal(map[string]string{"http.request.body": "key=" + traceSecret})
	call.Events, _ = json.Marshal([]map[string]any{{"name": "exception", "at": 5, "attrs": map[string]any{"exception.message": "bad key " + traceSecret}}})
	spool(t, h, entry, call)
	eventually(t, "the spans in the trace store", func() bool { ts := h.rt.TraceStore(); return ts != nil && len(ts.Spans()) == 2 })

	reader := Actor{Name: "reader", Kind: "token", Scope: apitypes.ScopeRead, OrgID: 1}
	ref := ServiceRef{"shop", "production", "web"}
	tr, err := h.c.GetTrace(h.ctx, reader, ref, trace.String(), time.Time{})
	if err != nil || len(tr.Spans) != 2 || tr.Spans[1].Name != "call" || tr.Spans[1].Attrs["http.request.body"] != "key="+secretMask {
		t.Fatalf("while the release is there, a reader's trace = %+v, %v", tr.Spans, err)
	}
	if _, err := h.c.ArchiveProject(h.ctx, testSystem, "old"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.DeleteProject(h.ctx, testSystem, "old", apitypes.DeleteProjectRequest{Confirm: "old"}); err != nil {
		t.Fatal(err)
	}
	// The reader, and an admin of web's environment: the span was never
	// theirs to read unmasked.
	for _, a := range []Actor{reader, testSystem} {
		tr, err = h.c.GetTrace(h.ctx, a, ref, trace.String(), time.Time{})
		if err != nil || len(tr.Spans) != 2 {
			t.Fatalf("once the release is gone, %s's trace = %+v, %v", a.Name, tr.Spans, err)
		}
		if got := tr.Spans[1]; got.Name != secretMask || got.Attrs["http.request.body"] != secretMask || len(got.Events) != 1 || got.Events[0].Attrs["exception.message"] != secretMask {
			t.Errorf("once the release is gone, %s reads its span as %+v, want its text masked whole", a.Name, got)
		}
		if got := tr.Spans[0]; got.Name != "GET /" {
			t.Errorf("web's own span = %+v", got)
		}
	}
}

// One trace read decodes a bounded amount of attributes and events: the
// spans past it come without theirs and the trace says it isn't whole.
func TestTraceReadDecodesABoundedAmount(t *testing.T) {
	h := newHarness(t)
	web := tracedService(t, h, "web")
	if _, err := h.c.EnableTraces(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	// 60 lists of 32 empty maps: 6 KB of JSON that decodes to many times
	// that.
	nested := map[string][]map[string]any{}
	for i := range 60 {
		nested[fmt.Sprintf("a%02d", i)] = make([]map[string]any, 32)
		for j := range 32 {
			nested[fmt.Sprintf("a%02d", i)][j] = map[string]any{}
		}
	}
	attrs, _ := json.Marshal(nested)
	spans := 2 * maxTraceDecode / decodedSize(attrs)
	start := now().Add(-time.Minute).UnixMicro()
	trace := analytics.TraceID{15: 0x51}
	batch := make([]analytics.Span, spans)
	for i := range batch {
		sp := web
		sp.Trace, sp.ID, sp.Start, sp.Duration, sp.Name, sp.Attrs = trace, analytics.SpanID(i+1), start+int64(i), 10, "s", attrs
		batch[i] = sp
	}
	spool(t, h, batch...)
	eventually(t, "the spans in the trace store", func() bool { ts := h.rt.TraceStore(); return ts != nil && len(ts.Spans()) == spans })

	ref := ServiceRef{"shop", "production", "web"}
	tr, err := h.c.GetTrace(h.ctx, testSystem, ref, trace.String(), time.UnixMicro(start))
	if err != nil || len(tr.Spans) != spans || !tr.Truncated {
		t.Fatalf("trace: %d spans, truncated %v, %v", len(tr.Spans), tr.Truncated, err)
	}
	decoded := 0
	for _, sp := range tr.Spans {
		if sp.Attrs != nil {
			decoded += decodedSize(attrs)
		}
	}
	if decoded > maxTraceDecode || decoded < maxTraceDecode-decodedSize(attrs) {
		t.Errorf("decoded about %d bytes of attributes, want just under %d", decoded, maxTraceDecode)
	}
	// A small trace is whole.
	small := web
	small.Trace, small.ID, small.Start, small.Duration, small.Name, small.Attrs = analytics.TraceID{15: 0x52}, 1, start, 10, "s", attrs
	spool(t, h, small)
	eventually(t, "the small trace in the store", func() bool { return len(h.rt.TraceStore().Spans()) == spans+1 })
	if tr, err := h.c.GetTrace(h.ctx, testSystem, ref, small.Trace.String(), time.UnixMicro(start)); err != nil || tr.Truncated || tr.Spans[0].Attrs == nil {
		t.Errorf("a small trace = %+v, %v", tr, err)
	}
}

// Masking a trace costs a bounded amount of searching, whatever the
// service's values are: past it, what is left is masked whole.
func TestTraceMaskingIsBounded(t *testing.T) {
	// 20,000 values and strings of 1 KB: the work is gone within 220.
	var big strings.Builder
	for i := range 20_000 {
		fmt.Fprintf(&big, "%08d\n", i)
	}
	env := map[string]string{"BIG": big.String(), "API_KEY": traceSecret}
	m := newTraceMask(newRedactor(env))
	text := strings.Repeat("x", 1000) + " key=" + traceSecret
	if got := m.mask(text); got != strings.Repeat("x", 1000)+" key="+secretMask {
		t.Fatalf("with work left: %q", got)
	}
	whole := 0
	for range 400 {
		switch got := m.mask(text); {
		case got == secretMask:
			whole++
		case strings.Contains(got, traceSecret):
			t.Fatalf("a secret value shows: %q", got)
		}
	}
	if whole < 150 {
		t.Errorf("%d of 400 strings masked whole, want those past the read's work", whole)
	}
	// A string of nothing but values pays for each search of it.
	m = newTraceMask(newRedactor(env))
	if got := m.mask(strings.Repeat("00000001", 4000)); got != secretMask {
		t.Errorf("a string of 4,000 values = %d bytes, want it masked whole", len(got))
	}
	// A service's usual few values never come near the bound.
	m = newTraceMask(newRedactor(map[string]string{"API_KEY": traceSecret}))
	for range 10_000 {
		if got := m.mask(text); got == secretMask {
			t.Fatal("a small service's strings ran out of work")
		}
	}
}
