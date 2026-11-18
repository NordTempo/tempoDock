package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Running a recovery (ADR 0098): every service of the control plane's own
// server comes back, one at a time (a small server never runs two
// restores at once), environment by environment, databases first, then
// services with volumes, then the rest. A service's volumes are filled
// before it starts; a template database that loads a dump starts alone
// and loads it before anything that uses it starts. Each service starts
// as a new release copying its active one. The run is resumable: a
// service that runs already is left alone.

// recoveryRun is the recovery in flight and its report.
type recoveryRun struct {
	mu     sync.Mutex
	report apitypes.RecoveryReport
}

// recoverVolumes fills a service's applied volumes from a volume backup's
// snapshot before the service first starts, and returns the job's log.
// It is ADR 0095's fillFromBackup, which isn't on main yet: until it is,
// a service with a volume backup fails here and says so.
var recoverVolumes = func(*Core, context.Context, svcScope, string, int64) (string, error) {
	return "", errors.New("restoring a volume backup needs ADR 0095's restore, which this dock doesn't have yet; update dock, or restore it by hand with restic (docs/guide/databases.md)")
}

// StartRecovery runs the recovery plan in the background. RecoveryReport
// shows how it goes. One run at a time.
func (c *Core) StartRecovery(ctx context.Context, a Actor) error {
	if err := a.requirePlatform(BackupsManage, "running a recovery"); err != nil {
		return err
	}
	rec := c.recovery.Load()
	if rec == nil {
		return conflict("this control plane is not in recovery mode; restore one backup with `dock backup restore`")
	}
	if c.backupsMoving() {
		return conflict("the backups are moving to other storage; recover once the move finishes")
	}
	_, backups, repository, err := c.recoveryPlan(ctx, rec)
	if err != nil {
		return err
	}
	todo, err := c.recoveryServices(ctx)
	if err != nil {
		return err
	}
	c.recoveryRun.mu.Lock()
	if c.recoveryRun.report.Running {
		c.recoveryRun.mu.Unlock()
		return conflict("a recovery is already running; `dock admin recover --report` shows it")
	}
	c.recoveryRun.report = apitypes.RecoveryReport{Running: true, StartedAt: ptr(now()), Results: []apitypes.RecoveryResult{}}
	c.recoveryRun.mu.Unlock()
	c.audit(ctx, a.platform(), "control_plane.recovery", nil, nil, "control plane", fmt.Sprintf("started recovering %d services", len(todo)))
	orgs := map[int64]string{}
	c.wg.Go(func() {
		ctx := c.ctx
		counts := map[apitypes.RecoveryOutcome]int{}
		for _, ss := range todo {
			if ctx.Err() != nil {
				break
			}
			org, ok := orgs[ss.project.OrgID]
			if !ok {
				if o, err := c.store.GetOrg(ctx, ss.project.OrgID); err == nil {
					org = o.Slug
				}
				orgs[ss.project.OrgID] = org
			}
			var planned *apitypes.RecoveryService
			if p, ok := backups[ss.svc.ID]; ok {
				planned = &p
			}
			res := c.recoverService(ctx, a, ss, org, planned, repository)
			counts[res.Outcome]++
			c.recoveryRun.mu.Lock()
			c.recoveryRun.report.Results = append(c.recoveryRun.report.Results, res)
			c.recoveryRun.mu.Unlock()
		}
		c.recoveryRun.mu.Lock()
		c.recoveryRun.report.Running, c.recoveryRun.report.FinishedAt = false, ptr(now())
		c.recoveryRun.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		c.audit(ctx, System, "control_plane.recovered", nil, nil, "control plane",
			fmt.Sprintf("recovery: %d restored, %d started, %d on empty volumes, %d being built again, %d already running, %d skipped, %d failed",
				counts[apitypes.RecoveryRestored], counts[apitypes.RecoveryStarted], counts[apitypes.RecoveryEmpty], counts[apitypes.RecoveryRebuilding],
				counts[apitypes.RecoveryRunning], counts[apitypes.RecoverySkipped], counts[apitypes.RecoveryFailed]))
	})
	return nil
}

// RecoveryReport returns the latest recovery run's report since the
// daemon started; empty before any.
func (c *Core) RecoveryReport(_ context.Context, a Actor) (apitypes.RecoveryReport, error) {
	if err := a.requirePlatform(BackupsManage, "reading a recovery's report"); err != nil {
		return apitypes.RecoveryReport{}, err
	}
	c.recoveryRun.mu.Lock()
	defer c.recoveryRun.mu.Unlock()
	out := c.recoveryRun.report
	out.Results = slices.Clone(out.Results)
	return out, nil
}

// recoveryServices lists what a recovery handles: the services of the
// local server with an active release, in the order they come back.
func (c *Core) recoveryServices(ctx context.Context) ([]svcScope, error) {
	actives, err := c.store.ListActiveReleases(ctx)
	if err != nil {
		return nil, err
	}
	type entry struct {
		ss   svcScope
		rank int
	}
	var all []entry
	for _, r := range actives {
		ss, err := c.scopeOf(ctx, r.ServiceID)
		if err != nil {
			return nil, err
		}
		if ss.env.ServerID != c.localID || ss.svc.ArchivedAt != nil || ss.project.ArchivedAt != nil {
			continue
		}
		rank := 2
		if _, ok := templateOf(ss.svc); ok {
			rank = 0
		} else if vols, err := c.appliedVolumes(ctx, ss); err != nil {
			return nil, err
		} else if len(vols) > 0 {
			rank = 1
		}
		all = append(all, entry{ss, rank})
	}
	slices.SortStableFunc(all, func(x, y entry) int {
		return cmp.Or(cmp.Compare(x.ss.env.ID, y.ss.env.ID), cmp.Compare(x.rank, y.rank), cmp.Compare(x.ss.svc.ID, y.ss.svc.ID))
	})
	out := make([]svcScope, len(all))
	for i, e := range all {
		out[i] = e.ss
	}
	return out, nil
}

// recoverService brings one service back: its data from planned's backup
// in repository if it has one (planned is nil for a service without
// volumes), then a release copying its active one. It waits for the
// release, so the next service starts against a running database.
func (c *Core) recoverService(ctx context.Context, a Actor, ss svcScope, org string, planned *apitypes.RecoveryService, repository string) apitypes.RecoveryResult {
	// What it does to a service is audited in the service's organization.
	a = a.InOrg(ss.project.OrgID)
	res := apitypes.RecoveryResult{Org: org, Project: ss.project.Name, Environment: ss.env.Name, Service: ss.svc.Name}
	fail := func(format string, args ...any) apitypes.RecoveryResult {
		res.Outcome, res.Detail = apitypes.RecoveryFailed, fmt.Sprintf(format, args...)
		return res
	}
	switch {
	case ss.svc.StoppedAt != nil:
		res.Outcome, res.Detail = apitypes.RecoverySkipped, "stopped on purpose before the server was lost; `dock start` starts it, on empty volumes"
		return res
	case ss.env.AsleepAt != nil:
		res.Outcome, res.Detail = apitypes.RecoverySkipped, "its environment is asleep; it starts on empty volumes when it wakes"
		return res
	}
	active, err := c.store.GetActiveRelease(ctx, ss.svc.ID)
	if err != nil {
		return fail("read its active release: %v", err)
	}
	rt, err := c.runtimeFor(ss.env.ServerID)
	if err != nil {
		return fail("%v", err)
	}
	if active.ContainerID != "" {
		ctrs, err := rt.List(ctx)
		if err != nil {
			return fail("list this server's containers: %v", c.nodeErr(ss.env.ServerID, err))
		}
		if slices.ContainsFunc(ctrs, func(ct node.Container) bool { return ct.ID == active.ContainerID }) {
			res.Outcome, res.Detail = apitypes.RecoveryRunning, "it has a container already and was left alone"
			return res
		}
	}
	restores := planned != nil && planned.State == apitypes.RecoveryRestore
	t, isTemplate := templateOf(ss.svc)
	// What a stateful service without a backup comes back as.
	res.Outcome = apitypes.RecoveryStarted
	if planned != nil {
		res.Outcome = apitypes.RecoveryEmpty
		res.Detail = "never backed up: it starts on empty volumes"
		if planned.State == apitypes.RecoveryLost {
			res.Detail = "its backups were not in this repository (a server's local one went with the server): it starts on empty volumes"
		}
	}
	var r *restore
	if restores {
		res.Snapshot, res.BackedUpAt = planned.Snapshot, planned.BackedUpAt
		if !c.claimService(ss.svc.ID) {
			return fail("a backup or restore of it is already running; run the recovery again once it finished")
		}
		row, err := c.store.CreateBackup(ctx, store.CreateBackupParams{
			ServiceID: ss.svc.ID, Operation: "restore", Kind: string(planned.Kind), Trigger: "manual",
			Repository: repository, SnapshotID: planned.Snapshot, RestoredTo: &ss.svc.ID, CreatedBy: a.Name, StartedAt: now(),
		})
		if err != nil {
			c.releaseService(ss.svc.ID)
			return fail("record the restore: %v", err)
		}
		r = &restore{c: c, from: ss, to: ss, t: t, snapshot: planned.Snapshot, row: row}
		// Volumes that are filled before the service starts: a plain
		// service's, and Redis's.
		if !isTemplate || t.backup == apitypes.BackupRedis {
			if err := c.lockForRestore(ctx, ss, volumeRestore{what: "the recovery", watch: "`sudo dock admin recover --report`"}); err != nil {
				c.finishRecoveryRestore(ctx, r, err)
				return fail("%v", err)
			}
			if isTemplate {
				err = r.fill(ctx)
			} else {
				r.log, err = recoverVolumes(c, ctx, ss, planned.Snapshot, row.ID)
			}
			c.unlockVolumes(ss.svc.ID)
			if err != nil {
				c.finishRecoveryRestore(ctx, r, err)
				return fail("restoring backup %s failed, so it was not started: %v", short(planned.Snapshot), err)
			}
		}
	}
	if isTemplate {
		// A database with point-in-time restore comes back as a new
		// history (a dump into a new cluster): its WAL goes to a new
		// generation, never next to the lost one's files of the same names.
		c.pitrNewGeneration(ctx, ss)
	}
	done := make(chan bool, 1)
	rel, err := c.startRelease(ctx, a, ss, releaseSpec{source: apitypes.SourceRestart, from: &active, done: func(ok bool) { done <- ok }})
	if err != nil {
		if r != nil {
			c.finishRecoveryRestore(ctx, r, err)
		}
		return fail("it couldn't be started: %v", err)
	}
	c.releaseEvent(ctx, ss, rel, fmt.Sprintf("Recovering %s after the server was lost: release %d copies release %d", ss.svc.Name, rel.Number, active.Number))
	var ok bool
	select {
	case ok = <-done:
	case <-ctx.Done():
		return fail("dock stopped during the recovery; run it again")
	}
	if !ok {
		if queued := c.rebuildLost(ctx, ss, active, rel); queued != "" {
			// Its volumes, if it has any, were filled before the start.
			if r != nil {
				c.finishRecoveryRestore(ctx, r, nil)
			}
			res.Outcome, res.Detail = apitypes.RecoveryRebuilding, queued
			return res
		}
		err := fmt.Errorf("release %d failed to start; `dock releases` shows why (an image that went with the server's registry needs a new deploy)", rel.Number)
		if r != nil {
			c.finishRecoveryRestore(ctx, r, err)
		}
		return fail("%v", err)
	}
	if r == nil {
		return res
	}
	// A database that loads a dump does it now, running and alone.
	if isTemplate && t.backup != apitypes.BackupRedis {
		if t.backup == apitypes.BackupMySQL {
			err = r.mysqlRestore(ctx)
		} else {
			err = r.pgRestore(ctx)
		}
		if err != nil {
			c.finishRecoveryRestore(ctx, r, err)
			return fail("it runs, but loading backup %s failed: %v. It is empty; `dock backup restore` loads a backup into a new service", short(planned.Snapshot), err)
		}
	}
	c.finishRecoveryRestore(ctx, r, nil)
	res.Outcome, res.Detail = apitypes.RecoveryRestored, ""
	return res
}

// rebuildLost queues a build of active's commit from ss's GitHub source,
// after release rel, its copy, failed to start: on a server that replaced
// a lost one that is most often an image that was only in the lost
// server's registry. It returns what the report says, or "" when the
// service has no GitHub source or the commit couldn't be queued.
func (c *Core) rebuildLost(ctx context.Context, ss svcScope, active, rel store.Release) string {
	conn, err := c.store.GetGitConnectionByService(ctx, ss.svc.ID)
	if err != nil {
		return ""
	}
	ref := ServiceRef{ss.project.Name, ss.env.Name, ss.svc.Name}
	got, err := c.GitDeploy(ctx, System.InOrg(ss.project.OrgID), ref, apitypes.GitDeployRequest{Ref: active.CommitSha})
	if err != nil {
		c.log.Warn("recovery: queue a rebuild", "service", ss.String(), "err", err)
		return ""
	}
	return fmt.Sprintf("release %d failed to start (an image that was only in the lost server's registry is gone), so commit %s of %s is being built again; `dock releases` shows it",
		rel.Number, shortSHA(got.Commit.SHA), conn.Repo)
}

// finishRecoveryRestore records the outcome of a recovery's restore of
// one service, with its job's log, and ends its claim.
func (c *Core) finishRecoveryRestore(ctx context.Context, r *restore, err error) {
	c.releaseService(r.from.svc.ID)
	c.panel.Forget(r.from.svc.ID)
	if ctx.Err() != nil {
		return // shutting down: the next start marks it failed
	}
	c.keepBackupLog(ctx, r.from.svc.ID, r.row.ID, r.log)
	fin := store.FinishBackupParams{Status: string(apitypes.BackupSucceeded), SnapshotID: r.snapshot, FinishedAt: ptr(now()), ID: r.row.ID}
	if err != nil {
		fin.Status, fin.Error = string(apitypes.BackupFailed), err.Error()
		c.audit(ctx, System, "backup.restore_failed", &r.from.project, &r.from.env, r.from.String(), fmt.Sprintf("recovery: restoring backup %s failed: %v", short(r.snapshot), err))
	} else {
		c.audit(ctx, System, "backup.restored", &r.from.project, &r.from.env, r.from.String(), fmt.Sprintf("recovery: restored backup %s into %s", short(r.snapshot), r.from.svc.Name))
	}
	if ferr := c.store.FinishBackup(ctx, fin); ferr != nil {
		c.log.Error("record restore", "restore", r.row.ID, "err", ferr)
	}
}

// short is a snapshot ID as lists show it.
func short(snapshot string) string { return snapshot[:min(8, len(snapshot))] }
