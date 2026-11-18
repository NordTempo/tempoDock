package core

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cpbackup"
	"github.com/NordTempo/tempoDock/internal/node"
)

// recover restarts the harness's daemon in recovery mode, as after
// `dock admin restore --recovery`, and returns the recovery file's token.
func (h *harness) recover() string {
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
	h.restart()
	return rec.Token
}

// The plan of a recovery names each stateful service's newest backup in
// the bucket and what has none; while the mode lasts the scheduler runs
// nothing, and ending it removes the file.
func TestRecoveryPlanAndMode(t *testing.T) {
	h := newHarness(t)
	h.offBoxBackups()
	db := h.templateDB("db", apitypes.TemplatePostgres)
	h.volumeService("files")
	const snap = "aaaa1111aaaa"
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		if strings.Contains(spec.Name, "_backup") {
			return node.JobResult{Output: resticOutput(snap, 4096, snap)}, nil
		}
		return node.JobResult{}, nil
	})
	if run := h.runBackup(db); run.Status != apitypes.BackupSucceeded {
		t.Fatalf("backup = %+v", run)
	}
	if _, err := h.c.RecoveryPlan(h.ctx, testSystem); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("plan outside recovery mode: %v", err)
	}

	token := h.recover()
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

	if err := h.c.EndRecovery(h.ctx, a); err != nil {
		t.Fatal(err)
	}
	if rec, err := cpbackup.ReadRecovery(h.opts.Config.DataDir); err != nil || rec != nil || h.c.Recovering() {
		t.Fatalf("still in recovery mode: %+v, %v", rec, err)
	}
	if _, ok := h.c.RecoveryActor(token); ok {
		t.Fatal("the token outlived recovery mode")
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
	h.recover()
	plan, err := h.c.RecoveryPlan(h.ctx, testSystem)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Services) != 1 || plan.Services[0].State != apitypes.RecoveryLost || plan.Repository != "local" {
		t.Fatalf("plan = %+v", plan)
	}
}
