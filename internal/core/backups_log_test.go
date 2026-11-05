package core

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// A finished run keeps its job's output for View log, with the
// repository's and database's secrets masked, for deploy access only and
// for the service's newest runs.
func TestBackupLog(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	fail := true
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		if !strings.Contains(spec.Name, "_backup") {
			return node.JobResult{}, nil // restic install
		}
		if fail {
			out := "connecting as " + spec.Env["PGPASSWORD"] + "\nrepository password " + spec.Env["RESTIC_PASSWORD"] + "\nFatal: unable to open repository\n"
			return node.JobResult{ExitCode: 1, Output: out}, nil
		}
		return node.JobResult{Output: resticOutput("eeee5555eeee", 10, "eeee5555eeee")}, nil
	})
	failed := h.runBackup(db)
	if failed.Status != apitypes.BackupFailed {
		t.Fatalf("run = %+v", failed)
	}
	log, err := h.c.BackupLog(h.ctx, testSystem, db, failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	pw := h.revealed(db)["POSTGRES_PASSWORD"]
	if !strings.Contains(log.Output, "Fatal: unable to open repository") || strings.Contains(log.Output, pw) || strings.Count(log.Output, secretMask) != 2 {
		t.Fatalf("log = %q; want the output with both secrets masked", log.Output)
	}
	if _, err := h.c.BackupLog(h.ctx, h.user("viewer@x", apitypes.ScopeRead), db, failed.ID); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a reader reading a log: %v", err)
	}
	other := h.templateDB("other", apitypes.TemplatePostgres)
	if _, err := h.c.BackupLog(h.ctx, testSystem, other, failed.ID); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("another service's run: %v", err)
	}

	fail = false
	var last apitypes.BackupRun
	for range backupLogRuns {
		last = h.runBackup(db)
	}
	if log, _ := h.c.BackupLog(h.ctx, testSystem, db, last.ID); !strings.Contains(log.Output, "eeee5555eeee") {
		t.Fatalf("newest log = %q", log.Output)
	}
	if log, err := h.c.BackupLog(h.ctx, testSystem, db, failed.ID); err != nil || log.Output != "" {
		t.Fatalf("a log older than the newest %d = %q, %v; want none kept", backupLogRuns, log.Output, err)
	}
}

// A backup in another repository than the current one says so before the
// click (BackupRun.NotRestorable) and in the restore's error, naming both;
// a local one restores however it was recorded.
func TestBackupRestorableHere(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	h.rt.OnJob(func(node.JobSpec) (node.JobResult, error) {
		return node.JobResult{Output: resticOutput("ffff6666ffff", 10, "ffff6666ffff")}, nil
	})
	run := h.runBackup(db)
	b, _ := h.c.ServiceBackups(h.ctx, testSystem, db)
	if b.Runs[0].NotRestorable != "" {
		t.Fatalf("a local backup is not restorable: %s", b.Runs[0].NotRestorable)
	}
	if _, err := h.store.DB.ExecContext(h.ctx, "UPDATE backups SET repository = 'local' WHERE id = ?", run.ID); err != nil {
		t.Fatal(err)
	}
	if b, _ := h.c.ServiceBackups(h.ctx, testSystem, db); b.Runs[0].NotRestorable != "" {
		t.Fatalf("a backup recorded as local is not restorable: %s", b.Runs[0].NotRestorable)
	}

	const elsewhere = "s3:https://s3.example.com/old-bucket/dock-backups"
	if _, err := h.store.DB.ExecContext(h.ctx, "UPDATE backups SET repository = ? WHERE id = ?", elsewhere, run.ID); err != nil {
		t.Fatal(err)
	}
	b, _ = h.c.ServiceBackups(h.ctx, testSystem, db)
	if why := b.Runs[0].NotRestorable; !strings.Contains(why, elsewhere) || !strings.Contains(why, "local repository") {
		t.Fatalf("not restorable = %q; want both repositories named", why)
	}
	_, err := h.c.RestoreBackup(h.ctx, testSystem, db, apitypes.RestoreBackupRequest{Snapshot: "ffff6666"})
	if CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), elsewhere) || !strings.Contains(err.Error(), "local repository") {
		t.Fatalf("restoring from another repository: %v", err)
	}
}
