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
