package core

import (
	"cmp"
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
	token := traces.Mint(traces.KeyFrom(cl.Token), traces.Claims{
		Org: ss.project.OrgID, Project: ss.project.ID, Env: ss.env.ID, Service: ss.svc.ID, Release: rel.ID,
	})
	base := "http://" + node.TraceIngestHost + ":" + strconv.Itoa(port)
	// W3C baggage format: the value's space percent-encoded.
	auth := "authorization=Bearer%20" + token
	set := map[string]string{
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": base + traces.TracesPath,
		"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL": "http/protobuf",
		"OTEL_EXPORTER_OTLP_TRACES_HEADERS":  auth,
		"OTEL_SERVICE_NAME":                  ss.svc.Name,
		"OTEL_RESOURCE_ATTRIBUTES": "service.namespace=" + url.QueryEscape(ss.project.Name) +
			",deployment.environment.name=" + url.QueryEscape(ss.env.Name) + ",service.version=r" + strconv.FormatInt(rel.Number, 10),
	}
	// An SDK exports metrics and logs over OTLP by default, to localhost
	// in the container, which logs an error a minute. A service that
	// configures no OTLP of its own sends them to Dock instead, which
	// answers and drops them (the ingest keeps traces only, for now).
	for signal, path := range map[string]string{"METRICS": traces.MetricsPath, "LOGS": traces.LogsPath} {
		if !configuresOTLP(spec.Env, signal) {
			set["OTEL_EXPORTER_OTLP_"+signal+"_ENDPOINT"] = base + path
			set["OTEL_EXPORTER_OTLP_"+signal+"_PROTOCOL"] = "http/protobuf"
			set["OTEL_EXPORTER_OTLP_"+signal+"_HEADERS"] = auth
		}
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
