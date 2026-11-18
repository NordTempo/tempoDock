package core

import (
	"encoding/json/v2"
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
