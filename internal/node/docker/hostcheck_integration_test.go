//go:build integration

package docker

import (
	"context"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/hostcheck"
)

// The host checks see a container Dock didn't start: its name, the
// Docker socket it mounts and the port it publishes, and say so.
func TestHostContainers(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, time.Minute)
	port := network.MustParsePort("8080/tcp")
	name := e.name("portainer")
	created, err := e.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:   name,
		Config: &container.Config{Image: shellImage, Cmd: []string{"sleep", "60"}, ExposedPorts: network.PortSet{port: {}}},
		HostConfig: &container.HostConfig{
			Binds:        []string{"/var/run/docker.sock:/var/run/docker.sock:ro"},
			PortBindings: network.PortMap{port: {{HostIP: netip.MustParseAddr("127.0.0.1")}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	list, err := e.HostContainers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(list, func(c hostcheck.Container) bool { return c.Name == name })
	if i < 0 {
		t.Fatalf("%s not listed in %+v", name, list)
	}
	c := list[i]
	if c.Dock || !slices.Contains(c.Mounts, "/var/run/docker.sock") || len(c.Ports) != 1 || !c.Ports[0].Addr().IsLoopback() {
		t.Fatalf("listed as %+v", c)
	}

	res, err := hostcheck.Checker{Root: "/", Containers: e.HostContainers, Apt: func(context.Context) ([]byte, error) { return nil, nil }}.CheckHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	j := slices.IndexFunc(res, func(h node.HostCheck) bool { return h.Key() == "docker_socket:"+name })
	if j < 0 || res[j].Status != node.HostFinding || !strings.Contains(res[j].Detail, "Dock didn't start it") {
		t.Fatalf("no docker_socket finding for %s: %+v", name, res)
	}
}
