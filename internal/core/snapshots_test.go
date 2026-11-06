package core

import (
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// snapshotJobs answers snapshot jobs as the real script would: a size for
// a copy, nothing for a restore or delete. fail, when set, answers a
// job instead (nil: as usual).
func (h *harness) snapshotJobs(fail func(node.JobSpec) *node.JobResult) {
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		if fail != nil {
			if res := fail(spec); res != nil {
				return *res, nil
			}
		}
		if spec.Script == snapshotScript {
			return node.JobResult{Output: "dock-snapshot: size 2048\n"}, nil
		}
		return node.JobResult{}, nil
	})
}

// snapshot takes a snapshot of ref and waits for it to finish.
func (h *harness) snapshot(ref ServiceRef) apitypes.Snapshot {
	h.t.Helper()
	var s apitypes.Snapshot
	h.untilFree(func() (err error) {
		s, err = h.c.CreateSnapshot(h.ctx, testSystem, ref, apitypes.CreateSnapshotRequest{Note: "test"})
		return err
	})
	return h.waitSnapshot(ref, s.ID)
}

// restoreSnapshot starts restoring snapshot id of ref.
func (h *harness) restoreSnapshot(ref ServiceRef, id int64, req apitypes.RestoreSnapshotRequest) apitypes.SnapshotRestoreResult {
	h.t.Helper()
	var res apitypes.SnapshotRestoreResult
	h.untilFree(func() (err error) {
		res, err = h.c.RestoreSnapshot(h.ctx, testSystem, ref, id, req)
		return err
	})
	return res
}

// untilFree runs fn until the service it acts on is free: the backup
// scheduler runs a new stateful service's overdue backup at once, which
// holds the service for a moment.
func (h *harness) untilFree(fn func() error) {
	h.t.Helper()
	eventually(h.t, "the service to be free", func() bool {
		err := fn()
		if err != nil && CodeOf(err) != apitypes.CodeConflict {
			h.t.Fatal(err)
		}
		return err == nil
	})
}

// waitSnapshot waits until snapshot id of ref finished.
func (h *harness) waitSnapshot(ref ServiceRef, id int64) apitypes.Snapshot {
	h.t.Helper()
	var got apitypes.Snapshot
	eventually(h.t, "snapshot "+strconv.FormatInt(id, 10), func() bool {
		list, err := h.c.ServiceSnapshots(h.ctx, testSystem, ref)
		if err != nil {
			h.t.Fatal(err)
		}
		for _, s := range list.Snapshots {
			if s.ID == id && s.Status != apitypes.SnapshotRunning {
				got = s
				return true
			}
		}
		return false
	})
	return got
}

// waitRestore waits until restore id of ref finished.
func (h *harness) waitRestore(ref ServiceRef, id int64) apitypes.SnapshotRestore {
	h.t.Helper()
	var got apitypes.SnapshotRestore
	eventually(h.t, "restore "+strconv.FormatInt(id, 10), func() bool {
		list, err := h.c.ServiceSnapshots(h.ctx, testSystem, ref)
		if err != nil {
			h.t.Fatal(err)
		}
		for _, r := range list.Restores {
			if r.ID == id && r.Status != apitypes.BackupRunning {
				got = r
				return true
			}
		}
		return false
	})
	return got
}

// volumeService creates a service with one volume, deployed.
func (h *harness) volumeService(name string) ServiceRef {
	h.t.Helper()
	ref := h.service(name, apitypes.CreateServiceRequest{Port: 8080})
	if _, err := h.c.AddVolume(h.ctx, testSystem, ref, apitypes.AddVolumeRequest{Name: "files", MountPath: "/files"}); err != nil {
		h.t.Fatal(err)
	}
	h.deploy(ref, "app:1")
	return ref
}

func TestSnapshotPausesTheServiceForTheCopy(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	ctr := h.ctr(db, 1)
	var during node.ContainerState
	var spec node.JobSpec
	h.rt.OnJob(func(s node.JobSpec) (node.JobResult, error) {
		if s.Script != snapshotScript {
			return node.JobResult{}, nil
		}
		spec = s
		if len(s.Pause) == 1 {
			ct, err := h.rt.Inspect(h.ctx, s.Pause[0])
			if err != nil {
				return node.JobResult{}, err
			}
			during = ct.State
		}
		return node.JobResult{Output: "dock-snapshot: size 2048\n"}, nil
	})

	s := h.snapshot(db)
	if s.Status != apitypes.SnapshotReady || s.SizeBytes != 2<<20 || s.Trigger != apitypes.SnapshotManual || s.Note != "test" ||
		!slices.Equal(s.Volumes, []string{"data"}) || s.Server == "" {
		t.Fatalf("snapshot = %+v", s)
	}
	if during != node.StatePaused {
		t.Errorf("container was %q during the copy, want paused", during)
	}
	if got := h.rt.Running(); !slices.Contains(got, ctr) {
		t.Errorf("running after = %v, want %s again", got, ctr)
	}
	ss, _ := h.c.serviceFor(h.ctx, testSystem, db, apitypes.ScopeRead, "testing")
	switch {
	case spec.Image != h.c.cfg.Backup.ResticImage:
		t.Errorf("image = %q", spec.Image)
	case spec.Env["SNAPSHOT"] != strconv.FormatInt(s.ID, 10) || spec.Env["VOLUMES"] != "data":
		t.Errorf("env = %v", spec.Env)
	case !slices.Contains(spec.Mounts, node.Mount{Volume: volumeName(ss, "data"), Path: "/volumes/data", ReadOnly: true}) ||
		!slices.Contains(spec.Mounts, node.Mount{Volume: h.c.snapshotVolume(), Path: snapshotsPath}):
		t.Errorf("mounts = %+v", spec.Mounts)
	}
	if evs, err := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Action: "snapshot.created"}); err != nil || len(evs) != 1 {
		t.Error("not audited")
	}

	// Read scope lists, but doesn't take one.
	reader := h.token(testSystem, apitypes.CreateTokenRequest{Name: "r", Scope: apitypes.ScopeRead})
	if _, err := h.c.ServiceSnapshots(h.ctx, reader, db); err != nil {
		t.Errorf("read: %v", err)
	}
	if _, err := h.c.CreateSnapshot(h.ctx, reader, db, apitypes.CreateSnapshotRequest{}); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("create with read scope: %v", err)
	}
	// Nothing to snapshot without a volume.
	web := h.service("web", apitypes.CreateServiceRequest{})
	if _, err := h.c.CreateSnapshot(h.ctx, testSystem, web, apitypes.CreateSnapshotRequest{}); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("no volume: %v", err)
	}
}

func TestSnapshotRefusedWithoutRoom(t *testing.T) {
	h := newHarness(t)
	app := h.volumeService("app")
	h.snapshotJobs(func(s node.JobSpec) *node.JobResult {
		if s.Script == snapshotScript {
			return &node.JobResult{ExitCode: 3, Output: "dock-snapshot: no-room 4096 2048 10240\n"}
		}
		return nil
	})
	s := h.snapshot(app)
	if s.Status != apitypes.SnapshotFailed || !strings.Contains(s.Error, "not enough disk") || !strings.Contains(s.Error, "4.0 MB") {
		t.Fatalf("snapshot = %+v", s)
	}
	// A failed snapshot keeps its job's output for View log.
	if log, err := h.c.SnapshotLog(h.ctx, testSystem, app, s.ID); err != nil || !strings.Contains(log.Output, "no-room 4096") {
		t.Fatalf("log = %+v, %v", log, err)
	}
}

func TestSnapshotRetentionKeepsTheNewest(t *testing.T) {
	h := newHarness(t)
	// Set before the daemon reads it (the scheduler prunes by age).
	h.opts.Config.Backup.SnapshotKeep, h.opts.Config.Backup.SnapshotDays = 2, 1
	h.restart()
	app := h.volumeService("app")
	var mu sync.Mutex
	var deletedJobs []string
	deleted := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(deletedJobs)
	}
	h.snapshotJobs(func(s node.JobSpec) *node.JobResult {
		if s.Script == snapshotDeleteScript {
			mu.Lock()
			deletedJobs = append(deletedJobs, s.Env["SNAPSHOTS"])
			mu.Unlock()
		}
		return nil
	})
	a, b, c := h.snapshot(app), h.snapshot(app), h.snapshot(app)
	// Retention runs right after the newest is ready.
	var ids []int64
	eventually(t, "retention", func() bool {
		list, err := h.c.ServiceSnapshots(h.ctx, testSystem, app)
		if err != nil {
			t.Fatal(err)
		}
		ids = ids[:0]
		for _, s := range list.Snapshots {
			ids = append(ids, s.ID)
		}
		return slices.Equal(ids, []int64{c.ID, b.ID})
	})
	if !slices.Equal(deleted(), []string{strconv.FormatInt(a.ID, 10)}) {
		t.Fatalf("kept %v, deleted %v", ids, deleted())
	}

	// Age: a snapshot past the limit goes on the scheduler's next pass.
	if _, err := h.store.DB.ExecContext(h.ctx, "UPDATE volume_snapshots SET started_at = ? WHERE id = ?", time.Now().Add(-48*time.Hour), b.ID); err != nil {
		t.Fatal(err)
	}
	h.c.pruneExpiredSnapshots(h.ctx)
	if _, err := h.store.GetSnapshot(h.ctx, b.ID); err == nil {
		t.Error("expired snapshot kept")
	}

	// Deleting one by hand removes its files first.
	if err := h.c.DeleteSnapshot(h.ctx, testSystem, app, c.ID); err != nil {
		t.Fatal(err)
	}
	if d := deleted(); d[len(d)-1] != strconv.FormatInt(c.ID, 10) {
		t.Errorf("deleted = %v", d)
	}
	if err := h.c.DeleteSnapshot(h.ctx, testSystem, app, c.ID); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("delete again: %v", err)
	}
}

func TestSnapshotRestoreIntoNewService(t *testing.T) {
	h := newHarness(t)
	h.snapshotJobs(nil)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	pw := h.revealed(db)["POSTGRES_PASSWORD"]
	s := h.snapshot(db)

	res := h.restoreSnapshot(db, s.ID, apitypes.RestoreSnapshotRequest{})
	name := "db-restored"
	if res.Service == nil || res.Service.Name != name || res.Restore.InPlace {
		t.Fatalf("result = %+v", res)
	}
	r := h.waitRestore(db, res.Restore.ID)
	if r.Status != apitypes.BackupSucceeded || r.To != name || r.Snapshot != s.ID {
		t.Fatalf("restore = %+v", r)
	}
	to := ServiceRef{db.Project, db.Environment, name}
	vars := h.revealed(to)
	if vars["POSTGRES_PASSWORD"] != pw || !strings.Contains(vars["DATABASE_URL"], "@"+name+":5432") {
		t.Errorf("new service's variables = %v; the data files need the snapshot's password", vars)
	}
	// The volume was filled before the new service's first container.
	ops := h.rt.Ops()
	fill := slices.IndexFunc(ops, func(op string) bool { return strings.Contains(op, "_restore_snapshot") })
	if start := indexOf(ops, "start "+h.ctr(to, 1)); fill < 0 || start < fill {
		t.Errorf("fill at %d, start at %d: %v", fill, start, ops)
	}
	if h.status(db, 1) != apitypes.ReleaseActive {
		t.Error("the source service was touched")
	}
}

func TestSnapshotRestoreInPlace(t *testing.T) {
	h := newHarness(t)
	h.snapshotJobs(nil)
	app := h.volumeService("app")
	s := h.snapshot(app)

	// A service that isn't a template database restores in place only.
	if _, err := h.c.RestoreSnapshot(h.ctx, testSystem, app, s.ID, apitypes.RestoreSnapshotRequest{}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("restore into new: %v", err)
	}

	// While the copy runs, deploys wait.
	copying, release := make(chan struct{}), make(chan struct{})
	h.snapshotJobs(func(spec node.JobSpec) *node.JobResult {
		if spec.Script == snapshotRestoreScript {
			close(copying)
			<-release
		}
		return nil
	})
	res := h.restoreSnapshot(app, s.ID, apitypes.RestoreSnapshotRequest{InPlace: true})
	<-copying
	if _, err := h.c.Restart(h.ctx, testSystem, app); CodeOf(err) != apitypes.CodeConflict {
		t.Errorf("deploy during an in-place restore: %v", err)
	}
	close(release)
	r := h.waitRestore(app, res.Restore.ID)
	if r.Status != apitypes.BackupSucceeded || !r.InPlace || r.Safety == 0 {
		t.Fatalf("restore = %+v", r)
	}
	safety := h.waitSnapshot(app, r.Safety)
	if safety.Status != apitypes.SnapshotReady || safety.Trigger != apitypes.SnapshotBeforeRestore || safety.PausedMS != 0 {
		t.Errorf("safety snapshot = %+v", safety)
	}
	// Stopped, then the safety copy, then the restore, then started again.
	ops := h.rt.Ops()
	ctr := h.ctr(app, 1)
	stop := indexOf(ops, "stop "+ctr)
	restore := slices.IndexFunc(ops, func(op string) bool { return strings.Contains(op, "_restore_snapshot") })
	start := slices.IndexFunc(ops[max(restore, 0):], func(op string) bool { return op == "start "+ctr })
	if stop < 0 || restore < stop || start < 0 {
		t.Errorf("ops = %v", ops)
	}
	if !slices.Contains(h.rt.Running(), ctr) {
		t.Errorf("running = %v", h.rt.Running())
	}
}

func TestSnapshotRestoreInPlacePutsTheDataBackOnFailure(t *testing.T) {
	h := newHarness(t)
	h.snapshotJobs(nil)
	app := h.volumeService("app")
	s := h.snapshot(app)
	var restored []string
	h.snapshotJobs(func(spec node.JobSpec) *node.JobResult {
		if spec.Script != snapshotRestoreScript {
			return nil
		}
		restored = append(restored, spec.Env["SNAPSHOT"])
		if len(restored) == 1 {
			return &node.JobResult{ExitCode: 1, Output: "cp: write error: No space left on device"}
		}
		return nil
	})
	res := h.restoreSnapshot(app, s.ID, apitypes.RestoreSnapshotRequest{InPlace: true})
	r := h.waitRestore(app, res.Restore.ID)
	if r.Status != apitypes.BackupFailed || !strings.Contains(r.Error, "put back") {
		t.Fatalf("restore = %+v", r)
	}
	if !slices.Equal(restored, []string{strconv.FormatInt(s.ID, 10), strconv.FormatInt(r.Safety, 10)}) {
		t.Errorf("restored %v, want the snapshot then the safety copy", restored)
	}
	if !slices.Contains(h.rt.Running(), h.ctr(app, 1)) {
		t.Error("the service wasn't started again")
	}
}

func TestSnapshotRestoreInterruptedByCrash(t *testing.T) {
	h := newHarness(t)
	h.snapshotJobs(nil)
	app := h.volumeService("app")
	s := h.snapshot(app)
	copying, stuck := make(chan struct{}), make(chan struct{})
	h.snapshotJobs(func(spec node.JobSpec) *node.JobResult {
		if spec.Script == snapshotRestoreScript {
			close(copying)
			<-stuck // the daemon dies first
		}
		return nil
	})
	res := h.restoreSnapshot(app, s.ID, apitypes.RestoreSnapshotRequest{InPlace: true})
	<-copying
	h.snapshotJobs(nil)
	h.crash()
	// Before the dead daemon's Close (cleanups run last first), which
	// waits for its job.
	t.Cleanup(func() { close(stuck) })
	r := h.waitRestore(app, res.Restore.ID)
	if r.Status != apitypes.BackupFailed || !strings.Contains(r.Error, "in place again") {
		t.Fatalf("restore = %+v", r)
	}
	// The new daemon restores again (once its overdue backup of app is
	// out of the way).
	eventually(t, "restore again", func() bool {
		_, err := h.c.RestoreSnapshot(h.ctx, testSystem, app, s.ID, apitypes.RestoreSnapshotRequest{InPlace: true})
		if err != nil && CodeOf(err) != apitypes.CodeConflict {
			t.Fatal(err)
		}
		return err == nil
	})
}

func TestSnapshotBeforeIsSynchronous(t *testing.T) {
	h := newHarness(t)
	h.snapshotJobs(nil)
	app := h.volumeService("app")
	ss, _ := h.c.serviceFor(h.ctx, testSystem, app, apitypes.ScopeRead, "testing")
	s, err := h.c.SnapshotBefore(h.ctx, testSystem, ss, apitypes.SnapshotBeforeRun, "dock run")
	if err != nil || s == nil || s.Status != apitypes.SnapshotReady || s.Trigger != apitypes.SnapshotBeforeRun {
		t.Fatalf("snapshot = %+v, %v", s, err)
	}
	h.snapshotJobs(func(node.JobSpec) *node.JobResult { return &node.JobResult{ExitCode: 1} })
	if _, err := h.c.SnapshotBefore(h.ctx, testSystem, ss, apitypes.SnapshotBeforeRun, ""); err == nil {
		t.Error("a failed snapshot must stop the action")
	}
	// Stateless services have nothing to keep.
	web := h.service("web", apitypes.CreateServiceRequest{})
	ws, _ := h.c.serviceFor(h.ctx, testSystem, web, apitypes.ScopeRead, "testing")
	if s, err := h.c.SnapshotBefore(h.ctx, testSystem, ws, apitypes.SnapshotBeforeRun, ""); s != nil || err != nil {
		t.Errorf("stateless: %+v, %v", s, err)
	}
}

// SnapshotBefore waits for a backup, snapshot or restore holding the
// service instead of failing the action it guards.
func TestSnapshotBeforeWaitsForTheService(t *testing.T) {
	h := newHarness(t)
	h.snapshotJobs(nil)
	app := h.volumeService("app")
	ss, _ := h.c.serviceFor(h.ctx, testSystem, app, apitypes.ScopeRead, "testing")
	h.untilFree(func() error {
		if !h.c.claimService(ss.svc.ID) {
			return conflict("busy")
		}
		return nil
	})
	done := make(chan error, 1)
	go func() {
		_, err := h.c.SnapshotBefore(h.ctx, testSystem, ss, apitypes.SnapshotBeforeRun, "")
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("returned while the service was held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	h.c.releaseService(ss.svc.ID)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
