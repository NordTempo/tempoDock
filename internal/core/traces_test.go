package core

import (
	"encoding/json/v2"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// waitTraceStore waits until the trace store started on server and
// answers.
func waitTraceStore(t *testing.T, c *Core, server string) {
	t.Helper()
	eventually(t, "the trace store started on "+server, func() bool {
		st, err := c.TracingStatus(c.ctx, testSystem)
		return err == nil && st.Enabled && st.Server == server && st.StoreRunning && !st.Starting
	})
}

func TestTracingRunsTheTraceStore(t *testing.T) {
	h := newHarness(t)
	admin := Actor{Name: "org-admin", Kind: "token", Scope: apitypes.ScopeAdmin, OrgID: 1}
	if _, err := h.c.EnableTraces(h.ctx, admin); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("an org admin turned tracing on: %v", err)
	}
	if st, err := h.c.TracingStatus(h.ctx, testSystem); err != nil || st.Enabled || st.RetentionDays != 7 {
		t.Errorf("status before = %+v, %v", st, err)
	}
	if _, err := h.c.EnableTraces(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	waitTraceStore(t, h.c, h.c.localName())
	spec := h.rt.TraceStoreSpec()
	if spec.Kind != node.StoreTraces || spec.MemoryBytes != 384<<20 || len(spec.Token) < 32 {
		t.Errorf("trace store spec = %+v", spec)
	}
	// Tracing doesn't turn analytics on.
	if h.rt.AnalyticsStoreSpec() != nil {
		t.Error("the analytics store started too")
	}
	if st, _ := h.c.AnalyticsStatus(h.ctx, testSystem); st.Enabled {
		t.Errorf("analytics status = %+v", st)
	}

	// A restart resumes it; the next day drops the days past retention.
	h.restart()
	waitTraceStore(t, h.c, h.c.localName())
	day := now().AddDate(0, 0, 1)
	h.c.rotateSalts(h.ctx, day)
	eventually(t, "the daily prune", func() bool {
		ts := h.rt.TraceStore()
		return ts != nil && len(ts.Prunes()) == 1
	})
	if before := time.UnixMilli(h.rt.TraceStore().Prunes()[0]); before.After(now().AddDate(0, 0, -6)) || before.Before(now().AddDate(0, 0, -8)) {
		t.Errorf("pruned before %s, want about 7 days ago", before)
	}

	// Off: the trace store goes with its spans.
	if st, err := h.c.DisableTraces(h.ctx, testSystem); err != nil || st.Enabled || st.StoreRunning {
		t.Fatalf("disable = %+v, %v", st, err)
	}
	if h.rt.TraceStoreSpec() != nil {
		t.Error("the trace store still runs")
	}
	events, err := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Platform: true, Action: "tracing.disable"})
	if err != nil || len(events) != 1 {
		t.Errorf("audit = %+v, %v", events, err)
	}
	h.restart()
	if st, _ := h.c.TracingStatus(h.ctx, testSystem); st.Enabled || h.rt.TraceStoreSpec() != nil {
		t.Errorf("after a restart, tracing is %+v", st)
	}
}

// The trace store runs on the analytics server and follows it, empty:
// spans are a week of diagnostics, not worth a copy.
func TestTraceStoreFollowsTheAnalyticsServer(t *testing.T) {
	p, lab := analyticsPool(t)
	cp := p.rt
	if _, err := p.c.EnableTraces(p.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	waitTraceStore(t, p.c, p.c.localName())
	// Choosing the analytics server while only tracing is on: the trace
	// store moves there, and analytics starts on it.
	if _, err := p.c.SetAnalyticsServer(p.ctx, testSystem, apitypes.SetAnalyticsServerRequest{Server: "homelab"}); err != nil {
		t.Fatal(err)
	}
	waitTraceStore(t, p.c, "homelab")
	eventually(t, "the old trace store removed", func() bool { return cp.TraceStoreSpec() == nil })
	if lab.TraceStoreSpec() == nil || lab.AnalyticsStoreSpec() == nil {
		t.Errorf("homelab runs trace store %+v, analytics store %+v", lab.TraceStoreSpec(), lab.AnalyticsStoreSpec())
	}

	// A move of the analytics store takes the trace store along.
	waitStoreStarted(t, p.c, "homelab")
	if _, err := p.c.SetAnalyticsServer(p.ctx, testSystem, apitypes.SetAnalyticsServerRequest{}); err != nil {
		t.Fatal(err)
	}
	waitStoreStarted(t, p.c, p.c.localName())
	waitTraceStore(t, p.c, p.c.localName())
	eventually(t, "the trace store off homelab", func() bool { return lab.TraceStoreSpec() == nil })
}

// spanLine is a spooled span of environment env, as the trace ingest writes
// it.
func spanLine(t *testing.T, env int64, id byte) []byte {
	t.Helper()
	b, err := json.Marshal(analytics.Span{Org: 1, Project: 1, Env: env, Service: 1, Release: 1, Trace: analytics.TraceID{15: id}, ID: analytics.SpanID(id), Start: now().UnixMicro(), Name: "GET /"})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// While tracing is on every server's ingest has the trace key, and its
// spans spool goes to the trace store, only for the environments on that
// server; off, the key goes.
func TestSpansReachTheTraceStore(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{})
	envs, err := h.store.ListServerEnvironmentIDs(h.ctx, h.c.localID)
	if err != nil || len(envs) != 1 {
		t.Fatalf("environments on the server = %v, %v", envs, err)
	}
	// Spans wait in the spool before tracing is even on.
	h.rt.AppendSpans(spanLine(t, envs[0], 1), spanLine(t, envs[0]+100, 2))
	if _, err := h.c.EnableTraces(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the trace key on the server", func() bool { return len(h.rt.TraceKey()) == 32 })
	eventually(t, "the placed span in the trace store", func() bool {
		ts := h.rt.TraceStore()
		return ts != nil && len(ts.Spans()) == 1 && h.rt.Acked(node.SpoolSpans) == 2
	})
	if sp := h.rt.TraceStore().Spans()[0]; sp.Env != envs[0] || sp.ID != 1 {
		t.Errorf("the trace store has %+v", sp)
	}
	// Live: an append reaches it without anything polling; a restart
	// resumes from the trace store's cursor.
	h.rt.AppendSpans(spanLine(t, envs[0], 3))
	eventually(t, "a live span", func() bool { return len(h.rt.TraceStore().Spans()) == 2 })
	h.restart()
	h.rt.AppendSpans(spanLine(t, envs[0], 4))
	eventually(t, "a span after a restart, nothing twice", func() bool {
		ts := h.rt.TraceStore()
		return ts != nil && len(ts.Spans()) == 3
	})

	if _, err := h.c.DisableTraces(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the key gone from the server", func() bool { return h.rt.TraceKey() == nil })
}
