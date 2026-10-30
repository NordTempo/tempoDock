package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
)

// GCTimeout bounds one garbage collection on disk, GCBucketTimeout one in
// a bucket, where the collector lists every repository and blob over the
// network. Collecting takes seconds to minutes; these only stop a
// collector that hangs.
const (
	GCTimeout       = 15 * time.Minute
	GCBucketTimeout = time.Hour
)

// GarbageCollect removes the blobs no manifest references any more, after
// Client.Prune deleted manifests, with Distribution's own
// `registry garbage-collect --delete-untagged` in a one-shot container.
//
// The collector must not run next to a registry that accepts pushes, or
// it can delete blobs an upload is still linking, so the registry
// container (registry, normally ContainerName) is stopped for the run and
// started again afterwards, whatever happens. Pulls and pushes fail for
// those seconds; the caller schedules it when no build or pull is in
// flight. The collector gets the registry's volumes and config
// (VolumesFrom) and a memory cap. On disk it gets no network; with the
// images in a bucket it gets the registry's network to reach it. Its
// output goes to logs. A collector still running after GCTimeout
// (GCBucketTimeout) is removed, so a hung one can't keep the registry
// stopped.
func GarbageCollect(ctx context.Context, docker *client.Client, registry string, bucket bool, logs io.Writer) (err error) {
	if logs == nil {
		logs = io.Discard
	}
	limit, network := GCTimeout, container.NetworkMode("none")
	if bucket {
		limit = GCBucketTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	inspect, err := docker.ContainerInspect(ctx, registry, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("garbage collect: inspect %s: %w", registry, err)
	}
	if h := inspect.Container.HostConfig; bucket && h != nil {
		network = h.NetworkMode
	}
	if inspect.Container.State.Running {
		timeout := 30
		if _, err := docker.ContainerStop(ctx, registry, client.ContainerStopOptions{Timeout: &timeout}); err != nil {
			return fmt.Errorf("garbage collect: stop %s: %w", registry, err)
		}
		defer func() {
			// Restart even if ctx ended: a stopped registry fails every deploy.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			if _, serr := docker.ContainerStart(ctx, registry, client.ContainerStartOptions{}); serr != nil && err == nil {
				err = fmt.Errorf("garbage collect: restart %s: %w", registry, serr)
			}
		}()
	}

	name := registry + "-gc"
	// A collector left over from a crash would hold the name.
	if _, err := docker.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("garbage collect: remove old %s: %w", name, err)
	}
	const mem = 256 << 20
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:  inspect.Container.Image, // the registry's own image ID
			Cmd:    []string{"garbage-collect", "--delete-untagged", configPath},
			Labels: map[string]string{node.LabelManaged: "true", node.LabelRole: node.RoleRegistryGC},
		},
		HostConfig: &container.HostConfig{
			VolumesFrom: []string{registry},
			NetworkMode: network,
			Resources:   container.Resources{Memory: mem, MemorySwap: mem},
		},
	})
	if err != nil {
		return fmt.Errorf("garbage collect: create %s: %w", name, err)
	}
	defer func() {
		_, _ = docker.ContainerRemove(context.WithoutCancel(ctx), created.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("garbage collect: start %s: %w", name, err)
	}
	wait := docker.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	var exit container.WaitResponse
	select {
	case exit = <-wait.Result:
	case err := <-wait.Error:
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("garbage collect: %s still ran after %s and was stopped", name, limit)
		}
		return fmt.Errorf("garbage collect: wait for %s: %w", name, err)
	}
	if out, err := docker.ContainerLogs(ctx, created.ID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true}); err == nil {
		_, _ = stdcopy.StdCopy(logs, logs, out)
		_ = out.Close()
	}
	if exit.StatusCode != 0 {
		return fmt.Errorf("garbage collect: registry garbage-collect exited with %d", exit.StatusCode)
	}
	return nil
}
