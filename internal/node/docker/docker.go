// Package docker implements node.Containers on top of the Docker Engine API.
//
// It only ever sees and touches resources labelled node.LabelManaged=true.
// Anything else on the host is reported as not found, and volumes are never
// removed.
package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/NordTempo/platform/internal/node"
)

// Runtime is a node.Containers backed by the local Docker Engine.
type Runtime struct {
	cli *client.Client
}

var _ node.Containers = (*Runtime)(nil)

// New connects to the Docker Engine configured by the environment
// (DOCKER_HOST etc., defaulting to the local socket).
func New() (*Runtime, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Runtime{cli: cli}, nil
}

// Close releases the underlying Docker client.
func (r *Runtime) Close() error { return r.cli.Close() }

// managedFilter matches only platform-managed resources.
func managedFilter() client.Filters {
	return client.Filters{}.Add("label", node.LabelManaged+"=true")
}

func isManaged(labels map[string]string) bool { return labels[node.LabelManaged] == "true" }

// isApp reports whether a container is a managed app container rather than
// platform infrastructure such as BuildKit.
func isApp(labels map[string]string) bool {
	return isManaged(labels) && labels[node.LabelRole] == ""
}

// notFound is returned for missing and for unmanaged resources alike, so
// callers can't tell (or care) that something unmanaged exists.
// The error matches both node.ErrNotFound and containerd's not-found class.
func notFound(kind, name string) error {
	return notFoundError(fmt.Sprintf("%s %q not found", kind, name))
}

type notFoundError string

func (e notFoundError) Error() string { return string(e) }

func (e notFoundError) Is(target error) bool {
	return target == node.ErrNotFound || cerrdefs.IsNotFound(target)
}

// Pull pulls ref, writing one readable line per status change to logs.
func (r *Runtime) Pull(ctx context.Context, ref string, logs io.Writer) (string, error) {
	if logs == nil {
		logs = io.Discard
	}
	resp, err := r.cli.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		return "", fmt.Errorf("pull %s: %w", ref, err)
	}
	defer func() { _ = resp.Close() }()

	seen := map[string]string{}
	for msg, err := range resp.JSONMessages(ctx) {
		if err != nil {
			return "", fmt.Errorf("pull %s: %w", ref, err)
		}
		if msg.Error != nil {
			return "", fmt.Errorf("pull %s: %s", ref, msg.Error.Message)
		}
		if line, ok := pullLine(msg, seen); ok {
			_, _ = fmt.Fprintln(logs, line)
		}
	}

	img, err := r.cli.ImageInspect(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("inspect pulled image %s: %w", ref, err)
	}
	return img.ID, nil
}

// pullLine renders a pull progress message as a log line. Byte-level
// progress updates and repeated statuses per layer are dropped; seen tracks
// the last status printed per layer ID.
func pullLine(msg jsonstream.Message, seen map[string]string) (string, bool) {
	if msg.Status == "" || msg.Progress != nil && msg.Progress.Total > 0 {
		return "", false
	}
	if msg.ID == "" {
		return msg.Status, true
	}
	if seen[msg.ID] == msg.Status {
		return "", false
	}
	seen[msg.ID] = msg.Status
	return msg.ID + ": " + msg.Status, true
}

// HasImage reports whether ref is present locally.
func (r *Runtime) HasImage(ctx context.Context, ref string) (bool, error) {
	_, err := r.cli.ImageInspect(ctx, ref)
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect image %s: %w", ref, err)
	}
	return true, nil
}

// EnsureNetwork creates a managed bridge network called name if missing. It
// fails if an unmanaged network already uses the name, or a managed one
// labelled for another project or environment.
func (r *Runtime) EnsureNetwork(ctx context.Context, name string, labels node.Labels) error {
	res, err := r.cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	switch {
	case err == nil && res.Network.Name == name:
		if !isManaged(res.Network.Labels) {
			return fmt.Errorf("network %q exists but is not managed by the platform", name)
		}
		if node.LabelsFrom(res.Network.Labels).Conflicts(labels) {
			return fmt.Errorf("network %q belongs to another environment", name)
		}
		return nil
	case err != nil && !cerrdefs.IsNotFound(err):
		return fmt.Errorf("inspect network %s: %w", name, err)
	}
	_, err = r.cli.NetworkCreate(ctx, name, client.NetworkCreateOptions{
		Driver: "bridge",
		Labels: node.Labels{Project: labels.Project, Environment: labels.Environment}.Map(),
	})
	if err != nil {
		return fmt.Errorf("create network %s: %w", name, err)
	}
	return nil
}

// EnsureVolume creates a managed volume called name if missing. It fails if
// an unmanaged volume already uses the name, or a managed one labelled for
// another service. Existing labels are left as is.
func (r *Runtime) EnsureVolume(ctx context.Context, name string, labels node.Labels) error {
	res, err := r.cli.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	switch {
	case err == nil:
		if !isManaged(res.Volume.Labels) {
			return fmt.Errorf("volume %q exists but is not managed by the platform", name)
		}
		if node.LabelsFrom(res.Volume.Labels).Conflicts(labels) {
			return fmt.Errorf("volume %q belongs to another service", name)
		}
		return nil
	case !cerrdefs.IsNotFound(err):
		return fmt.Errorf("inspect volume %s: %w", name, err)
	}
	_, err = r.cli.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Labels: labels.Map()})
	if err != nil {
		return fmt.Errorf("create volume %s: %w", name, err)
	}
	return nil
}

// Start creates and starts a container from spec. The network and volumes
// must already exist and be managed (see EnsureNetwork and EnsureVolume).
func (r *Runtime) Start(ctx context.Context, spec node.ContainerSpec) (node.Container, error) {
	if spec.Port < 0 || spec.Port > math.MaxUint16 {
		return node.Container{}, fmt.Errorf("invalid port %d", spec.Port)
	}
	if err := r.checkDeps(ctx, spec); err != nil {
		return node.Container{}, err
	}
	opts := createOptions(spec)
	created, err := r.cli.ContainerCreate(ctx, opts)
	if cerrdefs.IsConflict(err) {
		return node.Container{}, fmt.Errorf("container name %q is already in use", spec.Name)
	}
	if err != nil {
		return node.Container{}, fmt.Errorf("create container %s: %w", spec.Name, err)
	}
	if _, err := r.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		// Don't leave a half-made container holding the name.
		_, _ = r.cli.ContainerRemove(context.WithoutCancel(ctx), created.ID, client.ContainerRemoveOptions{Force: true})
		return node.Container{}, fmt.Errorf("start container %s: %w", spec.Name, err)
	}
	return r.Inspect(ctx, created.ID)
}

// checkDeps refuses to attach a container to unmanaged networks or volumes.
// Docker would otherwise silently create missing volumes without labels.
func (r *Runtime) checkDeps(ctx context.Context, spec node.ContainerSpec) error {
	if spec.Network != "" {
		res, err := r.cli.NetworkInspect(ctx, spec.Network, client.NetworkInspectOptions{})
		if err != nil && !cerrdefs.IsNotFound(err) {
			return fmt.Errorf("inspect network %s: %w", spec.Network, err)
		}
		if err != nil || res.Network.Name != spec.Network || !isManaged(res.Network.Labels) {
			return fmt.Errorf("network %q does not exist or is not managed by the platform", spec.Network)
		}
		if node.LabelsFrom(res.Network.Labels).Conflicts(spec.Labels) {
			return fmt.Errorf("network %q belongs to another environment", spec.Network)
		}
	}
	for _, m := range spec.Mounts {
		res, err := r.cli.VolumeInspect(ctx, m.Volume, client.VolumeInspectOptions{})
		if err != nil && !cerrdefs.IsNotFound(err) {
			return fmt.Errorf("inspect volume %s: %w", m.Volume, err)
		}
		if err != nil || !isManaged(res.Volume.Labels) {
			return fmt.Errorf("volume %q does not exist or is not managed by the platform", m.Volume)
		}
		if node.LabelsFrom(res.Volume.Labels).Conflicts(spec.Labels) {
			return fmt.Errorf("volume %q belongs to another service", m.Volume)
		}
	}
	return nil
}

// createOptions translates spec into Docker's create request.
func createOptions(spec node.ContainerSpec) client.ContainerCreateOptions {
	cfg := &container.Config{
		Image:  spec.Image,
		Labels: spec.Labels.Map(),
		Env:    envList(spec.Env),
	}
	if spec.Cmd != "" {
		cfg.Cmd = []string{"sh", "-c", spec.Cmd}
	}

	host := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		LogConfig: container.LogConfig{
			Type:   "local",
			Config: map[string]string{"max-size": "10m", "max-file": "3"},
		},
		Resources: container.Resources{
			Memory:   spec.Resources.MemoryBytes,
			NanoCPUs: spec.Resources.NanoCPUs,
		},
		SecurityOpt: []string{"no-new-privileges"},
	}
	for _, m := range spec.Mounts {
		host.Mounts = append(host.Mounts, mount.Mount{Type: mount.TypeVolume, Source: m.Volume, Target: m.Path})
	}
	if spec.Port > 0 {
		port, _ := network.PortFrom(uint16(spec.Port), network.TCP)
		cfg.ExposedPorts = network.PortSet{port: {}}
		binding := network.PortBinding{HostIP: netip.MustParseAddr("127.0.0.1")}
		if spec.HostPort > 0 {
			binding.HostPort = strconv.Itoa(spec.HostPort)
		}
		host.PortBindings = network.PortMap{port: {binding}}
	}

	opts := client.ContainerCreateOptions{Name: spec.Name, Config: cfg, HostConfig: host}
	if spec.Network != "" {
		opts.NetworkingConfig = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				spec.Network: {Aliases: spec.Aliases},
			},
		}
	}
	return opts
}

// envList renders env as sorted KEY=value pairs.
func envList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}

// Stop stops a managed container, killing it after timeout.
func (r *Runtime) Stop(ctx context.Context, id string, timeout time.Duration) error {
	if _, err := r.inspect(ctx, id); err != nil {
		return err
	}
	secs := int(math.Ceil(max(timeout, 0).Seconds()))
	if _, err := r.cli.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &secs}); err != nil {
		return fmt.Errorf("stop container %s: %w", id, err)
	}
	return nil
}

// Remove removes a stopped managed container, keeping its volumes.
func (r *Runtime) Remove(ctx context.Context, id string) error {
	if _, err := r.inspect(ctx, id); err != nil {
		return err
	}
	if _, err := r.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{}); err != nil {
		return fmt.Errorf("remove container %s: %w", id, err)
	}
	return nil
}

// Inspect returns the current state of a managed container.
func (r *Runtime) Inspect(ctx context.Context, id string) (node.Container, error) {
	c, err := r.inspect(ctx, id)
	if err != nil {
		return node.Container{}, err
	}
	return toContainer(c), nil
}

// List returns all managed containers, running or not.
func (r *Runtime) List(ctx context.Context) ([]node.Container, error) {
	res, err := r.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: managedFilter()})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	out := make([]node.Container, 0, len(res.Items))
	for _, s := range res.Items {
		c, err := r.Inspect(ctx, s.ID)
		if cerrdefs.IsNotFound(err) {
			continue // removed since listing
		}
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// inspect fetches a container, treating unmanaged ones as not found.
func (r *Runtime) inspect(ctx context.Context, id string) (container.InspectResponse, error) {
	res, err := r.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return container.InspectResponse{}, notFound("container", id)
	}
	if err != nil {
		return container.InspectResponse{}, fmt.Errorf("inspect container %s: %w", id, err)
	}
	if res.Container.Config == nil || !isApp(res.Container.Config.Labels) {
		return container.InspectResponse{}, notFound("container", id)
	}
	return res.Container, nil
}

// toContainer maps Docker's inspect response to node.Container.
func toContainer(c container.InspectResponse) node.Container {
	out := node.Container{
		ID:           c.ID,
		Name:         strings.TrimPrefix(c.Name, "/"),
		RestartCount: c.RestartCount,
	}
	if c.Config != nil {
		out.Image = c.Config.Image
		out.Labels = node.LabelsFrom(c.Config.Labels)
	}
	if s := c.State; s != nil {
		out.State = node.ContainerState(s.Status)
		out.ExitCode = s.ExitCode
		out.OOMKilled = s.OOMKilled
		if t, err := time.Parse(time.RFC3339Nano, s.StartedAt); err == nil && t.Year() > 1 {
			out.StartedAt = t
		}
	}
	if c.NetworkSettings != nil {
		out.HostPort = hostPort(c.NetworkSettings.Ports)
	}
	return out
}

// hostPort returns the loopback host port of the first published TCP port.
// Containers created by Start publish at most one.
func hostPort(ports network.PortMap) int {
	for p, bindings := range ports {
		if p.Proto() != network.TCP {
			continue
		}
		for _, b := range bindings {
			if n, err := strconv.Atoi(b.HostPort); err == nil && b.HostIP.IsLoopback() {
				return n
			}
		}
	}
	return 0
}

// errExited reports a container that died while being waited on.
func errExited(code int, oom bool) error {
	if oom {
		return fmt.Errorf("container ran out of memory and was killed (exit code %d) during health check", code)
	}
	return fmt.Errorf("container exited with code %d during health check", code)
}

var errRestarted = errors.New("container restarted during health check")
