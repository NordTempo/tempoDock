package core

import (
	"archive/tar"
	"bytes"
	"io"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// buildLog reads a release's whole build log.
func (h *harness) buildLog(ref ServiceRef, number int64) string {
	h.t.Helper()
	rc, err := h.c.BuildLog(h.ctx, testSystem, ref, number)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	b, _ := io.ReadAll(rc)
	return string(b)
}

// The pre-deploy command runs with the new image, the service's variables
// and its environment network, before the new container starts.
func TestPreDeployRunsBeforeStart(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{PreDeployCommand: "migrate up", MemoryMB: 256})
	h.setVars(web, map[string]string{"DATABASE_URL": "postgres://db/app"})
	h.rt.OnJob(func(node.JobSpec) (node.JobResult, error) {
		return node.JobResult{Output: "applied 2 migrations\n"}, nil
	})
	rel := h.deploy(web, "app:1")
	if rel.Status != apitypes.ReleaseActive || rel.Config.PreDeployCommand != "migrate up" {
		t.Fatalf("release = %+v", rel)
	}
	jobs := h.rt.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("jobs = %+v", jobs)
	}
	j, ctr := jobs[0], h.ctr(web, rel.Number)
	if j.Script != "migrate up" || j.Image != rel.ImageID || j.Env["DATABASE_URL"] != "postgres://db/app" ||
		j.Network == "" || j.Name != ctr+"_predeploy" || j.Resources.MemoryBytes != 256<<20 || len(j.Mounts) != 0 {
		t.Fatalf("job = %+v", j)
	}
	ops := h.rt.Ops()
	if i, k := indexOf(ops, "job "+j.Name), indexOf(ops, "start "+ctr); i < 0 || k < i {
		t.Fatalf("job must run before the container starts: %v", ops)
	}
	if log := h.buildLog(web, rel.Number); !strings.Contains(log, "$ migrate up\napplied 2 migrations\n") {
		t.Fatalf("build log: %s", log)
	}

	// Restarts and rollbacks run an image that already served traffic.
	if _, err := h.c.Restart(h.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	h.wait(web, 2)
	if _, err := h.c.Rollback(h.ctx, testSystem, web, 1); err != nil {
		t.Fatal(err)
	}
	h.wait(web, 3)
	if n := len(h.rt.Jobs()); n != 1 {
		t.Fatalf("restart and rollback ran %d more jobs", n-1)
	}
}

// A failing command fails the release before anything is started or
// stopped: the old release keeps serving, even for a stateful service,
// and the output is in the build log and the release's events.
func TestPreDeployFailureKeepsOldRelease(t *testing.T) {
	h := newHarness(t)
	db := h.service("db", apitypes.CreateServiceRequest{})
	if _, err := h.c.AddVolume(h.ctx, testSystem, db, apitypes.AddVolumeRequest{Name: "data", MountPath: "/data", Apply: true}); err != nil {
		t.Fatal(err)
	}
	h.deploy(db, "app:1")
	if _, err := h.c.UpdateService(h.ctx, testSystem, db, apitypes.UpdateServiceRequest{PreDeployCommand: ptr("migrate up")}); err != nil {
		t.Fatal(err)
	}
	h.rt.OnJob(func(node.JobSpec) (node.JobResult, error) {
		return node.JobResult{ExitCode: 1, Output: "connecting\nerror: relation \"users\" already exists\n"}, nil
	})
	ops := len(h.rt.Ops())
	rel := h.deploy(db, "app:2")
	if rel.Status != apitypes.ReleaseFailed || !strings.Contains(rel.Error, "pre-deploy command exited with code 1") {
		t.Fatalf("release = %+v", rel)
	}
	for _, op := range h.rt.Ops()[ops:] {
		if strings.HasPrefix(op, "start ") || strings.HasPrefix(op, "stop ") {
			t.Fatalf("a failed pre-deploy command must not touch containers: %v", h.rt.Ops()[ops:])
		}
	}
	if svc, err := h.c.GetService(h.ctx, testSystem, db); err != nil || svc.ActiveRelease == nil || *svc.ActiveRelease != 1 {
		t.Fatalf("active release = %v, %v", svc.ActiveRelease, err)
	}
	if log := h.buildLog(db, rel.Number); !strings.Contains(log, `relation "users" already exists`) {
		t.Fatalf("build log: %s", log)
	}
	full, err := h.c.GetRelease(h.ctx, testSystem, db, rel.Number)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(full.Events, func(e apitypes.ReleaseEvent) bool {
		return strings.Contains(e.Message, `error: relation "users" already exists`)
	}) {
		t.Fatalf("events: %+v", full.Events)
	}
}

// dock.toml's pre-deploy command and timeout win over the service's.
func TestPreDeployFromRepoFile(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{PreDeployCommand: "from settings"})
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := "[deploy]\npre_deploy_command = \"npm run migrate\"\npre_deploy_timeout = \"20m\"\npre_deploy_snapshot = true\n"
	_ = tw.WriteHeader(&tar.Header{Name: "dock.toml", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	rel, err := h.c.DeploySource(h.ctx, testSystem, web, &buf, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	r := h.wait(web, rel.Number)
	if r.Status != apitypes.ReleaseActive || r.Config.PreDeployCommand != "npm run migrate" || !r.Config.PreDeploySnapshot ||
		!slices.Equal(r.Config.FromRepoFile, []string{"pre_deploy_command", "pre_deploy_snapshot", "pre_deploy_timeout_seconds"}) {
		t.Fatalf("release = %+v", r)
	}
	if jobs := h.rt.Jobs(); len(jobs) != 1 || jobs[0].Script != "npm run migrate" {
		t.Fatalf("jobs = %+v", jobs)
	}
}

func TestPreDeploySettingsValidation(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	for _, req := range []apitypes.UpdateServiceRequest{
		{PreDeployTimeoutSeconds: ptr(3601)},
		{PreDeployTimeoutSeconds: ptr(-1)},
		{PreDeployCommand: ptr("  ")},
	} {
		if _, err := h.c.UpdateService(h.ctx, testSystem, web, req); CodeOf(err) != apitypes.CodeValidation {
			t.Errorf("UpdateService(%+v) = %v, want a validation error", req, err)
		}
	}
	reader := h.user("reader@example.com", apitypes.ScopeRead)
	if _, err := h.c.UpdateService(h.ctx, reader, web, apitypes.UpdateServiceRequest{PreDeployCommand: ptr("x")}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("read-only user = %v", err)
	}
}

// snapshotJobIndexes returns the positions of the snapshot jobs and of
// the job running script among the fake runtime's jobs.
func (h *harness) snapshotJobIndexes(script string) (snaps, cmds []int) {
	for i, j := range h.rt.Jobs() {
		switch j.Script {
		case snapshotScript:
			snaps = append(snaps, i)
		case script:
			cmds = append(cmds, i)
		}
	}
	return snaps, cmds
}

// With pre_deploy_snapshot on, every deployed stateful service in the
// environment is snapshotted before the command runs, the release noting
// each; off, nothing is.
func TestPreDeploySnapshotsStatefulServicesFirst(t *testing.T) {
	h := newHarness(t)
	h.snapshotJobs(nil)
	db := h.volumeService("db")
	web := h.service("web", apitypes.CreateServiceRequest{PreDeployCommand: "migrate up", PreDeploySnapshot: true})
	rel := h.deploy(web, "app:1")
	if rel.Status != apitypes.ReleaseActive || !rel.Config.PreDeploySnapshot {
		t.Fatalf("release = %+v", rel)
	}
	snaps, cmds := h.snapshotJobIndexes("migrate up")
	if len(snaps) != 1 || len(cmds) != 1 || snaps[0] > cmds[0] {
		t.Fatalf("one snapshot must run before the command: snapshots at %v, command at %v", snaps, cmds)
	}
	list, err := h.c.ServiceSnapshots(h.ctx, testSystem, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Snapshots) != 1 || list.Snapshots[0].Trigger != apitypes.SnapshotBeforeDeploy || list.Snapshots[0].Status != apitypes.SnapshotReady ||
		list.Snapshots[0].Note != "before web release 1's pre-deploy command" {
		t.Fatalf("snapshots = %+v", list.Snapshots)
	}
	msg := "Snapshot " + strconv.FormatInt(list.Snapshots[0].ID, 10) + " of db taken before the pre-deploy command"
	full, err := h.c.GetRelease(h.ctx, testSystem, web, rel.Number)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(full.Events, func(e apitypes.ReleaseEvent) bool { return e.Message == msg }) {
		t.Fatalf("events: %+v", full.Events)
	}
	if log := h.buildLog(web, rel.Number); !strings.Contains(log, msg+"\n") {
		t.Fatalf("build log: %s", log)
	}

	api := h.service("api", apitypes.CreateServiceRequest{PreDeployCommand: "migrate api"})
	h.deploy(api, "app:1")
	if snaps, cmds := h.snapshotJobIndexes("migrate api"); len(snaps) != 1 || len(cmds) != 1 {
		t.Fatalf("off: snapshots at %v, command at %v", snaps, cmds)
	}
}

// A failed snapshot fails the release before the command runs, naming
// the setting that skips it; the previous release keeps serving.
func TestPreDeploySnapshotFailureStopsTheRelease(t *testing.T) {
	h := newHarness(t)
	h.snapshotJobs(nil)
	h.volumeService("db")
	web := h.service("web", apitypes.CreateServiceRequest{PreDeployCommand: "migrate up", PreDeploySnapshot: true})
	h.deploy(web, "app:1")
	h.snapshotJobs(func(spec node.JobSpec) *node.JobResult {
		if spec.Script == snapshotScript {
			return &node.JobResult{ExitCode: 1, Output: "cp: read error"}
		}
		return nil
	})
	rel := h.deploy(web, "app:2")
	if rel.Status != apitypes.ReleaseFailed || !strings.Contains(rel.Error, "snapshot of db before the pre-deploy command failed") ||
		!strings.Contains(rel.Error, "--pre-deploy-snapshot=false") {
		t.Fatalf("release = %+v", rel)
	}
	if _, cmds := h.snapshotJobIndexes("migrate up"); len(cmds) != 1 {
		t.Fatalf("the command ran %d times; only release 1 may run it", len(cmds))
	}
	if svc, err := h.c.GetService(h.ctx, testSystem, web); err != nil || svc.ActiveRelease == nil || *svc.ActiveRelease != 1 {
		t.Fatalf("active release = %v, %v", svc.ActiveRelease, err)
	}
}

// A volume added to a running service exists only once the release that
// mounts it starts: the pre-deploy snapshot of that release skips it.
func TestPreDeploySnapshotSkipsANewVolume(t *testing.T) {
	h := newHarness(t)
	h.snapshotJobs(nil)
	web := h.service("web", apitypes.CreateServiceRequest{PreDeployCommand: "migrate up", PreDeploySnapshot: true})
	h.deploy(web, "app:1")
	if _, err := h.c.AddVolume(h.ctx, testSystem, web, apitypes.AddVolumeRequest{Name: "data", MountPath: "/data"}); err != nil {
		t.Fatal(err)
	}
	applied, err := h.c.ApplyChanges(h.ctx, testSystem, web.Env())
	if err != nil || len(applied.Releases) != 1 {
		t.Fatalf("apply = %+v, %v", applied, err)
	}
	if r := h.wait(web, applied.Releases[0].Number); r.Status != apitypes.ReleaseActive {
		t.Fatalf("release with the new volume = %+v", r)
	}
	if list, err := h.c.ServiceSnapshots(h.ctx, testSystem, web); err != nil || len(list.Snapshots) != 0 {
		t.Errorf("snapshots of web = %+v, %v; its new volume had nothing to copy", list.Snapshots, err)
	}
}

// A failed pre-deploy command's last lines are in the release for
// everyone who sees the environment, so the release's secret values are
// masked in them, as the build log masks them for someone who can't
// reveal: a migration tool that prints the URL it couldn't reach shows
// the mask, also to someone who may not read the logs.
func TestPreDeployOutputInTheReleaseIsMasked(t *testing.T) {
	h := newHarness(t)
	h.enforce()
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	const secret = "postgres://app:s3cr3t-Pa55w0rd@db.internal:5432/shop"
	if _, err := h.c.SetVariables(h.ctx, testSystem, web.Env(), apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"DATABASE_URL": secret}, Apply: true}); err != nil {
		t.Fatal(err)
	}
	h.wait(web, h.deploy(web, "app:1").Number)
	if _, err := h.c.UpdateService(h.ctx, testSystem, web, apitypes.UpdateServiceRequest{PreDeployCommand: ptr("migrate up")}); err != nil {
		t.Fatal(err)
	}
	// What a migration tool prints when it can't connect.
	h.rt.OnJob(func(node.JobSpec) (node.JobResult, error) {
		return node.JobResult{ExitCode: 1, Output: "connecting\nerror: dial " + secret + ": connection refused\n"}, nil
	})
	rel := h.deploy(web, "app:2")
	if rel.Status != apitypes.ReleaseFailed {
		t.Fatalf("release = %+v", rel)
	}
	admin := h.user("ada@example.com", apitypes.ScopeAdmin)
	viewer := h.user("vw@example.com", apitypes.ScopeRead)                                           // Viewers: logs:view, no reveal
	watcher := grantedPerson(h, admin, "st@example.com", apitypes.GrantRequest{Role: "deploy:view"}) // sees status only

	// Control: the build log masks it for the Viewer, and refuses the watcher.
	rc, err := h.c.BuildLog(h.ctx, viewer, web, rel.Number)
	if err != nil {
		t.Fatal(err)
	}
	log, _ := io.ReadAll(rc)
	_ = rc.Close()
	if strings.Contains(string(log), "s3cr3t") || !strings.Contains(string(log), secretMask) {
		t.Fatalf("the viewer's build log isn't masked: %s", log)
	}
	if _, err := h.c.BuildLog(h.ctx, watcher, web, rel.Number); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("deploy:view reading the build log: %v", err)
	}

	for _, a := range []Actor{viewer, watcher} {
		full, err := h.c.GetRelease(h.ctx, a, web, rel.Number)
		if err != nil {
			t.Fatal(err)
		}
		shown := false
		for _, e := range full.Events {
			if strings.Contains(e.Message, "s3cr3t") {
				t.Errorf("%s read DATABASE_URL from release %d's events: %q", a.Name, rel.Number, e.Message)
			}
			shown = shown || strings.Contains(e.Message, "error: dial "+secretMask+": connection refused")
		}
		if !shown {
			t.Errorf("%s should see the command's last lines, masked: %+v", a.Name, full.Events)
		}
	}
}
