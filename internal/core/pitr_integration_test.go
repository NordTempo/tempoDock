//go:build integration

package core

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/s3"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/walarchive"
)

// buildDock builds the dock binary for the test's archiver and jobs,
// which run it (walarchive.Command).
func buildDock(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dock")
	cmd := exec.Command("go", "build", "-trimpath", "-o", path, "github.com/NordTempo/tempoDock/cmd/dock")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build dock: %v\n%s", err, out)
	}
	return path
}

// ADR 0093 with real Postgres, restic and MinIO: rows are written while
// the database archives, a restore to a time between two writes holds
// exactly the rows before it, a time after the last commit replays the
// archive to its end, and a lost segment refuses with the time the
// archive still reaches.
func TestIntegrationPointInTimeRestore(t *testing.T) {
	t.Parallel()
	var install string
	t.Cleanup(func() { removeInstallVolumes(t, install) })
	c := dockerHarnessWithTools(t, buildProbe(t), buildDock(t))
	install = c.install
	ctx := context.Background()
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := startMinIOBucket(t, docker, "backups")
	if _, err := c.AddStorage(ctx, testSystem, apitypes.CreateStorageRequest{Name: "minio", Endpoint: endpoint, Bucket: "backups", AccessKeyID: "dockminio", SecretAccessKey: "dockminio-secret"}); err != nil {
		t.Fatal(err)
	}
	conn, err := c.store.GetStorageConnection(ctx, "minio")
	if err != nil {
		t.Fatal(err)
	}
	set, err := c.backupSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.repoOf(ctx, set); err != nil {
		t.Fatal(err)
	}
	if set, err = c.store.GetBackupSettings(ctx); err != nil {
		t.Fatal(err)
	}
	set.StorageID, set.StoragePath = &conn.ID, "dock-backups"
	if _, err := c.store.UpsertBackupSettings(ctx, upsertParams(set)); err != nil {
		t.Fatal(err)
	}

	project := fmt.Sprintf("pitr-%d", time.Now().UnixNano()%1_000_000)
	if _, err := c.CreateProject(ctx, testSystem, apitypes.CreateProjectRequest{Name: project}); err != nil {
		t.Fatal(err)
	}
	db := ServiceRef{project, "production", "db"}
	if _, err := c.CreateService(ctx, testSystem, EnvRef{project, "production"}, apitypes.CreateServiceRequest{Name: "db", Template: apitypes.TemplatePostgres}); err != nil {
		t.Fatal(err)
	}
	if r := waitRelease(t, c, db, 1); r.Status != apitypes.ReleaseActive {
		t.Fatalf("db: %s %s", r.Status, r.Error)
	}
	psql := func(ref ServiceRef, sql string) string {
		t.Helper()
		return execIn(t, c, ref, "psql", "-U", "postgres", "-d", "app", "-v", "ON_ERROR_STOP=1", "-tAc", sql)
	}
	psql(db, "CREATE TABLE notes (id int PRIMARY KEY, at timestamptz NOT NULL DEFAULT clock_timestamp())")

	on, days := true, 7
	if _, err := c.SetPITR(ctx, testSystem, db, apitypes.SetPITRRequest{Enabled: &on, Days: &days}); err != nil {
		t.Fatal(err)
	}
	if r := waitRelease(t, c, db, 2); r.Status != apitypes.ReleaseActive {
		t.Fatalf("restart with archiving: %s %s", r.Status, r.Error)
	}
	if got := psql(db, "SHOW archive_mode"); got != "on" {
		t.Fatalf("archive_mode = %q", got)
	}
	window := func(cond func(*apitypes.PITR) bool, what string) *apitypes.PITR {
		t.Helper()
		for deadline := time.Now().Add(3 * time.Minute); ; time.Sleep(time.Second) {
			b, err := c.ServiceBackups(ctx, testSystem, db)
			if err != nil {
				t.Fatal(err)
			}
			if b.PITR != nil && cond(b.PITR) {
				return b.PITR
			}
			if time.Now().After(deadline) {
				for _, r := range b.Runs {
					t.Logf("run %d %s %s %s: %s", r.ID, r.Operation, r.Kind, r.Status, r.Error)
				}
				t.Fatalf("%s: %+v %+v", what, b.PITR, b.PITR.Archiver)
			}
		}
	}
	window(func(p *apitypes.PITR) bool { return p.BaseBackups == 1 && p.From != nil }, "the first base backup")

	// Rows 1-10, the time, rows 11-20, each its own commit; segments
	// switch between them, so the archive holds several.
	for i := 1; i <= 20; i++ {
		psql(db, fmt.Sprintf("INSERT INTO notes (id) VALUES (%d)", i))
		if i%5 == 0 {
			psql(db, "SELECT pg_switch_wal()")
		}
		if i == 10 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	at := psql(db, "SELECT (extract(epoch FROM (SELECT at FROM notes WHERE id = 10)) * 1e6)::bigint || ' ' || (extract(epoch FROM (SELECT at FROM notes WHERE id = 11)) * 1e6)::bigint")
	var t10, t11 int64
	if _, err := fmt.Sscan(at, &t10, &t11); err != nil {
		t.Fatalf("times %q: %v", at, err)
	}
	between := time.UnixMicro((t10 + t11) / 2)
	// The last segment ends after end: the archive holds every commit
	// up to it, and none comes after.
	end := time.Now()
	psql(db, "SELECT pg_switch_wal()")
	window(func(p *apitypes.PITR) bool { return p.Through != nil && p.Through.After(end) }, "archiving past the last write")

	restore := func(to time.Time, as string) apitypes.BackupRun {
		t.Helper()
		res, err := c.RestoreBackup(ctx, testSystem, db, apitypes.RestoreBackupRequest{ToTime: &to, As: as})
		if err != nil {
			t.Fatal(err)
		}
		return waitBackupRun(t, c, db, res.Restore.ID)
	}
	if r := restore(between, "db-between"); r.Status != apitypes.BackupSucceeded {
		t.Fatalf("restore between rows 10 and 11: %+v", r)
	}
	if got := psql(ServiceRef{project, "production", "db-between"}, "SELECT string_agg(id::text, ',' ORDER BY id) FROM notes"); got != "1,2,3,4,5,6,7,8,9,10" {
		t.Fatalf("rows restored to between 10 and 11: %s", got)
	}
	noSecrets(t, c, docker, db, ServiceRef{project, "production", "db-between"})
	// No commit after it: the archive replays to its end.
	if r := restore(end, "db-end"); r.Status != apitypes.BackupSucceeded {
		t.Fatalf("restore after the last commit: %+v", r)
	}
	if got := psql(ServiceRef{project, "production", "db-end"}, "SELECT count(*) FROM notes"); got != "20" {
		t.Fatalf("rows restored to the end: %s", got)
	}
	if got := psql(db, "SELECT count(*) FROM notes"); got != "20" {
		t.Fatalf("the live database changed: %s", got)
	}

	// A lost segment between the base backup and the time: refused, with
	// the latest time the archive still reaches.
	ss, err := c.loadService(ctx, 1, db)
	if err != nil {
		t.Fatal(err)
	}
	row, err := c.store.GetPITR(ctx, ss.svc.ID)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := c.walConfig(ctx, ss.svc.ID, row.Generation)
	if err != nil {
		t.Fatal(err)
	}
	ix, err := walarchive.List(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	bases, err := c.store.ListPITRBases(ctx, ss.svc.ID)
	if err != nil || len(bases) != 1 {
		t.Fatalf("bases: %v %v", bases, err)
	}
	seg, err := walarchive.ParseSegment(bases[0].WalStart, bases[0].WalSegmentBytes)
	if err != nil {
		t.Fatal(err)
	}
	seg.Number += 2
	lost, ok := ix[seg.Name(bases[0].WalSegmentBytes)]
	if !ok {
		t.Fatalf("no segment %s in the archive: %v", seg.Name(bases[0].WalSegmentBytes), ix)
	}
	if err := (&s3.Client{Bucket: cfg.Bucket}).Delete(ctx, []string{lost.Key}); err != nil {
		t.Fatal(err)
	}
	_, err = c.RestoreBackup(ctx, testSystem, db, apitypes.RestoreBackupRequest{ToTime: &end, As: "db-gap"})
	if err == nil || !strings.Contains(err.Error(), lost.File+" is missing from the archive") || !strings.Contains(err.Error(), "restored only up to") {
		t.Fatalf("restore past a lost segment: %v", err)
	}

	// In place, to a time before the lost segment: db itself goes back,
	// and archiving starts a new generation with its own base backup.
	early := bases[0].ConsistentAt.Add(time.Millisecond)
	res, err := c.RestoreBackup(ctx, testSystem, db, apitypes.RestoreBackupRequest{ToTime: &early, InPlace: true})
	if err != nil {
		t.Fatal(err)
	}
	if r := waitBackupRun(t, c, db, res.Restore.ID); r.Status != apitypes.BackupSucceeded || r.RestoredTo != "db" {
		t.Fatalf("restore in place: %+v", r)
	}
	if got := psql(db, "SELECT count(*) FROM notes"); got != "0" {
		t.Fatalf("rows after restoring db in place to its first base backup: %s", got)
	}
	next, err := c.store.GetPITR(ctx, ss.svc.ID)
	if err != nil || next.Generation == row.Generation {
		t.Fatalf("generation after the restore: %+v %v", next, err)
	}
	window(func(p *apitypes.PITR) bool {
		bs, err := c.store.ListPITRBases(ctx, ss.svc.ID)
		return err == nil && slices.ContainsFunc(bs, func(b store.Backup) bool { return b.WalGeneration == next.Generation && b.WalStart != "" })
	}, "a base backup of the new generation")
}

// noSecrets checks with docker inspect that the archive's key and the
// bucket's secret are in the archiver's environment only: not in the
// databases' containers (env, entrypoint or command) and not on the
// archiver's command line.
func noSecrets(t *testing.T, c *Core, docker *client.Client, src ServiceRef, dbs ...ServiceRef) {
	t.Helper()
	ctx := context.Background()
	ss, err := c.loadService(ctx, 1, src)
	if err != nil {
		t.Fatal(err)
	}
	side, err := docker.ContainerInspect(ctx, archiverName(ss), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var secrets []string
	for _, e := range side.Container.Config.Env {
		if k, v, _ := strings.Cut(e, "="); slices.Contains(walarchive.Secrets, k) && v != "" {
			secrets = append(secrets, v)
		}
	}
	if len(secrets) != len(walarchive.Secrets) {
		t.Fatalf("the archiver's environment holds %d of the secrets", len(secrets))
	}
	leak := func(what string, texts []string) {
		t.Helper()
		for _, text := range texts {
			for _, sec := range secrets {
				if strings.Contains(text, sec) {
					t.Errorf("%s holds a secret", what)
				}
			}
		}
	}
	leak("the archiver's command line", append(slices.Clone(side.Container.Config.Entrypoint), side.Container.Config.Cmd...))
	for _, ref := range append([]ServiceRef{src}, dbs...) {
		s, err := c.loadService(ctx, 1, ref)
		if err != nil {
			t.Fatal(err)
		}
		rel, err := c.store.GetActiveRelease(ctx, s.svc.ID)
		if err != nil {
			t.Fatal(err)
		}
		ctr, err := docker.ContainerInspect(ctx, rel.ContainerID, client.ContainerInspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		cfg := ctr.Container.Config
		leak(ref.String()+"'s environment", cfg.Env)
		leak(ref.String()+"'s command line", append(slices.Clone(cfg.Entrypoint), cfg.Cmd...))
		for _, e := range cfg.Env {
			if strings.HasPrefix(e, "DOCK_WAL_") {
				t.Errorf("%s has %s", ref, e[:strings.Index(e, "=")])
			}
		}
	}
}
