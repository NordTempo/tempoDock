package core

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/pg"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// drillEngine is a Postgres that sets up and answers a schema of two
// tables and a view, whatever the service: what a drill's query needs.
type drillEngine struct{ data.Engine }

func (drillEngine) Kind() apitypes.DataEngine { return apitypes.EnginePostgres }

func (drillEngine) Status(context.Context, data.Target) (data.ServerInfo, error) {
	return data.ServerInfo{Version: "17.2", VersionNum: 170002}, nil
}

func (drillEngine) Setup(context.Context, data.Target, data.Credentials, data.Credentials, data.Credentials) (data.ServerInfo, error) {
	return data.ServerInfo{Version: "17.2", VersionNum: 170002}, nil
}

func (drillEngine) SyncGrants(context.Context, data.Target, data.Credentials, []apitypes.DataColumnRef) (string, error) {
	return "synced", nil
}

func (drillEngine) EnsureMonitor(context.Context, data.Target, data.Credentials) error { return nil }

func (drillEngine) EnableQueryStats(context.Context, data.Target, data.Credentials) (apitypes.DataQueryStatsState, bool, error) {
	return "", false, nil
}

func (drillEngine) Schema(context.Context, data.Target, data.Call, []apitypes.DataColumnRef) (apitypes.DataSchema, error) {
	return apitypes.DataSchema{Tables: []apitypes.DataTable{
		{Schema: "public", Name: "orders", Kind: apitypes.TableKindTable},
		{Schema: "public", Name: "customers", Kind: apitypes.TableKindTable},
		{Schema: "public", Name: "totals", Kind: apitypes.TableKindView},
	}}, nil
}

func (drillEngine) Page(context.Context, data.Target, data.Call, apitypes.DataPageRequest) (apitypes.DataPage, error) {
	return apitypes.DataPage{}, nil
}

func (drillEngine) References(context.Context, data.Target, data.Call, apitypes.DataReferencesRequest) ([]apitypes.DataReferences, error) {
	return nil, nil
}

func (drillEngine) Redact(sql string) string { return pg.New(pg.Options{}).Redact(sql) }
func (drillEngine) RereadSchema(int64) bool  { return false }
func (drillEngine) Forget(int64)             {}
func (drillEngine) Close()                   {}

// drillDB is a harness with a Postgres template database db whose one
// backup, of size bytes, succeeded. restoreErr, while set, fails every
// restore job. The weekly checks are off, so the daemon's own scheduler
// (a backup's end kicks a pass) never runs a drill beside the test's;
// drillNow runs its passes as if they were on.
func drillDB(t *testing.T, size int64) (h *harness, db ServiceRef, restoreErr *error) {
	h = newHarness(t)
	h.opts.DataEngines = []data.Engine{drillEngine{}}
	h.restart()
	db = h.templateDB("db", apitypes.TemplatePostgres)
	snap := "dddd4444dddd4444"
	restoreErr = new(error)
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		if strings.Contains(spec.Name, "_restore") && *restoreErr != nil {
			return node.JobResult{}, *restoreErr
		}
		return node.JobResult{Output: resticOutput(snap, size, snap)}, nil
	})
	// Off before the backup, so the pass its end kicks can't read them on.
	// The settings row is made with the repository: open it first.
	set, err := h.c.backupSettings(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.repoOf(h.ctx, set); err != nil {
		t.Fatal(err)
	}
	if err := h.c.store.SetBackupVerify(h.ctx, store.SetBackupVerifyParams{UpdatedAt: now(), UpdatedBy: System.Name}); err != nil {
		t.Fatal(err)
	}
	h.runBackup(db)
	return h, db, restoreErr
}

// drillNow runs a drill pass and returns db's latest drill.
func (h *harness) drillNow(db ServiceRef, force bool) apitypes.BackupCheck {
	h.t.Helper()
	set, err := h.c.backupSettings(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	set.Verify = true
	if _, err := h.c.drillPass(h.ctx, set, force); err != nil {
		h.t.Fatal(err)
	}
	ss, err := h.c.loadService(h.ctx, 1, db)
	if err != nil {
		h.t.Fatal(err)
	}
	d, err := h.c.latestDrill(h.ctx, ss)
	if err != nil || d == nil {
		h.t.Fatalf("latest drill = %v, %v", d, err)
	}
	return *d
}

// noDrillService fails the test when anything of db's drill service is
// left: its row, a container or a volume.
func (h *harness) noDrillService(db ServiceRef) {
	h.t.Helper()
	ss, err := h.c.loadService(h.ctx, 1, db)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.c.loadService(h.ctx, 1, ServiceRef{db.Project, db.Environment, db.Service + drillSuffix}); CodeOf(err) != apitypes.CodeNotFound {
		h.t.Errorf("the drill's service is still there: %v", err)
	}
	ctrs, _ := h.rt.List(h.ctx)
	for _, ct := range ctrs {
		if ct.Labels.Service != h.c.labels(ss, 0).Service {
			h.t.Errorf("container %s of another service is left", ct.Name)
		}
	}
	if vols := h.rt.Volumes(); slices.ContainsFunc(vols, func(v string) bool { return v != volumeName(ss, templateVolume) && strings.HasPrefix(v, "dock_p") }) {
		h.t.Errorf("volumes = %v", vols)
	}
}

func TestDrillRestoresQueriesAndRemovesTheCopy(t *testing.T) {
	h, db, _ := drillDB(t, 100)
	for range 2 { // the second finds the name free again
		d := h.drillNow(db, true)
		if d.Status != apitypes.BackupSucceeded || !strings.HasPrefix(d.Detail, "2 tables in ") || d.Snapshot != "dddd4444dddd4444" || d.Kind != "drill" {
			t.Fatalf("drill = %+v", d)
		}
		h.noDrillService(db)
	}
	// The live database is free for its next backup.
	if run := h.runBackup(db); run.Status != apitypes.BackupSucceeded {
		t.Fatalf("backup after the drill = %+v", run)
	}
	// Nothing of the drill blocks deleting the project.
	if _, err := h.c.ArchiveProject(h.ctx, testSystem, "shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.DeleteProject(h.ctx, testSystem, "shop", apitypes.DeleteProjectRequest{Confirm: "shop"}); err != nil {
		t.Fatalf("delete the project after a drill: %v", err)
	}
}

func TestDrillFailureAlertsUntilOnePasses(t *testing.T) {
	h, db, restoreErr := drillDB(t, 100)
	ss, _ := h.c.loadService(h.ctx, 1, db)
	*restoreErr = errors.New("pg_restore: error: could not read from input file")
	d := h.drillNow(db, true)
	// The cause alone: the drill's service is removed, never archived, so
	// the restore's own words about its target don't belong here.
	if d.Status != apitypes.BackupFailed || !strings.HasSuffix(d.Error, "restoring the snapshot failed: pg_restore: error: could not read from input file") {
		t.Fatalf("drill = %+v", d)
	}
	if !h.alertOpen(drillKey(ss)) {
		t.Error("a failed drill opened no alert")
	}
	h.noDrillService(db)

	*restoreErr = nil
	if d := h.drillNow(db, true); d.Status != apitypes.BackupSucceeded {
		t.Fatalf("drill = %+v", d)
	}
	if h.alertOpen(drillKey(ss)) {
		t.Error("the alert stayed open after a drill that passed")
	}
}

func TestDrillSkipsLargeSnapshotOnSchedule(t *testing.T) {
	h, db, _ := drillDB(t, 3<<30)
	jobs := len(h.rt.Jobs())
	// Its last drill passed eight days ago, so one is due.
	ss, _ := h.c.loadService(h.ctx, 1, db)
	old, err := h.c.store.CreateBackupCheck(h.ctx, store.CreateBackupCheckParams{
		Kind: "drill", ServiceID: &ss.svc.ID, Trigger: "schedule", StartedAt: now().Add(-8 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.c.store.FinishBackupCheck(h.ctx, store.FinishBackupCheckParams{Status: string(apitypes.BackupSucceeded), ID: old.ID}); err != nil {
		t.Fatal(err)
	}
	d := h.drillNow(db, false)
	if d.ID == old.ID || d.Status != apitypes.BackupSkipped || !strings.Contains(d.Detail, "over 2 GB") || !strings.Contains(d.Detail, "dock backup drill") {
		t.Fatalf("drill = %+v", d)
	}
	if len(h.rt.Jobs()) != jobs {
		t.Error("a skipped drill ran a restore job")
	}
	// Skipped counts as done: the next is due in a week, not now.
	set, _ := h.c.backupSettings(h.ctx)
	set.Verify = true
	if next, err := h.c.drillPass(h.ctx, set, false); err != nil || !next.After(now().Add(5*24*time.Hour)) {
		t.Fatalf("next drill due %v, %v", next, err)
	}

	// By hand the cap doesn't apply, and it is for operators.
	if _, err := h.c.RunBackupDrill(h.ctx, h.user("dev@x", apitypes.ScopeAdmin), db); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("drill by an org admin: %v", err)
	}
	run, err := h.c.RunBackupDrill(h.ctx, testSystem, db)
	if err != nil || run.Status != apitypes.BackupRunning || run.Trigger != "manual" {
		t.Fatalf("drill by hand = %+v, %v", run, err)
	}
	eventually(t, "the drill by hand to finish", func() bool {
		d, _ := h.c.latestDrill(h.ctx, ss)
		return d != nil && d.ID == run.ID && d.Status == apitypes.BackupSucceeded
	})
	h.noDrillService(db)
}

// A table that holds a service's or a release's ID and isn't in
// drillDeletes would keep a removed drill service's rows, or fail its
// removal on the foreign key.
func TestDrillDeletesCoverForeignKeys(t *testing.T) {
	h := newHarness(t)
	deleted := map[string]bool{}
	for _, d := range drillDeletes {
		deleted[d.table] = true
	}
	rows, err := h.c.store.DB.QueryContext(h.ctx, `SELECT m.name, f."from", f."table", f.on_delete
		FROM sqlite_master m, pragma_foreign_key_list(m.name) f WHERE m.type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var table, col, parent, onDelete string
		if err := rows.Scan(&table, &col, &parent, &onDelete); err != nil {
			t.Fatal(err)
		}
		unlinked := table == "backups" && col == "restored_to" // deleteDrillRows' UPDATE
		if (parent == "services" || parent == "releases") && !deleted[table] && !unlinked && onDelete != "CASCADE" && onDelete != "SET NULL" {
			t.Errorf("%s.%s references %s: add %s to drillDeletes", table, col, parent, table)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
