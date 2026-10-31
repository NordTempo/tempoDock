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

	"github.com/NordTempo/tempoDock/internal/analytics/ingest"
	"github.com/NordTempo/tempoDock/internal/analytics/spool"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/build"
	"github.com/NordTempo/tempoDock/internal/node/caddy"
	"github.com/NordTempo/tempoDock/internal/node/collect"
	"github.com/NordTempo/tempoDock/internal/node/docker"
	"github.com/NordTempo/tempoDock/internal/node/fake"
	"github.com/NordTempo/tempoDock/internal/node/stats"
	"github.com/NordTempo/tempoDock/internal/node/traffic"
)

// newRuntime builds this server's node runtime from Docker, BuildKit and
// Caddy, for `dock serve` and `dock node`. marker is the server's Caddy
// marker (remote.ServerMarker of its key). The proxy is returned too: a node answers Caddy's
// `ask` from it. So is the analytics ingest (ADR 0037), which the caller
// serves on [analytics] ingest_listen. DOCK_FAKE_RUNTIME=1 uses an
// in-memory fake (no Docker, nil proxy and ingest), for local development
// of the API, CLI and UI.
func newRuntime(ctx context.Context, cfg config.Config, marker string, log *slog.Logger) (node.Runtime, *caddy.Proxy, *ingest.Ingest, error) {
	if os.Getenv("DOCK_FAKE_RUNTIME") == "1" {
		return fake.New(), nil, nil, nil
	}
	containers, err := docker.New(cfg.Network.Pool)
	if err != nil {
		return nil, nil, nil, err
	}
	containers.DataDir = cfg.DataDir
	// Apps keep running if this fails; only their swap limit stays loose.
	if n, err := containers.UpgradeSwapLimits(ctx); err != nil {
		log.Error("upgrade container swap limits", "err", err)
	} else if n > 0 {
		log.Info("upgraded container swap limits", "containers", n)
	}
	opts := caddyOptions(cfg, marker)
	var counter node.Traffic = noTraffic{}
	if opts.TrafficAddr != "" {
		// Apps are served either way; only their traffic goes uncounted.
		if c, err := traffic.Listen(ctx, opts.TrafficAddr, log); err != nil {
			log.Error("traffic statistics are off", "traffic_listen", opts.TrafficAddr, "err", err)
			opts.TrafficAddr = ""
		} else {
			counter = c
		}
	}
	proxy, err := caddy.New(opts)
	if err != nil {
		return nil, nil, nil, err
	}
	owner, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil, nil, nil, err
	}
	var builder node.Builder = notBuilder{}
	if cfg.BuildKit.Enabled {
		cli, err := client.New(client.FromEnv)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("docker client: %w", err)
		}
		// BuildKit gets its own network from the pool, so the app firewall
		// covers build steps like any app.
		if err := containers.EnsureNetwork(ctx, buildkitNetwork, node.Labels{}); err != nil {
			return nil, nil, nil, err
		}
		b := build.New(cli, cfg.BuildKit, filepath.Join(cfg.DataDir, "builds", "work"))
		b.Network = buildkitNetwork
		// One dock runtime per Docker host: the BuildKit container is
		// this data dir's, and another runtime's is refused.
		b.Owner = owner
		builder = b
	}
	coll, in, err := newCollector(cfg, docker.NewAnalyticsStore(containers, cfg.Analytics.StorePort, owner), log)
	if err != nil {
		return nil, nil, nil, err
	}
	st := stats.New(stats.Options{Disks: []string{dockerRoot(), cfg.DataDir}, Container: containers.MemoryOwner})
	tunnel := docker.NewCloudflared(containers, cfg.Ingress.CloudflaredImage, owner, proxy.SetTunnelHostname)
	return node.Local{Containers: containers, Builder: builder, Proxy: proxy, Stats: st, Traffic: counter, Tunnel: tunnel, Analytics: coll}, proxy, in, nil
}

// ingestServer serves the analytics ingest on [analytics] ingest_listen,
// or is nil when there is none (the fake runtime, or the setting empty).
// Its own listener: Caddy forwards public requests here, so nothing else
// may answer on it.
func ingestServer(cfg config.Config, in *ingest.Ingest) *http.Server {
	if in == nil || cfg.Analytics.IngestListen == "" {
		return nil
	}
	return &http.Server{
		Addr: cfg.Analytics.IngestListen, Handler: in,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second,
		MaxHeaderBytes: 16 << 10,
	}
}

// newCollector opens this server's analytics spool and salts under the
// data dir (ADR 0037), for the runtime and the ingest that fills them.
func newCollector(cfg config.Config, store collect.Store, log *slog.Logger) (*collect.Collector, *ingest.Ingest, error) {
	dir := filepath.Join(cfg.DataDir, "analytics")
	sp, err := spool.Open(filepath.Join(dir, "spool"), spool.Options{MaxBytes: int64(cfg.Analytics.SpoolMaxMB) << 20})
	if err != nil {
		return nil, nil, fmt.Errorf("analytics spool: %w", err)
	}
	salts, err := ingest.OpenSalts(filepath.Join(dir, "salts.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("analytics salts: %w", err)
	}
	return &collect.Collector{Spool: sp, Salts: salts, Store: store},
		&ingest.Ingest{Spool: sp, Salts: salts, Log: log.With("component", "analytics")}, nil
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
		PublicIPs:      publicIPs(cfg),
		ServerMarker:   marker,
		TunnelHostname: cfg.Ingress.TunnelHostname,
		TrafficAddr:    cfg.Caddy.TrafficListen,
		AnalyticsAddr:  cfg.Analytics.IngestListen,
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

func (notBuilder) Build(context.Context, node.BuildRequest) (string, error) {
	return "", errors.New("this server doesn't build images ([buildkit] enabled = false); deploy a prebuilt image with --image instead")
}

// PruneBuildCache implements node.Builder: there is no build cache.
func (notBuilder) PruneBuildCache(context.Context, int64) (node.PruneResult, error) {
	return node.PruneResult{}, nil
}
