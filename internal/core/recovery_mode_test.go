package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/cpbackup"
	"github.com/NordTempo/tempoDock/internal/node"
)

// recover restarts the harness's daemon in recovery mode, as after
// `dock admin restore --recovery`, and returns the recovery file's token.
// With lose, every container is gone when it starts, as on a new server.
func (h *harness) recover(lose bool) string {
	h.t.Helper()
	dir := h.opts.Config.DataDir
	if err := cpbackup.MarkRecovering(dir, "a backup of old taken earlier"); err != nil {
		h.t.Fatal(err)
	}
	rec, err := cpbackup.ReadRecovery(dir)
	if err != nil || rec == nil {
		h.t.Fatalf("recovery file: %+v, %v", rec, err)
	}
	h.opts.Recovery = rec
	h.c.Close()
	if lose {
		ctrs, err := h.rt.List(h.ctx)
		if err != nil {
			h.t.Fatal(err)
		}
		for _, ct := range ctrs {
			_ = h.rt.Stop(h.ctx, ct.ID, 0)
			if err := h.rt.Remove(h.ctx, ct.ID, node.RemoveOptions{}); err != nil {
				h.t.Fatal(err)
			}
		}
	}
	h.c = h.start()
	return rec.Token
}

// listed is how the repository lists ref's newest snapshot to a recovery
// (backup.NewestScript).
func (h *harness) listed(ref ServiceRef, snapshot string, at time.Time) string {
	h.t.Helper()
	ss, err := h.c.serviceFor(h.ctx, testSystem, ref, apitypes.ScopeRead, "testing")
	if err != nil {
		h.t.Fatal(err)
	}
	return fmt.Sprintf("\"time\":%q\n\"tags\":[%q]\n\"id\":%q\n", at.Format(time.RFC3339), backup.Tag(ss.svc.ID), snapshot)
}

// The plan of a recovery names each stateful service's newest backup in
// the bucket and what has none; while the mode lasts the scheduler runs
// nothing, and ending it removes the file.
func TestRecoveryPlanAndMode(t *testing.T) {
	h := newHarness(t)
	h.offBoxBackups()
	db := h.templateDB("db", apitypes.TemplatePostgres)
	h.volumeService("files")
	// The bucket holds a backup taken after the control plane's own: the
	// restored database doesn't know it, the plan does.
	const recorded, snap = "aaaa1111aaaa", "dddd4444dddd"
	listing := backup.NewestMarker + "\n" + h.listed(db, snap, time.Now())
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		switch {
		case strings.Contains(spec.Name, "_backup"):
			return node.JobResult{Output: resticOutput(recorded, 4096, recorded)}, nil
		case strings.Contains(spec.Name, "recovery_list"):
			return node.JobResult{Output: listing}, nil
		}
		return node.JobResult{}, nil
	})
	if run := h.runBackup(db); run.Status != apitypes.BackupSucceeded {
		t.Fatalf("backup = %+v", run)
	}
	if _, err := h.c.RecoveryPlan(h.ctx, testSystem); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("plan outside recovery mode: %v", err)
	}

	token := h.recover(false)
	if !h.alertOpen(recoveryAlertKey) {
		t.Error("no alert says the control plane is in recovery mode")
	}
	if _, ok := h.c.RecoveryActor("not-" + token); ok {
		t.Fatal("a wrong token was taken")
	}
	a, ok := h.c.RecoveryActor(token)
	if !ok {
		t.Fatal("the recovery file's token was refused")
	}
	plan, err := h.c.RecoveryPlan(h.ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Services) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	got := map[string]apitypes.RecoveryService{}
	for _, s := range plan.Services {
		got[s.Service] = s
	}
	if s := got["db"]; s.State != apitypes.RecoveryRestore || s.Snapshot != snap || s.Kind != apitypes.BackupPostgres || s.BackedUpAt == nil {
		t.Errorf("db = %+v", s)
	}
	if s := got["files"]; s.State != apitypes.RecoveryNone || s.Kind != apitypes.BackupVolume || s.Snapshot != "" {
		t.Errorf("files = %+v", s)
	}

	jobs := len(h.rt.Jobs())
	if next, err := h.c.backupPass(h.ctx); err != nil || !next.IsZero() || len(h.rt.Jobs()) != jobs {
		t.Fatalf("the scheduler ran in recovery mode: next %v, err %v", next, err)
	}
	if next, err := h.c.jobPass(h.ctx); err != nil || !next.IsZero() {
		t.Fatalf("scheduled jobs ran in recovery mode: next %v, err %v", next, err)
	}

	if err := h.c.EndRecovery(h.ctx, a); err != nil {
		t.Fatal(err)
	}
	if rec, err := cpbackup.ReadRecovery(h.opts.Config.DataDir); err != nil || rec != nil || h.c.Recovering() {
		t.Fatalf("still in recovery mode: %+v, %v", rec, err)
	}
	if _, ok := h.c.RecoveryActor(token); ok {
		t.Fatal("the token outlived recovery mode")
	}
	if h.alertOpen(recoveryAlertKey) {
		t.Error("the recovery mode alert outlived the mode")
	}
	h.opts.Recovery = nil
}

// Backups in a server's local repository went with the server.
func TestRecoveryPlanLocalRepositoryIsLost(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		if strings.Contains(spec.Name, "_backup") {
			return node.JobResult{Output: resticOutput("bbbb2222bbbb", 4096, "bbbb2222bbbb")}, nil
		}
		return node.JobResult{}, nil
	})
	if run := h.runBackup(db); run.Status != apitypes.BackupSucceeded {
		t.Fatalf("backup = %+v", run)
	}
	h.recover(false)
	plan, err := h.c.RecoveryPlan(h.ctx, testSystem)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Services) != 1 || plan.Services[0].State != apitypes.RecoveryLost || plan.Repository != "local" {
		t.Fatalf("plan = %+v", plan)
	}
}

// A recovery fills volumes before their service starts, loads a
// database's dump once it runs alone, and brings services back
// databases first; the report says what came back, and a second run
// leaves what runs alone.
func TestRecoveryRun(t *testing.T) {
	h := newHarness(t)
	h.offBoxBackups()
	db := h.templateDB("db", apitypes.TemplatePostgres)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 8080})
	h.deploy(web, "app:1")
	files := h.volumeService("files")
	listing := backup.NewestMarker + "\n" + h.listed(db, "aaaa1111aaaa", time.Now()) + h.listed(files, "aaaa1111aaaa", time.Now())
	var steps []string
	var mu sync.Mutex
	step := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		steps = append(steps, s)
	}
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		switch {
		case strings.Contains(spec.Name, "_backup"):
			return node.JobResult{Output: resticOutput("aaaa1111aaaa", 4096, "aaaa1111aaaa")}, nil
		case strings.Contains(spec.Name, "recovery_list"):
			return node.JobResult{Output: listing}, nil
		case strings.Contains(spec.Name, "_restore"):
			step("load " + spec.Env["PGHOST"])
		}
		return node.JobResult{}, nil
	})
	for _, ref := range []ServiceRef{db, files} {
		if run := h.runBackup(ref); run.Status != apitypes.BackupSucceeded {
			t.Fatalf("backup = %+v", run)
		}
	}
	h.recover(true)
	old := recoverVolumes
	t.Cleanup(func() { recoverVolumes = old })
	recoverVolumes = func(c *Core, _ context.Context, ss svcScope, snapshot string, _ int64) (string, error) {
		if left, _ := c.rt.List(h.ctx); len(left) != 1 {
			t.Errorf("volumes of %s filled with %d containers running, want only the database", ss, len(left))
		}
		step("fill " + ss.svc.Name + " " + snapshot)
		return "filled", nil
	}

	run := func() apitypes.RecoveryReport {
		t.Helper()
		if err := h.c.StartRecovery(h.ctx, testSystem); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			rep, err := h.c.RecoveryReport(h.ctx, testSystem)
			if err != nil {
				t.Fatal(err)
			}
			if !rep.Running {
				return rep
			}
			if time.Now().After(deadline) {
				t.Fatalf("recovery still running: %+v", rep)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	rep := run()
	var got []string
	for _, r := range rep.Results {
		got = append(got, r.Service+" "+string(r.Outcome))
		if r.Outcome == apitypes.RecoveryFailed {
			t.Errorf("%s: %s", r.Service, r.Detail)
		}
	}
	if want := []string{"db restored", "files restored", "web started"}; !slices.Equal(got, want) {
		t.Fatalf("report = %v, want %v", got, want)
	}
	if want := []string{"load db", "fill files aaaa1111aaaa"}; !slices.Equal(steps, want) {
		t.Fatalf("steps = %v, want %v", steps, want)
	}
	if running := h.rt.Running(); len(running) != 3 {
		t.Fatalf("running = %v, want the three services", running)
	}

	for _, r := range run().Results {
		if r.Outcome != apitypes.RecoveryRunning {
			t.Errorf("second run: %s %s (%s)", r.Service, r.Outcome, r.Detail)
		}
	}
	h.opts.Recovery = nil
}

// A service whose volumes can't be filled is not started on empty ones,
// and one that was never backed up starts empty and the report says so.
func TestRecoveryRunFailedFillAndNoBackup(t *testing.T) {
	h := newHarness(t)
	h.offBoxBackups()
	files := h.volumeService("files")
	listing := backup.NewestMarker + "\n" + h.listed(files, "cccc3333cccc", time.Now())
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		switch {
		case strings.Contains(spec.Name, "_backup"):
			return node.JobResult{Output: resticOutput("cccc3333cccc", 4096, "cccc3333cccc")}, nil
		case strings.Contains(spec.Name, "recovery_list"):
			return node.JobResult{Output: listing}, nil
		}
		return node.JobResult{}, nil
	})
	if run := h.runBackup(files); run.Status != apitypes.BackupSucceeded {
		t.Fatalf("backup = %+v", run)
	}
	uploads := h.service("uploads", apitypes.CreateServiceRequest{Port: 8080})
	if _, err := h.c.AddVolume(h.ctx, testSystem, uploads, apitypes.AddVolumeRequest{Name: "data", MountPath: "/data"}); err != nil {
		t.Fatal(err)
	}
	h.deploy(uploads, "app:1")
	h.recover(true)
	old := recoverVolumes
	t.Cleanup(func() { recoverVolumes = old })
	recoverVolumes = func(*Core, context.Context, svcScope, string, int64) (string, error) {
		return "restic: wrong password", errors.New("the bucket refused the keys")
	}
	if err := h.c.StartRecovery(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	var rep apitypes.RecoveryReport
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(2 * time.Millisecond) {
		var err error
		if rep, err = h.c.RecoveryReport(h.ctx, testSystem); err != nil {
			t.Fatal(err)
		}
		if !rep.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recovery still running: %+v", rep)
		}
	}
	got := map[string]apitypes.RecoveryResult{}
	for _, r := range rep.Results {
		got[r.Service] = r
	}
	if r := got["files"]; r.Outcome != apitypes.RecoveryFailed || !strings.Contains(r.Detail, "the bucket refused the keys") {
		t.Errorf("files = %+v", r)
	}
	if r := got["uploads"]; r.Outcome != apitypes.RecoveryEmpty {
		t.Errorf("uploads = %+v", r)
	}
	if running := h.rt.Running(); len(running) != 1 {
		t.Errorf("running = %v, want only the service without a backup", running)
	}
	runs, err := h.c.ServiceBackups(h.ctx, testSystem, files)
	if err != nil {
		t.Fatal(err)
	}
	if last := runs.Runs[0]; last.Operation != "restore" || last.Status != apitypes.BackupFailed {
		t.Errorf("the failed restore is not in the backup list: %+v", last)
	}
	h.opts.Recovery = nil
}

// A service whose image went with the lost server's registry is built
// again from the commit it ran, when it has a GitHub source.
func TestRecoveryRebuildsFromGitHub(t *testing.T) {
	h, _ := gitHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.connect(web, apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main"})
	if _, err := h.c.GitDeploy(h.ctx, testSystem, web, apitypes.GitDeployRequest{}); err != nil {
		t.Fatal(err)
	}
	h.gitSettled(web)
	h.wait(web, 1)
	h.recover(true)
	// The registry went with the server: the image is nowhere.
	actives, err := h.store.ListActiveReleases(h.ctx)
	if err != nil || len(actives) != 1 {
		t.Fatalf("active releases: %+v, %v", actives, err)
	}
	if err := h.rt.RemoveImage(h.ctx, actives[0].ImageRef); err != nil {
		t.Fatal(err)
	}
	h.rt.FailPull(errors.New("manifest unknown"))
	if err := h.c.StartRecovery(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	var rep apitypes.RecoveryReport
	eventually(t, "the recovery to finish", func() bool {
		var err error
		rep, err = h.c.RecoveryReport(h.ctx, testSystem)
		return err == nil && !rep.Running
	})
	if len(rep.Results) != 1 || rep.Results[0].Outcome != apitypes.RecoveryRebuilding || !strings.Contains(rep.Results[0].Detail, "acme/shop") {
		t.Fatalf("report = %+v", rep.Results)
	}
	eventually(t, "the rebuild to go live", func() bool {
		rels, err := h.c.ListReleases(h.ctx, testSystem, web, 10)
		return err == nil && len(rels) == 3 && rels[0].Status == apitypes.ReleaseActive && rels[0].Commit != nil && rels[0].Commit.SHA == shaC
	})
	h.opts.Recovery = nil
}
