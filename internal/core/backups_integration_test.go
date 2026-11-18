//go:build integration

package core

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/cpbackup"
	"github.com/NordTempo/tempoDock/internal/node"
)

// Templates, backups and restores on the real Docker runtime: create a
// Redis from its template, write data, back it up to the local restic
// repository, restore it into a new service and read the data back there.
// Postgres's run in TestIntegrationPostgresRolesRestore and
// TestIntegrationMoveBackups.
func TestIntegrationTemplateBackupRestore(t *testing.T) {
	t.Parallel()
	// Registered first, so it runs after the harness removed the containers.
	var install string
	t.Cleanup(func() { removeInstallVolumes(t, install) })
	c := dockerHarness(t)
	install = c.install
	ctx := context.Background()
	project := fmt.Sprintf("it-%d", time.Now().UnixNano()%1_000_000)
	if _, err := c.CreateProject(ctx, testSystem, apitypes.CreateProjectRequest{Name: project}); err != nil {
		t.Fatal(err)
	}
	cache := ServiceRef{project, "production", "cache"}
	if _, err := c.CreateService(ctx, testSystem, cache.Env(), apitypes.CreateServiceRequest{Name: cache.Service, Template: apitypes.TemplateRedis}); err != nil {
		t.Fatal(err)
	}
	if r := waitRelease(t, c, cache, 1); r.Status != apitypes.ReleaseActive {
		t.Fatalf("cache: %s %s", r.Status, r.Error)
	}

	pw := liveVar(t, c, cache, "REDIS_PASSWORD")
	execIn(t, c, cache, "redis-cli", "-a", pw, "--no-auth-warning", "SET", "greeting", "hello")

	run, err := c.RunBackup(ctx, testSystem, cache)
	if err != nil {
		t.Fatal(err)
	}
	done := waitBackupRun(t, c, cache, run.ID)
	if done.Status != apitypes.BackupSucceeded || done.Snapshot == "" || done.SizeBytes == 0 {
		t.Fatalf("backup: %+v", done)
	}
	res, err := c.RestoreBackup(ctx, testSystem, cache, apitypes.RestoreBackupRequest{Snapshot: done.Snapshot[:8]})
	if err != nil {
		t.Fatal(err)
	}
	if r := waitBackupRun(t, c, cache, res.Restore.ID); r.Status != apitypes.BackupSucceeded {
		t.Fatalf("restore: %+v", r)
	}

	restoredCache := ServiceRef{project, "production", "cache-restored"}
	newPw := liveVar(t, c, restoredCache, "REDIS_PASSWORD")
	if newPw == pw {
		t.Error("the restored Redis reuses the live password")
	}
	if out := execIn(t, c, restoredCache, "redis-cli", "-a", newPw, "--no-auth-warning", "GET", "greeting"); out != "hello" {
		t.Errorf("restored Redis: %q", out)
	}
}

// A Postgres backup carries the server's roles: a table policy TO a role
// made inside the server restores into a new service, where the role is
// created (without its password) before pg_restore needs it.
func TestIntegrationPostgresRolesRestore(t *testing.T) {
	t.Parallel()
	var install string
	t.Cleanup(func() { removeInstallVolumes(t, install) })
	c := dockerHarness(t)
	install = c.install
	ctx := context.Background()
	project := fmt.Sprintf("pr-%d", time.Now().UnixNano()%1_000_000)
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
	for start := time.Now(); ; time.Sleep(200 * time.Millisecond) {
		if info, err := c.DataInfo(ctx, testSystem, db); err == nil && info.Status == apitypes.DataReady {
			break
		}
		if time.Since(start) > 30*time.Second {
			t.Fatal("data setup of the template database")
		}
	}
	execIn(t, c, db, "psql", "-U", "postgres", "-d", "app", "-v", "ON_ERROR_STOP=1", "-c", `
		CREATE ROLE tenant LOGIN PASSWORD 'secret' CONNECTION LIMIT 7;
		CREATE ROLE "Odd Name" IN ROLE tenant;
		CREATE TABLE notes (id int PRIMARY KEY, owner text);
		INSERT INTO notes SELECT n, 'tenant' FROM generate_series(1, 300) n;
		ALTER TABLE notes ENABLE ROW LEVEL SECURITY;
		CREATE POLICY own_notes ON notes TO tenant USING (owner = current_user);`)

	run, err := c.RunBackup(ctx, testSystem, db)
	if err != nil {
		t.Fatal(err)
	}
	done := waitBackupRun(t, c, db, run.ID)
	if done.Status != apitypes.BackupSucceeded {
		t.Fatalf("backup: %+v", done)
	}
	res, err := c.RestoreBackup(ctx, testSystem, db, apitypes.RestoreBackupRequest{Snapshot: done.Snapshot[:8]})
	if err != nil {
		t.Fatal(err)
	}
	if r := waitBackupRun(t, c, db, res.Restore.ID); r.Status != apitypes.BackupSucceeded {
		t.Fatalf("restore: %+v", r)
	}

	restored := ServiceRef{project, "production", "db-restored"}
	query := func(sql string) string {
		return execIn(t, c, restored, "psql", "-U", "postgres", "-d", "app", "-tAc", sql)
	}
	if out := query("SELECT count(*) FROM notes"); out != "300" {
		t.Errorf("restored rows: %q", out)
	}
	if out := query("SELECT array_to_string(roles, ',') FROM pg_policies WHERE policyname = 'own_notes'"); out != "tenant" {
		t.Errorf("the restored policy is for %q, want tenant", out)
	}
	// The role came back with its settings and memberships, and no password.
	if out := query("SELECT rolcanlogin, rolconnlimit, rolpassword IS NULL FROM pg_authid WHERE rolname = 'tenant'"); out != "t|7|t" {
		t.Errorf("restored role tenant: %q", out)
	}
	if out := query(`SELECT pg_has_role('Odd Name', 'tenant', 'MEMBER')`); out != "t" {
		t.Errorf("restored membership of \"Odd Name\" in tenant: %q", out)
	}
	// The new service's owner kept its own password: it still logs in.
	if out := query("SELECT rolsuper AND rolpassword IS NOT NULL FROM pg_authid WHERE rolname = 'postgres'"); out != "t" {
		t.Errorf("the restored service's owner: %q", out)
	}
}

// The control plane backs itself up into the local restic repository on
// the real Docker runtime, and the snapshot is the archive: its manifest
// reads back with restic dump.
func TestIntegrationControlPlaneBackup(t *testing.T) {
	t.Parallel()
	var install string
	t.Cleanup(func() { removeInstallVolumes(t, install) })
	c := dockerHarness(t)
	install = c.install
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(c.cfg.DataDir, cpbackup.SecretKeyFile), bytes.Repeat([]byte{7}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RunControlPlaneBackup(ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	var run apitypes.ControlPlaneBackupRun
	for deadline := time.Now().Add(5 * time.Minute); ; time.Sleep(200 * time.Millisecond) {
		set, err := c.GetBackupSettings(ctx, testSystem, false)
		if err != nil {
			t.Fatal(err)
		}
		if l := set.ControlPlane.Latest; l != nil && l.Status != apitypes.BackupRunning {
			run = *l
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("control plane backup still running")
		}
	}
	if run.Status != apitypes.BackupSucceeded || run.Snapshot == "" || run.SizeBytes == 0 {
		t.Fatalf("run = %+v", run)
	}
	set, err := c.backupSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := c.repoOf(ctx, set)
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.runResticJob(ctx, c.localID, c.rt, repo, node.JobSpec{
		Name: fmt.Sprintf("dock_it_cp_dump_%d", time.Now().UnixNano()), Image: c.cfg.Backup.ResticImage,
		Labels: node.Labels{ControlPlane: c.install}, Env: repo.Env(),
		Script: backup.BinDir + "/restic dump --tag " + backup.ControlPlaneTag + " latest /" + backup.ControlPlaneFile + " | tar -xOf - " + cpbackup.ManifestName,
	})
	if err != nil {
		t.Fatalf("dump: %v: %s", err, out)
	}
	if !strings.Contains(out, `"install_id": "`+c.install+`"`) {
		t.Fatalf("manifest from the snapshot: %s", out)
	}
}

// execIn runs a command in a service's active container and returns its
// trimmed output.
func execIn(t *testing.T, c *Core, ref ServiceRef, args ...string) string {
	t.Helper()
	ss, err := c.serviceFor(context.Background(), testSystem, ref, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := c.store.GetActiveRelease(context.Background(), ss.svc.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Postgres's image restarts its server once after init; retry briefly.
	var out []byte
	for start := time.Now(); ; time.Sleep(500 * time.Millisecond) {
		out, err = exec.Command("docker", append([]string{"exec", rel.ContainerID}, args...)...).CombinedOutput()
		if err == nil || time.Since(start) > 30*time.Second {
			break
		}
	}
	if err != nil {
		t.Fatalf("%s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func liveVar(t *testing.T, c *Core, ref ServiceRef, key string) string {
	t.Helper()
	vars, err := c.ListVariables(context.Background(), testSystem, ref.Env(), ref.Service, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vars {
		if v.Service == ref.Service && v.Key == key && v.Value != nil {
			return *v.Value
		}
	}
	t.Fatalf("%s has no %s", ref.Service, key)
	return ""
}

func waitBackupRun(t *testing.T, c *Core, ref ServiceRef, id int64) apitypes.BackupRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for {
		b, err := c.ServiceBackups(context.Background(), testSystem, ref)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range b.Runs {
			if r.ID == id && r.Status != apitypes.BackupRunning {
				return r
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %d of %s still running", id, ref.Service)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// removeInstallVolumes removes the volumes a test install created (its
// services', the restic binary and the local repository), all labelled
// with its install ID.
func removeInstallVolumes(t *testing.T, install string) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return
	}
	defer func() { _ = cli.Close() }()
	ctx := context.Background()
	res, err := cli.VolumeList(ctx, client.VolumeListOptions{Filters: client.Filters{}.Add("label", "dock.control_plane="+install)})
	if err != nil {
		t.Logf("list volumes: %v", err)
		return
	}
	for _, v := range res.Items {
		if _, err := cli.VolumeRemove(ctx, v.Name, client.VolumeRemoveOptions{Force: true}); err != nil {
			t.Logf("remove volume %s: %v", v.Name, err)
		}
	}
}
