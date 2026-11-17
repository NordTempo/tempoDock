package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/s3"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/walarchive"
)

// Base backups of point-in-time restore (ADR 0093): a job in the
// template's image copies the data directory between pg_backup_start and
// pg_backup_stop into restic, one right after archiving starts and then
// one with each scheduled backup. Retention runs with them: restic
// forgets the base backups before the window, and Dock deletes the WAL
// no kept base backup needs.

// startBase claims a database and records a running base backup. The
// caller must call runBase with it, which releases the claim.
func (c *Core) startBase(ctx context.Context, a Actor, ss svcScope, trigger string) (store.Backup, error) {
	set, err := c.backupSettings(ctx)
	if err != nil {
		return store.Backup{}, err
	}
	if set.StorageID == nil {
		return store.Backup{}, invalid("point-in-time restore needs backups on a storage connection")
	}
	if c.backupsMoving() {
		return store.Backup{}, conflict("the backups are moving to other storage; back up %s once the move finishes", ss)
	}
	if err := c.notMoving(ctx, ss.envScope, "back it up"); err != nil {
		return store.Backup{}, err
	}
	if !c.claimService(ss.svc.ID) {
		return store.Backup{}, conflict("a backup or restore of %s is already running; wait for it to finish", ss)
	}
	row, err := c.store.CreateBackup(ctx, store.CreateBackupParams{
		ServiceID: ss.svc.ID, Operation: "backup", Kind: string(apitypes.BackupPITR), Trigger: trigger,
		Repository: set.Repository, CreatedBy: a.Name, StartedAt: now(),
	})
	if err != nil {
		c.releaseService(ss.svc.ID)
		return store.Backup{}, err
	}
	return row, nil
}

// pitrForget picks the base backups retention removes from bases (in the
// order they became consistent): those that ended before the window
// (days before now), except, while point-in-time restore is on, the
// newest of them, which the window's start replays from. Off, the window
// ages out until none is left.
func pitrForget(bases []store.Backup, now time.Time, days int64, on bool) []store.Backup {
	start := now.Add(-time.Duration(days) * 24 * time.Hour)
	var before []store.Backup
	for _, b := range bases {
		if b.ConsistentAt != nil && b.ConsistentAt.Before(start) {
			before = append(before, b)
		}
	}
	if on && len(before) > 0 {
		before = before[:len(before)-1]
	}
	return before
}

// snapshotIDs are the restic snapshots of rows.
func snapshotIDs(rows []store.Backup) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.SnapshotID != "" {
			out = append(out, r.SnapshotID)
		}
	}
	return out
}

// baseForget is what retention removes after a database's next backup
// run, and its setting (zero when it never had point-in-time restore).
func (c *Core) baseForget(ctx context.Context, ss svcScope) ([]store.Backup, store.Pitr) {
	row, err := c.store.GetPITR(ctx, ss.svc.ID)
	if err != nil {
		return nil, store.Pitr{}
	}
	bases, err := c.store.ListPITRBases(ctx, ss.svc.ID)
	if err != nil {
		c.log.Warn("list base backups", "service", ss.String(), "err", err)
		return nil, row
	}
	return pitrForget(bases, now(), row.Days, row.Enabled), row
}

// runBase runs a recorded base backup to the end and records it, then
// retention: the base backups it forgot are marked pruned and the WAL no
// kept one needs is deleted. The claim ends with the job, as in
// runBackup.
func (c *Core) runBase(ctx context.Context, ss svcScope, row store.Backup) {
	defer c.kickBackups()
	forget, pitr := c.baseForget(ctx, ss)
	base, res, log, err := c.baseJob(ctx, ss, row, pitr, forget)
	c.releaseService(ss.svc.ID)
	if ctx.Err() != nil {
		return
	}
	c.keepBackupLog(ctx, ss.svc.ID, row.ID, log)
	fin := store.FinishBackupParams{Status: string(apitypes.BackupSucceeded), FinishedAt: ptr(now()), ID: row.ID}
	if err != nil {
		fin.Status, fin.Error = string(apitypes.BackupFailed), err.Error()
		c.audit(ctx, System, "backup.failed", &ss.project, &ss.env, ss.String(), fmt.Sprintf("base backup %d failed: %v", row.ID, err))
		s := backupFailed(ss.project.Name, ss.env.Name, ss.svc.Name, row.ID, err.Error(), ss.svc.ID)
		s.projectID = ss.project.ID
		c.signalAlert(ctx, s)
	} else {
		c.signalAlert(ctx, alertSignal{Key: "backup:" + alertServiceKey(ss.svc.ID, ss.project.Name, ss.env.Name, ss.svc.Name), Resolve: true})
		fin.SnapshotID, fin.SizeBytes = res.SnapshotID, res.Bytes
		if err := c.store.SetBackupBase(ctx, store.SetBackupBaseParams{
			WalGeneration: pitr.Generation, WalStart: base.Start, WalSegmentBytes: base.SegmentBytes, ConsistentAt: &base.Consistent, ID: row.ID,
		}); err != nil {
			c.log.Error("record base backup", "backup", row.ID, "err", err)
		}
		c.afterForget(ctx, ss, row.Repository, log, forget, res.Kept)
		c.audit(ctx, System, "backup.succeeded", &ss.project, &ss.env, ss.String(),
			fmt.Sprintf("base backup %d: snapshot %s, %s, restorable from %s", row.ID, res.SnapshotID[:min(8, len(res.SnapshotID))], humanBytes(res.Bytes), base.Consistent.UTC().Format(time.RFC3339)))
	}
	if err := c.store.FinishBackup(ctx, fin); err != nil {
		c.log.Error("record backup", "backup", row.ID, "err", err)
	}
	if err == nil {
		c.pruneWAL(ctx, ss)
	}
}

// afterForget marks the base backups a job forgot pruned (unless it says
// forgetting failed), and those its listing of the tag no longer holds.
func (c *Core) afterForget(ctx context.Context, ss svcScope, repository, log string, forget []store.Backup, kept []string) {
	if len(forget) > 0 && !strings.Contains(log, backup.ForgetFailed) {
		for _, b := range forget {
			if err := c.store.PruneBackup(ctx, b.ID); err != nil {
				c.log.Warn("mark base backup pruned", "backup", b.ID, "err", err)
			}
		}
	}
	if kept != nil {
		c.markPruned(ctx, ss.svc.ID, string(apitypes.BackupPITR), repository, kept)
	}
}

// baseJob runs a base backup's job: the database's volume read-only, its
// owner's connection for pg_backup_start, the dock binary, and the snapshots
// retention forgets.
func (c *Core) baseJob(ctx context.Context, ss svcScope, row store.Backup, pitr store.Pitr, forget []store.Backup) (walarchive.Base, backup.Result, string, error) {
	rt, err := c.runtimeFor(ss.env.ServerID)
	if err != nil {
		return walarchive.Base{}, backup.Result{}, "", err
	}
	set, err := c.backupSettings(ctx)
	if err != nil {
		return walarchive.Base{}, backup.Result{}, "", err
	}
	if set.Repository != row.Repository {
		return walarchive.Base{}, backup.Result{}, "", fmt.Errorf("the backup repository changed after this backup started; run it again")
	}
	if !pitr.Enabled {
		return walarchive.Base{}, backup.Result{}, "", invalid("point-in-time restore is off for %s", ss)
	}
	repo, err := c.repoOf(ctx, set)
	if err != nil {
		return walarchive.Base{}, backup.Result{}, "", err
	}
	conn, err := c.dbConnection(ctx, ss)
	if err != nil {
		return walarchive.Base{}, backup.Result{}, "", notRunningErr(ss, err)
	}
	vol, err := c.dataVolume(ctx, ss)
	if err != nil {
		return walarchive.Base{}, backup.Result{}, "", err
	}
	src := backup.Source{
		Kind: apitypes.BackupPostgres, ServiceID: ss.svc.ID, Host: ss.svc.Name, Port: conn.port,
		User: conn.owner.User, Password: conn.owner.Password, Database: conn.database,
	}
	spec := node.JobSpec{
		Name:   fmt.Sprintf("dock_p%d_e%d_s%d_base%d", ss.project.ID, ss.env.ID, ss.svc.ID, row.ID),
		Image:  jobImage(ss),
		Labels: c.labels(ss, 0),
		Env:    backup.BaseEnv(src, repo, snapshotIDs(forget)),
		Script: backup.BaseScript(), Network: networkName(ss.envScope), Dock: true,
		Mounts: []node.Mount{{Volume: vol.DockerName, Path: backup.BaseDir, ReadOnly: true}},
	}
	out, err := c.runResticJob(ctx, ss.env.ServerID, rt, repo, spec)
	log := jobLog(spec.Env, out)
	if err != nil {
		return walarchive.Base{}, backup.Result{}, log, err
	}
	res, err := backup.ParseBackup(out)
	if err != nil {
		return walarchive.Base{}, res, log, fmt.Errorf("%w%s", err, withOutput(out))
	}
	base, err := walarchive.ParseBase(out)
	if err != nil {
		return base, res, log, fmt.Errorf("%w%s", err, withOutput(out))
	}
	return base, res, log, nil
}

// pruneWAL deletes what of a database's WAL archive no kept base backup
// needs: in a generation with base backups, the segments before the
// oldest one's start (history files stay, they are tiny); other
// generations whole, except the current one, which the archiver may be
// filling before its first base backup. A failure is logged: the next
// backup run tries again.
func (c *Core) pruneWAL(ctx context.Context, ss svcScope) {
	if err := c.pruneWALErr(ctx, ss); err != nil {
		c.log.Warn("prune the WAL archive", "service", ss.String(), "err", err)
	}
}

func (c *Core) pruneWALErr(ctx context.Context, ss svcScope) error {
	pitr, err := c.store.GetPITR(ctx, ss.svc.ID)
	if err != nil {
		return err
	}
	bases, err := c.store.ListPITRBases(ctx, ss.svc.ID)
	if err != nil {
		return err
	}
	set, err := c.backupSettings(ctx)
	if err != nil || set.StorageID == nil {
		return err
	}
	conn, err := c.storageByID(ctx, *set.StorageID)
	if err != nil {
		return err
	}
	b, err := c.bucketOf(conn)
	if err != nil {
		return err
	}
	from := map[string]string{} // generation: its oldest base's start
	for _, base := range bases {
		if base.WalGeneration == "" || base.WalStart == "" || base.Repository != set.Repository {
			continue
		}
		if cur, ok := from[base.WalGeneration]; !ok || base.WalStart < cur {
			from[base.WalGeneration] = base.WalStart
		}
	}
	prefix := walarchive.ServicePrefix(set.StoragePath, ss.svc.ID)
	cl := &s3.Client{Bucket: b}
	var drop []string
	var kept int64
	err = cl.List(ctx, prefix, func(objs []s3.Object) error {
		for _, o := range objs {
			if walDroppable(prefix, o.Key, from, pitr.Enabled, pitr.Generation) {
				drop = append(drop, o.Key)
			} else {
				kept += o.Size
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := c.store.SetPITRWALBytes(ctx, store.SetPITRWALBytesParams{WalBytes: kept, ServiceID: ss.svc.ID}); err != nil {
		return err
	}
	if len(drop) == 0 {
		return nil
	}
	return cl.Delete(ctx, drop)
}

// walDroppable says whether retention deletes the archive object key
// under a database's prefix (walarchive.ServicePrefix): from maps each
// generation with kept base backups to the oldest one's start; current is
// the generation being archived while on.
func walDroppable(prefix, key string, from map[string]string, on bool, current string) bool {
	rest, ok := strings.CutPrefix(key, prefix)
	if !ok {
		return false
	}
	gen, _, ok := strings.Cut(rest, "/")
	if !ok {
		return false
	}
	start, kept := from[gen]
	if !kept {
		return !on || gen != current
	}
	file, _, ok := walarchive.ParseKey(prefix+gen+"/", key)
	return ok && walarchive.IsSegment(file) && file < start
}

// scheduledBase takes a database's scheduled base backup after its dump,
// while point-in-time restore is on.
func (c *Core) scheduledBase(ctx context.Context, ss svcScope) {
	if !c.pitrOn(ss.svc.ID) {
		return
	}
	row, err := c.startBase(ctx, System, ss, "schedule")
	if err != nil {
		c.log.Warn("scheduled base backup", "service", ss.String(), "err", err)
		return
	}
	c.runBase(ctx, ss, row)
}
