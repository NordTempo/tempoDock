package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/build"
	"github.com/NordTempo/tempoDock/internal/node/caddy"
	"github.com/NordTempo/tempoDock/internal/node/docker"
	"github.com/NordTempo/tempoDock/internal/node/fake"
)

// newRuntime builds the local server's node runtime from Docker, BuildKit
// and Caddy. DOCK_FAKE_RUNTIME=1 uses an in-memory fake (no Docker), for
// local development of the API, CLI and UI.
func newRuntime(cfg config.Config) (node.Runtime, error) {
	if os.Getenv("DOCK_FAKE_RUNTIME") == "1" {
		return fake.New(), nil
	}
	containers, err := docker.New()
	if err != nil {
		return nil, err
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
		builder = build.New(cli, cfg.BuildKit, filepath.Join(cfg.DataDir, "builds", "work"))
	}
	return node.Local{Containers: containers, Builder: builder, Proxy: proxy}, nil
}

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
