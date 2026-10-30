//go:build integration

package docker

import (
	"context"
	"slices"
	"testing"

	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node"
)

// TestCloudflared runs the tunnel connector the way ADR 0036 says: on the
// host network, read-only, without capabilities, metrics on loopback, the
// token in its environment and not its command. The same token leaves it
// alone, a new one recreates it, none removes it. The token is made up:
// cloudflared runs and fails to register, which is all this needs.
func TestCloudflared(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := &Cloudflared{cli: e.cli, name: e.prefix + "-cloudflared", image: config.Default().Ingress.CloudflaredImage, owner: "/var/lib/dock-test"}
	var hostname string
	c.onHostname = func(h string) { hostname = h }
	spec := &node.TunnelSpec{Token: "eyJhIjoiMSIsInQiOiIyIiwicyI6IjMifQ==", Hostname: "6ff42ae2-765d-4adf-8112-31c55c1551ef.cfargotunnel.com"}

	if err := c.EnsureTunnel(ctx, spec); err != nil {
		t.Fatal(err)
	}
	first, err := e.cli.ContainerInspect(ctx, c.name, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctr := first.Container
	h := ctr.HostConfig
	if h.NetworkMode != "host" || !h.ReadonlyRootfs || !slices.Equal(h.CapDrop, []string{"ALL"}) || h.Memory != cloudflaredMemory {
		t.Errorf("host config = network %s, read-only %v, cap drop %v, memory %d", h.NetworkMode, h.ReadonlyRootfs, h.CapDrop, h.Memory)
	}
	if !slices.Equal(ctr.Config.Cmd, []string{"tunnel", "--metrics", "127.0.0.1:0", "run"}) || !slices.Contains(ctr.Config.Env, "TUNNEL_TOKEN="+spec.Token) {
		t.Errorf("command %v, token not in the environment %v", ctr.Config.Cmd, ctr.Config.Env)
	}
	if ctr.Config.Labels[node.LabelRole] != node.RoleCloudflared || hostname != spec.Hostname {
		t.Errorf("labels %v, hostname %q", ctr.Config.Labels, hostname)
	}

	if err := c.EnsureTunnel(ctx, spec); err != nil {
		t.Fatal(err)
	}
	same, err := e.cli.ContainerInspect(ctx, c.name, client.ContainerInspectOptions{})
	if err != nil || same.Container.ID != ctr.ID {
		t.Fatalf("the same token recreated the container: %v", err)
	}
	if err := c.EnsureTunnel(ctx, &node.TunnelSpec{Token: "eyJuZXcifQ==", Hostname: spec.Hostname}); err != nil {
		t.Fatal(err)
	}
	renewed, err := e.cli.ContainerInspect(ctx, c.name, client.ContainerInspectOptions{})
	if err != nil || renewed.Container.ID == ctr.ID {
		t.Fatalf("a new token kept the old container: %v", err)
	}

	other := &Cloudflared{cli: e.cli, name: c.name, image: c.image, owner: "/var/lib/other"}
	if err := other.EnsureTunnel(ctx, spec); err == nil {
		t.Error("another runtime's container was taken over")
	}

	if err := c.EnsureTunnel(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cli.ContainerInspect(ctx, c.name, client.ContainerInspectOptions{}); err == nil {
		t.Error("the container is still there")
	}
	if err := c.EnsureTunnel(ctx, nil); err != nil {
		t.Errorf("stopping no tunnel: %v", err)
	}
}
