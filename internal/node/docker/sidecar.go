package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/version"
)

// labelSidecar holds a hash of the spec a sidecar runs, so a changed
// spec recreates it and the same one leaves it running.
const labelSidecar = "dock.sidecar.spec"

// isSidecar reports whether a container is one of EnsureSidecar's.
func isSidecar(labels map[string]string) bool {
	return isManaged(labels) && labels[node.LabelRole] == node.RoleSidecar
}

// EnsureSidecar implements node.Containers.
func (r *Runtime) EnsureSidecar(ctx context.Context, spec node.SidecarSpec) error {
	if r.Self == "" {
		return fmt.Errorf("sidecar %s: %w", spec.Name, node.ErrNoDock)
	}
	// The version too: a new dock binary replaces the sidecar, which
	// otherwise runs the old one's inode for as long as it lives.
	b, err := json.Marshal(struct {
		Spec                 node.SidecarSpec
		Probe, Self, Version string
	}{spec, r.Probe, r.Self, version.Version})
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	want := hex.EncodeToString(sum[:])
	res, err := r.cli.ContainerInspect(ctx, spec.Name, client.ContainerInspectOptions{})
	exists := err == nil
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("inspect %s: %w", spec.Name, err)
	}
	if exists {
		labels := map[string]string{}
		if res.Container.Config != nil {
			labels = res.Container.Config.Labels
		}
		if !isSidecar(labels) || node.LabelsFrom(labels).Conflicts(spec.Labels) {
			return fmt.Errorf("a container named %s exists that isn't this sidecar; remove it with `docker rm -f %s`", spec.Name, spec.Name)
		}
		if labels[labelSidecar] == want {
			if res.Container.State != nil && res.Container.State.Running {
				return nil
			}
			return r.startSidecar(ctx, spec.Name)
		}
		if _, err := r.cli.ContainerRemove(ctx, spec.Name, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil && !cerrdefs.IsNotFound(err) {
			return fmt.Errorf("remove %s: %w", spec.Name, err)
		}
	}
	labels := spec.Labels.Map()
	labels[node.LabelRole], labels[labelSidecar] = node.RoleSidecar, want
	if len(spec.Args) == 0 {
		return fmt.Errorf("sidecar %s has no command", spec.Name)
	}
	// The first argument is the entrypoint, so the image's own (Postgres's
	// docker-entrypoint.sh) never runs.
	cfg := &container.Config{Image: spec.Image, Labels: labels, Env: envList(spec.Env), Entrypoint: spec.Args[:1], Cmd: spec.Args[1:]}
	host := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		LogConfig:     container.LogConfig{Type: "local", Config: map[string]string{"max-size": "10m", "max-file": "3"}},
		Resources: container.Resources{
			Memory: spec.Resources.MemoryBytes, MemorySwap: NoSwap(spec.Resources.MemoryBytes), NanoCPUs: spec.Resources.NanoCPUs,
		},
		SecurityOpt: []string{"no-new-privileges"},
		Mounts:      []mount.Mount{{Type: mount.TypeBind, Source: r.Self, Target: node.DockPath, ReadOnly: true}},
	}
	for _, m := range spec.Mounts {
		host.Mounts = append(host.Mounts, volumeMount(m))
	}
	cfg.Healthcheck = noHealthcheck()
	if spec.Health != nil && r.Probe != "" {
		cfg.Healthcheck = healthcheck(*spec.Health)
		host.Mounts = append(host.Mounts, mount.Mount{Type: mount.TypeBind, Source: r.Probe, Target: probePath, ReadOnly: true})
	}
	opts := client.ContainerCreateOptions{Name: spec.Name, Config: cfg, HostConfig: host}
	if spec.Network != "" {
		opts.NetworkingConfig = networking(spec.Network, spec.Aliases)
	}
	if _, err := r.cli.ContainerCreate(ctx, opts); err != nil {
		return fmt.Errorf("create %s: %w", spec.Name, err)
	}
	return r.startSidecar(ctx, spec.Name)
}

func (r *Runtime) startSidecar(ctx context.Context, name string) error {
	if _, err := r.cli.ContainerStart(ctx, name, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	return nil
}

// RemoveSidecar implements node.Containers.
func (r *Runtime) RemoveSidecar(ctx context.Context, name string, labels node.Labels) error {
	res, err := r.cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect %s: %w", name, err)
	}
	if res.Container.Config == nil || !isSidecar(res.Container.Config.Labels) {
		return nil
	}
	if node.LabelsFrom(res.Container.Config.Labels).Conflicts(labels) {
		return nil
	}
	if _, err := r.cli.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove %s: %w", name, err)
	}
	return nil
}
