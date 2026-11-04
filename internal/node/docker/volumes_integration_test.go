//go:build integration

package docker

import (
	"context"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Removing a container takes the anonymous volume Docker made for its
// image's VOLUME with it (nothing ever mounts it again), and never a Dock
// volume.
func TestRemoveDropsAnonymousVolumes(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, 2*time.Minute)

	// An image that declares a VOLUME, like Postgres: alpine plus VOLUME.
	base, err := e.cli.ContainerCreate(ctx, client.ContainerCreateOptions{Name: e.name("base"), Image: shellImage})
	if err != nil {
		t.Fatal(err)
	}
	img, err := e.cli.ContainerCommit(ctx, base.ID, client.ContainerCommitOptions{Changes: []string{"VOLUME /scratch"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.cli.ImageRemove(context.Background(), img.ID, client.ImageRemoveOptions{}) })

	labels := node.Labels{Project: "p1", Environment: "e1", Service: "s1", Release: "r1"}
	vol := e.name("data")
	if err := e.EnsureVolume(ctx, vol, node.Labels{Project: "p1", Environment: "e1", Service: "s1"}); err != nil {
		t.Fatal(err)
	}
	c := e.start(node.ContainerSpec{
		Name: e.name("anon"), Image: img.ID, Labels: labels, Cmd: "sleep 300",
		Mounts: []node.Mount{{Volume: vol, Path: "/data"}},
	})
	res, err := e.cli.ContainerInspect(ctx, c.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var anon string
	for _, m := range res.Container.Mounts {
		if m.Destination == "/scratch" {
			anon = m.Name
		}
	}
	if anon == "" {
		t.Fatalf("no anonymous volume at /scratch: %+v", res.Container.Mounts)
	}

	if err := e.Stop(ctx, c.ID, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := e.Remove(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cli.VolumeInspect(ctx, anon, client.VolumeInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Errorf("anonymous volume %s after Remove: %v, want it gone", anon, err)
		_, _ = e.cli.VolumeRemove(context.Background(), anon, client.VolumeRemoveOptions{})
	}
	if _, err := e.cli.VolumeInspect(ctx, vol, client.VolumeInspectOptions{}); err != nil {
		t.Errorf("Dock volume %s after Remove: %v, want it kept", vol, err)
	}
}
