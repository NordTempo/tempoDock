package build

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/docker"
)

const (
	// ContainerName is the long-lived BuildKit container on a builder server.
	ContainerName = "dock-buildkit"
	// CacheVolume holds BuildKit's state and build cache across restarts.
	CacheVolume = "dock-buildkit-cache"

	// labelConfig records the settings the container was created with, so a
	// config change recreates it.
	labelConfig = "dock.buildkit.config"
	// roleBuildKit is the container's node.LabelRole.
	roleBuildKit = node.RoleBuildKit

	// cpuShares is a quarter of Docker's default weight, so app containers
	// win any CPU contention with a build.
	cpuShares = 256
)

// ensureDaemon makes sure the BuildKit container exists with the current
// config and limits and is running, then returns a client connected to it.
//
// The memory cap is verified where it takes effect, in the container's
// cgroup, before every build, and a container whose limits are off is
// recreated. The swap setting (docker.NoSwap) survives a systemd
// daemon-reload; this catches `docker update` and anything else outside
// the platform that loosens it, since a build swapping past the cap
// thrashes the disk.
func (b *Builder) ensureDaemon(ctx context.Context, logs io.Writer) (*bkclient.Client, error) {
	want := fmt.Sprintf("%s mem=%dMB cpus=%g", b.cfg.Image, b.cfg.MemoryMB, b.cfg.CPUs)
	if b.Network != "" {
		want += " net=" + b.Network
	}
	if b.Owner != "" {
		// A container from before owners were recorded is recreated
		// with this runtime's.
		want += " owner=" + b.Owner
	}
	mem := int64(b.cfg.MemoryMB) << 20

	inspect, err := b.docker.ContainerInspect(ctx, b.name, client.ContainerInspectOptions{})
	switch {
	case cerrdefs.IsNotFound(err):
		if err := b.createDaemon(ctx, want, logs); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, fmt.Errorf("inspect %s: %w", b.name, err)
	default:
		ctr := inspect.Container
		if err := b.checkOwner(ctr.Config.Labels); err != nil {
			return nil, err
		}
		var problem string
		switch h := ctr.HostConfig; {
		case ctr.Config.Labels[labelConfig] != want:
			problem = "settings changed to " + want
		case h == nil || h.Memory != mem || h.MemorySwap != docker.NoSwap(mem):
			problem = "its memory limit or swap was changed"
		case ctr.State.Running:
			problem = cgroupProblem(b.readCgroupLimits(ctx), mem)
		}
		if problem == "" && ctr.State.Running {
			return b.connect(ctx)
		}
		if problem != "" {
			_, _ = fmt.Fprintf(logs, "recreating %s: %s\n", b.name, problem)
			if _, err := b.docker.ContainerRemove(ctx, b.name, client.ContainerRemoveOptions{Force: true}); err != nil {
				return nil, fmt.Errorf("remove %s: %w", b.name, err)
			}
			if err := b.createDaemon(ctx, want, logs); err != nil {
				return nil, err
			}
		}
	}

	if _, err := b.docker.ContainerStart(ctx, b.name, client.ContainerStartOptions{}); err != nil {
		return nil, fmt.Errorf("start %s: %w", b.name, err)
	}
	return b.connect(ctx)
}

// checkOwner refuses a BuildKit container (by its labels) this runtime
// may not use: one dock didn't create, or one another dock runtime on the
// same Docker host did (node.LabelOwner). Recreating that one would kill
// the other runtime's builds, and both would keep doing it. A container
// without an owner is from before owners were recorded, and is taken over.
func (b *Builder) checkOwner(labels map[string]string) error {
	if labels[node.LabelManaged] != "true" || labels[node.LabelRole] != roleBuildKit {
		return fmt.Errorf("a container named %s exists that dock didn't create; dock runs BuildKit under that name, so rename or remove that container", b.name)
	}
	if owner := labels[node.LabelOwner]; owner != "" && owner != b.Owner {
		return fmt.Errorf("the BuildKit container %s on this Docker host belongs to another dock runtime (data dir %s); "+
			"run one dock runtime per Docker host, or if that one is gone for good, remove its container with `docker rm -f %s`", b.name, owner, b.name)
	}
	return nil
}

func (b *Builder) createDaemon(ctx context.Context, configLabel string, logs io.Writer) error {
	if _, err := b.docker.ImageInspect(ctx, b.cfg.Image); cerrdefs.IsNotFound(err) {
		_, _ = fmt.Fprintf(logs, "pulling %s\n", b.cfg.Image)
		pull, err := b.docker.ImagePull(ctx, b.cfg.Image, client.ImagePullOptions{})
		if err != nil {
			return fmt.Errorf("pull %s: %w", b.cfg.Image, err)
		}
		if err := pull.Wait(ctx); err != nil {
			return fmt.Errorf("pull %s: %w", b.cfg.Image, err)
		}
	}

	labels := map[string]string{
		node.LabelManaged: "true",
		node.LabelRole:    roleBuildKit,
	}
	if _, err := b.docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: b.cacheVolume, Labels: labels}); err != nil {
		return fmt.Errorf("create volume %s: %w", b.cacheVolume, err)
	}
	labels[labelConfig] = configLabel
	if b.Owner != "" {
		labels[node.LabelOwner] = b.Owner
	}

	mem := int64(b.cfg.MemoryMB) << 20
	_, err := b.docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: b.name,
		Config: &container.Config{
			Image:  b.cfg.Image,
			Labels: labels,
		},
		HostConfig: &container.HostConfig{
			// Rootful BuildKit needs privileges to create the sandboxes
			// build steps run in. See the package doc.
			Privileged:    true,
			NetworkMode:   container.NetworkMode(b.Network), // "" is the default bridge
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
			LogConfig: container.LogConfig{
				Type:   "local",
				Config: map[string]string{"max-size": "10m", "max-file": "3"},
			},
			Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: b.cacheVolume, Target: "/var/lib/buildkit"}},
			Resources: container.Resources{
				Memory:     mem,
				MemorySwap: docker.NoSwap(mem), // hitting the cap fails the build instead of thrashing the disk
				NanoCPUs:   int64(b.cfg.CPUs * 1e9),
				CPUShares:  cpuShares,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("create %s: %w", b.name, err)
	}
	return nil
}

// PruneBuildCache implements node.Builder with BuildKit's prune API: cache
// records go, least recently used first, until the cache is down to
// keepBytes (MaxUsedSpace) and never below it (ReservedSpace). Records a
// running build uses stay. A BuildKit container that isn't running isn't
// started for it: nothing has been built since it stopped.
//
// Only this runtime's own BuildKit is pruned (checkOwner): the prune runs
// over `buildctl dial-stdio` in that one container, so other BuildKit
// daemons on the host (buildx builders, Docker's own builder) and their
// caches are out of its reach.
func (b *Builder) PruneBuildCache(ctx context.Context, keepBytes int64) (node.PruneResult, error) {
	inspect, err := b.docker.ContainerInspect(ctx, b.name, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) || (err == nil && !inspect.Container.State.Running) {
		return node.PruneResult{}, nil
	}
	if err != nil {
		return node.PruneResult{}, fmt.Errorf("inspect %s: %w", b.name, err)
	}
	if err := b.checkOwner(inspect.Container.Config.Labels); err != nil {
		return node.PruneResult{}, err
	}
	bk, err := b.connect(ctx)
	if err != nil {
		return node.PruneResult{}, err
	}
	defer func() { _ = bk.Close() }()
	var out node.PruneResult
	ch := make(chan bkclient.UsageInfo)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for u := range ch {
			out.Removed++
			out.ReclaimedBytes += u.Size
		}
	}()
	err = bk.Prune(ctx, ch, bkclient.WithKeepOpt(0, keepBytes, keepBytes, 0))
	close(ch)
	<-done
	if err != nil {
		return out, fmt.Errorf("prune build cache: %w", err)
	}
	return out, nil
}

// cgroupLimitsScript prints the container's cgroup v2 memory.max and
// memory.swap.max, or "unknown" for each one that doesn't exist (cgroup v1,
// or swap accounting off).
const cgroupLimitsScript = `for f in memory.max memory.swap.max; do cat /sys/fs/cgroup/$f 2>/dev/null || echo unknown; done`

// readCgroupLimits returns cgroupLimitsScript's output from the running
// BuildKit container, or "" if it can't be read.
func (b *Builder) readCgroupLimits(ctx context.Context) string {
	conn, err := b.exec(ctx, "sh", "-c", cgroupLimitsScript)
	if err != nil {
		return ""
	}
	defer func() { _ = conn.Close() }()
	out, _ := io.ReadAll(io.LimitReader(conn, 1<<10))
	return string(out)
}

// cgroupProblem explains how the effective limits in out (see
// cgroupLimitsScript) differ from a memory cap of mem bytes with no swap,
// or returns "" if they match or can't be told.
func cgroupProblem(out string, mem int64) string {
	f := strings.Fields(out)
	if len(f) != 2 || mem == 0 {
		return ""
	}
	if f[0] != "unknown" && f[0] != strconv.FormatInt(mem, 10) {
		return fmt.Sprintf("its cgroup memory.max is %s, want %d", f[0], mem)
	}
	if f[1] != "unknown" && f[1] != "0" {
		return fmt.Sprintf("its cgroup memory.swap.max is %s, want 0 (no swap)", f[1])
	}
	return ""
}

// connect returns a BuildKit client once buildkitd answers, which takes a
// moment after the container (re)starts.
func (b *Builder) connect(ctx context.Context) (*bkclient.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		c, err := bkclient.New(ctx, "", bkclient.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return b.exec(ctx, "buildctl", "dial-stdio")
		}))
		if err == nil {
			if _, err = c.ListWorkers(ctx); err == nil {
				return c, nil
			}
			_ = c.Close()
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("buildkit in %s is not answering: %w", b.name, err)
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// exec runs cmd in the BuildKit container and returns its stdin/stdout as a
// connection. This goes through the Docker API, so reaching BuildKit needs
// no socket or port beyond the Docker socket the daemon already uses.
func (b *Builder) exec(ctx context.Context, cmd ...string) (net.Conn, error) {
	created, err := b.docker.ExecCreate(ctx, b.name, client.ExecCreateOptions{
		Cmd:          cmd,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return nil, err
	}
	attached, err := b.docker.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return nil, err
	}
	hr := attached.HijackedResponse
	if mt, _ := hr.MediaType(); mt == "application/vnd.docker.raw-stream" {
		return &execConn{Conn: hr.Conn, r: hr.Reader, hr: hr}, nil
	}
	// Without a TTY, stdout and stderr arrive multiplexed.
	pr, pw := io.Pipe()
	c := &execConn{Conn: hr.Conn, r: pr, hr: hr}
	go func() {
		_, err := stdcopy.StdCopy(pw, &c.stderr, hr.Reader)
		if err == nil && c.stderr.Len() > 0 {
			err = fmt.Errorf("%s: %s", cmd[0], strings.TrimSpace(c.stderr.String()))
		}
		pw.CloseWithError(err)
	}()
	return c, nil
}

// execConn is a hijacked exec session: writes go to the process's stdin,
// reads come from its stdout.
type execConn struct {
	net.Conn
	r      io.Reader
	hr     client.HijackedResponse
	stderr bytes.Buffer // only read after r returns an error
}

func (c *execConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// CloseWrite closes the process's stdin.
func (c *execConn) CloseWrite() error { return c.hr.CloseWrite() }
