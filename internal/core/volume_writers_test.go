package core

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// blockRestoreCopy holds every snapshot restore copy until the returned
// release is called; copying is closed once the first one started.
func (h *harness) blockRestoreCopy() (copying <-chan struct{}, release func()) {
	started, gate := make(chan struct{}), make(chan struct{})
	first := true
	h.snapshotJobs(func(spec node.JobSpec) *node.JobResult {
		if spec.Script == snapshotRestoreScript {
			if first {
				first = false
				close(started)
			}
			<-gate
		}
		return nil
	})
	return started, func() { close(gate) }
}

// TestInPlaceRestoreShutsOutVolumeWriters: while a snapshot replaces a
// service's volumes, no deploy, stop or `dock run` writing them goes
// ahead, and the refusal says what runs and what to do.
func TestInPlaceRestoreShutsOutVolumeWriters(t *testing.T) {
	h := newHarness(t)
	h.snapshotJobs(nil)
	app := h.volumeService("app")
	s := h.snapshot(app)

	copying, release := h.blockRestoreCopy()
	res := h.restoreSnapshot(app, s.ID, apitypes.RestoreSnapshotRequest{InPlace: true})
	<-copying
	busy := func(what string, err error) {
		t.Helper()
		if CodeOf(err) != apitypes.CodeConflict || !strings.Contains(err.Error(), "restore of snapshot") {
			t.Errorf("%s during an in-place restore: %v", what, err)
		}
	}
	_, err := h.c.Restart(h.ctx, testSystem, app)
	busy("deploy", err)
	_, err = h.c.StopService(h.ctx, testSystem, app)
	busy("stop", err)
	_, err = h.c.StartRun(h.ctx, testSystem, app, apitypes.RunCommandRequest{Command: "bin/seed", Volumes: apitypes.VolumesWrite, NoSnapshot: true})
	busy("a run writing the volumes", err)
	if _, err := h.runCmd(testSystem, app, apitypes.RunCommandRequest{Command: "ls", Volumes: apitypes.VolumesRead}); err != nil {
		t.Errorf("a read-only run waits for nothing: %v", err)
	}
	release()
	if r := h.waitRestore(app, res.Restore.ID); r.Status != apitypes.BackupSucceeded {
		t.Fatalf("restore = %+v", r)
	}
	if _, err := h.runCmd(testSystem, app, apitypes.RunCommandRequest{Command: "bin/seed", Volumes: apitypes.VolumesWrite, NoSnapshot: true}); err != nil {
		t.Fatalf("a run writing the volumes after the restore: %v", err)
	}
}

// TestInPlaceRestoreWaitsForAWritingRun: a run with the volumes open for
// writing keeps an in-place restore out until it ends.
func TestInPlaceRestoreWaitsForAWritingRun(t *testing.T) {
	h := newHarness(t)
	h.snapshotJobs(nil)
	app := h.volumeService("app")
	s := h.snapshot(app)

	w, err := h.c.OpenRun(h.ctx, testSystem, app, apitypes.RunCommandRequest{Command: "bin/seed", Volumes: apitypes.VolumesWrite, NoSnapshot: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	// The backup scheduler's first backup of the service may hold it for
	// a moment (untilFree); past that, the run is what refuses.
	eventually(t, "the restore refused for the run", func() bool {
		_, err = h.c.RestoreSnapshot(h.ctx, testSystem, app, s.ID, apitypes.RestoreSnapshotRequest{InPlace: true})
		if CodeOf(err) != apitypes.CodeConflict {
			t.Fatalf("restore while a run writes the volumes: %v", err)
		}
		return strings.Contains(err.Error(), "dock run")
	})
	w.Wait(h.ctx, nil)
	res := h.restoreSnapshot(app, s.ID, apitypes.RestoreSnapshotRequest{InPlace: true})
	if r := h.waitRestore(app, res.Restore.ID); r.Status != apitypes.BackupSucceeded {
		t.Fatalf("restore after the run = %+v", r)
	}
}

// TestRestoreIntoNewShutsOutDeploys: the new service of a restore can't be
// deployed while the snapshot fills its volume; the restore deploys it
// once the copy is done.
func TestRestoreIntoNewShutsOutDeploys(t *testing.T) {
	h := newHarness(t)
	h.snapshotJobs(nil)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	s := h.snapshot(db)

	copying, release := h.blockRestoreCopy()
	res := h.restoreSnapshot(db, s.ID, apitypes.RestoreSnapshotRequest{})
	<-copying
	to := ServiceRef{db.Project, db.Environment, res.Service.Name}
	if _, err := h.c.DeployImage(h.ctx, testSystem, to, apitypes.DeployImageRequest{}); CodeOf(err) != apitypes.CodeConflict ||
		!strings.Contains(err.Error(), "restore of snapshot") {
		t.Errorf("deploy of the new service while it fills: %v", err)
	}
	release()
	if r := h.waitRestore(db, res.Restore.ID); r.Status != apitypes.BackupSucceeded {
		t.Fatalf("restore = %+v", r)
	}
	if got := h.status(to, 1); got != apitypes.ReleaseActive {
		t.Fatalf("the restore's own deploy is %s", got)
	}
}
