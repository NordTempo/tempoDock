//go:build integration

package core

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// Deleting a project right after archiving it, while its container may
// still be stopping, leaves no container, volume or network of it on a
// real Docker Engine.
func TestIntegrationDeleteProject(t *testing.T) {
	t.Parallel()
	c := dockerHarness(t)
	ctx := context.Background()
	project := fmt.Sprintf("del-%d", time.Now().UnixNano()%1_000_000)
	if _, err := c.CreateProject(ctx, testSystem, apitypes.CreateProjectRequest{Name: project}); err != nil {
		t.Fatal(err)
	}
	db := ServiceRef{project, "production", "db"}
	if _, err := c.CreateService(ctx, testSystem, db.Env(), apitypes.CreateServiceRequest{Name: "db", StartCommand: "trap 'exit 0' TERM; while true; do sleep 1; done", HealthCheckTimeoutSeconds: 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddVolume(ctx, testSystem, db, apitypes.AddVolumeRequest{Name: "data", MountPath: "/data"}); err != nil {
		t.Fatal(err)
	}
	rel, err := c.DeployImage(ctx, testSystem, db, apitypes.DeployImageRequest{Image: "alpine:latest"})
	if err != nil {
		t.Fatal(err)
	}
	if r := waitRelease(t, c, db, rel.Number); r.Status != apitypes.ReleaseActive {
		t.Fatalf("release %d: %s %s", r.Number, r.Status, r.Error)
	}
	ss, err := c.loadService(ctx, 1, db)
	if err != nil {
		t.Fatal(err)
	}
	vols, err := c.store.ListVolumes(ctx, ss.svc.ID)
	if err != nil || len(vols) != 1 {
		t.Fatalf("volumes = %+v, %v", vols, err)
	}
	network := networkName(ss.envScope)

	if _, err := c.ArchiveProject(ctx, testSystem, project); err != nil {
		t.Fatal(err)
	}
	got, err := c.DeleteProject(ctx, testSystem, project, apitypes.DeleteProjectRequest{Confirm: project})
	if err != nil {
		t.Fatal(err)
	}
	if got.Volumes != 1 || got.Services != 1 {
		t.Errorf("deleted = %+v", got)
	}

	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()
	ctrs, err := c.rt.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, ct := range ctrs {
		if ct.Labels.Project == strconv.FormatInt(ss.project.ID, 10) {
			t.Errorf("container %s of the deleted project is left", ct.Name)
		}
	}
	if _, err := cli.VolumeInspect(ctx, vols[0].DockerName, client.VolumeInspectOptions{}); err == nil {
		t.Errorf("volume %s is left", vols[0].DockerName)
	}
	if _, err := cli.NetworkInspect(ctx, network, client.NetworkInspectOptions{}); err == nil {
		t.Errorf("network %s is left", network)
	}
	if _, err := c.CreateProject(ctx, testSystem, apitypes.CreateProjectRequest{Name: project}); err != nil {
		t.Errorf("the deleted project's name: %v", err)
	}
}

// Deleting an environment with a running stateful service stops it and
// leaves no container, volume or network of that environment on a real
// Docker Engine, while the project's other environment keeps running.
func TestIntegrationDeleteEnvironment(t *testing.T) {
	t.Parallel()
	c := dockerHarness(t)
	ctx := context.Background()
	project := fmt.Sprintf("delenv-%d", time.Now().UnixNano()%1_000_000)
	if _, err := c.CreateProject(ctx, testSystem, apitypes.CreateProjectRequest{Name: project}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateEnvironment(ctx, testSystem, project, apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	loop := apitypes.CreateServiceRequest{Name: "db", StartCommand: "trap 'exit 0' TERM; while true; do sleep 1; done", HealthCheckTimeoutSeconds: 20}
	keep, db := ServiceRef{project, "production", "db"}, ServiceRef{project, "staging", "db"}
	for _, ref := range []ServiceRef{keep, db} {
		if _, err := c.CreateService(ctx, testSystem, ref.Env(), loop); err != nil {
			t.Fatal(err)
		}
		if _, err := c.AddVolume(ctx, testSystem, ref, apitypes.AddVolumeRequest{Name: "data", MountPath: "/data"}); err != nil {
			t.Fatal(err)
		}
		rel, err := c.DeployImage(ctx, testSystem, ref, apitypes.DeployImageRequest{Image: "alpine:latest"})
		if err != nil {
			t.Fatal(err)
		}
		if r := waitRelease(t, c, ref, rel.Number); r.Status != apitypes.ReleaseActive {
			t.Fatalf("release %d of %s: %s %s", r.Number, ref, r.Status, r.Error)
		}
	}
	ss, err := c.loadService(ctx, 1, db)
	if err != nil {
		t.Fatal(err)
	}
	vols, err := c.store.ListVolumes(ctx, ss.svc.ID)
	if err != nil || len(vols) != 1 {
		t.Fatalf("volumes = %+v, %v", vols, err)
	}
	network := networkName(ss.envScope)

	got, err := c.DeleteEnvironment(ctx, testSystem, db.Env(), apitypes.DeleteEnvironmentRequest{Confirm: "staging"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Volumes != 1 || got.Services != 1 {
		t.Errorf("deleted = %+v", got)
	}

	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()
	ctrs, err := c.rt.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	kept := false
	for _, ct := range ctrs {
		switch ct.Labels.Environment {
		case strconv.FormatInt(ss.env.ID, 10):
			t.Errorf("container %s of the deleted environment is left", ct.Name)
		default:
			kept = kept || ct.Labels.Project == strconv.FormatInt(ss.project.ID, 10) && ct.State == node.StateRunning
		}
	}
	if !kept {
		t.Error("production's container isn't running")
	}
	if _, err := cli.VolumeInspect(ctx, vols[0].DockerName, client.VolumeInspectOptions{}); err == nil {
		t.Errorf("volume %s is left", vols[0].DockerName)
	}
	if _, err := cli.NetworkInspect(ctx, network, client.NetworkInspectOptions{}); err == nil {
		t.Errorf("network %s is left", network)
	}
	if _, err := c.ArchiveProject(ctx, testSystem, project); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeleteProject(ctx, testSystem, project, apitypes.DeleteProjectRequest{Confirm: project}); err != nil {
		t.Fatal(err)
	}
}
