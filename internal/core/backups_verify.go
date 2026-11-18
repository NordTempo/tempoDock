package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Checked backups (ADR 0096): the weekly repository checks. The restore
// drills are in backups_drill.go.

// verifyEvery is how long after a check or drill that ran the next one
// waits: six and a half days, so the weekly one falls on the same
// backup hour whenever the last one started within it.
const verifyEvery = 7*24*time.Hour - 12*time.Hour

// verifyDue is when a target's next check or drill is due, given its
// latest attempt (nil: never): the first backup hour (in loc) a week
// after one that ran, a day after one that failed, and after the daemon
// started for a target never checked, so an upgrade doesn't start
// reading data at once. At or before now means due now; zero that one is
// running.
func verifyDue(last *store.BackupCheck, hour int, started time.Time, loc *time.Location) time.Time {
	since := started
	if last != nil {
		switch apitypes.BackupStatus(last.Status) {
		case apitypes.BackupRunning:
			return time.Time{}
		case apitypes.BackupFailed:
			since = last.StartedAt
		default:
			since = last.StartedAt.Add(verifyEvery)
		}
	}
	s := since.In(loc)
	at := time.Date(s.Year(), s.Month(), s.Day(), hour, 0, 0, 0, loc)
	if !at.After(s) {
		at = at.AddDate(0, 0, 1)
	}
	return at
}

// toBackupCheck is a check or drill row as the API shows it; server and
// service are the names of what it ran on.
func toBackupCheck(r store.BackupCheck, server, service string) apitypes.BackupCheck {
	return apitypes.BackupCheck{
		ID: r.ID, Kind: r.Kind, Server: server, Service: service, Repository: repoLabel(r.Repository),
		Snapshot: r.SnapshotID, Trigger: r.Trigger, Status: apitypes.BackupStatus(r.Status),
		Error: r.Error, Detail: r.Detail, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
	}
}

// checkTarget is one repository the weekly check reads: the bucket's, on
// the control plane's server, or a server's local one.
type checkTarget struct {
	serverID int64
	// repository is as backups record it: "" for a local one.
	repository string
}

// checkAlertKey is the alert of a repository's failed check.
func checkAlertKey(t checkTarget) string {
	return fmt.Sprintf("backup-check:%d:%s", t.serverID, repoLabel(t.repository))
}

// checkTargets lists the repositories holding backups now: the bucket's
// when backups go off-box, else the local repository of every server
// with backups restores can use, and the control plane's for its own (the
// sources a backup move would copy from).
func (c *Core) checkTargets(ctx context.Context, set store.BackupSetting) ([]checkTarget, error) {
	if set.Repository != "" {
		return []checkTarget{{serverID: c.localID, repository: set.Repository}}, nil
	}
	ids, err := c.store.ListLocalBackupServers(ctx)
	if err != nil {
		return nil, err
	}
	if cp, err := c.store.GetControlPlaneBackup(ctx); err == nil && cp.Repository == "" && cp.SnapshotID != "" {
		ids = append(ids, c.localID)
	} else if err != nil && !store.IsNotFound(err) {
		return nil, err
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	out := make([]checkTarget, len(ids))
	for i, id := range ids {
		out[i] = checkTarget{serverID: id}
	}
	return out, nil
}

// latestChecks is each repository's latest check attempt, by target.
func (c *Core) latestChecks(ctx context.Context) (map[checkTarget]*store.BackupCheck, error) {
	rows, err := c.store.ListLatestRepositoryChecks(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[checkTarget]*store.BackupCheck, len(rows))
	for i := range rows {
		out[checkTarget{serverID: rows[i].ServerID, repository: rows[i].Repository}] = &rows[i]
	}
	return out, nil
}

// verifyPass runs the repository checks RunBackupChecks queued and those
// that are due (verifyDue), one at a time, then the restore drills
// (drillPass), and returns when the next of them is due (zero: none).
// The backup scheduler's pass calls it after its backups: three queries
// when nothing is due. A repository on an offline server waits for the
// pass its reconnect kicks, and nothing is checked while the backups
// move.
func (c *Core) verifyPass(ctx context.Context) (time.Time, error) {
	c.backups.mu.Lock()
	queued := c.backups.checkQueue
	c.backups.checkQueue = nil
	moving := c.backups.moving()
	c.backups.mu.Unlock()
	for _, row := range queued {
		c.runCheck(ctx, checkTarget{serverID: row.ServerID, repository: row.Repository}, row)
	}
	set, err := c.backupSettings(ctx)
	if err != nil || moving || !set.Verify {
		return time.Time{}, err
	}
	targets, err := c.checkTargets(ctx, set)
	if err != nil {
		return time.Time{}, err
	}
	var next time.Time
	if len(targets) > 0 {
		latest, err := c.latestChecks(ctx)
		if err != nil {
			return time.Time{}, err
		}
		for _, t := range targets {
			if ctx.Err() != nil {
				return time.Time{}, nil
			}
			at := verifyDue(latest[t], int(set.Hour), c.backups.started, time.Local)
			if at.IsZero() {
				continue // running: started by hand since this pass began
			}
			if !at.After(now()) && c.online(t.serverID) {
				row, err := c.startCheck(ctx, t, "schedule")
				if err != nil {
					return time.Time{}, err
				}
				at = verifyDue(c.runCheck(ctx, t, row), int(set.Hour), c.backups.started, time.Local)
			}
			if at.After(now()) {
				next = sooner(next, at)
			}
		}
	}
	drills, err := c.drillPass(ctx, set, false)
	return sooner(next, drills), err
}

// startCheck records a repository check as running.
func (c *Core) startCheck(ctx context.Context, t checkTarget, trigger string) (store.BackupCheck, error) {
	return c.store.CreateBackupCheck(ctx, store.CreateBackupCheckParams{
		Kind: "repository", ServerID: t.serverID, Repository: t.repository, Trigger: trigger, StartedAt: now(),
	})
}

// runCheck runs one repository's check (row, from startCheck) on its
// server and records it: the row, an audit event, and an alert on
// failure that the next check of the repository that passes resolves. It
// returns the finished row.
func (c *Core) runCheck(ctx context.Context, t checkTarget, row store.BackupCheck) *store.BackupCheck {
	server := "a server"
	if srv, err := c.store.GetServer(ctx, t.serverID); err == nil {
		server = srv.Name
	}
	where := repoLabel(t.repository)
	if t.repository == "" {
		where = "local on " + server
	}
	err := c.checkJob(ctx, t, row.ID)
	if ctx.Err() != nil {
		return &row // shutting down: the next start marks it failed
	}
	row.Status, row.FinishedAt = string(apitypes.BackupSucceeded), ptr(now())
	if err != nil {
		row.Status, row.Error = string(apitypes.BackupFailed), err.Error()
		c.audit(ctx, System, "backup.check_failed", nil, nil, "backups", fmt.Sprintf("check of the backups (%s) failed: %v", where, err))
		c.signalAlert(ctx, alertSignal{
			Key: checkAlertKey(t), Kind: apitypes.AlertBackupFailed, Severity: apitypes.SeverityWarning, Server: server,
			Title:   "Backups in " + where + " failed their check",
			Message: fmt.Sprintf("The weekly check of the backup repository (%s) failed: %s. Backups there may not restore. Run a backup of what matters now and see the guide's checked backups section for what to repair; the check runs again tomorrow.", where, strings.TrimRight(strings.TrimSpace(err.Error()), ".:; ")),
			Command: "dock backup check",
		})
	} else {
		row.Detail = "structure checked, up to 1 GiB of data read back"
		c.audit(ctx, System, "backup.checked", nil, nil, "backups", fmt.Sprintf("checked the backups (%s): %s", where, row.Detail))
		c.signalAlert(ctx, alertSignal{Key: checkAlertKey(t), Resolve: true})
	}
	if ferr := c.store.FinishBackupCheck(ctx, store.FinishBackupCheckParams{Status: row.Status, Error: row.Error, Detail: row.Detail, FinishedAt: row.FinishedAt, ID: row.ID}); ferr != nil {
		c.log.Error("record backup check", "check", row.ID, "err", ferr)
	}
	if terr := c.store.TrimRepositoryChecks(ctx, store.TrimRepositoryChecksParams{Repository: t.repository, ServerID: t.serverID}); terr != nil {
		c.log.Warn("trim backup checks", "err", terr)
	}
	return &row
}

// checkJob runs backup.VerifyScript against a repository on its server,
// in the restic image with the backup jobs' limits.
func (c *Core) checkJob(ctx context.Context, t checkTarget, id int64) error {
	rt := c.connected(t.serverID)
	if rt == nil {
		return errors.New("the server went offline")
	}
	set, err := c.backupSettings(ctx)
	if err != nil {
		return err
	}
	repo, err := c.repoOf(ctx, set)
	if err != nil {
		return err
	}
	if repoLabel(set.Repository) != repoLabel(t.repository) {
		return errors.New("the backups moved to other storage meanwhile")
	}
	_, err = c.runResticJob(ctx, t.serverID, rt, repo, node.JobSpec{
		Name: fmt.Sprintf("dock_backup_check_%d", id), Image: c.cfg.Backup.ResticImage,
		Labels: node.Labels{ControlPlane: c.install}, Script: backup.VerifyScript, Env: repo.Env(),
	})
	return err
}

// withBackupChecks adds each repository's latest check (operators) and
// when the next one is due to the settings: three queries.
func (c *Core) withBackupChecks(ctx context.Context, a Actor, out *apitypes.BackupSettings, s store.BackupSetting) error {
	if !a.Operator {
		return nil
	}
	targets, err := c.checkTargets(ctx, s)
	if err != nil || len(targets) == 0 {
		return err
	}
	latest, err := c.latestChecks(ctx)
	if err != nil {
		return err
	}
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		return err
	}
	names := make(map[int64]string, len(servers))
	for _, srv := range servers {
		names[srv.ID] = srv.Name
	}
	var next time.Time
	for _, t := range targets {
		if last := latest[t]; last != nil {
			out.Checks = append(out.Checks, toBackupCheck(*last, names[t.serverID], ""))
		}
		next = sooner(next, verifyDue(latest[t], int(s.Hour), c.backups.started, time.Local))
	}
	if s.Verify && !next.IsZero() {
		next = later(next, now()).In(time.Local)
		out.NextCheckAt = &next
	}
	return nil
}

// withServiceChecks adds to a service's backups the latest check of the
// repository they go to, its latest restore drill (template databases)
// and when the next check is due: two queries.
func (c *Core) withServiceChecks(ctx context.Context, ss svcScope, set store.BackupSetting, out *apitypes.ServiceBackups) error {
	t := checkTarget{serverID: ss.env.ServerID}
	if set.Repository != "" {
		t = checkTarget{serverID: c.localID, repository: set.Repository}
	}
	latest, err := c.latestChecks(ctx)
	if err != nil {
		return err
	}
	if last := latest[t]; last != nil {
		out.Check = ptr(toBackupCheck(*last, "", ""))
	}
	if out.Restorable {
		if out.Drill, err = c.latestDrill(ctx, ss); err != nil {
			return err
		}
	}
	if next := verifyDue(latest[t], int(set.Hour), c.backups.started, time.Local); set.Verify && !next.IsZero() {
		next = later(next, now()).In(time.Local)
		out.NextCheckAt = &next
	}
	return nil
}

// UpdateBackupChecks turns the weekly repository checks and restore
// drills on or off (operators). Turning them off closes the open alerts
// of failed repository checks, which nothing would resolve otherwise.
func (c *Core) UpdateBackupChecks(ctx context.Context, a Actor, req apitypes.UpdateBackupChecksRequest) (apitypes.BackupSettings, error) {
	if err := a.requirePlatform(BackupsManage, "changing the backup checks"); err != nil {
		return apitypes.BackupSettings{}, err
	}
	if req.Enabled == nil {
		return apitypes.BackupSettings{}, invalid("set enabled to true or false")
	}
	// The switch lives on the settings row, which the first use of the
	// repository creates (repoOf).
	set, err := c.backupSettings(ctx)
	if err != nil {
		return apitypes.BackupSettings{}, err
	}
	if _, err := c.repoOf(ctx, set); err != nil {
		return apitypes.BackupSettings{}, err
	}
	if err := c.store.SetBackupVerify(ctx, store.SetBackupVerifyParams{Verify: *req.Enabled, UpdatedAt: now(), UpdatedBy: a.Name}); err != nil {
		return apitypes.BackupSettings{}, err
	}
	summary := "turned the weekly backup checks and restore drills on"
	if !*req.Enabled {
		summary = "turned the weekly backup checks and restore drills off"
		latest, err := c.latestChecks(ctx)
		if err != nil {
			return apitypes.BackupSettings{}, err
		}
		for t := range latest {
			c.signalAlert(ctx, alertSignal{Key: checkAlertKey(t), Resolve: true})
		}
	}
	c.audit(ctx, a.platform(), "backup.configure", nil, nil, "backups", summary)
	c.kickBackups()
	return c.GetBackupSettings(ctx, a, false)
}

// RunBackupChecks starts a check of every repository now (operators):
// each is recorded as running at once and run by the backup scheduler's
// next pass, which this wakes, so checks run one at a time and never
// beside a scheduled backup. The answer is the settings; their Checks
// show each check running, then finished. Repositories on offline
// servers and ones already being checked are left out.
func (c *Core) RunBackupChecks(ctx context.Context, a Actor) (apitypes.BackupSettings, error) {
	if err := a.requirePlatform(BackupsManage, "checking the backups"); err != nil {
		return apitypes.BackupSettings{}, err
	}
	if c.backupsMoving() {
		return apitypes.BackupSettings{}, conflict("the backups are moving to other storage; check them once the move finishes")
	}
	set, err := c.backupSettings(ctx)
	if err != nil {
		return apitypes.BackupSettings{}, err
	}
	targets, err := c.checkTargets(ctx, set)
	if err != nil {
		return apitypes.BackupSettings{}, err
	}
	if len(targets) == 0 {
		return apitypes.BackupSettings{}, invalid("there are no backups to check yet; `dock backup run` makes one")
	}
	latest, err := c.latestChecks(ctx)
	if err != nil {
		return apitypes.BackupSettings{}, err
	}
	var queued []store.BackupCheck
	for _, t := range targets {
		if last := latest[t]; (last != nil && last.Status == string(apitypes.BackupRunning)) || !c.online(t.serverID) {
			continue
		}
		row, err := c.startCheck(ctx, t, "manual")
		if err != nil {
			return apitypes.BackupSettings{}, err
		}
		queued = append(queued, row)
	}
	if len(queued) == 0 {
		return apitypes.BackupSettings{}, conflict("every repository is being checked already or its server is offline; `dock backup status` shows them")
	}
	c.backups.mu.Lock()
	c.backups.checkQueue = append(c.backups.checkQueue, queued...)
	c.backups.mu.Unlock()
	c.audit(ctx, a.platform(), "backup.check", nil, nil, "backups", "started a check of the backups")
	c.kickBackups()
	return c.GetBackupSettings(ctx, a, false)
}
