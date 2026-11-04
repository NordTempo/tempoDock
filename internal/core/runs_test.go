package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// runCmd runs a command in ref as a, waiting for its end.
func (h *harness) runCmd(a Actor, ref ServiceRef, req apitypes.RunCommandRequest) (apitypes.CommandRun, error) {
	h.t.Helper()
	return h.c.RunCommand(h.ctx, a, ref, req)
}

// lastJob returns the newest job the fake runtime ran.
func (h *harness) lastJob() node.JobSpec {
	h.t.Helper()
	jobs := h.rt.Jobs()
	if len(jobs) == 0 {
		h.t.Fatal("no job ran")
	}
	return jobs[len(jobs)-1]
}

// TestRunCommand: a run is a job in the active release's image with its
// variables, limits and network, recorded with its exit code and output's
// tail, and audited with who ran what and how it ended.
func TestRunCommand(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1", MemoryMB: 256, CPUs: 0.5})
	if _, err := h.c.SetVariables(h.ctx, testSystem, web.Env(), apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"GREETING": "hi"}}); err != nil {
		t.Fatal(err)
	}
	h.deploy(web, "app:1")
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		return node.JobResult{ExitCode: 3, Output: strings.Repeat("x", 2*apitypes.MaxRunOutputBytes) + "the end"}, nil
	})
	run, err := h.runCmd(testSystem, web, apitypes.RunCommandRequest{Command: "bin/seed --all"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != apitypes.RunFailed || run.ExitCode == nil || *run.ExitCode != 3 || run.Release != 1 {
		t.Fatalf("run = %+v", run)
	}
	job := h.lastJob()
	if job.Script != "bin/seed --all" || job.Env["GREETING"] != "hi" || job.Network == "" || len(job.Mounts) != 0 ||
		job.Resources.MemoryBytes != 256<<20 || job.Resources.NanoCPUs != 5e8 || job.Stream == nil || job.Labels.Release == "" {
		t.Fatalf("job = %+v", job)
	}
	got, err := h.c.GetRun(h.ctx, testSystem, web, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Output) != apitypes.MaxRunOutputBytes || !strings.HasSuffix(got.Output, "the end") {
		t.Fatalf("output tail is %d bytes", len(got.Output))
	}
	list, err := h.c.ListRuns(h.ctx, testSystem, web)
	if err != nil || len(list) != 1 || list[0].Output != "" || list[0].Release != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	evs, _ := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Action: "service.run"})
	if len(evs) != 1 || !strings.Contains(evs[0].Summary, "`bin/seed --all`: exit code 3") {
		t.Fatalf("audit = %+v", evs)
	}
}

// TestRunPermissions: running takes deploy in the environment, reading
// output too; the list is read. A locked environment refuses tokens.
func TestRunPermissions(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.deploy(web, "app:1")
	reader := Actor{Kind: "token", OrgID: 1, Name: "viewer", TokenID: 1, Scope: apitypes.ScopeRead}
	deployer := Actor{Kind: "token", OrgID: 1, Name: "ci", TokenID: 2, Scope: apitypes.ScopeDeploy}
	if _, err := h.runCmd(reader, web, apitypes.RunCommandRequest{Command: "ls"}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("read token runs: %v", err)
	}
	run, err := h.runCmd(deployer, web, apitypes.RunCommandRequest{Command: "ls"})
	if err != nil || run.Status != apitypes.RunSucceeded || run.CreatedBy != "ci" {
		t.Fatalf("deploy token: %+v, %v", run, err)
	}
	if _, err := h.c.GetRun(h.ctx, reader, web, run.ID); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("read token reads output: %v", err)
	}
	if list, err := h.c.ListRuns(h.ctx, reader, web); err != nil || len(list) != 1 {
		t.Fatalf("read token lists: %v, %v", list, err)
	}
	h.lock("production")
	if _, err := h.runCmd(deployer, web, apitypes.RunCommandRequest{Command: "ls"}); CodeOf(err) != apitypes.CodeEnvironmentLocked {
		t.Fatalf("token in a locked environment: %v", err)
	}
}

// TestRunChecks: a run needs an active release and a command within its
// limits, and a service runs at most maxRunsPerService at once.
func TestRunChecks(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	if _, err := h.runCmd(testSystem, web, apitypes.RunCommandRequest{Command: "ls"}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("without a release: %v", err)
	}
	h.deploy(web, "app:1")
	for _, req := range []apitypes.RunCommandRequest{
		{Command: "  "},
		{Command: strings.Repeat("a", apitypes.MaxCommandBytes+1)},
		{Command: "ls", TimeoutSeconds: apitypes.MaxRunTimeoutSeconds + 1},
		{Command: "ls", Volumes: apitypes.VolumesRead}, // no volumes
	} {
		if _, err := h.runCmd(testSystem, web, req); CodeOf(err) != apitypes.CodeValidation {
			t.Errorf("%.20q: %v", req.Command, err)
		}
	}
	var open []*Run
	for range maxRunsPerService {
		r, err := h.c.OpenRun(h.ctx, testSystem, web, apitypes.RunCommandRequest{Command: "sleep 1"}, false)
		if err != nil {
			t.Fatal(err)
		}
		open = append(open, r)
	}
	if _, err := h.runCmd(testSystem, web, apitypes.RunCommandRequest{Command: "ls"}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("over the limit: %v", err)
	}
	open[0].Abandon()
	if run, err := h.runCmd(testSystem, web, apitypes.RunCommandRequest{Command: "ls"}); err != nil || run.Status != apitypes.RunSucceeded {
		t.Fatalf("after one ended: %+v, %v", run, err)
	}
	for _, r := range open[1:] {
		r.Abandon()
	}
}

// TestRunVolumes: a stateful app's volumes mount read-only unless asked
// for write, one writer at a time; a database's never mount.
func TestRunVolumes(t *testing.T) {
	h := newHarness(t)
	app := h.service("app", apitypes.CreateServiceRequest{Image: "app:1"})
	if _, err := h.c.AddVolume(h.ctx, testSystem, app, apitypes.AddVolumeRequest{Name: "data", MountPath: "/data"}); err != nil {
		t.Fatal(err)
	}
	h.deploy(app, "app:1")
	if _, err := h.runCmd(testSystem, app, apitypes.RunCommandRequest{Command: "ls /data"}); err != nil {
		t.Fatal(err)
	}
	if m := h.lastJob().Mounts; len(m) != 1 || !m[0].ReadOnly || m[0].Path != "/data" {
		t.Fatalf("default mounts = %+v", m)
	}
	w, err := h.c.OpenRun(h.ctx, testSystem, app, apitypes.RunCommandRequest{Command: "seed", Volumes: apitypes.VolumesWrite}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.runCmd(testSystem, app, apitypes.RunCommandRequest{Command: "seed", Volumes: apitypes.VolumesWrite}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("second writer: %v", err)
	}
	if run := w.Wait(h.ctx, nil); run.Status != apitypes.RunSucceeded || run.Volumes != apitypes.VolumesWrite {
		t.Fatalf("writer = %+v", run)
	}
	if m := h.lastJob().Mounts; len(m) != 1 || m[0].ReadOnly {
		t.Fatalf("write mounts = %+v", m)
	}

	db := h.service("db", apitypes.CreateServiceRequest{Template: apitypes.TemplatePostgres})
	h.wait(db, 1) // a template deploys itself
	if _, err := h.runCmd(testSystem, db, apitypes.RunCommandRequest{Command: "psql", Volumes: apitypes.VolumesRead}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("database volume: %v", err)
	}
	if run, err := h.runCmd(testSystem, db, apitypes.RunCommandRequest{Command: "psql"}); err != nil || run.Volumes != apitypes.VolumesNone || len(h.lastJob().Mounts) != 0 {
		t.Fatalf("database run = %+v, %v", run, err)
	}
}

// TestRunEnds: a run past its timeout is timed_out, one whose caller
// went away canceled, and one a crash cut short interrupted and audited
// on the next start.
func TestRunEnds(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.deploy(web, "app:1")
	h.rt.OnJobContext(func(ctx context.Context, _ node.JobSpec) (node.JobResult, error) {
		<-ctx.Done()
		return node.JobResult{}, ctx.Err()
	})
	run, err := h.runCmd(testSystem, web, apitypes.RunCommandRequest{Command: "sleep 1d", TimeoutSeconds: 1})
	if err != nil || run.Status != apitypes.RunTimedOut || run.ExitCode != nil {
		t.Fatalf("timeout: %+v, %v", run, err)
	}
	ctx, cancel := context.WithTimeout(h.ctx, 50*time.Millisecond)
	defer cancel()
	if run, err := h.c.RunCommand(ctx, testSystem, web, apitypes.RunCommandRequest{Command: "sleep 1d"}); err != nil || run.Status != apitypes.RunCanceled {
		t.Fatalf("caller gone: %+v, %v", run, err)
	}
	if _, err := h.c.OpenRun(h.ctx, testSystem, web, apitypes.RunCommandRequest{Command: "never ends"}, false); err != nil {
		t.Fatal(err)
	}
	h.crash()
	list, err := h.c.ListRuns(h.ctx, testSystem, web)
	if err != nil || len(list) != 3 || list[0].Status != apitypes.RunInterrupted {
		t.Fatalf("after the crash: %+v, %v", list, err)
	}
	evs, _ := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Action: "service.run"})
	if len(evs) != 3 || !strings.Contains(evs[0].Summary, "`never ends`: interrupted") {
		t.Fatalf("audit = %+v", evs)
	}
}

// TestRunStreams: a streamed run hands its stream to the job.
func TestRunStreams(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.deploy(web, "app:1")
	r, err := h.c.OpenRun(h.ctx, testSystem, web, apitypes.RunCommandRequest{Command: "cat"}, true)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	run := r.Wait(h.ctx, &node.JobStream{Stdin: strings.NewReader("hello"), Stdout: &out, TTY: true})
	if out.String() != "hello" || run.Status != apitypes.RunSucceeded || !run.TTY || run.Output != "" {
		t.Fatalf("run = %+v, out %q", run, out.String())
	}
}
