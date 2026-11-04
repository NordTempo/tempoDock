//go:build integration

package docker

import (
	"context"
	"slices"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
)

// volumeImage commits an image that declares a VOLUME at /scratch, like
// Postgres does at its data dir: alpine plus VOLUME. It is removed when
// the test ends.
func volumeImage(t *testing.T, e *testEnv) string {
	t.Helper()
	ctx := ctxTimeout(t, time.Minute)
	base, err := e.cli.ContainerCreate(ctx, client.ContainerCreateOptions{Name: e.name("base"), Image: shellImage})
	if err != nil {
		t.Fatal(err)
	}
	img, err := e.cli.ContainerCommit(ctx, base.ID, client.ContainerCommitOptions{Changes: []string{"VOLUME /scratch"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.cli.ImageRemove(context.Background(), img.ID, client.ImageRemoveOptions{}) })
	return img.ID
}

// anonVolume returns the name of the volume mounted at path in container id.
func anonVolume(t *testing.T, e *testEnv, id, path string) string {
	t.Helper()
	res, err := e.cli.ContainerInspect(context.Background(), id, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range res.Container.Mounts {
		if m.Destination == path {
			return m.Name
		}
	}
	t.Fatalf("no volume at %s: %+v", path, res.Container.Mounts)
	return ""
}

// Removing a container takes the anonymous volume Docker made for its
// image's VOLUME with it (nothing ever mounts it again), and never a Dock
// volume.
func TestRemoveDropsAnonymousVolumes(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, 2*time.Minute)
	img := volumeImage(t, e)

	labels := node.Labels{Project: "p1", Environment: "e1", Service: "s1", Release: "r1"}
	vol := e.name("data")
	if err := e.EnsureVolume(ctx, vol, node.Labels{Project: "p1", Environment: "e1", Service: "s1"}); err != nil {
		t.Fatal(err)
	}
	c := e.start(node.ContainerSpec{
		Name: e.name("anon"), Image: img, Labels: labels, Cmd: "sleep 300",
		Mounts: []node.Mount{{Volume: vol, Path: "/data"}},
	})
	anon := anonVolume(t, e, c.ID, "/scratch")

	if err := e.Stop(ctx, c.ID, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := e.Remove(ctx, c.ID, node.RemoveOptions{}); err != nil {
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

// A container that served keeps its anonymous volume when removed (ADR
// 0052): the image's data dir is in it. Only one such copy stays per
// service: the next keeping removal drops the one before, never one a
// container still uses.
func TestRemoveKeepsServedAnonymousVolumes(t *testing.T) {
	e := newEnv(t)
	e.prefix = "anon-" + e.prefix
	ctx := ctxTimeout(t, 2*time.Minute)
	img := volumeImage(t, e)
	t.Cleanup(func() { // before newEnv's: kept volumes have no name prefix
		f := client.Filters{}.Add("label", node.LabelProject+"="+e.prefix)
		list, _ := e.cli.ContainerList(context.Background(), client.ContainerListOptions{All: true, Filters: f})
		for _, c := range list.Items {
			_, _ = e.cli.ContainerRemove(context.Background(), c.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		}
		vols, _ := e.cli.VolumeList(context.Background(), client.VolumeListOptions{Filters: f})
		for _, v := range vols.Items {
			_, _ = e.cli.VolumeRemove(context.Background(), v.Name, client.VolumeRemoveOptions{})
		}
	})

	// start runs release n of one service; it writes to its data dir.
	start := func(n string) (node.Container, string) {
		c := e.start(node.ContainerSpec{
			Name: e.name("r" + n), Image: img, Cmd: "echo " + n + " > /scratch/f && sleep 300",
			Labels: node.Labels{ControlPlane: e.prefix, Project: e.prefix, Environment: "e1", Service: "s1", Release: n},
		})
		if !slices.Equal(c.AnonymousVolumes, []string{"/scratch"}) {
			t.Fatalf("AnonymousVolumes = %q, want /scratch", c.AnonymousVolumes)
		}
		return c, anonVolume(t, e, c.ID, "/scratch")
	}
	remove := func(c node.Container) {
		t.Helper()
		if err := e.Stop(ctx, c.ID, time.Second); err != nil {
			t.Fatal(err)
		}
		if err := e.Remove(ctx, c.ID, node.RemoveOptions{KeepAnonymousVolumes: true}); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(v string) bool {
		_, err := e.cli.VolumeInspect(ctx, v, client.VolumeInspectOptions{})
		if err != nil && !cerrdefs.IsNotFound(err) {
			t.Fatal(err)
		}
		return err == nil
	}

	c1, v1 := start("1")
	res, err := e.cli.VolumeInspect(ctx, v1, client.VolumeInspectOptions{})
	if err != nil || res.Volume.Labels[labelAnonymous] != "true" || res.Volume.Labels[node.LabelRelease] != "1" {
		t.Fatalf("volume %s labels = %v, %v", v1, res.Volume.Labels, err)
	}
	remove(c1)
	if !exists(v1) {
		t.Fatalf("anonymous volume %s of a served container was removed with it", v1)
	}

	c2, v2 := start("2")
	c3, v3 := start("3") // running next to 2, as in a zero-downtime deploy
	remove(c2)
	if exists(v1) || !exists(v2) || !exists(v3) {
		t.Errorf("after removing release 2: v1 %t, v2 %t, v3 %t; want only v2 and v3", exists(v1), exists(v2), exists(v3))
	}
	remove(c3)
	if exists(v2) || !exists(v3) {
		t.Errorf("after removing release 3: v2 %t, v3 %t; want only v3", exists(v2), exists(v3))
	}
}
