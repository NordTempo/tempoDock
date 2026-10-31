package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
)

const (
	// AnalyticsContainer runs the analytics store (ADR 0037) on the
	// analytics server.
	AnalyticsContainer = "dock-analytics"
	// AnalyticsVolume holds its database. It is the one volume the
	// runtime removes: with the store, once it moved to another server
	// (Remove).
	AnalyticsVolume = "dock-analytics-data"
	// analyticsPort is the store's port inside the container.
	analyticsPort = 7076
	// analyticsHome is where the volume is mounted: the base image's
	// nonroot home, so a new volume is created owned by that user.
	analyticsHome = "/home/nonroot"
	// labelAnalytics holds a hash of what the container runs with, so a
	// new binary, token or limit recreates it and the same ones don't.
	labelAnalytics = "dock.analytics.config"
	// analyticsStop is how long the store gets to checkpoint and close
	// its database when it is replaced.
	analyticsStop = 30
)

// AnalyticsStore runs the dock-analytics container: the release's binary
// mounted read-only into node.AnalyticsBaseImage, published on one
// loopback port of the host, with its database on AnalyticsVolume.
type AnalyticsStore struct {
	cli *client.Client
	// name and volume are the container's and its volume's names,
	// AnalyticsContainer and AnalyticsVolume (tests change them).
	name, volume string
	// port is the host's loopback port the store is published on
	// ([analytics] store_port).
	port int
	// owner is the data dir of the dock runtime it belongs to
	// (node.LabelOwner): one runtime per Docker host.
	owner string
	// mu makes Ensure one at a time, so two callers can't both create
	// the container or remove each other's.
	mu sync.Mutex
}

// NewAnalyticsStore returns the analytics store of the Docker host r uses.
func NewAnalyticsStore(r *Runtime, port int, owner string) *AnalyticsStore {
	return &AnalyticsStore{cli: r.cli, name: AnalyticsContainer, volume: AnalyticsVolume, port: port, owner: owner}
}

// Ensure runs the container as spec says. The store gets the memory
// limit's three eighths as DuckDB's memory limit and trim mark (192 MB of
// 512 MB): DuckDB holds memory past its own limit (ADR 0037).
func (a *AnalyticsStore) Ensure(ctx context.Context, spec node.AnalyticsStoreSpec) error {
	if !filepath.IsAbs(spec.Binary) || len(spec.SHA256) != 64 || len(spec.Token) < 32 || spec.MemoryBytes < 128<<20 {
		return fmt.Errorf("the analytics store needs its binary's path and SHA-256, a token of 32 characters or more and at least 128 MB of memory")
	}
	if _, err := os.Stat(spec.Binary); err != nil {
		return fmt.Errorf("the analytics store's binary: %w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	inspect, exists, err := a.inspect(ctx)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%s\x00%s\x00%s\x00%d\x00%d", node.AnalyticsBaseImage, spec.Binary, spec.SHA256, spec.Token, spec.MemoryBytes, a.port))
	want := hex.EncodeToString(sum[:])
	if exists && inspect.Container.Config.Labels[labelAnalytics] == want {
		if inspect.Container.State.Running {
			return nil
		}
		return a.start(ctx)
	}
	// Pulled before the old one goes, so a failed pull leaves it running.
	if err := a.pull(ctx, node.AnalyticsBaseImage); err != nil {
		return err
	}
	if exists {
		if err := a.remove(ctx); err != nil {
			return err
		}
	}
	labels := map[string]string{node.LabelManaged: "true", node.LabelRole: node.RoleAnalytics}
	if _, err := a.cli.VolumeInspect(ctx, a.volume, client.VolumeInspectOptions{}); cerrdefs.IsNotFound(err) {
		if _, err := a.cli.VolumeCreate(ctx, client.VolumeCreateOptions{Name: a.volume, Labels: labels}); err != nil {
			return fmt.Errorf("create volume %s: %w", a.volume, err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect volume %s: %w", a.volume, err)
	}
	labels[labelAnalytics] = want
	if a.owner != "" {
		labels[node.LabelOwner] = a.owner
	}
	duckMB := strconv.FormatInt(spec.MemoryBytes*3/8>>20, 10)
	port, _ := network.PortFrom(analyticsPort, network.TCP)
	_, err = a.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: a.name,
		Config: &container.Config{
			Image:      node.AnalyticsBaseImage,
			Entrypoint: []string{"/dock-analytics"},
			Cmd:        []string{"serve", "-memory-mb", duckMB, "-trim-above-mb", duckMB},
			Env: []string{
				"DOCK_ANALYTICS_TOKEN=" + spec.Token,
				"DOCK_ANALYTICS_DB=" + analyticsHome + "/analytics.duckdb",
				"DOCK_ANALYTICS_LISTEN=:" + strconv.Itoa(analyticsPort),
				// The Go side is small; DuckDB's memory is outside it.
				"GOMEMLIMIT=48MiB",
			},
			Labels:       labels,
			ExposedPorts: network.PortSet{port: {}},
		},
		HostConfig: &container.HostConfig{
			PortBindings: network.PortMap{port: {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: strconv.Itoa(a.port)}}},
			Mounts: []mount.Mount{
				{Type: mount.TypeBind, Source: spec.Binary, Target: "/dock-analytics", ReadOnly: true},
				{Type: mount.TypeVolume, Source: a.volume, Target: analyticsHome},
			},
			// DuckDB writes only to its volume, its spill dir included.
			ReadonlyRootfs: true,
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges"},
			RestartPolicy:  container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
			LogConfig:      container.LogConfig{Type: "local", Config: map[string]string{"max-size": "10m", "max-file": "3"}},
			Resources:      container.Resources{Memory: spec.MemoryBytes, MemorySwap: NoSwap(spec.MemoryBytes)},
		},
	})
	if err != nil {
		return fmt.Errorf("create %s: %w", a.name, err)
	}
	return a.start(ctx)
}

// Dial connects to the store's published port.
func (a *AnalyticsStore) Dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(a.port)))
}

// Remove removes the store's container and its volume, with the data.
func (a *AnalyticsStore) Remove(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, exists, err := a.inspect(ctx); err != nil {
		return err
	} else if exists {
		if err := a.remove(ctx); err != nil {
			return err
		}
	}
	v, err := a.cli.VolumeInspect(ctx, a.volume, client.VolumeInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect volume %s: %w", a.volume, err)
	}
	if l := v.Volume.Labels; l[node.LabelManaged] != "true" || l[node.LabelRole] != node.RoleAnalytics {
		return fmt.Errorf("the volume %s wasn't created by dock; it is left as it is", a.volume)
	}
	if _, err := a.cli.VolumeRemove(ctx, a.volume, client.VolumeRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove volume %s: %w", a.volume, err)
	}
	return nil
}

// inspect looks the container up and checks dock made it.
func (a *AnalyticsStore) inspect(ctx context.Context) (client.ContainerInspectResult, bool, error) {
	inspect, err := a.cli.ContainerInspect(ctx, a.name, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return inspect, false, nil
	}
	if err != nil {
		return inspect, false, fmt.Errorf("inspect %s: %w", a.name, err)
	}
	return inspect, true, a.checkOwner(inspect.Container.Config.Labels)
}

// remove stops the store, giving it time to close its database, and
// removes its container.
func (a *AnalyticsStore) remove(ctx context.Context) error {
	timeout := analyticsStop
	if _, err := a.cli.ContainerStop(ctx, a.name, client.ContainerStopOptions{Timeout: &timeout}); err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("stop %s: %w", a.name, err)
	}
	if _, err := a.cli.ContainerRemove(ctx, a.name, client.ContainerRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove %s: %w", a.name, err)
	}
	return nil
}

// start starts the container.
func (a *AnalyticsStore) start(ctx context.Context) error {
	if _, err := a.cli.ContainerStart(ctx, a.name, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("start %s: %w", a.name, err)
	}
	return nil
}

// pull fetches the image unless it is there.
func (a *AnalyticsStore) pull(ctx context.Context, image string) error {
	if _, err := a.cli.ImageInspect(ctx, image); err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("inspect image %s: %w", image, err)
	}
	pull, err := a.cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	if err := pull.Wait(ctx); err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	return nil
}

// checkOwner refuses a container under the store's name that dock didn't
// make, or that another dock runtime on this Docker host made.
func (a *AnalyticsStore) checkOwner(labels map[string]string) error {
	if labels[node.LabelManaged] != "true" || labels[node.LabelRole] != node.RoleAnalytics {
		return fmt.Errorf("a container named %s exists that dock didn't create; dock runs the analytics store under that name, so rename or remove that container", a.name)
	}
	if owner := labels[node.LabelOwner]; owner != "" && a.owner != "" && owner != a.owner {
		return fmt.Errorf("the container %s on this Docker host belongs to another dock runtime (data dir %s); run one dock runtime per Docker host", a.name, owner)
	}
	return nil
}
