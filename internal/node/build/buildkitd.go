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
	mem := int64(b.cfg.MemoryMB) << 20

	inspect, err := b.docker.ContainerInspect(ctx, ContainerName, client.ContainerInspectOptions{})
	switch {
	case cerrdefs.IsNotFound(err):
		if err := b.createDaemon(ctx, want, logs); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, fmt.Errorf("inspect %s: %w", ContainerName, err)
	default:
		ctr := inspect.Container
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
			_, _ = fmt.Fprintf(logs, "recreating %s: %s\n", ContainerName, problem)
			if _, err := b.docker.ContainerRemove(ctx, ContainerName, client.ContainerRemoveOptions{Force: true}); err != nil {
				return nil, fmt.Errorf("remove %s: %w", ContainerName, err)
			}
			if err := b.createDaemon(ctx, want, logs); err != nil {
				return nil, err
			}
		}
	}

	if _, err := b.docker.ContainerStart(ctx, ContainerName, client.ContainerStartOptions{}); err != nil {
		return nil, fmt.Errorf("start %s: %w", ContainerName, err)
	}
	return b.connect(ctx)
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
		node.LabelRole:    "buildkit",
	}
	if _, err := b.docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: CacheVolume, Labels: labels}); err != nil {
		return fmt.Errorf("create volume %s: %w", CacheVolume, err)
	}

	mem := int64(b.cfg.MemoryMB) << 20
	_, err := b.docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: ContainerName,
		Config: &container.Config{
			Image:  b.cfg.Image,
			Labels: withLabel(labels, labelConfig, configLabel),
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
			Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: CacheVolume, Target: "/var/lib/buildkit"}},
			Resources: container.Resources{
				Memory:     mem,
				MemorySwap: docker.NoSwap(mem), // hitting the cap fails the build instead of thrashing the disk
				NanoCPUs:   int64(b.cfg.CPUs * 1e9),
				CPUShares:  cpuShares,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("create %s: %w", ContainerName, err)
	}
	return nil
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

func withLabel(m map[string]string, k, v string) map[string]string {
	out := map[string]string{k: v}
	for k, v := range m {
		out[k] = v
	}
	return out
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
			return nil, fmt.Errorf("buildkit in %s is not answering: %w", ContainerName, err)
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// exec runs cmd in the BuildKit container and returns its stdin/stdout as a
// connection. This goes through the Docker API, so reaching BuildKit needs
// no socket or port beyond the Docker socket the daemon already uses.
func (b *Builder) exec(ctx context.Context, cmd ...string) (net.Conn, error) {
	created, err := b.docker.ExecCreate(ctx, ContainerName, client.ExecCreateOptions{
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
