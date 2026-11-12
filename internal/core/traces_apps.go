package core

import (
	"cmp"
	"context"
	"maps"
	"net/url"
	"strconv"
	"strings"

	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/traces"
)

// Apps send traces with no setup: while tracing is on, every release's
// container starts with the standard OpenTelemetry variables, which stock
// SDKs read, and dock-otlp in its hosts file: its server's Caddy, which
// forwards the exports to that server's trace ingest. The variables are
// per signal (OTEL_EXPORTER_OTLP_TRACES_*), so Dock's token only ever goes
// to Dock's endpoint, and Dock sets only what the service hasn't: one
// that sends traces elsewhere itself (exportsElsewhere) gets nothing, and
// metrics and logs are left alone once the service configures OTLP at
// all. The variables change with tracing only for containers started
// after it: the next deploy or restart of each service.

// exportsElsewhere is the variable a service set that sends its traces
// somewhere else, or turns them off, and "" when Dock may trace it.
func exportsElsewhere(env map[string]string) string {
	for _, k := range []string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_TRACES_EXPORTER", "OTEL_SDK_DISABLED"} {
		if _, ok := env[k]; ok {
			return k
		}
	}
	return ""
}

// configuresOTLP reports whether a service set any OTLP exporter
// variable of its own, or chose a signal's exporter.
func configuresOTLP(env map[string]string, signal string) bool {
	for k := range env {
		if strings.HasPrefix(k, "OTEL_EXPORTER_OTLP_") || k == "OTEL_"+signal+"_EXPORTER" {
			return true
		}
	}
	return false
}

// addTracing gives spec the OTEL_* variables and the dock-otlp host while
// tracing is on, the release's server lets apps reach its ingest and the
// service doesn't send its traces elsewhere.
func (c *Core) addTracing(spec *node.ContainerSpec, ss svcScope, rel store.Release) {
	cl, _ := c.tracesClient()
	if cl == nil || exportsElsewhere(spec.Env) != "" {
		return
	}
	port, ok := c.otlpPort(ss.env.ServerID)
	if !ok {
		return
	}
	token := traces.Mint(c.traceKey(), traces.Claims{
		Org: ss.project.OrgID, Project: ss.project.ID, Env: ss.env.ID, Service: ss.svc.ID, Release: rel.ID,
	})
	// The token is in the endpoint's path, not a header: an SDK merges
	// OTEL_EXPORTER_OTLP_*_HEADERS into exporters configured in code, and
	// would send it to the app's own backend.
	base := "http://" + node.TraceIngestHost + ":" + strconv.Itoa(port) + traces.TokenPathPrefix + token
	set := map[string]string{
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": base + traces.TracesPath,
		"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL": "http/protobuf",
		"OTEL_SERVICE_NAME":                  ss.svc.Name,
		"OTEL_RESOURCE_ATTRIBUTES": "service.namespace=" + url.QueryEscape(ss.project.Name) +
			",deployment.environment.name=" + url.QueryEscape(ss.env.Name) + ",service.version=r" + strconv.FormatInt(rel.Number, 10),
	}
	// An SDK exports metrics and logs over OTLP by default, to localhost
	// in the container, which logs an error a minute. A service that
	// configures no OTLP of its own sends them to Dock instead, which
	// keeps metrics (as deltas, so the ingest needs no state per series)
	// and answers and drops logs (Dock keeps stdout).
	for signal, path := range map[string]string{"METRICS": traces.MetricsPath, "LOGS": traces.LogsPath} {
		if !configuresOTLP(spec.Env, signal) {
			set["OTEL_EXPORTER_OTLP_"+signal+"_ENDPOINT"] = base + path
			set["OTEL_EXPORTER_OTLP_"+signal+"_PROTOCOL"] = "http/protobuf"
		}
	}
	if _, ok := set["OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"]; ok {
		set["OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE"] = "delta"
	}
	env := maps.Clone(spec.Env)
	if env == nil {
		env = map[string]string{}
	}
	for k, v := range set {
		if _, own := env[k]; !own {
			env[k] = v
		}
	}
	spec.Env = env
	spec.ExtraHosts = append(spec.ExtraHosts, node.TraceIngestHost+":host-gateway")
}

// otlpPort is the port apps on serverID reach its Caddy on for dock-otlp,
// and whether they can: a direct server (in tunnel mode Caddy listens only
// on loopback) on a protocol with the route, with its trace ingest on for
// the control plane's own server.
func (c *Core) otlpPort(serverID int64) (int, bool) {
	if serverID == c.localID {
		return c.cfg.Caddy.HTTPPort, c.cfg.Ingress.Mode != config.IngressCloudflareTunnel && c.cfg.Traces.IngestListen != ""
	}
	if c.nodes == nil {
		return 0, false
	}
	h := c.nodes.State(serverID).Hello
	if h.Protocol < remote.TraceAppsProtocol || h.Ingress != remote.IngressDirect {
		return 0, false
	}
	return cmp.Or(h.HTTPPort, 80), true
}

// edgeTraced is the trace token of each routed service whose requests are
// traced from the edge (node.Route.Trace): every service with a live
// release (the active one, or the one override switches to) while tracing
// is on, but those whose release sends its traces elsewhere, whose own
// backend would get spans with a parent it never sees, and those a server
// forwards for another (traced there).
func (c *Core) edgeTraced(ctx context.Context, rows []store.ListRoutableDomainsRow, forwarded []store.ListForwardedDomainsRow, override map[int64]routeOverride) (map[int64]string, error) {
	out := map[int64]string{}
	if cl, _ := c.tracesClient(); cl == nil {
		return out, nil
	}
	skip := map[int64]bool{}
	for _, f := range forwarded {
		skip[f.ServiceID] = true
	}
	live := func(d store.ListRoutableDomainsRow) int64 {
		if o, ok := override[d.ServiceID]; ok {
			return o.release
		}
		return d.ReleaseID
	}
	var ids []int64
	for _, d := range rows {
		if live(d) != 0 && !skip[d.ServiceID] {
			ids = append(ids, live(d))
		}
	}
	rels, err := c.releasesByID(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, d := range rows {
		rel, ok := rels[live(d)]
		if !ok || skip[d.ServiceID] || out[d.ServiceID] != "" {
			continue
		}
		cfg, err := c.openConfig(rel)
		if err != nil {
			// Its routes go out untraced; every other service's still do.
			c.log.Error("traces: read a release's settings; its domains aren't traced", "service", d.ServiceID, "release", rel.Number, "err", err)
			continue
		}
		if exportsElsewhere(cfg.Env) == "" {
			out[d.ServiceID] = c.edgeToken(d, rel.ID)
		}
	}
	return out, nil
}

// edgeToken is the trace token edge spans of d's service and release
// carry, or "" with tracing off.
func (c *Core) edgeToken(d store.ListRoutableDomainsRow, release int64) string {
	key := c.traceKey()
	if key == nil {
		return ""
	}
	return traces.Mint(key, traces.Claims{
		Org: d.OrgID, Project: d.ProjectID, Env: d.EnvironmentID, Service: d.ServiceID, Release: release,
	})
}
