//go:build integration

package core

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// TestIntegrationJobs runs a job on real Docker: run now in the service's
// image with its variables, its output kept in the history; a failing run
// raises job_failed.
func TestIntegrationJobs(t *testing.T) {
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
	if _, err := c.SetVariables(ctx, testSystem, app.Env(), apitypes.SetVariablesRequest{Service: "app", Set: map[string]string{"WHO": "cron"}}); err != nil {
		t.Fatal(err)
	}
	rel, err := c.DeployImage(ctx, testSystem, app, apitypes.DeployImageRequest{Image: "alpine:latest"})
	if err != nil {
		t.Fatal(err)
	}
	if r := waitRelease(t, c, app, rel.Number); r.Status != apitypes.ReleaseActive {
		t.Fatalf("release: %s %s", r.Status, r.Error)
	}
	defer func() { _, _ = c.ArchiveService(ctx, testSystem, app) }()
	run := func(command string) apitypes.CommandRun {
		t.Helper()
		if _, err := c.SetJob(ctx, testSystem, app, "nightly", apitypes.SetJobRequest{Schedule: "0 3 * * *", Command: command}); err != nil {
			t.Fatal(err)
		}
		r, err := c.RunJobNow(ctx, testSystem, app, "nightly")
		if err != nil {
			t.Fatal(err)
		}
		if r, err = c.WaitRun(ctx, testSystem, app, r.ID, time.Minute); err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := run(`echo "hello $WHO"`); r.Status != apitypes.RunSucceeded || r.Output != "hello cron\n" || r.Job != "nightly" {
		t.Fatalf("run: %+v", r)
	}
	if r := run("echo nope; exit 2"); r.Status != apitypes.RunFailed || r.ExitCode == nil || *r.ExitCode != 2 {
		t.Fatalf("failing run: %+v", r)
	}
	alerts, err := c.ListAlerts(ctx, testSystem, false, false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range alerts {
		found = found || a.Kind == apitypes.AlertJobFailed && a.Project == project
	}
	if !found {
		t.Fatalf("no job_failed alert: %+v", alerts)
	}
	if runs, err := c.JobRuns(ctx, testSystem, app, "nightly"); err != nil || len(runs) != 2 {
		t.Fatalf("history: %+v %v", runs, err)
	}
}
