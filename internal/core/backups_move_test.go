package core

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// moveCheckOutput is what a move's check job prints: restic's check, then
// the target's snapshots with the originals they were copied from.
func moveCheckOutput(copies map[string]string) string {
	type snap struct {
		ID       string `json:"id"`
		Original string `json:"original"`
	}
	list := []snap{} // restic prints [] for none
	for orig, id := range copies {
		list = append(list, snap{id, orig})
	}
	b, _ := json.Marshal(list)
	return "no errors were found\n" + backup.MoveMarker + "\n" + string(b) + "\n"
}

// waitMoved waits until the backup move finished.
func (h *harness) waitMoved() apitypes.BackupSettings {
	h.t.Helper()
	var set apitypes.BackupSettings
	eventually(h.t, "the backup move to finish", func() bool {
		var err error
		set, err = h.c.GetBackupSettings(h.ctx, testSystem, false)
		return err == nil && set.Move != nil && !set.Move.Running
	})
	return set
}

// Moving local backups into a bucket copies the server's repository,
// checks the copy, points every backup at its copy (restores use it),
// switches where new backups go and empties the local repository. Nothing
// else that touches backups runs meanwhile.
func TestMoveBackups(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	old := []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}
	copies := map[string]string{old[0]: strings.Repeat("c", 64), old[1]: strings.Repeat("d", 64)}
	n := 0
	gate := make(chan struct{})
	var gated bool
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		switch {
		case strings.Contains(spec.Name, "_move_check"):
			return node.JobResult{Output: moveCheckOutput(copies)}, nil
		case strings.Contains(spec.Name, "_move_empty"), strings.Contains(spec.Name, "_backup_move_"):
			if strings.Contains(spec.Name, "_backup_move_") && !strings.Contains(spec.Name, "_empty") && gated {
				<-gate
			}
			return node.JobResult{}, nil
		case strings.Contains(spec.Name, "_backup"):
			n++
			return node.JobResult{Output: resticOutput(old[n-1], 1000*int64(n), old[:n]...)}, nil
		}
		return node.JobResult{}, nil // restic install
	})
	h.runBackup(db)
	h.runBackup(db)
	endpoint := fakeS3(t)
	if _, err := h.c.AddStorage(h.ctx, testSystem, apitypes.CreateStorageRequest{Name: "hetzner", Endpoint: endpoint, Bucket: "images", AccessKeyID: "k", SecretAccessKey: "s"}); err != nil {
		t.Fatal(err)
	}

	admin := Actor{Kind: "token", OrgID: 1, Name: "ops", Scope: apitypes.ScopeAdmin}
	if _, err := h.c.MoveBackups(h.ctx, admin, apitypes.MoveBackupsRequest{Storage: "hetzner"}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("an org admin moved the backups: %v", err)
	}
	gated = true
	set, err := h.c.MoveBackups(h.ctx, testSystem, apitypes.MoveBackupsRequest{Storage: "hetzner"})
	if err != nil || set.Move == nil || !set.Move.Running || set.Move.Path != "dock-backups" || len(set.Move.Sources) != 1 || set.Move.Sources[0].Server != h.c.local.Name {
		t.Fatalf("move = %+v, %v", set.Move, err)
	}
	if _, err := h.c.RunBackup(h.ctx, testSystem, db); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("a backup started during the move: %v", err)
	}
	if _, err := h.c.RunControlPlaneBackup(h.ctx, testSystem); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("a control plane backup started during the move: %v", err)
	}
	if err := h.c.RemoveStorage(h.ctx, testSystem, "hetzner"); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("removed the move's target: %v", err)
	}
	if _, err := h.c.MoveBackups(h.ctx, testSystem, apitypes.MoveBackupsRequest{Storage: "hetzner", Path: "other"}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("a second move started: %v", err)
	}
	close(gate)
	set = h.waitMoved()

	url := backup.S3URL(endpoint, "images", "dock-backups")
	if set.Move.Error != "" || set.Move.Moved != 2 || set.Storage != "hetzner" || set.Repository != url || !set.OffBox || set.Move.Sources[0].Status != "emptied" {
		t.Fatalf("after the move: %+v %+v", set, set.Move)
	}
	if len(set.Repositories) != 1 || !set.Repositories[0].Current || set.Repositories[0].Snapshots != 2 {
		t.Errorf("repositories = %+v", set.Repositories)
	}
	var copyJob, emptyJob *node.JobSpec
	for _, j := range h.rt.Jobs() {
		switch {
		case strings.Contains(j.Name, "_move_empty"):
			emptyJob = &j
		case strings.Contains(j.Name, "_backup_move_") && !strings.Contains(j.Name, "_check"):
			copyJob = &j
		}
	}
	local := func(j *node.JobSpec) bool {
		return j != nil && slices.ContainsFunc(j.Mounts, func(m node.Mount) bool { return m.Volume == h.c.localRepoVolume() })
	}
	if !local(copyJob) || copyJob.Env["RESTIC_FROM_REPOSITORY"] != backup.LocalPath || copyJob.Env["RESTIC_REPOSITORY"] != url || copyJob.Env["DOCK_RESTIC_OPTS"] != "-o s3.bucket-lookup=path" {
		t.Errorf("copy job = %+v", copyJob)
	}
	if !local(emptyJob) || emptyJob.Script != backup.ClearLocalScript {
		t.Errorf("empty job = %+v", emptyJob)
	}

	b, err := h.c.ServiceBackups(h.ctx, testSystem, db)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range b.Runs {
		if r.Operation == "backup" && (r.Repository != url || (r.Snapshot != copies[old[0]] && r.Snapshot != copies[old[1]])) {
			t.Errorf("backup run not pointed at its copy: %+v", r)
		}
	}
	res, err := h.c.RestoreBackup(h.ctx, testSystem, db, apitypes.RestoreBackupRequest{Snapshot: copies[old[1]][:8]})
	if err != nil {
		t.Fatalf("restore from the moved snapshot: %v", err)
	}
	h.waitRun(db, res.Restore.ID)
	st, _ := h.c.ListStorage(h.ctx, testSystem)
	if len(st) != 1 || !slices.Equal(st[0].UsedBy, []string{"backups"}) {
		t.Errorf("storage = %+v", st)
	}
	if err := h.c.RemoveStorage(h.ctx, testSystem, "hetzner"); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("removed the backups' storage: %v", err)
	}
}

// A copy the check doesn't find fails the move before anything switches:
// the backups keep their snapshots and new ones still go to the local
// repository, which isn't emptied.
func TestMoveBackupsFailsBeforeTheSwitch(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	snap := strings.Repeat("e", 64)
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		switch {
		case strings.Contains(spec.Name, "_move_check"):
			return node.JobResult{Output: moveCheckOutput(nil)}, nil
		case strings.Contains(spec.Name, "_backup_move_"):
			return node.JobResult{}, nil
		case strings.Contains(spec.Name, "_backup"):
			return node.JobResult{Output: resticOutput(snap, 10, snap)}, nil
		}
		return node.JobResult{}, nil
	})
	h.runBackup(db)
	if _, err := h.c.AddStorage(h.ctx, testSystem, apitypes.CreateStorageRequest{Name: "hetzner", Endpoint: fakeS3(t), Bucket: "images", AccessKeyID: "k", SecretAccessKey: "s"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.MoveBackups(h.ctx, testSystem, apitypes.MoveBackupsRequest{Storage: "hetzner"}); err != nil {
		t.Fatal(err)
	}
	set := h.waitMoved()
	if !strings.Contains(set.Move.Error, "no copy") || set.Storage != "" || set.Repository != "" {
		t.Fatalf("after a failed move: %+v %+v", set, set.Move)
	}
	if slices.ContainsFunc(h.rt.Jobs(), func(j node.JobSpec) bool { return strings.Contains(j.Name, "_move_empty") }) {
		t.Error("the local repository was emptied after a failed move")
	}
	if b, _ := h.c.ServiceBackups(h.ctx, testSystem, db); b.Runs[0].Snapshot != snap || b.Runs[0].Repository != "local" {
		t.Errorf("backup = %+v", b.Runs[0])
	}
	if r := h.runBackup(db); r.Status != apitypes.BackupSucceeded || r.Repository != "local" {
		t.Errorf("a backup after the failed move = %+v", r)
	}
}

// A repository URL set before storage connections becomes a connection
// on start, and backups keep going to the same place.
func TestConvertBackupRepository(t *testing.T) {
	h := newHarness(t)
	keys, _ := json.Marshal(s3Credentials{AccessKeyID: "AK", SecretAccessKey: "SK", Region: "fsn1"})
	sealed, _ := h.c.box.Seal(keys)
	pw, _ := h.c.box.Seal([]byte("pw"))
	old := "s3:https://fsn1.your-objectstorage.com/Old.Backups/dock/"
	if _, err := h.store.UpsertBackupSettings(h.ctx, store.UpsertBackupSettingsParams{
		Repository: old, Credentials: sealed, Password: pw, KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 6, Hour: 3, UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	h.restart()
	set, err := h.c.GetBackupSettings(h.ctx, testSystem, false)
	want := "s3:https://fsn1.your-objectstorage.com/Old.Backups/dock"
	if err != nil || set.Storage != "old-backups" || set.Path != "dock" || set.Repository != want {
		t.Fatalf("converted = %+v, %v", set, err)
	}
	s, _ := h.c.backupSettings(h.ctx)
	repo, err := h.c.repoOf(h.ctx, s)
	if err != nil || repo.AccessKeyID != "AK" || repo.SecretAccessKey != "SK" || repo.Region != "fsn1" || !repo.PathStyle || repo.URL != want {
		t.Fatalf("repo = %+v, %v", repo, err)
	}
	if _, err := h.c.ConfigureBackups(h.ctx, testSystem, apitypes.ConfigureBackupRequest{Repository: new("")}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("set a repository URL: %v", err)
	}
	if got, err := h.c.ConfigureBackups(h.ctx, testSystem, apitypes.ConfigureBackupRequest{KeepDaily: new(9)}); err != nil || got.Storage != "old-backups" || got.KeepDaily != 9 {
		t.Fatalf("retention changed storage: %+v, %v", got, err)
	}
}

// Without backups there is nothing to copy or check: the move switches
// where new backups go at once.
func TestMoveBackupsWithNothingToCopy(t *testing.T) {
	h := newHarness(t)
	if _, err := h.c.AddStorage(h.ctx, testSystem, apitypes.CreateStorageRequest{Name: "hetzner", Endpoint: fakeS3(t), Bucket: "images", AccessKeyID: "k", SecretAccessKey: "s"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.MoveBackups(h.ctx, testSystem, apitypes.MoveBackupsRequest{Storage: "hetzner", Path: "dock/backups"}); err != nil {
		t.Fatal(err)
	}
	set := h.waitMoved()
	if set.Move.Error != "" || set.Storage != "hetzner" || set.Path != "dock/backups" || len(set.Move.Sources) != 0 {
		t.Fatalf("after the move: %+v %+v", set, set.Move)
	}
	if slices.ContainsFunc(h.rt.Jobs(), func(j node.JobSpec) bool { return strings.Contains(j.Name, "_move") }) {
		t.Error("a move with nothing to copy ran jobs")
	}
}
