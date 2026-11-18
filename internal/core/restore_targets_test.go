package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// restoreAlerts are the open failed-restore alerts.
func restoreAlerts(h *harness) []apitypes.Alert {
	h.t.Helper()
	var out []apitypes.Alert
	for _, a := range siteAlerts(h, apitypes.AlertBackupFailed) {
		if strings.HasPrefix(a.Title, "Restore of ") {
			out = append(out, a)
		}
	}
	return out
}

// restoreBackup starts restoring snapshot snap of ref into the default
// name, once ref is free.
func (h *harness) restoreBackup(ref ServiceRef, snap string) apitypes.RestoreResult {
	h.t.Helper()
	var res apitypes.RestoreResult
	h.untilFree(func() (err error) {
		res, err = h.c.RestoreBackup(h.ctx, testSystem, ref, apitypes.RestoreBackupRequest{Snapshot: snap})
		return err
	})
	return res
}

func TestSwitchReferencesRefusesARunningRestore(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	snap := "aaaa1111aaaa1111"
	loaded := make(chan struct{})
	load := sync.OnceFunc(func() { close(loaded) })
	t.Cleanup(load)
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		if strings.Contains(spec.Name, "_restore") {
			<-loaded
		}
		return node.JobResult{Output: resticOutput(snap, 100, snap)}, nil
	})
	h.runBackup(db)
	res := h.restoreBackup(db, snap)
	sw := func() error {
		_, err := h.c.SwitchReferences(h.ctx, testSystem, db, apitypes.SwitchReferencesRequest{To: "db-restored", DryRun: true})
		return err
	}
	if err := sw(); CodeOf(err) != apitypes.CodeConflict || !strings.Contains(err.Error(), "still being restored") {
		t.Fatalf("switching to a service that is loading: %v, want it refused as still being restored", err)
	}
	load()
	if done := h.waitRun(db, res.Restore.ID); done.Status != apitypes.BackupSucceeded {
		t.Fatalf("restore = %+v", done)
	}
	if err := sw(); err != nil {
		t.Errorf("switching to a restored service: %v", err)
	}
}

func TestFailedRestoreFreesItsName(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	snap := "bbbb2222bbbb2222"
	var mu sync.Mutex
	fail := true
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		mu.Lock()
		defer mu.Unlock()
		if fail && strings.Contains(spec.Name, "_restore") {
			return node.JobResult{ExitCode: 1, Output: "pg_restore: error: out of disk"}, nil
		}
		return node.JobResult{Output: resticOutput(snap, 100, snap)}, nil
	})
	h.runBackup(db)
	res := h.restoreBackup(db, snap)
	if done := h.waitRun(db, res.Restore.ID); done.Status != apitypes.BackupFailed {
		t.Fatalf("restore = %+v, want failed", done)
	}

	// Whoever reads "failed" finds the target archived under its other
	// name, refused as what references switch to, and an alert open.
	kept := ServiceRef{db.Project, db.Environment, failedTargetName("db-restored", restoreInto{id: res.Restore.ID})}
	if kept.Service != "db-restored-failed-"+itoa(res.Restore.ID) {
		t.Fatalf("the failed target's name = %s", kept.Service)
	}
	ss, err := h.c.loadService(h.ctx, 1, kept)
	if err != nil || ss.svc.ArchivedAt == nil {
		t.Fatalf("the failed target %s: %v, archived %v; want it archived", kept.Service, err, ss.svc.ArchivedAt)
	}
	_, err = h.c.SwitchReferences(h.ctx, testSystem, db, apitypes.SwitchReferencesRequest{To: kept.Service})
	if CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "restore failed") {
		t.Errorf("switching to a failed restore's target: %v, want it refused as failed", err)
	}
	if !h.c.alertsHandled(h.ctx) {
		t.Fatal("alerts not handled")
	}
	if open := restoreAlerts(h); len(open) != 1 || !strings.Contains(open[0].Message, kept.Service) || open[0].Service != "db" {
		t.Fatalf("open restore alerts = %+v, want one on db naming %s", open, kept.Service)
	}

	// The next try takes the default name again, and closes the alert.
	mu.Lock()
	fail = false
	mu.Unlock()
	again := h.restoreBackup(db, snap)
	if again.Service.Name != "db-restored" {
		t.Fatalf("the next restore's service = %s", again.Service.Name)
	}
	if done := h.waitRun(db, again.Restore.ID); done.Status != apitypes.BackupSucceeded {
		t.Fatalf("the next restore = %+v", done)
	}
	eventually(t, "the restore alert to close", func() bool { return len(restoreAlerts(h)) == 0 })
}

func TestFailedSnapshotRestoreFreesItsName(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	fail := true
	h.snapshotJobs(func(spec node.JobSpec) *node.JobResult {
		mu.Lock()
		defer mu.Unlock()
		if fail && strings.Contains(spec.Name, "_restore_snapshot") {
			return &node.JobResult{ExitCode: 1, Output: "cp: no space left on device"}
		}
		return nil
	})
	db := h.templateDB("db", apitypes.TemplatePostgres)
	s := h.snapshot(db)
	res := h.restoreSnapshot(db, s.ID, apitypes.RestoreSnapshotRequest{})
	if r := h.waitRestore(db, res.Restore.ID); r.Status != apitypes.BackupFailed {
		t.Fatalf("restore = %+v, want failed", r)
	}
	kept := ServiceRef{db.Project, db.Environment, "db-restored-failed-s" + itoa(res.Restore.ID)}
	if ss, err := h.c.loadService(h.ctx, 1, kept); err != nil || ss.svc.ArchivedAt == nil {
		t.Fatalf("the failed target %s: %v; want it archived", kept.Service, err)
	}
	if !h.c.alertsHandled(h.ctx) {
		t.Fatal("alerts not handled")
	}
	if open := restoreAlerts(h); len(open) != 1 {
		t.Fatalf("open restore alerts = %+v, want one", open)
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	again := h.restoreSnapshot(db, s.ID, apitypes.RestoreSnapshotRequest{})
	if r := h.waitRestore(db, again.Restore.ID); r.Status != apitypes.BackupSucceeded || r.To != "db-restored" {
		t.Fatalf("the next restore = %+v", r)
	}
	eventually(t, "the restore alert to close", func() bool { return len(restoreAlerts(h)) == 0 })
}

// A restart during a restore into a new service ends it like any failed
// one, at the next start: the target is archived under its other name, so
// the name is free, and an alert opens on the service restored from.
func TestRestartArchivesACutRestoresTarget(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	snap := "cccc3333cccc3333"
	loading, hold := make(chan struct{}), make(chan struct{})
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		if strings.Contains(spec.Name, "_restore") {
			close(loading)
			<-hold
			return node.JobResult{}, context.Canceled
		}
		return node.JobResult{Output: resticOutput(snap, 100, snap)}, nil
	})
	h.runBackup(db)
	res := h.restoreBackup(db, snap)
	<-loading
	// Stopping waits for the job, which ends once the daemon stopped.
	go func() { time.Sleep(20 * time.Millisecond); close(hold) }()
	h.restart()
	if done := h.waitRun(db, res.Restore.ID); done.Status != apitypes.BackupFailed || !strings.Contains(done.Error, "restarted") {
		t.Fatalf("restore = %+v, want failed by the restart", done)
	}
	kept := ServiceRef{db.Project, db.Environment, "db-restored-failed-" + itoa(res.Restore.ID)}
	eventually(t, "the cut-short restore's target to be archived", func() bool {
		ss, err := h.c.loadService(h.ctx, 1, kept)
		return err == nil && ss.svc.ArchivedAt != nil
	})
	if _, err := h.c.loadService(h.ctx, 1, ServiceRef{db.Project, db.Environment, "db-restored"}); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("the target's name is still taken: %v", err)
	}
	eventually(t, "the restore alert to open", func() bool { return len(restoreAlerts(h)) == 1 })
	if open := restoreAlerts(h); !strings.Contains(open[0].Message, kept.Service) || !strings.Contains(open[0].Message, "restarted") || open[0].Service != "db" {
		t.Errorf("restore alert = %+v, want one on db naming %s and the restart", open[0], kept.Service)
	}
}

// A failed restore drill's throwaway service is the drill's: it is
// removed for good, never archived as a failed restore's target, and the
// drill's own alert is the only one. A person's failed restore into the
// same name is archived like any other.
func TestFailedDrillTargetIsLeftToItsDrill(t *testing.T) {
	h, db, restoreErr := drillDB(t, 100)
	*restoreErr = errors.New("pg_restore: error: could not read from input file")
	if d := h.drillNow(db, true); d.Status != apitypes.BackupFailed {
		t.Fatalf("drill = %+v, want failed", d)
	}
	if !h.c.alertsHandled(h.ctx) {
		t.Fatal("alerts not handled")
	}
	if open := restoreAlerts(h); len(open) != 0 {
		t.Errorf("a failed drill opened restore alerts: %+v", open)
	}
	ss, err := h.c.loadService(h.ctx, 1, db)
	if err != nil {
		t.Fatal(err)
	}
	svcs, err := h.c.store.ListServices(h.ctx, ss.env.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range svcs {
		if s.Name != "db" {
			t.Errorf("service %s is left of a failed drill", s.Name)
		}
	}

	// Not the system's: someone restored into that name themselves.
	dev := h.user("dev@x", apitypes.ScopeAdmin)
	var res apitypes.RestoreResult
	h.untilFree(func() (err error) {
		res, err = h.c.RestoreBackup(h.ctx, dev, db, apitypes.RestoreBackupRequest{Snapshot: "dddd4444dddd4444", As: "db" + drillSuffix})
		return err
	})
	if done := h.waitRun(db, res.Restore.ID); done.Status != apitypes.BackupFailed {
		t.Fatalf("restore = %+v, want failed", done)
	}
	kept := ServiceRef{db.Project, db.Environment, "db-drill-failed-" + itoa(res.Restore.ID)}
	if to, err := h.c.loadService(h.ctx, 1, kept); err != nil || to.svc.ArchivedAt == nil {
		t.Errorf("a person's failed restore into db-drill: %v; want it archived as %s", err, kept.Service)
	}
}
