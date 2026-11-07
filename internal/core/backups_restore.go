package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// RestoreBackup restores a template database's snapshot into a NEW
// service from the same template (ADR 0011, 0019): new credentials, the
// snapshot's data, nothing referencing it yet. The live service is never
// touched; switch references to the new one when it checks out. The
// answer comes at once with the new service; the restore finishes in the
// background (a restore row on the source service, and audit events).
func (c *Core) RestoreBackup(ctx context.Context, a Actor, ref ServiceRef, req apitypes.RestoreBackupRequest) (apitypes.RestoreResult, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "restoring a backup")
	if err != nil {
		return apitypes.RestoreResult{}, err
	}
	if err := notArchived(ss.project); err != nil {
		return apitypes.RestoreResult{}, err
	}
	t, ok := templateOf(ss.svc)
	if !ok {
		return apitypes.RestoreResult{}, invalid("only template databases restore into a new service; restore %s's volume backups by hand with restic (see docs/guide/databases.md)", ss)
	}
	snap := strings.TrimSpace(req.Snapshot)
	if len(snap) < 8 {
		return apitypes.RestoreResult{}, invalid("give the snapshot ID or at least its first 8 characters; `dock backup list` shows them")
	}
	from, err := c.store.GetBackupBySnapshot(ctx, store.GetBackupBySnapshotParams{ServiceID: ss.svc.ID, Prefix: snap})
	if store.IsNotFound(err) {
		return apitypes.RestoreResult{}, notFound("%s has no backup with snapshot %s", ss, snap)
	}
	if err != nil {
		return apitypes.RestoreResult{}, err
	}
	if from.Pruned {
		return apitypes.RestoreResult{}, invalid("snapshot %s was removed by retention; pick a newer one", snap)
	}
	set, err := c.backupSettings(ctx)
	if err != nil {
		return apitypes.RestoreResult{}, err
	}
	if why := notRestorableHere(from.Repository, set.Repository); why != "" {
		return apitypes.RestoreResult{}, invalid("backup %s can't be restored here: %s", from.SnapshotID[:min(8, len(from.SnapshotID))], why)
	}
	name := req.As
	if name == "" {
		name = ss.svc.Name + "-restored"
	}
	if err := validName("service", name); err != nil {
		return apitypes.RestoreResult{}, err
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
	if !c.claimService(ss.svc.ID) {
		return apitypes.RestoreResult{}, conflict("a backup or restore of %s is already running; wait for it to finish", ss)
	}
	r := &restore{c: c, from: ss, t: t, snapshot: from.SnapshotID}
	// Created without a deploy, so the restore row exists before anything
	// can finish.
	r.to, err = c.createFromTemplate(ctx, a, ss.envScope, name, t, templateOptions{noDeploy: true})
	if err != nil {
		c.releaseService(ss.svc.ID)
		return apitypes.RestoreResult{}, err
	}
	r.row, err = c.store.CreateBackup(ctx, store.CreateBackupParams{
		ServiceID: ss.svc.ID, Operation: "restore", Kind: string(t.backup), Trigger: "manual",
		Repository: from.Repository, SnapshotID: from.SnapshotID, RestoredTo: &r.to.svc.ID, CreatedBy: a.Name, StartedAt: now(),
	})
	if err != nil {
		c.releaseService(ss.svc.ID)
		return apitypes.RestoreResult{}, err
	}
	c.audit(ctx, a, "backup.restore", &ss.project, &ss.env, ss.String(),
		fmt.Sprintf("restoring snapshot %s into new service %s", from.SnapshotID[:8], name))
	if t.backup == apitypes.BackupRedis {
		// Redis: fill the new volume first, then deploy. The new service
		// isn't deployed meanwhile.
		if err := c.lockForRestore(ctx, r.to, volumeRestore{what: "the restore of backup " + from.SnapshotID[:8], watch: "`dock backup list`"}); err != nil {
			r.finish(c.ctx, err)
		} else {
			c.wg.Go(func() { r.fillVolume(c.ctx, a) })
		}
	} else if err := c.deployTemplate(ctx, a, r.to, r.deployed); err != nil {
		r.finish(c.ctx, err)
	}
	svc, err := c.toService(ctx, r.to, apitypes.PendingChanges{})
	return apitypes.RestoreResult{Service: svc, Restore: toBackupRun(r.row, ss.svc.Name, map[int64]string{r.to.svc.ID: name})}, err
}

// restore is one restore in flight.
type restore struct {
	c        *Core
	from, to svcScope
	t        dbTemplate
	snapshot string
	row      store.Backup
	// log is what the restore job printed (jobLog), kept by finish.
	log string
}

// deployed continues a Postgres restore once the new service's first
// release finished: pg_restore into it while it runs.
func (r *restore) deployed(ok bool) {
	ctx := r.c.ctx
	if r.t.backup == apitypes.BackupRedis {
		// The data was in place before it started.
		if !ok {
			r.finish(ctx, fmt.Errorf("%s failed to start with the restored data; see its release", r.to.svc.Name))
			return
		}
		r.finish(ctx, nil)
		return
	}
	if !ok {
		r.finish(ctx, fmt.Errorf("%s failed to start; see its release", r.to.svc.Name))
		return
	}
	r.finish(ctx, r.pgRestore(ctx))
}

// pgRestore streams the dump into the new, running Postgres.
func (r *restore) pgRestore(ctx context.Context) error {
	c := r.c
	to, err := c.scopeOf(ctx, r.to.svc.ID)
	if err != nil {
		return err
	}
	conn, err := c.dbConnection(ctx, to)
	if err != nil {
		return notRunningErr(to, err)
	}
	target := backup.Source{
		Kind: apitypes.BackupPostgres, Host: to.svc.Name, Port: conn.port,
		User: conn.owner.User, Password: conn.owner.Password, Database: conn.database,
	}
	return r.run(ctx, to, target, nil)
}

// fillVolume writes a Redis snapshot into the new service's volume, which
// RestoreBackup locked for it, then deploys it.
func (r *restore) fillVolume(ctx context.Context, a Actor) {
	c := r.c
	err := r.fill(ctx)
	c.unlockVolumes(r.to.svc.ID)
	if err != nil {
		r.finish(ctx, err)
		return
	}
	if err := c.deployTemplate(ctx, a, r.to, r.deployed); err != nil {
		r.finish(ctx, err)
	}
}

// fill runs the restore job into the new service's volume.
func (r *restore) fill(ctx context.Context) error {
	c := r.c
	rt, err := c.runtimeFor(r.to.env.ServerID)
	if err != nil {
		return err
	}
	vol := volumeName(r.to, templateVolume)
	if err := rt.EnsureVolume(ctx, vol, c.labels(r.to, 0)); err != nil {
		return fmt.Errorf("create volume: %w", c.nodeErr(r.to.env.ServerID, err))
	}
	return r.run(ctx, r.to, backup.Source{}, []node.Mount{{Volume: vol, Path: r.t.mountPath}})
}

// run runs the restore job for the new service on its server, in the
// template's image (jobImage): target is how it reaches the new database
// (Postgres), mounts its volume (Redis). Its log is kept in r.log.
func (r *restore) run(ctx context.Context, to svcScope, target backup.Source, mounts []node.Mount) error {
	c := r.c
	rt, err := c.runtimeFor(to.env.ServerID)
	if err != nil {
		return err
	}
	set, err := c.backupSettings(ctx)
	if err != nil {
		return err
	}
	repo, err := c.repoOf(ctx, set)
	if err != nil {
		return err
	}
	script, err := backup.RestoreScript(r.t.backup, r.t.mountPath)
	if err != nil {
		return err
	}
	spec := node.JobSpec{
		Name:  fmt.Sprintf("dock_p%d_e%d_s%d_restore%d", to.project.ID, to.env.ID, to.svc.ID, r.row.ID),
		Image: jobImage(to), Labels: c.labels(to, 0), Env: backup.RestoreEnv(r.snapshot, target, repo), Script: script,
		Network: networkName(to.envScope), Mounts: mounts,
	}
	out, err := c.runResticJob(ctx, to.env.ServerID, rt, repo, spec)
	r.log = jobLog(spec.Env, out)
	return err
}

// finish records a restore's outcome once.
func (r *restore) finish(ctx context.Context, err error) {
	c := r.c
	c.releaseService(r.from.svc.ID) // before the row says finished, as in runBackup
	if ctx.Err() != nil {
		return // shutting down: the next start marks it failed
	}
	c.keepBackupLog(ctx, r.from.svc.ID, r.row.ID, r.log)
	// The row is written last, as in runBackup.
	fin := store.FinishBackupParams{Status: string(apitypes.BackupSucceeded), SnapshotID: r.snapshot, FinishedAt: ptr(now()), ID: r.row.ID}
	if err != nil {
		fin.Status, fin.Error = string(apitypes.BackupFailed), err.Error()
		c.audit(ctx, System, "backup.restore_failed", &r.from.project, &r.from.env, r.from.String(),
			fmt.Sprintf("restore into %s failed: %v; %s is kept for a look, archive it when done", r.to.svc.Name, err, r.to.svc.Name))
	} else {
		c.audit(ctx, System, "backup.restored", &r.from.project, &r.from.env, r.from.String(),
			fmt.Sprintf("restored snapshot %s into %s; point references at ${{%s.%s}} when it checks out", r.snapshot[:8], r.to.svc.Name, r.to.svc.Name, r.t.exports[0]))
	}
	if ferr := c.store.FinishBackup(ctx, fin); ferr != nil {
		c.log.Error("record restore", "restore", r.row.ID, "err", ferr)
	}
}
