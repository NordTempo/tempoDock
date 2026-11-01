package core

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/cpbackup"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// waitControlPlaneBackup waits until the latest control plane backup
// finished and returns it.
func (h *harness) waitControlPlaneBackup() apitypes.ControlPlaneBackupRun {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		set, err := h.c.GetBackupSettings(h.ctx, testSystem, false)
		if err != nil {
			h.t.Fatal(err)
		}
		if l := set.ControlPlane.Latest; l != nil && l.Status != apitypes.BackupRunning {
			return *l
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("control plane backup still running: %+v", set.ControlPlane)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// writeKey puts a secrets key in the harness's data dir, as `dock serve`
// has one.
func (h *harness) writeKey() {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.opts.Config.DataDir, cpbackup.SecretKeyFile), bytes.Repeat([]byte{7}, 32), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

// The control plane backs itself up into the repository: the archive
// streams into a restic job's stdin on its own server, the run is
// recorded, a failure raises an alert and the next success resolves it.
func TestControlPlaneBackup(t *testing.T) {
	h := newHarness(t)
	h.writeKey()
	dev := h.user("dev@x", apitypes.ScopeDeploy)
	if _, err := h.c.RunControlPlaneBackup(h.ctx, dev); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("deploy user backed up the control plane: %v", err)
	}

	var archive bytes.Buffer
	var spec node.JobSpec
	h.rt.OnJob(func(s node.JobSpec) (node.JobResult, error) {
		if s.Stdin == nil {
			return node.JobResult{}, nil // the restic install job
		}
		spec = s
		if _, err := io.Copy(&archive, s.Stdin); err != nil {
			return node.JobResult{ExitCode: 130}, err
		}
		return node.JobResult{Output: resticOutput("cccc1111dddd", int64(archive.Len()), "cccc1111dddd")}, nil
	})
	run, err := h.c.RunControlPlaneBackup(h.ctx, testSystem)
	if err != nil || run.Status != apitypes.BackupRunning || run.Trigger != "manual" {
		t.Fatalf("run = %+v, %v", run, err)
	}
	done := h.waitControlPlaneBackup()
	if done.Status != apitypes.BackupSucceeded || done.Snapshot != "cccc1111dddd" || done.SizeBytes != int64(archive.Len()) || done.Repository != "local" {
		t.Fatalf("done = %+v", done)
	}
	if spec.Image != h.opts.Config.Backup.ResticImage || spec.Env["DOCK_TAG"] != backup.ControlPlaneTag ||
		!strings.Contains(spec.Script, "--stdin-filename "+backup.ControlPlaneFile) || spec.Network != "" {
		t.Errorf("job = %+v", spec)
	}
	names := map[string]bool{}
	tr := tar.NewReader(&archive)
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		names[hdr.Name] = true
	}
	for _, want := range []string{cpbackup.ManifestName, "data/" + cpbackup.DBFile, "data/" + cpbackup.SecretKeyFile} {
		if !names[want] {
			t.Errorf("the archive has no %s: %v", want, names)
		}
	}

	// A failed job is a failed run and an alert.
	h.rt.OnJob(func(s node.JobSpec) (node.JobResult, error) {
		if s.Stdin != nil {
			_, _ = io.Copy(io.Discard, s.Stdin)
		}
		return node.JobResult{ExitCode: 1, Output: "Fatal: unable to open repository\n"}, nil
	})
	if _, err := h.c.RunControlPlaneBackup(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	if done := h.waitControlPlaneBackup(); done.Status != apitypes.BackupFailed || !strings.Contains(done.Error, "unable to open repository") {
		t.Fatalf("failed run = %+v", done)
	}
	if !h.alertOpen(controlPlaneAlertKey) {
		t.Fatal("no alert for the failed control plane backup")
	}
	h.rt.OnJob(func(s node.JobSpec) (node.JobResult, error) {
		if s.Stdin != nil {
			_, _ = io.Copy(io.Discard, s.Stdin)
		}
		return node.JobResult{Output: resticOutput("eeee2222", 10)}, nil
	})
	if _, err := h.c.RunControlPlaneBackup(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	if done := h.waitControlPlaneBackup(); done.Status != apitypes.BackupSucceeded || h.alertOpen(controlPlaneAlertKey) {
		t.Fatalf("after a success: %+v, alert open %v", done, h.alertOpen(controlPlaneAlertKey))
	}
}

func (h *harness) alertOpen(key string) bool {
	h.c.alerts.mu.Lock()
	defer h.c.alerts.mu.Unlock()
	_, ok := h.c.alerts.open[key]
	return ok
}

// An archive that can't be written stops the job before restic sees the
// end of its input: the run fails with why, never with a snapshot.
func TestControlPlaneBackupArchiveFailure(t *testing.T) {
	h := newHarness(t) // no secrets key in the data dir
	if _, err := h.c.RunControlPlaneBackup(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	done := h.waitControlPlaneBackup()
	if done.Status != apitypes.BackupFailed || !strings.Contains(done.Error, "write the control plane archive") || done.Snapshot != "" {
		t.Fatalf("done = %+v", done)
	}
}

// The control plane's backup is due daily at the install's hour, counted
// from the local server's creation, and runs on the scheduler.
func TestScheduledControlPlaneBackup(t *testing.T) {
	h := newHarness(t)
	h.writeKey()
	h.rt.OnJob(func(s node.JobSpec) (node.JobResult, error) {
		if s.Stdin != nil {
			_, _ = io.Copy(io.Discard, s.Stdin)
		}
		return node.JobResult{Output: resticOutput("ffff3333", 10)}, nil
	})
	at, err := h.c.controlPlaneBackupDue(h.ctx)
	if err != nil || !at.After(time.Now()) || time.Until(at) > 25*time.Hour {
		t.Fatalf("new install: due %v, %v", at, err)
	}
	if _, err := h.store.DB.ExecContext(h.ctx, "UPDATE servers SET created_at = ? WHERE local = 1", time.Now().UTC().Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	h.restart()
	done := h.waitControlPlaneBackup()
	if done.Status != apitypes.BackupSucceeded || done.Trigger != "schedule" || done.CreatedBy != "system" {
		t.Fatalf("scheduled run = %+v", done)
	}
	if at, err := h.c.controlPlaneBackupDue(h.ctx); err != nil || !at.After(time.Now()) {
		t.Fatalf("after the scheduled run: due %v, %v", at, err)
	}
	// A manual run doesn't move the schedule.
	row, _ := h.store.GetControlPlaneBackup(h.ctx)
	if _, err := h.c.RunControlPlaneBackup(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	h.waitControlPlaneBackup()
	if again, _ := h.store.GetControlPlaneBackup(h.ctx); !again.ScheduledAt.Equal(*row.ScheduledAt) {
		t.Errorf("scheduled_at moved from %v to %v", row.ScheduledAt, again.ScheduledAt)
	}
}

// A control plane restored with --old-server-becomes-node: the old server
// keeps its environments, waits to join as a node under its name, and
// this server registers as the local one. The run that took the backup
// is dropped.
func TestAfterRestoreOldServerBecomesNode(t *testing.T) {
	h := newHarness(t)
	h.nodes = newFakeNodes()
	h.restart()
	web := h.service("web", apitypes.CreateServiceRequest{})
	old := h.c.local
	if _, err := h.store.StartControlPlaneBackup(h.ctx, store.StartControlPlaneBackupParams{
		Trigger: "schedule", Repository: "", CreatedBy: "system", StartedAt: now(), ScheduledAt: ptr(now()),
	}); err != nil {
		t.Fatal(err)
	}
	h.c.Close()

	name, err := AfterRestore(h.ctx, h.store, "a test backup", true)
	if err != nil || name != old.Name {
		t.Fatalf("AfterRestore = %q, %v; want %q", name, err, old.Name)
	}
	if _, err := h.store.GetControlPlaneBackup(h.ctx); !store.IsNotFound(err) {
		t.Errorf("the running control plane backup is still there: %v", err)
	}
	h.c = h.start()
	if h.c.local.ID == old.ID || h.c.local.Name != old.Name+"-2" || !h.c.local.Local {
		t.Fatalf("new local server = %+v (old %d %s)", h.c.local, old.ID, old.Name)
	}
	ss, err := h.c.serviceFor(h.ctx, testSystem, web, apitypes.ScopeRead, "testing")
	if err != nil || ss.env.ServerID != old.ID {
		t.Fatalf("environment moved: on server %d, %v", ss.env.ServerID, err)
	}
	srv, err := h.c.GetServer(h.ctx, testSystem, old.Name)
	if err != nil || srv.Server.Status != apitypes.ServerRevoked {
		t.Fatalf("old server = %+v, %v", srv.Server, err)
	}
	// It joins again under its name.
	if _, err := h.c.AddServer(h.ctx, testSystem, apitypes.AddServerRequest{Name: old.Name, AcceptsNonProduction: true}); err != nil {
		t.Fatalf("add the old server back: %v", err)
	}
}

// Restored in the old server's place, the local server stays as it was.
func TestAfterRestoreReplaces(t *testing.T) {
	h := newHarness(t)
	old := h.c.local
	h.c.Close()
	if name, err := AfterRestore(h.ctx, h.store, "a test backup", false); err != nil || name != "" {
		t.Fatalf("AfterRestore = %q, %v", name, err)
	}
	h.c = h.start()
	if h.c.local.ID != old.ID {
		t.Fatalf("local server %d, want %d", h.c.local.ID, old.ID)
	}
}

// The analytics store (its database and replays) is in the control
// plane's backup, and a control plane restored from it fills its new,
// empty store from it once the store runs.
func TestControlPlaneBackupHasTheAnalyticsStore(t *testing.T) {
	h := newHarness(t)
	h.writeKey()
	h.rt.AppendSpool(pageview(1), pageview(2))
	if err := h.c.EnableAnalytics(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the events in the store", func() bool { return storeEvents(h) == 2 })
	var archive bytes.Buffer
	h.rt.OnJob(func(s node.JobSpec) (node.JobResult, error) {
		if s.Stdin == nil {
			return node.JobResult{}, nil
		}
		if _, err := io.Copy(&archive, s.Stdin); err != nil {
			return node.JobResult{ExitCode: 130}, err
		}
		return node.JobResult{Output: resticOutput("cccc1111dddd", int64(archive.Len()), "cccc1111dddd")}, nil
	})
	if _, err := h.c.RunControlPlaneBackup(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	if done := h.waitControlPlaneBackup(); done.Status != apitypes.BackupSucceeded {
		t.Fatalf("done = %+v", done)
	}
	files := map[string][]byte{}
	tr := tar.NewReader(&archive)
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if strings.HasPrefix(hdr.Name, "data/analytics/restore.") {
			files[hdr.Name], _ = io.ReadAll(tr)
		}
	}
	if len(files["data/"+cpbackup.AnalyticsFile]) == 0 || !strings.Contains(string(files["data/"+cpbackup.AnalyticsInfoFile]), `"events":2`) {
		t.Fatalf("the archive's analytics: %q", files["data/"+cpbackup.AnalyticsInfoFile])
	}

	// A new control plane with the archive's files in its data dir.
	h2 := newHarness(t)
	for name, b := range files {
		dst := filepath.Join(h2.opts.Config.DataDir, strings.TrimPrefix(name, "data/"))
		_ = os.MkdirAll(filepath.Dir(dst), 0o700)
		if err := os.WriteFile(dst, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := h2.c.EnableAnalytics(h2.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the new store filled from the backup", func() bool { return storeEvents(h2) == 2 })
	if _, err := os.Stat(filepath.Join(h2.opts.Config.DataDir, cpbackup.AnalyticsFile)); !os.IsNotExist(err) {
		t.Errorf("the backup's snapshot is still there: %v", err)
	}
}
