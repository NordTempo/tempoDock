package core

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Restore drills (ADR 0096): a template database's newest snapshot
// restored into a throwaway service, queried, and removed.

// drillMaxBytes is the largest snapshot a scheduled drill restores: a
// bigger one needs that much free disk and minutes of CPU on a shared
// server, so it is skipped and run by hand (`dock backup drill`).
const drillMaxBytes = 2 << 30

// drillSuffix is appended to a database's name for its drill service.
const drillSuffix = "-drill"

// drillPass runs every restore drill that is due (verifyDue), one at a
// time, and returns when the next one is due (zero: none). force runs
// every database's drill now. The backup scheduler's pass calls it after
// the repository checks. Two queries list what is due, however many
// databases; a database on an offline server or with a backup or restore
// running is left for a later pass (its reconnect or that run's end
// kicks one).
func (c *Core) drillPass(ctx context.Context, set store.BackupSetting, force bool) (time.Time, error) {
	if !set.Verify && !force {
		return time.Time{}, nil
	}
	cands, err := c.store.ListDrillCandidates(ctx)
	if err != nil || len(cands) == 0 {
		return time.Time{}, err
	}
	latest, err := c.store.ListLatestDrills(ctx)
	if err != nil {
		return time.Time{}, err
	}
	last := make(map[int64]*store.BackupCheck, len(latest))
	for i, d := range latest {
		if d.ServiceID != nil {
			last[*d.ServiceID] = &latest[i]
		}
	}
	var next time.Time
	sooner := func(at time.Time) {
		if !at.IsZero() && (next.IsZero() || at.Before(next)) {
			next = at
		}
	}
	for _, cand := range cands {
		if ctx.Err() != nil {
			break
		}
		at := verifyDue(last[cand.ID], int(set.Hour), c.backups.started, time.Local)
		if at.IsZero() {
			continue // one is running
		}
		if !force && at.After(now()) {
			sooner(at)
			continue
		}
		if !c.online(cand.ServerID) || c.serviceBusy(cand.ID) {
			continue
		}
		ss, err := c.scopeOf(ctx, cand.ID)
		if err != nil {
			c.log.Warn("restore drill", "service", cand.ID, "err", err)
			continue
		}
		snap := store.Backup{SnapshotID: cand.SnapshotID, Repository: cand.Repository, SizeBytes: cand.SizeBytes}
		row, err := c.createDrill(ctx, ss, snap, "schedule")
		if err != nil {
			return next, err
		}
		row.Status = string(c.runDrill(ctx, ss, row, snap.SizeBytes, set))
		sooner(verifyDue(&row, int(set.Hour), c.backups.started, time.Local))
	}
	return next, nil
}

// createDrill records a drill of ss restoring snap as running.
func (c *Core) createDrill(ctx context.Context, ss svcScope, snap store.Backup, trigger string) (store.BackupCheck, error) {
	return c.store.CreateBackupCheck(ctx, store.CreateBackupCheckParams{
		Kind: "drill", ServiceID: &ss.svc.ID, Repository: snap.Repository, SnapshotID: snap.SnapshotID, Trigger: trigger, StartedAt: now(),
	})
}

// RunBackupDrill starts a restore drill of a template database now
// (operators) and answers with its running row; it finishes in the
// background. A drill by hand restores a snapshot of any size.
func (c *Core) RunBackupDrill(ctx context.Context, a Actor, ref ServiceRef) (apitypes.BackupCheck, error) {
	const action = "running a restore drill"
	if err := a.requirePlatform(BackupsManage, action); err != nil {
		return apitypes.BackupCheck{}, err
	}
	// A platform action on an org's service, as UpdateBackupSchedule: the
	// drill creates a service in the environment, so its lock applies.
	ss, err := c.loadService(ctx, a.OrgID, ref)
	if err != nil {
		return apitypes.BackupCheck{}, err
	}
	if err := a.checkLock(ss.envScope, action); err != nil {
		return apitypes.BackupCheck{}, err
	}
	if err := notArchived(ss.project); err != nil {
		return apitypes.BackupCheck{}, err
	}
	if _, ok := templateOf(ss.svc); !ok || ss.svc.ArchivedAt != nil {
		return apitypes.BackupCheck{}, invalid("only live template databases have restore drills; the weekly repository check covers %s's volume backups (`dock backup check`)", ss)
	}
	snap, err := c.store.GetDrillSnapshot(ctx, ss.svc.ID)
	if store.IsNotFound(err) {
		return apitypes.BackupCheck{}, invalid("%s has no backup a drill can restore yet; run one first: dock backup run %s", ss, svcFlags(ss.project.Name, ss.env.Name, ss.svc.Name))
	}
	if err != nil {
		return apitypes.BackupCheck{}, err
	}
	set, err := c.backupSettings(ctx)
	if err != nil {
		return apitypes.BackupCheck{}, err
	}
	if last, err := c.store.GetLatestDrill(ctx, &ss.svc.ID); err == nil && last.Status == string(apitypes.BackupRunning) {
		return apitypes.BackupCheck{}, conflict("a restore drill of %s is already running; wait for it to finish", ss)
	} else if err != nil && !store.IsNotFound(err) {
		return apitypes.BackupCheck{}, err
	}
	if c.serviceBusy(ss.svc.ID) {
		return apitypes.BackupCheck{}, conflict("a backup or restore of %s is running, or the backups are moving; run the drill once that finishes", ss)
	}
	row, err := c.createDrill(ctx, ss, snap, "manual")
	if err != nil {
		return apitypes.BackupCheck{}, err
	}
	c.audit(ctx, a, "backup.drill_start", &ss.project, &ss.env, ss.String(),
		fmt.Sprintf("started a restore drill of snapshot %s", shortSnapshot(snap.SnapshotID)))
	c.wg.Add(1)
	c.goCaught("running a restore drill", func() {
		defer c.wg.Done()
		defer c.kickBackups() // a due backup of it may have waited for the drill
		c.runDrill(c.ctx, ss, row, snap.SizeBytes, set)
	})
	return toBackupCheck(row, "", ss.svc.Name), nil
}

// latestDrill is a service's latest restore drill, nil if it never had
// one.
func (c *Core) latestDrill(ctx context.Context, ss svcScope) (*apitypes.BackupCheck, error) {
	r, err := c.store.GetLatestDrill(ctx, &ss.svc.ID)
	if store.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ptr(toBackupCheck(r, "", ss.svc.Name)), nil
}

// shortSnapshot is a snapshot ID as restic's short one.
func shortSnapshot(id string) string { return id[:min(8, len(id))] }

// drillKey is the key of a database's restore drill alert.
func drillKey(ss svcScope) string {
	return "backup-drill:" + alertServiceKey(ss.svc.ID, ss.project.Name, ss.env.Name, ss.svc.Name)
}

// runDrill runs the drill recorded as row to its end and records the
// outcome, which it returns: the row, the audit event, the ten newest
// rows kept, and the alert (raised by a failure, resolved by a success or
// a skip). size is the snapshot's. A daemon that is shutting down leaves
// the row running: the next start fails it, and the next drill removes
// the service this one left.
func (c *Core) runDrill(ctx context.Context, ss svcScope, row store.BackupCheck, size int64, set store.BackupSetting) apitypes.BackupStatus {
	status, detail, err := c.drill(ctx, ss, row, size, set)
	if ctx.Err() != nil {
		return apitypes.BackupRunning
	}
	fin := store.FinishBackupCheckParams{Status: string(status), Detail: detail, FinishedAt: ptr(now()), ID: row.ID}
	short := shortSnapshot(row.SnapshotID)
	if err != nil {
		fin.Status, fin.Error = string(apitypes.BackupFailed), err.Error()
		c.audit(ctx, System, "backup.drill_failed", &ss.project, &ss.env, ss.String(),
			fmt.Sprintf("restore drill of snapshot %s failed: %v", short, err))
		c.signalAlert(ctx, alertSignal{
			Key: drillKey(ss), serviceID: ss.svc.ID, projectID: ss.project.ID,
			Kind: apitypes.AlertBackupFailed, Severity: apitypes.SeverityWarning,
			Title: "Restore drill of " + serviceKey(ss.project.Name, ss.env.Name, ss.svc.Name) + " failed",
			Message: fmt.Sprintf("Restoring snapshot %s into a throwaway service failed: %s. Until a drill passes, don't count on this database's backups: run a backup, then the drill again.",
				short, strings.TrimRight(strings.TrimSpace(err.Error()), ".:; ")),
			Command: "dock backup drill " + svcFlags(ss.project.Name, ss.env.Name, ss.svc.Name),
			Project: ss.project.Name, Environment: ss.env.Name, Service: ss.svc.Name,
		})
	} else {
		c.signalAlert(ctx, alertSignal{Key: drillKey(ss), Resolve: true})
		summary := fmt.Sprintf("restore drill of snapshot %s passed: %s", short, detail)
		if status == apitypes.BackupSkipped {
			summary = fmt.Sprintf("restore drill of snapshot %s skipped: %s", short, detail)
		}
		c.audit(ctx, System, "backup.drill", &ss.project, &ss.env, ss.String(), summary)
	}
	if err := c.store.FinishBackupCheck(ctx, fin); err != nil {
		c.log.Error("record restore drill", "drill", row.ID, "err", err)
	}
	if err := c.store.TrimDrillChecks(ctx, &ss.svc.ID); err != nil {
		c.log.Warn("trim restore drills", "service", ss.svc.ID, "err", err)
	}
	return apitypes.BackupStatus(fin.Status)
}

// drill does one drill's work: it restores the row's snapshot of ss into
// <name>-drill with the ordinary restore, waits for it, queries the
// copy, and removes the copy whatever happened. It answers succeeded with
// what the query counted, skipped with why, or the error that failed it.
//
// Nothing polls and no timer runs: the restore sends its outcome when it
// finishes (restoreBackup's done signal), after its row is written. The
// restore held the source's claim; the drill takes it next
// (claimServiceWait) and keeps it while it queries and removes the copy,
// so no backup of the source starts on the same small server meanwhile.
func (c *Core) drill(ctx context.Context, ss svcScope, row store.BackupCheck, size int64, set store.BackupSetting) (apitypes.BackupStatus, string, error) {
	if row.Trigger == "schedule" && size > drillMaxBytes {
		return apitypes.BackupSkipped, fmt.Sprintf("the snapshot is over 2 GB (%s), more than a scheduled drill restores on a shared server; run it by hand: dock backup drill %s",
			humanBytes(size), svcFlags(ss.project.Name, ss.env.Name, ss.svc.Name)), nil
	}
	if why := notRestorableHere(row.Repository, set.Repository); why != "" {
		return apitypes.BackupSkipped, why, nil
	}
	sys := System.InOrg(ss.project.OrgID)
	name := ss.svc.Name + drillSuffix
	if err := c.removeLeftDrill(ctx, ss, name); err != nil {
		return "", "", err
	}
	res, done, rerr := c.restoreBackup(ctx, sys, ServiceRef{ss.project.Name, ss.env.Name, ss.svc.Name},
		apitypes.RestoreBackupRequest{Snapshot: row.SnapshotID, As: name})
	var restoreErr error
	if done != nil {
		// The restore runs, whatever rerr says: wait for how it ends.
		select {
		case restoreErr = <-done:
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
	}
	if !c.claimServiceWait(ctx, ss.svc.ID) {
		return "", "", ctx.Err()
	}
	defer c.releaseService(ss.svc.ID)
	if rerr != nil {
		// It may have created the service before it failed.
		return "", "", errors.Join(rerr, c.removeLeftDrill(ctx, ss, name))
	}
	to, err := c.loadService(ctx, ss.project.OrgID, ServiceRef{ss.project.Name, ss.env.Name, name})
	if err != nil {
		return "", "", fmt.Errorf("find the drill's service %s: %w", name, err)
	}
	detail, err := c.drillResult(ctx, ss, to, restoreErr, row.StartedAt)
	if ctx.Err() != nil {
		return "", "", ctx.Err()
	}
	if rmErr := c.removeDrillService(ctx, to, res.Restore.ID); rmErr != nil && ctx.Err() == nil {
		c.log.Error("remove a restore drill's service", "service", to.String(), "err", rmErr)
		if err == nil {
			err = fmt.Errorf("the restore worked (%s), but its service %s couldn't be removed: %w; the next drill removes it, or archive it by hand", detail, name, rmErr)
		}
	}
	if err != nil {
		return "", "", err
	}
	return apitypes.BackupSucceeded, detail, nil
}

// drillResult is what a drill found once its restore ended with
// restoreErr: a failed restore fails the drill; one that worked has its
// copy to queried, and the detail is what that counted and how long the
// drill took since started.
func (c *Core) drillResult(ctx context.Context, from, to svcScope, restoreErr error, started time.Time) (string, error) {
	if restoreErr != nil {
		return "", fmt.Errorf("restoring the snapshot failed: %w", restoreErr)
	}
	found, err := c.drillQuery(ctx, from, to)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s in %d s", found, int(now().Sub(started).Round(time.Second).Seconds())), nil
}

// drillQuery is a drill's sanity query against the restored copy to,
// through the Data panel's connection to it (no other path to a
// database): Postgres and MySQL count the user tables, and fail when
// there are none while the live database from has some; Redis answers
// its key count. Template databases get data access when their first
// release goes live (afterActive); a copy that has none yet is set up
// here, once.
func (c *Core) drillQuery(ctx context.Context, from, to svcScope) (string, error) {
	sys := System.InOrg(to.project.OrgID)
	ref := ServiceRef{to.project.Name, to.env.Name, to.svc.Name}
	s, err := c.DataSchema(ctx, sys, ref, "")
	if err != nil {
		if _, serr := c.SetupData(ctx, sys, ref); serr != nil {
			return "", fmt.Errorf("the restore finished, but the restored copy couldn't be queried: %w", err)
		}
		if s, err = c.DataSchema(ctx, sys, ref, ""); err != nil {
			return "", fmt.Errorf("the restore finished, but the restored copy couldn't be queried: %w", err)
		}
	}
	if t, _ := templateOf(to.svc); t.backup == apitypes.BackupRedis {
		var keys int64
		for _, db := range s.Keyspace {
			keys += db.Keys
		}
		return drillCount(keys, "key"), nil
	}
	n := userTables(s)
	if n == 0 {
		// Read from the panel's cached schema of the live database; one
		// that can't be read (no data access) is not compared.
		live, err := c.DataSchema(ctx, sys, ServiceRef{from.project.Name, from.env.Name, from.svc.Name}, "")
		if err == nil && userTables(live) > 0 {
			return "", fmt.Errorf("the restored copy has no tables, but %s has %d: the snapshot doesn't hold its data", from.svc.Name, userTables(live))
		}
	}
	return drillCount(n, "table"), nil
}

// userTables counts a schema's tables, without its views.
func userTables(s apitypes.DataSchema) int64 {
	var n int64
	for _, t := range s.Tables {
		if t.Kind == apitypes.TableKindTable || t.Kind == apitypes.TableKindPartitioned || t.Kind == "" {
			n++
		}
	}
	return n
}

// drillCount is "1 table" or "1,204 tables".
func drillCount(n int64, word string) string {
	if n != 1 {
		word += "s"
	}
	return data.Count(n) + " " + word
}

// removeLeftDrill removes the service called name beside ss when it is
// one a drill of ss left behind (a crash, a removal that failed): the
// restore into it is the system's. Any other service of that name is
// someone's, and the drill fails saying so.
func (c *Core) removeLeftDrill(ctx context.Context, ss svcScope, name string) error {
	left, err := c.store.GetServiceByName(ctx, store.GetServiceByNameParams{EnvironmentID: ss.env.ID, Name: name})
	if store.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	run, err := c.store.GetDrillRestore(ctx, store.GetDrillRestoreParams{ServiceID: ss.svc.ID, RestoredTo: &left.ID})
	if err != nil && !store.IsNotFound(err) {
		return err
	}
	if err != nil || run.CreatedBy != System.Name {
		return fmt.Errorf("a service named %s exists in %s, the name the drill restores into; rename or archive and delete it, then run the drill again", name, ss.envScope)
	}
	return c.removeDrillService(ctx, svcScope{ss.envScope, left}, run.ID)
}

// removeDrillService removes a drill's service for good: it is archived
// (routes, alerts, status and its container go, finishArchive), any
// container still carrying its labels and its volumes are removed on its
// server, and then every row of it is deleted (drillDeletes), so the
// next drill can take the name and nothing of it blocks deleting the
// project. restoreID is the restore run that filled it, which goes with
// it. The server must be online; a failure leaves the service for the
// next drill of the same database.
func (c *Core) removeDrillService(ctx context.Context, ds svcScope, restoreID int64) error {
	var done []archivedService
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		as, ok, err := archiveTx(ctx, q, ds)
		if ok {
			done = append(done, as)
		}
		return err
	})
	if err != nil {
		return err
	}
	if len(done) > 0 {
		_, stopped := c.finishArchive(ctx, done)
		stopped.Wait()
	}
	rt := c.connected(ds.env.ServerID)
	if rt == nil {
		return conflict("the server of %s can't be reached, so its container and volume stay for now", ds)
	}
	ctrs, err := rt.List(ctx)
	if err != nil {
		return fmt.Errorf("list the containers of %s: %w", ds, c.nodeErr(ds.env.ServerID, err))
	}
	want := c.labels(ds, 0)
	var mine []node.Container
	for _, ct := range ctrs {
		if ct.Labels.Service == want.Service && ct.Labels.Project == want.Project && !c.foreign(ct.Labels) {
			mine = append(mine, ct)
		}
	}
	if err := c.awaitRemovals(ctx, mine); err != nil {
		return err
	}
	if err := c.removeContainers(ctx, ds.env.ServerID, mine, node.RemoveOptions{}); err != nil {
		return fmt.Errorf("remove the containers of %s: %w", ds, err)
	}
	vols, err := c.store.ListVolumes(ctx, ds.svc.ID)
	if err != nil {
		return err
	}
	for _, v := range vols {
		// A staged one may exist too: a Redis restore fills it before
		// the first release applies it.
		if err := rt.RemoveVolume(ctx, v.DockerName, want); err != nil && !errors.Is(err, node.ErrNotFound) {
			return fmt.Errorf("remove volume %s of %s: %w", v.Name, ds, c.nodeErr(ds.env.ServerID, err))
		}
	}
	return c.deleteDrillRows(ctx, ds, restoreID)
}

// drillReleases selects a drill service's releases; ?1 is its ID.
const drillReleases = `(SELECT id FROM releases WHERE service_id = +?1)`

// drillDeletes deletes every row of one service, children before
// parents, as store's projectDeletes does for a project: the tables
// that hold a service's or a release's ID. ?1 is the service's ID. A new
// such table gets a statement in both lists;
// TestDrillDeletesCoverForeignKeys fails for one that is missing here.
// The audit log and service_stats keep their rows, as for a deleted
// project. Every statement reads through an index but two, as in
// projectDeletes: alerts and vuln_ignores have none on the service and
// are scanned, once per drill; retention keeps the first small and the
// second holds a handful of rows.
var drillDeletes = []struct{ table, where string }{
	{"alerts", `service_id = +?1`},
	{"vuln_ignores", `service_id = +?1`},
	{"preview_data", `service_id = +?1`},
	{"service_previews", `service_id = +?1`},
	{"diagnoses", `service_id = +?1 OR release_id IN ` + drillReleases},
	{"release_events", `release_id IN ` + drillReleases},
	{"service_runs", `service_id = +?1`},
	{"service_jobs", `service_id = +?1`},
	{"server_forwards", `service_id = +?1`},
	{"environment_move_services", `service_id = +?1`},
	{"backup_schedules", `service_id = +?1`},
	{"pitr", `service_id = +?1`},
	{"backup_checks", `service_id = +?1`},
	{"backups", `service_id = +?1`},
	{"volume_snapshot_restores", `service_id = +?1 OR to_service_id = +?1`},
	{"volume_snapshots", `service_id = +?1`},
	{"volumes", `service_id = +?1`},
	{"changesets", `service_id = +?1`},
	{"data_access", `service_id = +?1`},
	{"service_access", `service_id = +?1`},
	{"saved_queries", `service_id = +?1`},
	{"sensitive_columns", `service_id = +?1`},
	{"sensitive_keys", `service_id = +?1`},
	{"staged_domains", `service_id = +?1`},
	{"domains", `service_id = +?1`},
	{"git_connections", `service_id = +?1`},
	{"ensure_running", `service_id = +?1`},
	{"uptime_checks", `service_id = +?1`},
	{"status_sources", `service_id = +?1`},
	{"metric_dashboards", `service_id = +?1`},
	{"traffic_stats", `service_id = +?1`},
	{"traffic_paths", `service_id = +?1`},
	{"variables", `environment_id = (SELECT environment_id FROM services WHERE id = +?1) AND coalesce(service_id, 0) = +?1`},
	{"site_funnels", `site_id IN (SELECT id FROM sites WHERE service_id = +?1)`},
	{"site_goals", `site_id IN (SELECT id FROM sites WHERE service_id = +?1)`},
	{"site_markers", `site_id IN (SELECT id FROM sites WHERE service_id = +?1)`},
	{"sites", `service_id = +?1`},
	{"releases", `service_id = +?1`},
	{"services", `id = +?1`},
}

// deleteDrillRows deletes a removed drill service's rows in one
// transaction and forgets what memory holds of it (its alerts, status and
// the Data panel's connections), as deleteProjectRows does for a project.
// Alert writes wait meanwhile, so none lands on a row that is going. The
// statements run unprepared, as store.DeleteProject's: a statement cache
// entry each for a weekly delete would stay for the daemon's life.
func (c *Core) deleteDrillRows(ctx context.Context, ds svcScope, restoreID int64) error {
	c.alerts.writes.Lock()
	defer c.alerts.writes.Unlock()
	tx, err := c.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// The drill's restore run goes with its service: the drill's own row
	// says how it went, and a restore run without a target would read as
	// one made in place.
	for _, q := range []string{
		`DELETE FROM backup_logs WHERE backup_id = +?1 AND +?2 IN (SELECT restored_to FROM backups WHERE id = +?1)`,
		`DELETE FROM backups WHERE id = +?1 AND restored_to = +?2`,
	} {
		if _, err := tx.ExecContext(ctx, q, restoreID, ds.svc.ID); err != nil {
			return fmt.Errorf("delete the drill's restore run: %w", err)
		}
	}
	for _, d := range drillDeletes {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+d.table+" WHERE "+d.where, ds.svc.ID); err != nil {
			return fmt.Errorf("delete the %s of %s: %w", d.table, ds, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	c.alerts.mu.Lock()
	maps.DeleteFunc(c.alerts.open, func(_ string, al store.Alert) bool {
		return al.ServiceID != nil && *al.ServiceID == ds.svc.ID
	})
	c.alerts.mu.Unlock()
	c.statusMu.Lock()
	delete(c.status, ds.svc.ID)
	c.statusMu.Unlock()
	c.panel.Forget(ds.svc.ID)
	return nil
}
