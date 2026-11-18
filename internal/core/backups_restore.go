package core

import (
	"cmp"
	"context"
	"fmt"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// RestoreBackup restores a backup. A service that isn't a template
// database restores its volume backup in place (ADR 0095,
// restoreVolumeBackup). A template database's snapshot restores into a NEW
// service from the same template (ADR 0011, 0019): new credentials, the
// snapshot's data, nothing referencing it yet. With req.ToTime it
// restores a Postgres database as it was then instead (ADR 0093,
// restoreToTime), into a new service or, with req.InPlace, itself. Into a
// new service the live one is never touched; switch references to the
// new one when it checks out. The
// answer comes at once with the new service; the restore finishes in the
// background (a restore row on the source service, and audit events).
func (c *Core) RestoreBackup(ctx context.Context, a Actor, ref ServiceRef, req apitypes.RestoreBackupRequest) (apitypes.RestoreResult, error) {
	res, _, err := c.restoreBackup(ctx, a, ref, req)
	return res, err
}

// restoreBackup is RestoreBackup with a done signal, for a caller that
// waits for the restore (a restore drill): once a template database's
// snapshot is being restored into a new service, done is not nil and
// receives the restore's outcome (nil, or why it failed) once, after its
// row says so. A daemon that is shutting down sends nothing. The other
// kinds of restore answer no signal.
func (c *Core) restoreBackup(ctx context.Context, a Actor, ref ServiceRef, req apitypes.RestoreBackupRequest) (res apitypes.RestoreResult, done <-chan error, err error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "restoring a backup")
	if err != nil {
		return apitypes.RestoreResult{}, nil, err
	}
	if err := notArchived(ss.project); err != nil {
		return apitypes.RestoreResult{}, nil, err
	}
	t, ok := templateOf(ss.svc)
	if !ok {
		res, err = c.restoreVolumeBackup(ctx, a, ss, req)
		return res, nil, err
	}
	if req.ToTime != nil {
		res, err = c.restoreToTime(ctx, a, ss, t, req)
		return res, nil, err
	}
	if req.InPlace {
		return apitypes.RestoreResult{}, nil, invalid("a backup restores into a new service; only a restore to a time (to_time) goes in place")
	}
	from, err := c.backupToRestore(ctx, ss, req.Snapshot)
	if err != nil {
		return apitypes.RestoreResult{}, nil, err
	}
	name := req.As
	if name == "" {
		name = ss.svc.Name + "-restored"
	}
	if err := validName("service", name); err != nil {
		return apitypes.RestoreResult{}, nil, err
	}
	if _, err := c.runtimeFor(ss.env.ServerID); err != nil {
		return apitypes.RestoreResult{}, nil, err
	}
	if c.backupsMoving() {
		return apitypes.RestoreResult{}, nil, conflict("the backups are moving to other storage; restore once the move finishes")
	}
	if err := c.notMoving(ctx, ss.envScope, "restore"); err != nil {
		return apitypes.RestoreResult{}, nil, err
	}
	if !c.claimService(ss.svc.ID) {
		return apitypes.RestoreResult{}, nil, conflict("a backup or restore of %s is already running; wait for it to finish", ss)
	}
	r := &restore{c: c, from: ss, t: t, snapshot: from.SnapshotID, done: make(chan error, 1)}
	// Created without a deploy, so the restore row exists before anything
	// can finish.
	r.to, err = c.createFromTemplate(ctx, a, ss.envScope, name, t, templateOptions{noDeploy: true, marksFrom: ss.svc.ID})
	if err != nil {
		c.releaseService(ss.svc.ID)
		return apitypes.RestoreResult{}, nil, err
	}
	r.row, err = c.store.CreateBackup(ctx, store.CreateBackupParams{
		ServiceID: ss.svc.ID, Operation: "restore", Kind: string(t.backup), Trigger: "manual",
		Repository: from.Repository, SnapshotID: from.SnapshotID, RestoredTo: &r.to.svc.ID, CreatedBy: a.Name, StartedAt: now(),
	})
	if err != nil {
		c.releaseService(ss.svc.ID)
		return apitypes.RestoreResult{}, nil, err
	}
	c.audit(ctx, a, "backup.restore", &ss.project, &ss.env, ss.String(),
		fmt.Sprintf("restoring snapshot %s into new service %s", from.SnapshotID[:8], name))
	if t.backup == apitypes.BackupRedis {
		// Redis: fill the new volume first, then deploy. The new service
		// isn't deployed meanwhile.
		if err := c.lockForRestore(ctx, r.to, volumeRestore{what: "the restore of backup " + from.SnapshotID[:8], watch: "`dock backup list`"}); err != nil {
			r.finish(c.ctx, err)
		} else {
			c.goCounted("restoring a backup into a new service", func() { r.fillVolume(c.ctx, a) })
		}
	} else if err := c.deployTemplate(ctx, a, r.to, r.deployed); err != nil {
		r.finish(c.ctx, err)
	}
	svc, err := c.toService(ctx, a, r.to, apitypes.PendingChanges{})
	return apitypes.RestoreResult{Service: svc, Restore: toBackupRun(r.row, ss.svc.Name, map[int64]string{r.to.svc.ID: name})}, r.done, err
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
	// done receives the outcome from finish, once (restoreBackup); it
	// holds one, so finish never waits for a reader.
	done chan error
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
	if r.t.backup == apitypes.BackupMySQL {
		r.finish(ctx, r.mysqlRestore(ctx))
		return
	}
	r.finish(ctx, r.pgRestore(ctx))
}

// mysqlRestore streams the dump into the new, running MySQL or MariaDB as
// its root.
func (r *restore) mysqlRestore(ctx context.Context) error {
	to, err := r.c.scopeOf(ctx, r.to.svc.ID)
	if err != nil {
		return err
	}
	env, err := r.c.activeEnv(ctx, to)
	if err != nil {
		return notRunningErr(to, err)
	}
	target := backup.Source{Kind: apitypes.BackupMySQL, Host: to.svc.Name, Port: r.t.port, Password: mysqlRootPassword(env)}
	return r.run(ctx, to, target, nil)
}

// mysqlRootPassword is a MySQL or MariaDB service's root password, from
// the variable its image reads.
func mysqlRootPassword(env map[string]string) string {
	return cmp.Or(env["MYSQL_ROOT_PASSWORD"], env["MARIADB_ROOT_PASSWORD"])
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
	err := caughtErr(c.log, "restoring a backup into a new service", func() error { return r.fill(ctx) })
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

// finish records a restore's outcome once. A failed one archives its
// target under another name and raises an alert (restoreTargetFailed)
// before the row says so; a successful one closes that alert. Whoever
// waits on done gets the outcome last, with the row written.
func (r *restore) finish(ctx context.Context, err error) {
	c := r.c
	c.releaseService(r.from.svc.ID) // before the row says finished, as in runBackup
	// The copy's schema was cached when its first release went live,
	// before the dump filled it: the Data panel reads it again.
	c.panel.Forget(r.to.svc.ID)
	if ctx.Err() != nil {
		return // shutting down: the next start marks it failed
	}
	c.keepBackupLog(ctx, r.from.svc.ID, r.row.ID, r.log)
	if err == nil {
		_ = c.ensureDataAccess(ctx, r.to) // not left to afterActive's retry: `dock db query` works once this says done
	}
	// The row is written last, as in runBackup.
	fin := store.FinishBackupParams{Status: string(apitypes.BackupSucceeded), SnapshotID: r.snapshot, FinishedAt: ptr(now()), ID: r.row.ID}
	if err != nil {
		fin.Status, fin.Error = string(apitypes.BackupFailed), err.Error()
		kept := c.restoreTargetFailed(ctx, r.from, r.to.svc.ID, restoreInto{id: r.row.ID}, err)
		c.audit(ctx, System, "backup.restore_failed", &r.from.project, &r.from.env, r.from.String(),
			fmt.Sprintf("restore into %s failed: %v%s", r.to.svc.Name, err, keptText(r.to.svc.Name, kept)))
	} else {
		c.restoreSucceeded(ctx, r.from)
		c.audit(ctx, System, "backup.restored", &r.from.project, &r.from.env, r.from.String(),
			fmt.Sprintf("restored snapshot %s into %s; point references at ${{%s.%s}} when it checks out", r.snapshot[:8], r.to.svc.Name, r.to.svc.Name, r.t.exports[0]))
	}
	if ferr := c.store.FinishBackup(ctx, fin); ferr != nil {
		c.log.Error("record restore", "restore", r.row.ID, "err", ferr)
	}
	select {
	case r.done <- err:
	default: // a second finish: the first outcome stands
	}
}
