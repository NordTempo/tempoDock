//go:build integration

package core

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Checked backups on the real Docker runtime (ADR 0096): a Postgres with
// a table is backed up to the local restic repository; the repository
// check reads it back with restic, and the restore drill restores the
// snapshot into a throwaway service, counts its tables through the Data
// panel and leaves nothing behind; then the same drill for Redis and MySQL.
func TestIntegrationCheckedBackups(t *testing.T) {
	t.Parallel()
	var install string
	t.Cleanup(func() { removeInstallVolumes(t, install) })
	c := dockerHarness(t)
	install = c.install
	ctx := context.Background()
	project := fmt.Sprintf("it-%d", time.Now().UnixNano()%1_000_000)
	if _, err := c.CreateProject(ctx, testSystem, apitypes.CreateProjectRequest{Name: project}); err != nil {
		t.Fatal(err)
	}
	db := ServiceRef{project, "production", "db"}
	if _, err := c.CreateService(ctx, testSystem, db.Env(), apitypes.CreateServiceRequest{Name: db.Service, Template: apitypes.TemplatePostgres}); err != nil {
		t.Fatal(err)
	}
	if r := waitRelease(t, c, db, 1); r.Status != apitypes.ReleaseActive {
		t.Fatalf("db: %s %s", r.Status, r.Error)
	}
	execIn(t, c, db, "psql", "-U", "postgres", "-d", "app", "-v", "ON_ERROR_STOP=1", "-c",
		"CREATE TABLE notes (id serial PRIMARY KEY, body text); INSERT INTO notes (body) VALUES ('hello')")
	run, err := c.RunBackup(ctx, testSystem, db)
	if err != nil {
		t.Fatal(err)
	}
	if done := waitBackupRun(t, c, db, run.ID); done.Status != apitypes.BackupSucceeded {
		t.Fatalf("backup: %+v", done)
	}

	// The repository check: restic check with data read back.
	if _, err := c.RunBackupChecks(ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	wait := func(what string, read func(apitypes.ServiceBackups) *apitypes.BackupCheck) apitypes.BackupCheck {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Minute); ; time.Sleep(200 * time.Millisecond) {
			b, err := c.ServiceBackups(ctx, testSystem, db)
			if err != nil {
				t.Fatal(err)
			}
			if got := read(b); got != nil && got.Status != apitypes.BackupRunning {
				return *got
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s still running", what)
			}
		}
	}
	if check := wait("the repository check", func(b apitypes.ServiceBackups) *apitypes.BackupCheck { return b.Check }); check.Status != apitypes.BackupSucceeded {
		t.Fatalf("repository check: %+v", check)
	}

	// The drill: restored, queried, removed.
	if _, err := c.RunBackupDrill(ctx, testSystem, db); err != nil {
		t.Fatal(err)
	}
	drill := wait("the restore drill", func(b apitypes.ServiceBackups) *apitypes.BackupCheck { return b.Drill })
	if drill.Status != apitypes.BackupSucceeded || !strings.HasPrefix(drill.Detail, "1 table in ") {
		t.Fatalf("drill: %+v", drill)
	}
	// Redis restores differently (its volume is filled before it starts)
	// and answers a key count; MySQL restores as root into a new server.
	for _, tc := range []struct {
		name   string
		tpl    apitypes.Template
		write  func(ServiceRef)
		detail string
	}{
		{"cache", apitypes.TemplateRedis, func(ref ServiceRef) {
			execIn(t, c, ref, "redis-cli", "-a", liveVar(t, c, ref, "REDIS_PASSWORD"), "--no-auth-warning", "SET", "greeting", "hello")
		}, "1 key in "},
		{"my", apitypes.TemplateMySQL, func(ref ServiceRef) {
			execIn(t, c, ref, "mysql", "-uroot", "-p"+liveVar(t, c, ref, "MYSQL_ROOT_PASSWORD"), "app", "-e",
				"CREATE TABLE notes (id int PRIMARY KEY); CREATE TABLE tags (id int PRIMARY KEY); INSERT INTO notes VALUES (1)")
		}, "2 tables in "},
	} {
		ref := ServiceRef{project, "production", tc.name}
		if _, err := c.CreateService(ctx, testSystem, ref.Env(), apitypes.CreateServiceRequest{Name: ref.Service, Template: tc.tpl}); err != nil {
			t.Fatal(err)
		}
		if r := waitRelease(t, c, ref, 1); r.Status != apitypes.ReleaseActive {
			t.Fatalf("%s: %s %s", tc.name, r.Status, r.Error)
		}
		tc.write(ref)
		if run, err = c.RunBackup(ctx, testSystem, ref); err != nil {
			t.Fatal(err)
		}
		if done := waitBackupRun(t, c, ref, run.ID); done.Status != apitypes.BackupSucceeded {
			t.Fatalf("%s backup: %+v", tc.name, done)
		}
		if _, err := c.RunBackupDrill(ctx, testSystem, ref); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(5 * time.Minute); ; time.Sleep(200 * time.Millisecond) {
			b, err := c.ServiceBackups(ctx, testSystem, ref)
			if err != nil {
				t.Fatal(err)
			}
			if d := b.Drill; d != nil && d.Status != apitypes.BackupRunning {
				if d.Status != apitypes.BackupSucceeded || !strings.HasPrefix(d.Detail, tc.detail) {
					t.Fatalf("%s drill: %+v", tc.name, d)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the drill of %s still running", tc.name)
			}
		}
	}
	svcs, err := c.ListServices(ctx, testSystem, db.Env())
	if err != nil || len(svcs) != 3 {
		t.Fatalf("services after the drill: %+v, %v", svcs, err)
	}
}
