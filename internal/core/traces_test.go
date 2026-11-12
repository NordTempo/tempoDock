package core

import (
	"bytes"
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/traces"
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
	if spec.Kind != node.StoreTraces || spec.MemoryBytes != 512<<20 || len(spec.Token) < 32 {
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

// spanLine is a spooled span of the service in sc, as the trace ingest
// writes it.
func spanLine(t *testing.T, sc analytics.Span, id byte) []byte {
	t.Helper()
	sc.Trace, sc.ID, sc.Start, sc.Name = analytics.TraceID{15: id}, analytics.SpanID(id), now().UnixMicro(), "GET /"
	b, err := json.Marshal(sc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// While tracing is on every server's ingest has the trace key, and its
// spans spool goes to the trace store, only for spans naming a service on
// that server with its own environment, project, org and release; off,
// the key goes.
func TestSpansReachTheTraceStore(t *testing.T) {
	h := newHarness(t)
	ref := h.service("web", apitypes.CreateServiceRequest{})
	rel := h.wait(ref, h.deploy(ref, "nginx:1").Number)
	scopes, err := h.store.ListServerServiceScopes(h.ctx, h.c.localID)
	if err != nil || len(scopes) != 1 {
		t.Fatalf("services on the server = %v, %v", scopes, err)
	}
	sc := scopes[0]
	web := analytics.Span{Org: sc.OrgID, Project: sc.ProjectID, Env: sc.EnvironmentID, Service: sc.ID, Release: rel.ID}
	// Spans wait in the spool before tracing is even on. A server's spool
	// can't speak for another environment, org, service or release.
	otherEnv, otherOrg, otherService, otherRelease := web, web, web, web
	otherEnv.Env += 100
	otherOrg.Org += 100
	otherService.Service += 100
	otherRelease.Release += 100
	h.rt.AppendSpans(spanLine(t, web, 1), spanLine(t, otherEnv, 2), spanLine(t, otherOrg, 5), spanLine(t, otherService, 6), spanLine(t, otherRelease, 7), []byte("not a span"))
	if _, err := h.c.EnableTraces(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the trace key on the server", func() bool { return len(h.rt.TraceKey()) == 32 })
	eventually(t, "the placed span in the trace store", func() bool {
		ts := h.rt.TraceStore()
		return ts != nil && len(ts.Spans()) == 1 && h.rt.Acked(node.SpoolSpans) == 6
	})
	if sp := h.rt.TraceStore().Spans()[0]; sp.Service != sc.ID || sp.ID != 1 {
		t.Errorf("the trace store has %+v", sp)
	}
	// Live: an append reaches it without anything polling; a restart
	// resumes from the trace store's cursor.
	h.rt.AppendSpans(spanLine(t, web, 3))
	eventually(t, "a live span", func() bool { return len(h.rt.TraceStore().Spans()) == 2 })
	h.restart()
	h.rt.AppendSpans(spanLine(t, web, 4))
	eventually(t, "a span after a restart, nothing twice", func() bool {
		ts := h.rt.TraceStore()
		return ts != nil && len(ts.Spans()) == 3
	})

	if _, err := h.c.DisableTraces(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the key gone from the server", func() bool { return h.rt.TraceKey() == nil })
}

// While tracing is on a release's container starts with the standard
// OpenTelemetry variables, per signal so Dock's token only goes to Dock,
// and dock-otlp in its hosts file; the service's own variables win, one
// that sends traces elsewhere gets none (and the list says so), and with
// tracing off it gets none.
func TestTracedAppsGetTheVariables(t *testing.T) {
	h := newHarness(t)
	ref := h.service("web", apitypes.CreateServiceRequest{})
	if _, err := h.c.SetVariables(h.ctx, testSystem, EnvRef{"shop", "production"}, apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"OTEL_SERVICE_NAME": "storefront", "OTEL_METRICS_EXPORTER": "prometheus"}, Apply: true}); err != nil {
		t.Fatal(err)
	}
	h.wait(ref, h.deploy(ref, "nginx:1").Number)
	spec, _ := h.rt.Spec(h.ctr(ref, 1))
	if _, set := spec.Env["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"]; set || len(spec.ExtraHosts) > 0 {
		t.Fatalf("tracing off, the container got %v, %v", spec.Env, spec.ExtraHosts)
	}

	if _, err := h.c.EnableTraces(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	rel := h.wait(ref, h.deploy(ref, "nginx:1").Number)
	spec, _ = h.rt.Spec(h.ctr(ref, rel.Number))
	for k, want := range map[string]string{
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://dock-otlp:80/v1/traces", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL": "http/protobuf",
		"OTEL_SERVICE_NAME":                "storefront",
		"OTEL_RESOURCE_ATTRIBUTES":         "service.namespace=shop,deployment.environment.name=production,service.version=r2",
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT": "http://dock-otlp:80/v1/logs",
		// The service chose its metrics exporter: they're its own.
		"OTEL_METRICS_EXPORTER": "prometheus", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT": "",
		// Never the global ones, which would reach any other endpoint.
		"OTEL_EXPORTER_OTLP_ENDPOINT": "", "OTEL_EXPORTER_OTLP_HEADERS": "", "OTEL_LOGS_EXPORTER": "",
	} {
		if spec.Env[k] != want {
			t.Errorf("%s = %q, want %q", k, spec.Env[k], want)
		}
	}
	if len(spec.ExtraHosts) != 1 || spec.ExtraHosts[0] != "dock-otlp:host-gateway" {
		t.Fatalf("traced container: hosts %v", spec.ExtraHosts)
	}
	// The token names this release, under the key the servers have.
	tok, ok := strings.CutPrefix(spec.Env["OTEL_EXPORTER_OTLP_TRACES_HEADERS"], "authorization=Bearer%20")
	if !ok {
		t.Fatalf("headers = %q", spec.Env["OTEL_EXPORTER_OTLP_TRACES_HEADERS"])
	}
	eventually(t, "the trace key on the server", func() bool { return len(h.rt.TraceKey()) > 0 })
	claims, err := traces.Parse(h.rt.TraceKey(), tok)
	if err != nil || claims.Service == 0 || claims.Env == 0 || claims.Org != 1 {
		t.Fatalf("token claims = %+v, %v", claims, err)
	}
	if r, err := h.store.GetReleaseByNumber(h.ctx, store.GetReleaseByNumberParams{ServiceID: claims.Service, Number: rel.Number}); err != nil || r.ID != claims.Release {
		t.Errorf("the token's release is %d, the release's ID %d (%v)", claims.Release, r.ID, err)
	}

	// A service that sends its traces elsewhere gets none of Dock's
	// variables, and its trace list says why it has no traces.
	if _, err := h.c.SetVariables(h.ctx, testSystem, EnvRef{"shop", "production"}, apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "https://otlp.vendor.example"}, Apply: true}); err != nil {
		t.Fatal(err)
	}
	rel = h.wait(ref, rel.Number+1)
	spec, _ = h.rt.Spec(h.ctr(ref, rel.Number))
	for k := range spec.Env {
		if strings.HasPrefix(k, "OTEL_EXPORTER_OTLP_") && k != "OTEL_EXPORTER_OTLP_ENDPOINT" {
			t.Errorf("exporting elsewhere, the container got %s", k)
		}
	}
	if l, err := h.c.ListTraces(h.ctx, testSystem, ServiceRef{"shop", "production", "web"}, TraceFilter{}); err != nil || l.ExportsElsewhere != "OTEL_EXPORTER_OTLP_ENDPOINT" {
		t.Errorf("list = %+v, %v", l, err)
	}
}

// While tracing is on a service's route starts traces at the edge, with
// a token for the service and its live release; one that sends its traces
// elsewhere isn't traced at the edge either (its backend would get spans
// with a parent it never sees), and tracing off stops it.
func TestRoutesTraceAtTheEdge(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1", Port: 8080})
	rel := h.wait(web, h.deploy(web, "app:1").Number)
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	if r := h.routeOf("shop.example.com"); r.Trace != "" {
		t.Fatalf("tracing off, the route traces: %q", r.Trace)
	}
	if _, err := h.c.EnableTraces(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the trace key on the server", func() bool { return len(h.rt.TraceKey()) > 0 })
	claims, err := traces.Parse(h.rt.TraceKey(), h.routeOf("shop.example.com").Trace)
	if err != nil || claims.Org != 1 || claims.Service == 0 {
		t.Fatalf("route token claims = %+v, %v", claims, err)
	}
	if r, err := h.store.GetReleaseByNumber(h.ctx, store.GetReleaseByNumberParams{ServiceID: claims.Service, Number: rel.Number}); err != nil || r.ID != claims.Release {
		t.Errorf("the token's release is %d, the live one %d (%v)", claims.Release, r.ID, err)
	}

	if _, err := h.c.SetVariables(h.ctx, testSystem, EnvRef{"shop", "production"}, apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "https://otlp.vendor.example"}, Apply: true}); err != nil {
		t.Fatal(err)
	}
	h.wait(web, rel.Number+1)
	if r := h.routeOf("shop.example.com"); r.Trace != "" {
		t.Errorf("exporting elsewhere, the route traces: %q", r.Trace)
	}
	if _, err := h.c.SetVariables(h.ctx, testSystem, EnvRef{"shop", "production"}, apitypes.SetVariablesRequest{Service: "web", Unset: []string{"OTEL_EXPORTER_OTLP_ENDPOINT"}, Apply: true}); err != nil {
		t.Fatal(err)
	}
	h.wait(web, rel.Number+2)
	if h.routeOf("shop.example.com").Trace == "" {
		t.Fatal("back to Dock, the route isn't traced")
	}
	if _, err := h.c.DisableTraces(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	if r := h.routeOf("shop.example.com"); r.Trace != "" {
		t.Errorf("tracing off, the route traces: %q", r.Trace)
	}
}

// Rotating the trace key revokes every ingest token: the servers get a
// new key that refuses the old tokens, routes get new edge tokens, and
// new containers get new tokens. Operators only; an archived service's
// spans are dropped too.
func TestRotateTraceKeyRevokesTokens(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1", Port: 8080})
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.RotateTraceKey(h.ctx, testSystem); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("tracing off: %v", err)
	}
	if _, err := h.c.EnableTraces(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	rel := h.wait(web, h.deploy(web, "app:1").Number)
	eventually(t, "the trace key on the server", func() bool { return len(h.rt.TraceKey()) > 0 })
	oldKey := h.rt.TraceKey()
	spec, _ := h.rt.Spec(h.ctr(web, rel.Number))
	oldTok, _ := strings.CutPrefix(spec.Env["OTEL_EXPORTER_OTLP_TRACES_HEADERS"], "authorization=Bearer%20")
	oldEdge := h.routeOf("shop.example.com").Trace

	member := Actor{Kind: "token", OrgID: 1, Name: "ci", Scope: apitypes.ScopeAdmin}
	if _, err := h.c.RotateTraceKey(h.ctx, member); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("an org admin token: %v", err)
	}
	st, err := h.c.RotateTraceKey(h.ctx, testSystem)
	if err != nil || st.KeyRotatedAt == nil {
		t.Fatalf("rotate = %+v, %v", st, err)
	}
	eventually(t, "the new key on the server", func() bool { return !bytes.Equal(h.rt.TraceKey(), oldKey) })
	if _, err := traces.Parse(h.rt.TraceKey(), oldTok); err == nil {
		t.Error("the new key takes a token minted before")
	}
	if e := h.routeOf("shop.example.com").Trace; e == oldEdge || e == "" {
		t.Errorf("edge token %q after rotating, was %q", e, oldEdge)
	}
	rel = h.wait(web, h.deploy(web, "app:1").Number)
	spec, _ = h.rt.Spec(h.ctr(web, rel.Number))
	tok, _ := strings.CutPrefix(spec.Env["OTEL_EXPORTER_OTLP_TRACES_HEADERS"], "authorization=Bearer%20")
	if _, err := traces.Parse(h.rt.TraceKey(), tok); err != nil {
		t.Errorf("a new container's token: %v", err)
	}
	// A restart keeps the epoch.
	h.restart()
	if _, err := traces.Parse(h.c.traceKey(), tok); err != nil {
		t.Errorf("after a restart, the key refuses the new token: %v", err)
	}

	// An archived service's spans are dropped.
	scopes, err := h.store.ListServerServiceScopes(h.ctx, h.c.localID)
	if err != nil || len(scopes) != 1 {
		t.Fatalf("scopes = %v, %v", scopes, err)
	}
	if _, err := h.c.ArchiveService(h.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	if scopes, _ := h.store.ListServerServiceScopes(h.ctx, h.c.localID); len(scopes) != 0 {
		t.Errorf("an archived service still takes spans: %v", scopes)
	}
}
