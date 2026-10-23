package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/cpbackup"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// The control plane's own backup (ADR 0021): daily at the install's hour,
// into the backup repository of ADR 0019, with its retention. The daemon
// writes the archive (internal/cpbackup: a consistent SQLite snapshot, the
// keys, build logs and config) straight into the stdin of a restic job on
// its own server, so nothing but the SQLite snapshot touches the disk.
// The scheduler in backups_schedule.go runs it next to service backups.

// controlPlaneAlertKey is the alert of a failed control plane backup.
const controlPlaneAlertKey = "backup:control-plane"

// errJobEnded closes the archive pipe once the backup job has returned.
var errJobEnded = errors.New("the backup job ended before the archive was written")

// RunControlPlaneBackup backs up the control plane now, in the
// background; the answer is the running backup. Operators only:
// the archive holds every secret.
func (c *Core) RunControlPlaneBackup(ctx context.Context, a Actor) (apitypes.ControlPlaneBackupRun, error) {
	if err := a.requireOperator("backing up the control plane"); err != nil {
		return apitypes.ControlPlaneBackupRun{}, err
	}
	row, err := c.startControlPlaneBackup(ctx, a, "manual")
	if err != nil {
		return apitypes.ControlPlaneBackupRun{}, err
	}
	c.audit(ctx, a.platform(), "backup.run", nil, nil, "control plane", "started a control plane backup")
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.runControlPlaneBackup(c.ctx, row)
	}()
	return toControlPlaneRun(row), nil
}

// startControlPlaneBackup claims the control plane backup and records it
// running. The caller must call runControlPlaneBackup with the row.
func (c *Core) startControlPlaneBackup(ctx context.Context, a Actor, trigger string) (store.ControlPlaneBackup, error) {
	set, err := c.backupSettings(ctx)
	if err != nil {
		return store.ControlPlaneBackup{}, err
	}
	c.backups.mu.Lock()
	busy := c.backups.controlPlane
	c.backups.controlPlane = true
	c.backups.mu.Unlock()
	if busy {
		return store.ControlPlaneBackup{}, conflict("a control plane backup is already running; wait for it to finish")
	}
	t := now()
	params := store.StartControlPlaneBackupParams{Trigger: trigger, Repository: set.Repository, CreatedBy: a.Name, StartedAt: t}
	if trigger == "schedule" {
		params.ScheduledAt = &t
	}
	row, err := c.store.StartControlPlaneBackup(ctx, params)
	if err != nil {
		c.releaseControlPlane()
		return store.ControlPlaneBackup{}, err
	}
	return row, nil
}

func (c *Core) releaseControlPlane() {
	c.backups.mu.Lock()
	c.backups.controlPlane = false
	c.backups.mu.Unlock()
}

// controlPlaneBusy reports whether a control plane backup is running.
func (c *Core) controlPlaneBusy() bool {
	c.backups.mu.Lock()
	defer c.backups.mu.Unlock()
	return c.backups.controlPlane
}

// runControlPlaneBackup runs a recorded control plane backup to the end
// and records the outcome. A failure is audited and raises an alert; the
// next success resolves it.
func (c *Core) runControlPlaneBackup(ctx context.Context, row store.ControlPlaneBackup) {
	defer c.kickBackups() // a scheduled run may have waited for this one
	res, err := c.controlPlaneJob(ctx, row)
	c.releaseControlPlane()
	if ctx.Err() != nil {
		return // shutting down: the next start marks it failed
	}
	fin := store.FinishControlPlaneBackupParams{Status: string(apitypes.BackupSucceeded), FinishedAt: ptr(now())}
	if err != nil {
		fin.Status, fin.Error = string(apitypes.BackupFailed), err.Error()
		c.audit(ctx, System, "backup.failed", nil, nil, "control plane", fmt.Sprintf("control plane backup failed: %v", err))
		c.signalAlert(ctx, alertSignal{
			Key: controlPlaneAlertKey, Kind: apitypes.AlertBackupFailed, Severity: apitypes.SeverityWarning,
			Title:   "Control plane backup failed",
			Message: fmt.Sprintf("The control plane's backup failed: %s. Without one, losing this server loses the platform's state; `dock admin backup` makes one by hand.", err),
		})
	} else {
		c.signalAlert(ctx, alertSignal{Key: controlPlaneAlertKey, Resolve: true})
		fin.SnapshotID, fin.SizeBytes = res.SnapshotID, res.Bytes
		short := res.SnapshotID[:min(8, len(res.SnapshotID))]
		c.audit(ctx, System, "backup.succeeded", nil, nil, "control plane",
			fmt.Sprintf("control plane backup: snapshot %s, %s, to %s", short, humanBytes(res.Bytes), repoLabel(row.Repository)))
	}
	if err := c.store.FinishControlPlaneBackup(ctx, fin); err != nil {
		c.log.Error("record control plane backup", "err", err)
	}
}

// controlPlaneJob streams the control plane's archive into a restic job
// on the local server. If writing the archive fails, the job is stopped
// before restic sees the end of its input, so a cut-short archive never
// becomes a snapshot.
func (c *Core) controlPlaneJob(ctx context.Context, row store.ControlPlaneBackup) (backup.Result, error) {
	set, err := c.backupSettings(ctx)
	if err != nil {
		return backup.Result{}, err
	}
	if set.Repository != row.Repository {
		return backup.Result{}, errors.New("the backup repository changed after this backup started; run it again")
	}
	repo, err := c.repoOf(ctx, set)
	if err != nil {
		return backup.Result{}, err
	}
	pr, pw := io.Pipe()
	written := make(chan error, 1)
	go func() {
		_, err := cpbackup.Write(ctx, pw, cpbackup.Source{
			DataDir: c.cfg.DataDir, ConfigPath: c.configPath,
			Snapshot: func(ctx context.Context, dst string) (store.SnapshotInfo, error) {
				return store.Snapshot(ctx, c.store.DB, dst)
			},
		})
		_ = pw.CloseWithError(err) // nil: the job's input ends here
		written <- err
	}()
	spec := node.JobSpec{
		Name: fmt.Sprintf("dock_control_plane_backup_%d", now().UnixNano()), Image: c.cfg.Backup.ResticImage,
		Labels: node.Labels{ControlPlane: c.install},
		Env:    backup.ControlPlaneEnv(repo, backup.Retention{Daily: int(set.KeepDaily), Weekly: int(set.KeepWeekly), Monthly: int(set.KeepMonthly)}),
		Script: backup.ControlPlaneScript(), Stdin: pr,
	}
	out, jobErr := c.runResticJob(ctx, c.local.ID, c.rt, repo, spec)
	_ = pr.CloseWithError(errJobEnded) // the writer stops if the job didn't read it all
	if werr := <-written; werr != nil && !errors.Is(werr, errJobEnded) {
		return backup.Result{}, fmt.Errorf("write the control plane archive: %w", werr)
	}
	if jobErr != nil {
		return backup.Result{}, jobErr
	}
	res, err := backup.ParseBackup(out)
	if err != nil {
		return res, fmt.Errorf("%w%s", err, withOutput(out))
	}
	return res, nil
}

// controlPlaneBackupDue is when the control plane's scheduled backup is
// due: daily at the install's hour, counted from the latest scheduled
// attempt (or the local server's creation). At or before now means now.
func (c *Core) controlPlaneBackupDue(ctx context.Context) (time.Time, error) {
	set, err := c.backupSettings(ctx)
	if err != nil {
		return time.Time{}, err
	}
	since := c.local.CreatedAt
	row, err := c.store.GetControlPlaneBackup(ctx)
	switch {
	case err == nil && row.ScheduledAt != nil:
		since = later(since, *row.ScheduledAt)
	case err != nil && !store.IsNotFound(err):
		return time.Time{}, err
	}
	return backup.NextDue(int(set.Hour), since, now(), time.Local), nil
}

// controlPlaneBackupPass runs the control plane's scheduled backup if it
// is due, and returns when it is next due. While a manual run is going it
// returns zero: that run's end kicks the scheduler.
func (c *Core) controlPlaneBackupPass(ctx context.Context) (time.Time, error) {
	at, err := c.controlPlaneBackupDue(ctx)
	if err != nil || at.After(now()) {
		return at, err
	}
	if c.controlPlaneBusy() {
		return time.Time{}, nil
	}
	row, err := c.startControlPlaneBackup(ctx, System, "schedule")
	if err != nil {
		return time.Time{}, err
	}
	c.runControlPlaneBackup(ctx, row)
	return c.controlPlaneBackupDue(ctx)
}

// controlPlaneBackups is the control plane's backup as backup settings
// show it.
func (c *Core) controlPlaneBackups(ctx context.Context) (apitypes.ControlPlaneBackup, error) {
	var out apitypes.ControlPlaneBackup
	row, err := c.store.GetControlPlaneBackup(ctx)
	switch {
	case err == nil:
		run := toControlPlaneRun(row)
		out.Latest = &run
	case !store.IsNotFound(err):
		return out, err
	}
	at, err := c.controlPlaneBackupDue(ctx)
	if err != nil {
		return out, err
	}
	at = later(at, now())
	out.NextAt = &at
	return out, nil
}

// AfterRestore prepares a database just restored on a new server (`dock
// admin restore`, ADR 0021), before its first start. The control plane
// backup that was running when the archive was taken never finished
// here, so it is dropped. With oldServerBecomesNode, the old server stops
// being the local one: it keeps its environments and waits to join again
// as a node under its name (`dock server add <name>`), and the next start
// registers this server as the local one. It returns that old server's
// name ("" otherwise). from describes the archive for the audit log.
func AfterRestore(ctx context.Context, st *store.Store, from string, oldServerBecomesNode bool) (string, error) {
	var old string
	err := st.Tx(ctx, func(q *store.Queries) error {
		if err := q.ForgetRunningControlPlaneBackup(ctx); err != nil {
			return err
		}
		summary := "restored the control plane from " + from + "; this server took the old one's place"
		if oldServerBecomesNode {
			srv, err := q.DetachLocalServer(ctx, ptr(now()))
			if err != nil {
				return fmt.Errorf("detach the old server: %w", err)
			}
			old = srv.Name
			summary = "restored the control plane from " + from + "; server " + old + " waits to join as a node"
		}
		_, err := q.AddAuditEvent(ctx, store.AddAuditEventParams{
			Actor: System.Name, ActorKind: System.Kind, Action: "control_plane.restore", Target: "control plane",
			Summary: summary, CreatedAt: now(),
		})
		return err
	})
	return old, err
}

func toControlPlaneRun(r store.ControlPlaneBackup) apitypes.ControlPlaneBackupRun {
	return apitypes.ControlPlaneBackupRun{
		Trigger: r.Trigger, Status: apitypes.BackupStatus(r.Status), Snapshot: r.SnapshotID, SizeBytes: r.SizeBytes,
		Repository: repoLabel(r.Repository), OffBox: r.Repository != "", Error: r.Error, CreatedBy: r.CreatedBy,
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
	}
}
