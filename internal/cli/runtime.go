package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/build"
	"github.com/NordTempo/tempoDock/internal/node/caddy"
	"github.com/NordTempo/tempoDock/internal/node/docker"
	"github.com/NordTempo/tempoDock/internal/node/fake"
	"github.com/NordTempo/tempoDock/internal/node/stats"
)

// newRuntime builds the local server's node runtime from Docker, BuildKit
// and Caddy. DOCK_FAKE_RUNTIME=1 uses an in-memory fake (no Docker), for
// local development of the API, CLI and UI.
func newRuntime(ctx context.Context, cfg config.Config, log *slog.Logger) (node.Runtime, error) {
	if os.Getenv("DOCK_FAKE_RUNTIME") == "1" {
		return fake.New(), nil
	}
	containers, err := docker.New(cfg.Network.Pool)
	if err != nil {
		return nil, err
	}
	// Apps keep running if this fails; only their swap limit stays loose.
	if n, err := containers.UpgradeSwapLimits(ctx); err != nil {
		log.Error("upgrade container swap limits", "err", err)
	} else if n > 0 {
		log.Info("upgraded container swap limits", "containers", n)
	}
	proxy, err := caddy.New(caddyOptions(cfg))
	if err != nil {
		return nil, err
	}
	var builder node.Builder = notBuilder{}
	if cfg.BuildKit.Enabled {
		cli, err := client.New(client.FromEnv)
		if err != nil {
			return nil, fmt.Errorf("docker client: %w", err)
		}
		// BuildKit gets its own network from the pool, so the app firewall
		// covers build steps like any app.
		if err := containers.EnsureNetwork(ctx, buildkitNetwork, node.Labels{}); err != nil {
			return nil, err
		}
		b := build.New(cli, cfg.BuildKit, filepath.Join(cfg.DataDir, "builds", "work"))
		b.Network = buildkitNetwork
		builder = b
	}
	st := stats.New(stats.Options{Disks: []string{cfg.DataDir, dockerRoot()}})
	return node.Local{Containers: containers, Builder: builder, Proxy: proxy, Stats: st}, nil
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

// caddyOptions maps the daemon config onto the Caddy proxy's options.
func caddyOptions(cfg config.Config) caddy.Options {
	var ips []string
	if cfg.PublicIP != "" {
		ips = []string{cfg.PublicIP}
	}
	return caddy.Options{
		AdminSocket: cfg.Caddy.AdminSocket,
		HTTPPort:    cfg.Caddy.HTTPPort,
		HTTPSPort:   cfg.Caddy.HTTPSPort,
		ACMEEmail:   cfg.Caddy.ACMEEmail,
		DisableTLS:  cfg.Caddy.DisableTLS,
		AskURL:      "http://" + cfg.InternalListen + "/internal/caddy/ask",
		PublicIPs:   ips,
	}
}

// notBuilder is the Builder of servers with [buildkit] enabled = false.
type notBuilder struct{}

func (notBuilder) Build(context.Context, node.BuildRequest) (string, error) {
	return "", errors.New("this server doesn't build images ([buildkit] enabled = false); deploy a prebuilt image with --image instead")
}
