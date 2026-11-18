package core

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/walarchive"
)

// Restores to a time (ADR 0093): the newest base backup that ended by
// then, unpacked by a job that runs Postgres itself on it with
// restore_command and recovery_target_time until it is promoted. Into a
// new service like every database restore (ADR 0061), or explicitly in
// place behind a safety snapshot (ADR 0047).

// pitrPlan is what a restore to a time restores from.
type pitrPlan struct {
	base   store.Backup
	cfg    walarchive.Config
	target time.Time
}

// planToTime picks the base backup a restore of ss to target starts from
// and checks the archive holds every commit up to it, saying what's
// possible when it doesn't.
func (c *Core) planToTime(ctx context.Context, ss svcScope, target time.Time) (pitrPlan, error) {
	if target.After(now()) {
		return pitrPlan{}, invalid("%s is in the future", target.UTC().Format(time.RFC3339))
	}
	set, err := c.backupSettings(ctx)
	if err != nil {
		return pitrPlan{}, err
	}
	bases, err := c.store.ListPITRBases(ctx, ss.svc.ID)
	if err != nil {
		return pitrPlan{}, err
	}
	var base *store.Backup
	var earliest, next *time.Time
	for i, b := range bases {
		if b.ConsistentAt == nil || b.WalStart == "" || b.Repository != set.Repository {
			continue
		}
		if earliest == nil {
			earliest = b.ConsistentAt
		}
		switch {
		case !b.ConsistentAt.After(target):
			base = &bases[i]
		case next == nil:
			next = b.ConsistentAt // the first base backup after the time
		}
	}
	switch {
	case base == nil && earliest == nil:
		return pitrPlan{}, invalid("%s has no base backup to restore to a time from; point-in-time restore takes one when it is turned on (`dock backup pitr on`)", ss)
	case base == nil:
		return pitrPlan{}, invalid("the earliest time %s restores to is %s", ss, earliest.UTC().Format(time.RFC3339))
	}
	cfg, err := c.walConfig(ctx, ss.svc.ID, base.WalGeneration)
	if err != nil {
		return pitrPlan{}, err
	}
	ix, err := walarchive.List(ctx, cfg)
	if err != nil {
		return pitrPlan{}, fmt.Errorf("list the WAL archive: %w", err)
	}
	if err := ix.Reaches(base.WalStart, base.WalSegmentBytes, target); err != nil {
		// A later base backup means the archive started over after this
		// one's ended (turned off and on, an in-place restore): the time
		// is in the gap, and waiting won't fill it.
		if cov, cerr := ix.Cover(base.WalStart, base.WalSegmentBytes); cerr == nil && cov.Last != "" && next != nil {
			return pitrPlan{}, invalid("%s can't be restored to %s: it wasn't archived from %s to %s (point-in-time restore was off or started over); %s",
				ss, target.UTC().Format(time.RFC3339), cov.Through.UTC().Format(time.RFC3339), next.UTC().Format(time.RFC3339), c.restorableText(ctx, ss, set, bases))
		}
		return pitrPlan{}, invalid("%s can't be restored to %s: %v", ss, target.UTC().Format(time.RFC3339), err)
	}
	return pitrPlan{base: *base, cfg: cfg, target: target}, nil
}

// restorableText says which times ss restores to, for a refusal: the
// ranges its Backups page shows (pitrRanges).
func (c *Core) restorableText(ctx context.Context, ss svcScope, set store.BackupSetting, bases []store.Backup) string {
	row, _ := c.store.GetPITR(ctx, ss.svc.ID) // zero without one: every generation ended
	live, _ := c.archiverLive(ctx, ss, row.Enabled && !c.isLeaving(ss.svc.ID))
	ranges := c.pitrRanges(ctx, ss, row, pitrGenerations(bases, set.Repository), live)
	if len(ranges) == 0 {
		return "pick a time before or after that"
	}
	parts := make([]string, len(ranges))
	for i, r := range ranges {
		parts[i] = "from " + r.From.UTC().Format(time.RFC3339) + " to " + r.Through.UTC().Format(time.RFC3339)
	}
	return "it restores " + strings.Join(parts, " and ")
}

// latestRestorable is the newest moment ss restores to: where the
// archive of its newest base backup ends, read from the bucket (one
// listing). For a running database that is up to a minute behind it
// (archive_timeout); for a stopped one it is its last commit, since
// Postgres hands over its last segment at a clean shutdown.
func (c *Core) latestRestorable(ctx context.Context, ss svcScope) (time.Time, error) {
	set, err := c.backupSettings(ctx)
	if err != nil {
		return time.Time{}, err
	}
	bases, err := c.store.ListPITRBases(ctx, ss.svc.ID)
	if err != nil {
		return time.Time{}, err
	}
	gens := pitrGenerations(bases, set.Repository)
	if len(gens) == 0 {
		return time.Time{}, invalid("%s has no base backup to restore to a time from; point-in-time restore takes one when it is turned on (`dock backup pitr on`)", ss)
	}
	base := gens[len(gens)-1].last
	cfg, err := c.walConfig(ctx, ss.svc.ID, base.WalGeneration)
	if err != nil {
		return time.Time{}, err
	}
	ix, err := walarchive.List(ctx, cfg)
	if err != nil {
		return time.Time{}, fmt.Errorf("list the WAL archive: %w", err)
	}
	cov, err := ix.Cover(base.WalStart, base.WalSegmentBytes)
	if err != nil {
		return time.Time{}, err
	}
	if cov.Last == "" || !cov.Through.After(*base.ConsistentAt) {
		return time.Time{}, invalid("nothing of %s is archived past its newest base backup (%s) yet; try again in a minute, or restore to a time before it",
			ss, base.ConsistentAt.UTC().Format(time.RFC3339))
	}
	return cov.Through.UTC(), nil
}

// RestoreLatest is RestoreBackup to the newest moment a Postgres
// database with point-in-time restore restores to (req.ToLatest, ADR
// 0093), without naming a time: a stopped database, or "as it is now",
// which no clock on the caller's side can name exactly. The restore it
// starts records the time it resolved to.
func (c *Core) RestoreLatest(ctx context.Context, a Actor, ref ServiceRef, req apitypes.RestoreBackupRequest) (apitypes.RestoreResult, error) {
	if req.Snapshot != "" || req.ToTime != nil {
		return apitypes.RestoreResult{}, invalid("give a snapshot, a time (to_time) or to_latest, not more than one")
	}
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "restoring a backup")
	if err != nil {
		return apitypes.RestoreResult{}, err
	}
	if t, ok := templateOf(ss.svc); !ok || t.database != apitypes.DatabasePostgres {
		return apitypes.RestoreResult{}, invalid("only a Postgres database with point-in-time restore restores to its latest moment; restore one of %s's backups instead", ss)
	}
	at, err := c.latestRestorable(ctx, ss)
	if err != nil {
		return apitypes.RestoreResult{}, err
	}
	req.ToTime = &at
	return c.RestoreBackup(ctx, a, ref, req)
}

// restoreToTime restores ss as it was at req.ToTime, into a new service
// or (req.InPlace) itself. The caller checked deploy scope and that ss
// is a template database.
func (c *Core) restoreToTime(ctx context.Context, a Actor, ss svcScope, t dbTemplate, req apitypes.RestoreBackupRequest) (apitypes.RestoreResult, error) {
	if t.database != apitypes.DatabasePostgres {
		return apitypes.RestoreResult{}, invalid("only Postgres databases restore to a time; restore one of %s's backups instead", ss)
	}
	if req.Snapshot != "" {
		return apitypes.RestoreResult{}, invalid("give a snapshot or a time to restore, not both")
	}
	if req.InPlace && req.As != "" {
		return apitypes.RestoreResult{}, invalid("an in-place restore keeps the service's name; leave out the new name")
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
	plan, err := c.planToTime(ctx, ss, req.ToTime.UTC())
	if err != nil {
		return apitypes.RestoreResult{}, err
	}
	keep, err := c.templateLiterals(ctx, ss, t)
	if err != nil {
		return apitypes.RestoreResult{}, err
	}
	if req.InPlace {
		return c.restoreToTimeInPlace(ctx, a, ss, t, plan, keep)
	}
	name := cmp.Or(req.As, ss.svc.Name+"-restored")
	if err := validName("service", name); err != nil {
		return apitypes.RestoreResult{}, err
	}
	if !c.claimService(ss.svc.ID) {
		return apitypes.RestoreResult{}, conflict("a backup or restore of %s is already running; wait for it to finish", ss)
	}
	// The data files hold the source's credentials, so the new service
	// takes them (as a snapshot's restore does).
	to, err := c.createFromTemplate(ctx, a, ss.envScope, name, t, templateOptions{noDeploy: true, image: ss.svc.Image, keep: keep, marksFrom: ss.svc.ID})
	if err != nil {
		c.releaseService(ss.svc.ID)
		return apitypes.RestoreResult{}, err
	}
	if err := c.lockForRestore(ctx, to, timeRestore(plan.target)); err != nil {
		c.releaseService(ss.svc.ID)
		return apitypes.RestoreResult{}, err
	}
	row, err := c.store.CreatePITRRestore(ctx, store.CreatePITRRestoreParams{
		ServiceID: ss.svc.ID, Repository: plan.base.Repository, SnapshotID: plan.base.SnapshotID, RestoredTo: &to.svc.ID,
		CreatedBy: a.Name, StartedAt: now(), WalGeneration: plan.base.WalGeneration, RestoreToTime: &plan.target,
	})
	if err != nil {
		c.unlockVolumes(to.svc.ID)
		c.releaseService(ss.svc.ID)
		return apitypes.RestoreResult{}, err
	}
	at := plan.target.UTC().Format(time.RFC3339)
	c.audit(ctx, a, "backup.restore", &ss.project, &ss.env, ss.String(), fmt.Sprintf("restoring %s as it was at %s into new service %s", ss.svc.Name, at, name))
	c.wg.Go(func() {
		ctx := c.ctx
		vol := volumeName(to, templateVolume)
		err := c.nodeErr(to.env.ServerID, c.runtime(to.env.ServerID).EnsureVolume(ctx, vol, c.labels(to, 0)))
		log := ""
		if err == nil {
			log, err = c.recoverJob(ctx, to, t, plan, row.ID, vol, keep)
		}
		c.unlockVolumes(to.svc.ID)
		if err != nil {
			c.finishToTime(ctx, ss, to.svc.Name, row, log, err)
			return
		}
		if err := c.deployTemplate(ctx, a, to, func(ok bool) {
			var err error
			if !ok {
				err = fmt.Errorf("%s failed to start with the restored data; see its release", name)
			}
			c.finishToTime(ctx, ss, name, row, log, err)
		}); err != nil {
			c.finishToTime(ctx, ss, name, row, log, err)
		}
	})
	svc, err := c.toService(ctx, a, to, apitypes.PendingChanges{})
	return apitypes.RestoreResult{Service: svc, Restore: toBackupRun(row, ss.svc.Name, map[int64]string{to.svc.ID: name})}, err
}

// timeRestore names a restore to a time for lockForRestore.
func timeRestore(t time.Time) volumeRestore {
	return volumeRestore{what: "the restore to " + t.UTC().Format(time.RFC3339), watch: "`dock backup list`"}
}

// recoverJob runs the restore to a time's job into the volume vol of to
// (the new service, or the database itself): the base backup unpacked
// and the archive replayed to the plan's time. keep holds the
// credentials the data files have.
func (c *Core) recoverJob(ctx context.Context, to svcScope, t dbTemplate, plan pitrPlan, rowID int64, vol string, keep map[string]string) (string, error) {
	rt, err := c.runtimeFor(to.env.ServerID)
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
	user := cmp.Or(keep["POSTGRES_USER"], "postgres")
	env := backup.PITRRestoreEnv(repo, plan.cfg.Env(nil), plan.base.SnapshotID, plan.base.WalStart, plan.base.WalSegmentBytes, plan.target, user)
	env["PGPASSWORD"] = keep["POSTGRES_PASSWORD"]
	spec := node.JobSpec{
		Name:  fmt.Sprintf("dock_p%d_e%d_s%d_pitr%d", to.project.ID, to.env.ID, to.svc.ID, rowID),
		Image: t.image, Labels: c.labels(to, 0), Env: env, Script: backup.PITRRestoreScript,
		Network: networkName(to.envScope), Dock: true,
		Mounts: []node.Mount{{Volume: vol, Path: t.mountPath}},
	}
	out, err := c.runResticJob(ctx, to.env.ServerID, rt, repo, spec)
	return jobLog(spec.Env, out), err
}

// restoreToTimeInPlace restores the database itself to the plan's time:
// it stops, a safety snapshot keeps what it holds (restoring that one
// undoes this), the job restores into its volume, and it starts again.
// Archiving then starts a new generation with a base backup: the restored
// database is another history than the one archived so far.
func (c *Core) restoreToTimeInPlace(ctx context.Context, a Actor, ss svcScope, t dbTemplate, plan pitrPlan, keep map[string]string) (apitypes.RestoreResult, error) {
	at := plan.target.UTC().Format(time.RFC3339)
	safety, safetyVols, err := c.startSnapshot(ctx, a, ss, apitypes.SnapshotBeforeRestore, "before restoring to "+at, 0)
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
	vol, err := c.dataVolume(ctx, ss)
	if err != nil {
		return fail("nothing was restored", err)
	}
	if err := c.lockForRestore(ctx, ss, timeRestore(plan.target)); err != nil {
		return fail("something else writes the volumes, so nothing was restored", err)
	}
	svc, err := c.store.GetService(ctx, ss.svc.ID)
	if err != nil {
		c.unlockVolumes(ss.svc.ID)
		return fail("the service couldn't be read", err)
	}
	ss.svc = svc
	row, err := c.store.CreatePITRRestore(ctx, store.CreatePITRRestoreParams{
		ServiceID: ss.svc.ID, Repository: plan.base.Repository, SnapshotID: plan.base.SnapshotID, RestoredTo: &ss.svc.ID,
		CreatedBy: a.Name, StartedAt: now(), WalGeneration: plan.base.WalGeneration, RestoreToTime: &plan.target,
	})
	if err != nil {
		c.unlockVolumes(ss.svc.ID)
		return fail("nothing was restored", err)
	}
	c.audit(ctx, a, "backup.restore", &ss.project, &ss.env, ss.String(),
		fmt.Sprintf("restoring %s in place to %s; it stops, and snapshot %d keeps the data it holds now", ss.svc.Name, at, safety.ID))
	c.wg.Go(func() {
		ctx := c.ctx
		var log string
		err := c.restoreInPlaceWith(ctx, ss, safety, safetyVols, mounts, "restore to "+at, func(ctx context.Context) error {
			var err error
			log, err = c.recoverJob(ctx, ss, t, plan, row.ID, vol.DockerName, keep)
			return err
		}, func(ctx context.Context) {
			c.pitrNewGeneration(ctx, ss)
		})
		if ctx.Err() == nil {
			c.pruneSnapshots(ctx, ss, 0)
		}
		c.unlockVolumes(ss.svc.ID)
		c.finishToTime(ctx, ss, ss.svc.Name, row, log, err)
		if err == nil {
			c.pitrAfterActive(ctx, ss, false)
		}
	})
	out, err := c.toService(ctx, a, ss, apitypes.PendingChanges{})
	return apitypes.RestoreResult{Service: out, Restore: toBackupRun(row, ss.svc.Name, map[int64]string{ss.svc.ID: ss.svc.Name})}, err
}

// pitrNewGeneration starts a new archive generation for a database whose
// data was replaced, before it starts again: its archiver moves to the
// new folder, and the next base backup starts the window over.
func (c *Core) pitrNewGeneration(ctx context.Context, ss svcScope) {
	if !c.pitrOn(ss.svc.ID) {
		return
	}
	gen := newGeneration()
	if err := c.store.SetPITRGeneration(ctx, store.SetPITRGenerationParams{Generation: gen, ServiceID: ss.svc.ID}); err != nil {
		c.log.Warn("start a new WAL archive generation", "service", ss.String(), "err", err)
		return
	}
	if _, _, err := c.keepArchiver(ctx, ss); err != nil {
		c.log.Warn("move the WAL archiver to a new generation", "service", ss.String(), "err", err)
	}
}

// finishToTime records a restore to a time's outcome once; to is the
// service restored into. The claim on the source ends first, as in
// runBackup. A failed restore into a new service archives that service
// under another name and raises an alert (restoreTargetFailed) before
// the row says so; a successful one closes that alert.
func (c *Core) finishToTime(ctx context.Context, from svcScope, to string, row store.Backup, log string, err error) {
	c.releaseService(from.svc.ID)
	c.panel.Forget(from.svc.ID)
	if row.RestoredTo != nil {
		c.panel.Forget(*row.RestoredTo)
	}
	if ctx.Err() != nil {
		return
	}
	c.keepBackupLog(ctx, from.svc.ID, row.ID, log)
	at := "the time"
	if row.RestoreToTime != nil {
		at = row.RestoreToTime.UTC().Format(time.RFC3339)
	}
	fin := store.FinishBackupParams{Status: string(apitypes.BackupSucceeded), SnapshotID: row.SnapshotID, FinishedAt: ptr(now()), ID: row.ID}
	if err != nil {
		fin.Status, fin.Error = string(apitypes.BackupFailed), err.Error()
		msg := fmt.Sprintf("restore to %s into %s failed: %v", at, to, err)
		if row.RestoredTo != nil && *row.RestoredTo != from.svc.ID {
			kept := c.restoreTargetFailed(ctx, from, *row.RestoredTo, restoreInto{id: row.ID}, err)
			msg += fmt.Sprintf("; it is archived as %s for a look, and its name is free to restore again", kept)
		}
		c.audit(ctx, System, "backup.restore_failed", &from.project, &from.env, from.String(), msg)
	} else {
		c.restoreSucceeded(ctx, from)
		if row.RestoredTo != nil {
			c.dataAccessAfterRestore(ctx, *row.RestoredTo)
		}
		c.audit(ctx, System, "backup.restored", &from.project, &from.env, from.String(), fmt.Sprintf("restored %s as it was at %s into %s", from.svc.Name, at, to))
	}
	if ferr := c.store.FinishBackup(ctx, fin); ferr != nil {
		c.log.Error("record restore", "restore", row.ID, "err", ferr)
	}
}
