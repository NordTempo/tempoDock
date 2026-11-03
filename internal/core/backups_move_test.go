package core

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// listing is what a move job prints after the copy or check: a token
// line per snapshot's original (if it is a copy) and its ID, as
// backup.CopyScript and CheckScript emit them. snaps maps ID to original
// ("" for none).
func listing(snaps map[string]string) string {
	var b strings.Builder
	b.WriteString("done\n" + backup.MoveMarker + "\n")
	for id, orig := range snaps {
		if orig != "" {
			fmt.Fprintf(&b, "\"original\":%q\n", orig)
		}
		fmt.Fprintf(&b, "\"id\":%q\n", id)
	}
	return b.String()
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
			return node.JobResult{Output: listing(map[string]string{copies[old[0]]: old[0], copies[old[1]]: old[1]})}, nil
		case strings.Contains(spec.Name, "_move_empty"):
			return node.JobResult{}, nil
		case strings.Contains(spec.Name, "_backup_move_"):
			if gated {
				<-gate
			}
			return node.JobResult{Output: listing(map[string]string{old[0]: "", old[1]: ""})}, nil
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
			return node.JobResult{Output: listing(nil)}, nil
		case strings.Contains(spec.Name, "_backup_move_"):
			return node.JobResult{Output: listing(map[string]string{snap: ""})}, nil
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
	sealed, _ := h.c.box.Seal(keys, backupKeysAAD)
	pw, _ := h.c.box.Seal([]byte("pw"), backupPasswordAAD)
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

// A caller still holding the defaults (read before a move created the
// settings and switched storage) only gets the stored password from
// repoOf: the move's storage and password stay.
func TestRepoOfKeepsSettingsWrittenMeanwhile(t *testing.T) {
	h := newHarness(t)
	stale, err := h.c.backupSettings(h.ctx)
	if err != nil || stale.Password != nil {
		t.Fatalf("defaults = %+v, %v", stale, err)
	}
	if _, err := h.c.AddStorage(h.ctx, testSystem, apitypes.CreateStorageRequest{Name: "hetzner", Endpoint: fakeS3(t), Bucket: "images", AccessKeyID: "k", SecretAccessKey: "s"}); err != nil {
		t.Fatal(err)
	}
	conn, err := h.store.GetStorageConnection(h.ctx, "hetzner")
	if err != nil {
		t.Fatal(err)
	}
	pw, _ := h.c.box.Seal([]byte("moved-pw"), backupPasswordAAD)
	if _, err := h.store.UpsertBackupSettings(h.ctx, store.UpsertBackupSettingsParams{
		Password: pw, KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 6, Hour: 3, UpdatedAt: time.Now(), StorageID: &conn.ID, StoragePath: "dock",
	}); err != nil {
		t.Fatal(err)
	}

	repo, err := h.c.repoOf(h.ctx, stale)
	if err != nil || repo.Password != "moved-pw" {
		t.Fatalf("repo = %+v, %v", repo, err)
	}
	row, err := h.store.GetBackupSettings(h.ctx)
	if err != nil || row.StorageID == nil || *row.StorageID != conn.ID || row.StoragePath != "dock" || string(row.Password) != string(pw) {
		t.Fatalf("settings after repoOf = %+v, %v", row, err)
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

// A backup whose snapshot its repository doesn't hold any more was gone
// before the move: it is marked pruned, and the move goes on. A second
// move, from that bucket to another folder of it, matches the backups
// through the original they were first copied from; a folder inside the
// current one is refused, since emptying the source would delete it.
func TestMoveBackupsAgainAndGone(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	local := []string{strings.Repeat("1", 64), strings.Repeat("2", 64)}
	inA, inB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	n := 0
	var source, target map[string]string
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		switch {
		case strings.Contains(spec.Name, "_move_check"):
			return node.JobResult{Output: listing(target)}, nil
		case strings.Contains(spec.Name, "_move_empty"):
			return node.JobResult{}, nil
		case strings.Contains(spec.Name, "_backup_move_"):
			return node.JobResult{Output: listing(source)}, nil
		case strings.Contains(spec.Name, "_backup"):
			n++
			return node.JobResult{Output: resticOutput(local[n-1], 10, local[:n]...)}, nil
		}
		return node.JobResult{}, nil
	})
	h.runBackup(db)
	h.runBackup(db)
	endpoint := fakeS3(t)
	if _, err := h.c.AddStorage(h.ctx, testSystem, apitypes.CreateStorageRequest{Name: "hetzner", Endpoint: endpoint, Bucket: "images", AccessKeyID: "k", SecretAccessKey: "s"}); err != nil {
		t.Fatal(err)
	}

	// The first backup's snapshot went missing from the local repository.
	source = map[string]string{local[1]: ""}
	target = map[string]string{inA: local[1]}
	if _, err := h.c.MoveBackups(h.ctx, testSystem, apitypes.MoveBackupsRequest{Storage: "hetzner", Path: "a"}); err != nil {
		t.Fatal(err)
	}
	set := h.waitMoved()
	if set.Move.Error != "" || set.Move.Moved != 1 || set.Move.Gone != 1 {
		t.Fatalf("first move: %+v", set.Move)
	}
	if b, _ := h.c.ServiceBackups(h.ctx, testSystem, db); len(b.Runs) != 2 || b.Runs[0].Snapshot != inA {
		t.Fatalf("backups after the first move: %+v", b.Runs)
	}

	if _, err := h.c.MoveBackups(h.ctx, testSystem, apitypes.MoveBackupsRequest{Storage: "hetzner", Path: "a/inner"}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("moved into a folder inside the current one: %v", err)
	}
	// Bucket A's snapshot is a copy of the local one; B's copy keeps that
	// original, not A's ID.
	source = map[string]string{inA: local[1]}
	target = map[string]string{inB: local[1]}
	if _, err := h.c.MoveBackups(h.ctx, testSystem, apitypes.MoveBackupsRequest{Storage: "hetzner", Path: "b"}); err != nil {
		t.Fatal(err)
	}
	if set := h.waitMoved(); set.Move.Error != "" || set.Move.Moved != 1 || set.Path != "b" {
		t.Fatalf("second move: %+v", set.Move)
	}
	if b, _ := h.c.ServiceBackups(h.ctx, testSystem, db); b.Runs[0].Snapshot != inB || b.Runs[0].Repository != backup.S3URL(endpoint, "images", "b") {
		t.Fatalf("backup after the second move: %+v", b.Runs[0])
	}
}
