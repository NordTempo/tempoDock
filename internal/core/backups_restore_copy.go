package core

import (
	"context"
	"fmt"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// restoreVolumeCopy restores a volume backup of ss, a service that isn't
// a template database, into a new service called name (ADR 0095): a copy
// of ss in the same environment (copyVolumeService), its volumes filled
// from the backup before its first deploy, then deployed with the image
// ss's active release runs. ss is never touched: it is the way to look at
// old data without stopping anything. It runs in the background; the
// answer is the copy and the restore, running, a row in ss's backup list
// with the copy as its target.
func (c *Core) restoreVolumeCopy(ctx context.Context, a Actor, ss svcScope, req apitypes.RestoreBackupRequest) (apitypes.RestoreResult, error) {
	name := req.As
	// The copy is a new service: what creating one asks for.
	if err := a.checkEnv(apitypes.ScopeDeploy, "creating a service", ss.project, ss.env); err != nil {
		return apitypes.RestoreResult{}, err
	}
	if err := validName("service", name); err != nil {
		return apitypes.RestoreResult{}, err
	}
	if name == sharedScope || name == orgScope {
		return apitypes.RestoreResult{}, invalid("%q is reserved for variable references (${{%s.KEY}}); pick another name for the copy", name, name)
	}
	from, err := c.backupToRestore(ctx, ss, req.Snapshot)
	if err != nil {
		return apitypes.RestoreResult{}, err
	}
	short := from.SnapshotID[:8]
	if from.Kind != string(apitypes.BackupVolume) {
		return apitypes.RestoreResult{}, invalid("snapshot %s is a %s backup, not one of %s's volumes", short, from.Kind, ss)
	}
	if c.backupsMoving() {
		return apitypes.RestoreResult{}, conflict("the backups are moving to other storage; restore once the move finishes")
	}
	if err := c.notMoving(ctx, ss.envScope, "restore"); err != nil {
		return apitypes.RestoreResult{}, err
	}
	step, err := c.copyImage(ctx, ss, name)
	if err != nil {
		return apitypes.RestoreResult{}, err
	}
	if !c.claimService(ss.svc.ID) {
		return apitypes.RestoreResult{}, conflict("a backup or restore of %s is already running; wait for it to finish", ss)
	}
	to, err := c.copyVolumeService(ctx, ss, name)
	if err != nil {
		c.releaseService(ss.svc.ID)
		return apitypes.RestoreResult{}, err
	}
	c.audit(ctx, a, "service.create", &ss.project, &ss.env, to.String(),
		fmt.Sprintf("created service as a copy of %s, for the restore of backup %s", ss.svc.Name, short))
	// The copy isn't deployed until its volumes are filled.
	if err := c.lockForRestore(ctx, to, backupRestore(from.SnapshotID)); err != nil {
		c.releaseService(ss.svc.ID)
		return apitypes.RestoreResult{}, err
	}
	row, err := c.store.CreateBackup(ctx, store.CreateBackupParams{
		ServiceID: ss.svc.ID, Operation: "restore", Kind: string(apitypes.BackupVolume), Trigger: "manual",
		Repository: from.Repository, SnapshotID: from.SnapshotID, RestoredTo: &to.svc.ID, CreatedBy: a.Name, StartedAt: now(),
	})
	if err != nil {
		c.unlockVolumes(to.svc.ID)
		c.releaseService(ss.svc.ID)
		return apitypes.RestoreResult{}, err
	}
	c.audit(ctx, a, "backup.restore", &ss.project, &ss.env, ss.String(),
		fmt.Sprintf("restoring backup %s into new service %s; %s keeps running", short, name, ss.svc.Name))
	c.wg.Go(func() {
		ctx := c.ctx
		log, err := c.fillFromBackup(ctx, to, from.SnapshotID, row.ID)
		// The fill is done; the deploy is the copy's.
		c.unlockVolumes(to.svc.ID)
		if err != nil {
			c.finishVolumeCopy(ctx, ss, to, row, log, fmt.Errorf("%w; %s was never started", err, name))
			return
		}
		rel, err := c.startRelease(ctx, a, to, releaseSpec{source: apitypes.SourcePromote, promote: step, done: func(ok bool) {
			var err error
			if !ok {
				err = fmt.Errorf("%s failed to start with the backup's data; see its release (`dock releases -s %s`)", name, name)
			}
			c.finishVolumeCopy(c.ctx, ss, to, row, log, err)
		}})
		if err != nil {
			c.finishVolumeCopy(ctx, ss, to, row, log,
				fmt.Errorf("%s holds the backup's data, but couldn't be deployed: %w. Deploy it once that's fixed", name, err))
			return
		}
		c.audit(ctx, a, "release.deploy", &to.project, &to.env, to.String(),
			fmt.Sprintf("release %d from image %s, as release %d of %s runs", rel.Number, rel.ImageRef, step.from.Number, ss.svc.Name))
	})
	out, err := c.toService(ctx, a, to, apitypes.PendingChanges{})
	return apitypes.RestoreResult{Service: out, Restore: toBackupRun(row, ss.svc.Name, map[int64]string{to.svc.ID: name})}, err
}

// copyImage is the release a copy of ss called name first runs, as a
// promotion step: the image of ss's active release, which is on the
// server already or comes from the registry. It refuses, before anything
// is created, when ss has no active release or its image is neither on
// the server nor anywhere it can be pulled from.
func (c *Core) copyImage(ctx context.Context, ss svcScope, name string) (*promoteStep, error) {
	rt, err := c.runtimeFor(ss.env.ServerID)
	if err != nil {
		return nil, err
	}
	active, err := c.store.GetActiveRelease(ctx, ss.svc.ID)
	if store.IsNotFound(err) {
		return nil, invalid("%s has no active release, so there is no image for the copy to run; deploy it first, or restore with --in-place", ss)
	}
	if err != nil {
		return nil, err
	}
	// An image Dock built that isn't in the registry exists on the server
	// only.
	if c.dockNamed(active.ImageRef) && (active.ImageDigest == "" || c.reg == nil) {
		ok, err := rt.HasImage(ctx, active.ImageRef)
		if err != nil {
			return nil, c.nodeErr(ss.env.ServerID, err)
		}
		if !ok {
			return nil, invalid("the image release %d of %s runs (%s) is no longer on the server and isn't in the registry; deploy %s again, then restore into a copy",
				active.Number, ss, active.ImageRef, ss.svc.Name)
		}
	}
	ref, id, _ := c.promotedImage(active, true)
	return &promoteStep{service: name, from: active, imageRef: ref, imageID: id, done: make(chan struct{})}, nil
}

// copyVolumeService creates a copy of ss called name in its environment
// for a restore: ss's live settings, its own live variables (sealed again
// for the copy; references are kept as they are, one to ss itself too),
// its applied volumes (same names and mount paths, new empty Docker
// volumes, applied at once so they can be filled before the first deploy)
// and its sensitive marks. Domains, the git connection and staged changes
// are left out.
func (c *Core) copyVolumeService(ctx context.Context, ss svcScope, name string) (svcScope, error) {
	to := svcScope{envScope: ss.envScope}
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		src, err := q.GetService(ctx, ss.svc.ID)
		if err != nil {
			return err
		}
		to.svc, err = copyService(ctx, q, ss.envScope, src, name)
		if store.IsUnique(err) {
			return conflict("service %q already exists in %s; pick another name for the copy (--as)", name, ss.envScope)
		}
		if err != nil {
			return err
		}
		vols, err := q.ListVolumes(ctx, src.ID)
		if err != nil {
			return err
		}
		for _, v := range vols {
			if v.Pending {
				continue
			}
			if _, err := q.CreateVolume(ctx, store.CreateVolumeParams{
				ServiceID: to.svc.ID, Name: v.Name, MountPath: v.MountPath, DockerName: volumeName(to, v.Name),
			}); err != nil {
				return err
			}
		}
		vars, err := q.ListVariables(ctx, ss.env.ID)
		if err != nil {
			return err
		}
		for _, v := range vars {
			if v.ServiceID == nil || *v.ServiceID != src.ID || v.Value == nil {
				continue // another's, or never live: only staged
			}
			value, _, err := c.clonedValue(v, variableAAD(ss.env.ID, &to.svc.ID, v.Key), nil, true, false)
			if err != nil {
				return err
			}
			if err := q.InsertLiveVariable(ctx, store.InsertLiveVariableParams{
				EnvironmentID: ss.env.ID, ServiceID: &to.svc.ID, Key: v.Key, Value: value, UpdatedAt: now(),
			}); err != nil {
				return err
			}
		}
		marks := store.CopySensitiveMarksParams{ToService: to.svc.ID, FromService: src.ID}
		if err := q.CopySensitiveMarks(ctx, marks); err != nil {
			return err
		}
		return q.CopySensitiveKeys(ctx, store.CopySensitiveKeysParams(marks))
	})
	return to, err
}

// finishVolumeCopy records the outcome of a volume backup's restore into
// the copy to, once. The claim on the source ends first, as in runBackup.
func (c *Core) finishVolumeCopy(ctx context.Context, ss, to svcScope, row store.Backup, log string, err error) {
	c.releaseService(ss.svc.ID)
	if ctx.Err() != nil {
		return // shutting down: the next start marks it failed
	}
	c.keepBackupLog(ctx, ss.svc.ID, row.ID, log)
	short := row.SnapshotID[:min(8, len(row.SnapshotID))]
	fin := store.FinishBackupParams{Status: string(apitypes.BackupSucceeded), SnapshotID: row.SnapshotID, FinishedAt: ptr(now()), ID: row.ID}
	if err != nil {
		fin.Status, fin.Error = string(apitypes.BackupFailed), err.Error()
		c.audit(ctx, System, "backup.restore_failed", &ss.project, &ss.env, ss.String(),
			fmt.Sprintf("restore of backup %s into %s failed: %v; %s is kept for a look, archive it when done", short, to.svc.Name, err, to.svc.Name))
	} else {
		c.audit(ctx, System, "backup.restored", &ss.project, &ss.env, ss.String(),
			fmt.Sprintf("restored backup %s into %s, a copy of %s without its domains; archive it when done", short, to.svc.Name, ss.svc.Name))
	}
	if ferr := c.store.FinishBackup(ctx, fin); ferr != nil {
		c.log.Error("record restore", "restore", row.ID, "err", ferr)
	}
}
