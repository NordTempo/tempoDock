//go:build !dockcli

package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/analytics/ingest"
	"github.com/NordTempo/tempoDock/internal/analytics/spool"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/build"
	"github.com/NordTempo/tempoDock/internal/node/caddy"
	"github.com/NordTempo/tempoDock/internal/node/collect"
	"github.com/NordTempo/tempoDock/internal/node/docker"
	"github.com/NordTempo/tempoDock/internal/node/fake"
	"github.com/NordTempo/tempoDock/internal/node/probe"
	"github.com/NordTempo/tempoDock/internal/node/stats"
	"github.com/NordTempo/tempoDock/internal/node/traffic"
	"github.com/NordTempo/tempoDock/internal/node/uptime"
	"github.com/NordTempo/tempoDock/internal/traces"
)

// newRuntime builds this server's node runtime from Docker, BuildKit and
// Caddy, for `dock serve` and `dock node`. marker is the server's Caddy
// marker (remote.ServerMarker of its key). The proxy is returned too: a node answers Caddy's
// `ask` from it. So is the analytics ingest (ADR 0037), which the caller
// serves on [analytics] ingest_listen. It installs dock-probe under the
// data dir for the containers' health checks. DOCK_FAKE_RUNTIME=1 uses an
// in-memory fake (no Docker, nil proxy and ingest), for local development
// of the API, CLI and UI; DOCK_FAKE_DIAL=host:port sends its database
// connections there (a local Postgres, MySQL or Redis), so the Data panel
// works too.
func newRuntime(ctx context.Context, cfg config.Config, marker string, log *slog.Logger) (node.Runtime, *caddy.Proxy, ingests, error) {
	if os.Getenv("DOCK_FAKE_RUNTIME") == "1" {
		rt := fake.New()
		rt.PassHealthAfter(time.Second) // health checks pass, as on Docker
		if addr := os.Getenv("DOCK_FAKE_DIAL"); addr != "" {
			rt.DialTo(addr)
		}
		return rt, nil, ingests{}, nil
	}
	containers, err := docker.New(cfg.Network.Pool)
	if err != nil {
		return nil, nil, ingests{}, err
	}
	containers.DataDir = cfg.DataDir
	// Without the probe apps run as before, only without ongoing health
	// checks (ADR 0043).
	if containers.Probe, err = probe.Install(cfg.DataDir); err != nil {
		log.Warn("ongoing health checks are off", "err", err)
	}
	// Without dock-sync, environments can't move to or from this server.
	if containers.Sync, err = probe.InstallSync(cfg.DataDir); err != nil {
		log.Warn("environments can't move to or from this server", "err", err)
	}
	// Apps keep running if this fails; only their swap limit stays loose.
	if n, err := containers.UpgradeSwapLimits(ctx); err != nil {
		log.Error("upgrade container swap limits", "err", err)
	} else if n > 0 {
		log.Info("upgraded container swap limits", "containers", n)
	}
	// A daemon that died during a job left its container running (a
	// pre-deploy command, `dock run`), or during a snapshot (ADR 0047) the
	// service it copied paused; Dock never pauses anything beyond a job.
	if n, err := containers.UnpauseStranded(ctx); err != nil {
		log.Error("unpause containers a cut-short job left paused", "err", err)
	} else if n > 0 {
		log.Warn("unpaused containers a cut-short job left paused", "containers", n)
	}
	opts := caddyOptions(cfg, marker)
	var counter node.Traffic = noTraffic{}
	var lines *traffic.Counter
	if opts.TrafficAddr != "" {
		// Apps are served either way; only their traffic goes uncounted.
		if c, err := traffic.Listen(ctx, opts.TrafficAddr, log); err != nil {
			log.Error("traffic statistics are off", "traffic_listen", opts.TrafficAddr, "err", err)
			opts.TrafficAddr = ""
		} else {
			counter, lines = c, c
		}
	}
	if opts.UpstreamsDir != "" {
		// Apps are served either way; switches reload Caddy without it.
		if err := caddy.CheckUpstreamsDir(opts.UpstreamsDir); err != nil {
			log.Warn("traffic switches reload Caddy: the upstreams directory can't be used", "upstreams_dir", opts.UpstreamsDir, "err", err)
			opts.UpstreamsDir = ""
		}
	}
	proxy, err := caddy.New(opts)
	if err != nil {
		return nil, nil, ingests{}, err
	}
	owner, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil, nil, ingests{}, err
	}
	var builder node.Builder = notBuilder{}
	if cfg.BuildKit.Enabled {
		cli, err := client.New(client.FromEnv)
		if err != nil {
			return nil, nil, ingests{}, fmt.Errorf("docker client: %w", err)
		}
		// BuildKit gets its own network from the pool, so the app firewall
		// covers build steps like any app.
		if err := containers.EnsureNetwork(ctx, buildkitNetwork, node.Labels{}); err != nil {
			return nil, nil, ingests{}, err
		}
		b := build.New(cli, cfg.BuildKit, filepath.Join(cfg.DataDir, "builds", "work"))
		b.Network = buildkitNetwork
		// One dock runtime per Docker host: the BuildKit container is
		// this data dir's, and another runtime's is refused.
		b.Owner = owner
		builder = b
		// The vulnerability database downloads on the build network,
		// which the app firewall covers (ADR 0074).
		containers.ScanNetwork = buildkitNetwork
	}
	coll, in, err := newCollector(cfg, docker.NewAnalyticsStore(containers, cfg.Analytics.StorePort, owner), log)
	if err != nil {
		return nil, nil, ingests{}, err
	}
	coll.Traces = docker.NewTraceStore(containers, cfg.Traces.StorePort, owner)
	if lines != nil {
		// Traced routes' requests become their traces' edge spans (ADR
		// 0075), from the access log lines traffic already reads.
		edge := &traces.Edge{In: in.traces}
		lines.OnEdge(func(e traffic.Edge) {
			if tok := proxy.TraceToken(e.Host); tok != "" {
				edge.Request(traces.EdgeRequest{Token: tok, Method: e.Method, Route: e.Route, Status: e.Status,
					End: e.End, Duration: e.Duration, TraceID: e.TraceID, SpanID: e.SpanID})
			}
		})
	}
	st := stats.New(stats.Options{Disks: []string{dockerRoot(), cfg.DataDir}, Container: containers.MemoryOwner})
	tunnel := docker.NewCloudflared(containers, cfg.Ingress.CloudflaredImage, owner, proxy.SetTunnelHostname)
	return node.Local{Containers: containers, Builder: builder, Proxy: proxy, Stats: st, Traffic: counter, Tunnel: tunnel, Analytics: coll, Prober: uptime.New(false), Scanner: containers}, proxy, in, nil
}

// ingests are a server's ingests: Dock Analytics' events and the trace
// ingest's spans. Both nil on the fake runtime.
type ingests struct {
	analytics *ingest.Ingest
	traces    *traces.Ingest
}

// ingestServers are the ingests' listeners: the analytics ingest on
// [analytics] ingest_listen and the trace ingest on [traces]
// ingest_listen, each left out when it or its setting is empty. Their own
// listeners: Caddy forwards public requests to the first and containers'
// to the second, so nothing else may answer on them.
func ingestServers(cfg config.Config, in ingests) []*http.Server {
	var out []*http.Server
	if in.analytics != nil && cfg.Analytics.IngestListen != "" {
		out = append(out, &http.Server{
			Addr: cfg.Analytics.IngestListen, Handler: in.analytics,
			ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second,
			MaxHeaderBytes: 16 << 10,
		})
	}
	if in.traces != nil && cfg.Traces.IngestListen != "" {
		out = append(out, &http.Server{
			Addr: cfg.Traces.IngestListen, Handler: in.traces,
			ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 10 * time.Second,
			MaxHeaderBytes: 16 << 10,
		})
	}
	return out
}

// newCollector opens this server's analytics spools and salts (ADR 0037)
// and its spans spool under the data dir, for the runtime and the ingests
// that fill them.
func newCollector(cfg config.Config, store collect.Store, log *slog.Logger) (*collect.Collector, ingests, error) {
	dir := filepath.Join(cfg.DataDir, "analytics")
	sp, err := spool.Open(filepath.Join(dir, "spool"), spool.Options{MaxBytes: int64(cfg.Analytics.SpoolMaxMB) << 20})
	if err != nil {
		return nil, ingests{}, fmt.Errorf("analytics spool: %w", err)
	}
	// Replays have their own spool, a quarter of the size: a long outage
	// fills it first and costs replays, never page views.
	replay, err := spool.Open(filepath.Join(dir, "replay"), spool.Options{MaxBytes: int64(cfg.Analytics.SpoolMaxMB) << 20 / 4, MaxLine: analytics.MaxReplayLine})
	if err != nil {
		return nil, ingests{}, fmt.Errorf("analytics replay spool: %w", err)
	}
	salts, err := ingest.OpenSalts(filepath.Join(dir, "salts.json"))
	if err != nil {
		return nil, ingests{}, fmt.Errorf("analytics salts: %w", err)
	}
	rules, err := ingest.OpenRules(filepath.Join(dir, "rules.json"))
	if err != nil {
		return nil, ingests{}, fmt.Errorf("analytics site rules: %w", err)
	}
	// Spans have their own spool and cap (tracing over OTLP): a flood of
	// spans can never push out page views.
	spans, err := spool.Open(filepath.Join(cfg.DataDir, "traces", "spool"), spool.Options{MaxBytes: int64(cfg.Traces.SpoolMaxMB) << 20, MaxLine: analytics.MaxSpanBytes})
	if err != nil {
		return nil, ingests{}, fmt.Errorf("spans spool: %w", err)
	}
	// App metrics' points too, so a cardinality bomb can't push out spans.
	metrics, err := spool.Open(filepath.Join(cfg.DataDir, "traces", "metrics"), spool.Options{MaxBytes: int64(cfg.Traces.MetricsSpoolMaxMB) << 20, MaxLine: analytics.MaxMetricLine})
	if err != nil {
		return nil, ingests{}, fmt.Errorf("metrics spool: %w", err)
	}
	key := &traces.Key{}
	return &collect.Collector{Spool: sp, Replay: replay, Spans: spans, Metrics: metrics, TraceKey: key, Salts: salts, Rules: rules, Store: store},
		ingests{
			analytics: &ingest.Ingest{Spool: sp, Replay: replay, Salts: salts, Rules: rules, Log: log.With("component", "analytics")},
			traces:    &traces.Ingest{Spool: spans, Metrics: metrics, Key: key, Log: log.With("component", "traces")},
		}, nil
}

// dockerRoot asks Docker once where it keeps images, volumes and logs, for
// disk statistics. The usual location is the fallback.
func dockerRoot() string {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return "/var/lib/docker"
	}
	defer func() { _ = cli.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := cli.Info(ctx, client.InfoOptions{})
	if err != nil || info.Info.DockerRootDir == "" {
		return "/var/lib/docker"
	}
	return info.Info.DockerRootDir
}

// buildkitNetwork is the dock network the BuildKit container runs on.
const buildkitNetwork = "dock_buildkit"

// askPath is where Caddy's on-demand TLS check is served on the internal
// listener: by core on the control plane, by the proxy itself on a node.
const askPath = "/internal/caddy/ask"

// accessPath is where the access check for login-protected routes
// (caddy.AccessHandler, ADR 0030) is served on the internal listener, on
// the control plane and on nodes alike.
const accessPath = "/internal/caddy/access"

// wakePath is where the wake check of asleep environments' routes
// (caddy.WakeHandler, ADR 0070) is served on the internal listener, on
// the control plane and on nodes alike.
const wakePath = "/internal/caddy/wake"

// unjoinedMarker stands in for a server's marker where Caddy needs one
// before the server knows its own: `dock admin caddy-config` on a tunnel
// node that hasn't joined yet. The daemon replaces the whole config with
// the real marker when it starts.
const unjoinedMarker = "dock-unjoined"

// caddyOptions maps the config onto the Caddy proxy's options. marker is
// the server's Dock-Server header value (remote.ServerMarker); empty means not
// known yet.
func caddyOptions(cfg config.Config, marker string) caddy.Options {
	if marker == "" && cfg.Ingress.Mode == config.IngressCloudflareTunnel {
		marker = unjoinedMarker
	}
	return caddy.Options{
		AdminSocket:    cfg.Caddy.AdminSocket,
		Ingress:        caddy.Ingress(cfg.Ingress.Mode),
		HTTPPort:       cfg.Caddy.HTTPPort,
		HTTPSPort:      cfg.Caddy.HTTPSPort,
		TunnelPort:     cfg.Caddy.TunnelPort,
		ACMEEmail:      cfg.Caddy.ACMEEmail,
		DisableTLS:     cfg.Caddy.DisableTLS,
		AskURL:         "http://" + cfg.InternalListen + askPath,
		AccessURL:      "http://" + cfg.InternalListen + accessPath,
		WakeURL:        "http://" + cfg.InternalListen + wakePath,
		PublicIPs:      publicIPs(cfg),
		ServerMarker:   marker,
		TunnelHostname: cfg.Ingress.TunnelHostname,
		TrafficAddr:    cfg.Caddy.TrafficListen,
		AnalyticsAddr:  cfg.Analytics.IngestListen,
		// Traced apps' exports reach the trace ingest through Caddy.
		TraceIngestAddr: cfg.Traces.IngestListen,
		UpstreamsDir:    cfg.Caddy.UpstreamsDir,
	}
}

// noTraffic is the counter of a server whose traffic_listen is off or
// taken: it never counts anything.
type noTraffic struct{}

func (noTraffic) DrainTraffic(context.Context) ([]node.HostTraffic, error) { return nil, nil }

// publicIPs are the addresses DNS should point at: public_ip on a direct
// server, none behind a tunnel.
func publicIPs(cfg config.Config) []string {
	if cfg.PublicIP == "" || cfg.Ingress.Mode == config.IngressCloudflareTunnel {
		return nil
	}
	return []string{cfg.PublicIP}
}

// notBuilder is the Builder of servers with [buildkit] enabled = false.
type notBuilder struct{}

func (notBuilder) Build(context.Context, node.BuildRequest) (node.Built, error) {
	return node.Built{}, errors.New("this server doesn't build images ([buildkit] enabled = false); deploy a prebuilt image with --image instead")
}

// PruneBuildCache implements node.Builder: there is no build cache.
func (notBuilder) PruneBuildCache(context.Context, int64) (node.PruneResult, error) {
	return node.PruneResult{}, nil
}
