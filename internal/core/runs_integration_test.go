//go:build integration

package core

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// TestIntegrationRunCommand runs commands in a deployed service on real
// Docker: they see its variables, a stateful service's volume read-only
// by default and writable when asked, and leave no container behind.
func TestIntegrationRunCommand(t *testing.T) {
	t.Parallel()
	c := dockerHarness(t)
	ctx := context.Background()
	project := fmt.Sprintf("it-%d", time.Now().UnixNano()%1_000_000)
	if _, err := c.CreateProject(ctx, testSystem, apitypes.CreateProjectRequest{Name: project}); err != nil {
		t.Fatal(err)
	}
	app := ServiceRef{project, "production", "app"}
	if _, err := c.CreateService(ctx, testSystem, app.Env(), apitypes.CreateServiceRequest{Name: "app", StartCommand: "trap 'exit 0' TERM; while true; do sleep 1; done", HealthCheckTimeoutSeconds: 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetVariables(ctx, testSystem, app.Env(), apitypes.SetVariablesRequest{Service: "app", Set: map[string]string{"GREETING": "hej"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddVolume(ctx, testSystem, app, apitypes.AddVolumeRequest{Name: "data", MountPath: "/data"}); err != nil {
		t.Fatal(err)
	}
	rel, err := c.DeployImage(ctx, testSystem, app, apitypes.DeployImageRequest{Image: "alpine:latest"})
	if err != nil {
		t.Fatal(err)
	}
	if r := waitRelease(t, c, app, rel.Number); r.Status != apitypes.ReleaseActive {
		t.Fatalf("release: %s %s", r.Status, r.Error)
	}
	run := func(req apitypes.RunCommandRequest) apitypes.CommandRun {
		t.Helper()
		r, err := c.RunCommand(ctx, testSystem, app, req)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := run(apitypes.RunCommandRequest{Command: `echo "$GREETING"`}); r.Status != apitypes.RunSucceeded || r.Output != "hej\n" {
		t.Fatalf("variables: %+v", r)
	}
	if r := run(apitypes.RunCommandRequest{Command: "touch /data/x"}); r.Status != apitypes.RunFailed || r.ExitCode == nil || *r.ExitCode == 0 {
		t.Fatalf("read-only volume written: %+v", r)
	}
	if r := run(apitypes.RunCommandRequest{Command: "touch /data/x && ls /data", Volumes: apitypes.VolumesWrite}); r.Status != apitypes.RunSucceeded || !strings.Contains(r.Output, "x") {
		t.Fatalf("write volume: %+v", r)
	}
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()
	ss, err := c.loadService(ctx, 1, app)
	if err != nil {
		t.Fatal(err)
	}
	f := make(client.Filters).Add("label", node.LabelService+"="+fmt.Sprint(ss.svc.ID)).Add("label", node.LabelRole+"="+node.RoleJob)
	if left, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f}); err != nil || len(left.Items) != 0 {
		t.Fatalf("run containers left behind: %d, %v", len(left.Items), err)
	}
	if _, err := c.ArchiveService(ctx, testSystem, app); err != nil {
		t.Fatal(err)
	}
}
