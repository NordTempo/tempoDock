package core

import (
	"cmp"
	"maps"
	"net/url"
	"strconv"

	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/traces"
)

// Apps send traces with no setup: while tracing is on, every release's
// container starts with the standard OpenTelemetry variables, which stock
// SDKs read, and dock-otlp in its hosts file: its server's Caddy, which
// forwards the exports to that server's trace ingest. A variable the
// service sets itself wins, so an app that sends elsewhere keeps doing
// that; OTEL_SDK_DISABLED=true turns an app's SDK off. The variables
// change with tracing only for containers started after it: the next
// deploy or restart of each service.

// addTracing gives spec the OTEL_* variables and the dock-otlp host while
// tracing is on and the release's server lets apps reach its ingest.
func (c *Core) addTracing(spec *node.ContainerSpec, ss svcScope, rel store.Release) {
	cl, _ := c.tracesClient()
	if cl == nil {
		return
	}
	port, ok := c.otlpPort(ss.env.ServerID)
	if !ok {
		return
	}
	token := traces.Mint(traces.KeyFrom(cl.Token), traces.Claims{
		Org: ss.project.OrgID, Project: ss.project.ID, Env: ss.env.ID, Service: ss.svc.ID, Release: rel.ID,
	})
	env := maps.Clone(spec.Env)
	if env == nil {
		env = map[string]string{}
	}
	for k, v := range map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://" + node.TraceIngestHost + ":" + strconv.Itoa(port),
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
		// W3C baggage format: the value's space percent-encoded.
		"OTEL_EXPORTER_OTLP_HEADERS": "authorization=Bearer%20" + token,
		"OTEL_SERVICE_NAME":          ss.svc.Name,
		"OTEL_RESOURCE_ATTRIBUTES": "service.namespace=" + url.QueryEscape(ss.project.Name) +
			",deployment.environment.name=" + url.QueryEscape(ss.env.Name) + ",service.version=r" + strconv.FormatInt(rel.Number, 10),
		// Dock keeps no OTLP metrics or logs: logs come from stdout.
		"OTEL_METRICS_EXPORTER": "none",
		"OTEL_LOGS_EXPORTER":    "none",
	} {
		if _, set := env[k]; !set {
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
