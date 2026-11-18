package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// backupToRestore finds the backup of ss that a restore names by its
// snapshot ID (or 8 or more leading characters), and checks it can be
// restored from the repository backups go to now.
func (c *Core) backupToRestore(ctx context.Context, ss svcScope, snapshot string) (store.Backup, error) {
	snap := strings.TrimSpace(snapshot)
	if len(snap) < 8 {
		return store.Backup{}, invalid("give the snapshot ID or at least its first 8 characters; `dock backup list` shows them")
	}
	from, err := c.store.GetBackupBySnapshot(ctx, store.GetBackupBySnapshotParams{ServiceID: ss.svc.ID, Prefix: snap})
	if store.IsNotFound(err) {
		return from, notFound("%s has no backup with snapshot %s", ss, snap)
	}
	if err != nil {
		return from, err
	}
	if from.Pruned {
		return from, invalid("snapshot %s was removed by retention; pick a newer one", snap)
	}
	if from.Kind == string(apitypes.BackupPITR) {
		return from, invalid("snapshot %s is a base backup of point-in-time restore; restore a time instead (`dock backup restore --to-time`)", snap)
	}
	set, err := c.backupSettings(ctx)
	if err != nil {
		return from, err
	}
	if why := notRestorableHere(from.Repository, set.Repository); why != "" {
		return from, invalid("backup %s can't be restored here: %s", from.SnapshotID[:min(8, len(from.SnapshotID))], why)
	}
	return from, nil
}

// backupRestore names a restore of a volume backup for lockForRestore.
func backupRestore(snapshot string) volumeRestore {
	return volumeRestore{what: "the restore of backup " + snapshot[:min(8, len(snapshot))], watch: "`dock backup list`"}
}

// restoreVolumeBackup restores a volume backup of ss, a service that
// isn't a template database (ADR 0095). With req.As it goes into a copy,
// a new service (restoreVolumeCopy). Otherwise in place: the service stops, a
// safety snapshot keeps what its volumes hold (restoring that one undoes
// this), the job fills the volumes the backup holds, and the active
// release starts again. A failed fill puts the safety snapshot back. It
// runs in the background; the answer is the restore, running, a row in
// the service's backup list with the service itself as its target.
func (c *Core) restoreVolumeBackup(ctx context.Context, a Actor, ss svcScope, req apitypes.RestoreBackupRequest) (apitypes.RestoreResult, error) {
	if req.ToTime != nil {
		return apitypes.RestoreResult{}, invalid("only a Postgres database with point-in-time restore restores to a time; restore one of %s's backups (`dock backup restore --snapshot ID --in-place`)", ss)
	}
	if !req.InPlace && req.As != "" {
		return c.restoreVolumeCopy(ctx, a, ss, req)
	}
	if !req.InPlace {
		return apitypes.RestoreResult{}, invalid("%s isn't a template database, so say how its backup restores: --in-place (in_place: true) replaces its volumes, after a snapshot of what they hold now, or --as NAME for a copy (as: a new service with the backup's data, next to this one)", ss)
	}
	if req.As != "" {
		return apitypes.RestoreResult{}, invalid("an in-place restore keeps the service; leave out the new service's name")
	}
	from, err := c.backupToRestore(ctx, ss, req.Snapshot)
	if err != nil {
		return apitypes.RestoreResult{}, err
	}
	if from.Kind != string(apitypes.BackupVolume) {
		return apitypes.RestoreResult{}, invalid("snapshot %s is a %s backup, not one of %s's volumes", from.SnapshotID[:8], from.Kind, ss)
	}
	if _, err := c.runtimeFor(ss.env.ServerID); err != nil {
		return apitypes.RestoreResult{}, err
	}
	if c.backupsMoving() {
		return apitypes.RestoreResult{}, conflict("the backups are moving to other storage; restore once the move finishes")
	}
	if err := c.notMoving(ctx, ss.envScope, "restore"); err != nil {
		return apitypes.RestoreResult{}, err
	}
	short := from.SnapshotID[:8]
	// The safety snapshot is recorded now, so its claim covers the restore.
	safety, safetyVols, err := c.startSnapshot(ctx, a, ss, apitypes.SnapshotBeforeRestore, backupSafetyNote(from.SnapshotID), 0)
	if err != nil {
		return apitypes.RestoreResult{}, err
	}
	fail := func(why string, err error) (apitypes.RestoreResult, error) {
		c.failSafety(ctx, safety, why)
		c.releaseService(ss.svc.ID)
		return apitypes.RestoreResult{}, err
	}
	mounts, err := snapshotMounts(safety, safetyVols, ss.String())
	if err != nil {
		return fail("nothing was restored", err)
	}
	if err := c.lockForRestore(ctx, ss, backupRestore(from.SnapshotID)); err != nil {
		return fail("something else writes the volumes, so nothing was restored", err)
	}
	// Read again now that StopService refuses to run, as restoreInPlace does.
	svc, err := c.store.GetService(ctx, ss.svc.ID)
	if err != nil {
		c.unlockVolumes(ss.svc.ID)
		return fail("the service couldn't be read", err)
	}
	ss.svc = svc
	row, err := c.store.CreateBackup(ctx, store.CreateBackupParams{
		ServiceID: ss.svc.ID, Operation: "restore", Kind: string(apitypes.BackupVolume), Trigger: "manual",
		Repository: from.Repository, SnapshotID: from.SnapshotID, RestoredTo: &ss.svc.ID, CreatedBy: a.Name, StartedAt: now(),
	})
	if err != nil {
		c.unlockVolumes(ss.svc.ID)
		return fail("nothing was restored", err)
	}
	c.audit(ctx, a, "backup.restore", &ss.project, &ss.env, ss.String(),
		fmt.Sprintf("restoring backup %s in place; %s stops, and snapshot %d keeps the data it holds now", short, ss.svc.Name, safety.ID))
	c.wg.Go(func() {
		ctx := c.ctx
		var log string
		err := c.restoreInPlaceWith(ctx, ss, safety, safetyVols, mounts, "restore backup "+short, func(ctx context.Context) error {
			var err error
			log, err = c.fillFromBackup(ctx, ss, from.SnapshotID, row.ID)
			return err
		}, nil)
		if ctx.Err() == nil {
			c.pruneSnapshots(ctx, ss, 0)
		}
		c.unlockVolumes(ss.svc.ID)
		c.finishVolumeRestore(ctx, ss, row, safety.ID, log, err)
	})
	out, err := c.toService(ctx, a, ss, apitypes.PendingChanges{})
	return apitypes.RestoreResult{Service: out, Restore: toBackupRun(row, ss.svc.Name, map[int64]string{ss.svc.ID: ss.svc.Name})}, err
}

// backupSafetyNote is the note of the safety snapshot an in-place restore
// of a volume backup takes first. safetySnapshots finds it by this.
func backupSafetyNote(snapshot string) string {
	return "before restoring backup " + shortSnapshot(snapshot)
}

// safetyNote is the note of the safety snapshot the in-place restore row
// took first: a volume backup's (backupSafetyNote) or a restore to a
// time's (restoreToTimeInPlace writes that one).
func safetyNote(row store.Backup) string {
	if row.RestoreToTime != nil {
		return "before restoring to " + row.RestoreToTime.UTC().Format(time.RFC3339)
	}
	return backupSafetyNote(row.SnapshotID)
}

// safetySnapshots finds the safety snapshot of each in-place restore
// that succeeded among rows, a service's runs newest first, by the
// restore's ID. A backups row doesn't record it, so it is found: the
// newest snapshot taken before a restore that carries the restore's note
// (safetyNote) and started after the run before it and not after the
// restore itself. The claim on the service keeps every other run out of
// that window, so it is this restore's and never an earlier one's of the
// same backup. One indexed query, and none when the list holds no such
// restore; a snapshot that retention or a person removed is left out.
func (c *Core) safetySnapshots(ctx context.Context, ss svcScope, rows []store.Backup) (map[int64]int64, error) {
	inPlace := func(r store.Backup) bool {
		return r.Operation == "restore" && r.Status == string(apitypes.BackupSucceeded) && r.RestoredTo != nil && *r.RestoredTo == r.ServiceID
	}
	if !slices.ContainsFunc(rows, inPlace) {
		return nil, nil
	}
	snaps, err := c.store.ListSafetySnapshots(ctx, store.ListSafetySnapshotsParams{ServiceID: ss.svc.ID, Limit: snapshotListLimit})
	if err != nil {
		return nil, err
	}
	out := map[int64]int64{}
	for i, r := range rows {
		if !inPlace(r) {
			continue
		}
		var after time.Time // the run before it, when the list reaches that far
		if i+1 < len(rows) {
			after = rows[i+1].StartedAt
		}
		note := safetyNote(r)
		for _, s := range snaps { // newest first
			if s.StartedAt.After(r.StartedAt) {
				continue
			}
			if s.StartedAt.Before(after) {
				break
			}
			if s.Note == note {
				out[r.ID] = s.ID
				break
			}
		}
	}
	return out, nil
}

// fillFromBackup runs the job that fills ss's volumes from the volume
// backup snapshot (backup.VolumeRestoreScript): the volumes the backup
// holds are emptied and filled, others are left. Nothing may write the
// volumes meanwhile: the caller holds lockForRestore and the service
// isn't running. It takes no safety snapshot, so it also fills the empty
// volumes of a service that hasn't started (recovering a server). runID
// names the job; the answer is its log (jobLog).
func (c *Core) fillFromBackup(ctx context.Context, ss svcScope, snapshot string, runID int64) (string, error) {
	rt, err := c.runtimeFor(ss.env.ServerID)
	if err != nil {
		return "", err
	}
	set, err := c.backupSettings(ctx)
	if err != nil {
		return "", err
	}
	repo, err := c.repoOf(ctx, set)
	if err != nil {
		return "", err
	}
	vols, err := c.appliedVolumes(ctx, ss)
	if err != nil {
		return "", err
	}
	mounts := make([]node.Mount, 0, len(vols))
	for _, v := range vols {
		if err := rt.EnsureVolume(ctx, v.DockerName, c.labels(ss, 0)); err != nil {
			return "", fmt.Errorf("create volume %s: %w", v.Name, c.nodeErr(ss.env.ServerID, err))
		}
		mounts = append(mounts, node.Mount{Volume: v.DockerName, Path: backup.VolumesPath + "/" + v.Name})
	}
	spec := node.JobSpec{
		Name:  fmt.Sprintf("dock_p%d_e%d_s%d_restore%d", ss.project.ID, ss.env.ID, ss.svc.ID, runID),
		Image: c.cfg.Backup.ResticImage, Labels: c.labels(ss, 0), Env: backup.RestoreEnv(snapshot, backup.Source{}, repo),
		Script: backup.VolumeRestoreScript, Network: networkName(ss.envScope), Mounts: mounts,
	}
	out, err := c.runResticJob(ctx, ss.env.ServerID, rt, repo, spec)
	return jobLog(spec.Env, out), err
}

// finishVolumeRestore records an in-place volume restore's outcome once.
// The claim on the service ends first, as in runBackup.
func (c *Core) finishVolumeRestore(ctx context.Context, ss svcScope, row store.Backup, safetyID int64, log string, err error) {
	c.releaseService(ss.svc.ID)
	if ctx.Err() != nil {
		return // shutting down: the next start marks it failed
	}
	c.keepBackupLog(ctx, ss.svc.ID, row.ID, log)
	short := row.SnapshotID[:min(8, len(row.SnapshotID))]
	fin := store.FinishBackupParams{Status: string(apitypes.BackupSucceeded), SnapshotID: row.SnapshotID, FinishedAt: ptr(now()), ID: row.ID}
	if err != nil {
		fin.Status, fin.Error = string(apitypes.BackupFailed), err.Error()
		c.audit(ctx, System, "backup.restore_failed", &ss.project, &ss.env, ss.String(), fmt.Sprintf("restore of backup %s in place failed: %v", short, err))
	} else {
		c.audit(ctx, System, "backup.restored", &ss.project, &ss.env, ss.String(),
			fmt.Sprintf("restored backup %s into %s's volumes; restore snapshot %d in place to undo", short, ss.svc.Name, safetyID))
	}
	if ferr := c.store.FinishBackup(ctx, fin); ferr != nil {
		c.log.Error("record restore", "restore", row.ID, "err", ferr)
	}
}
