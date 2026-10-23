package core

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/store"
)

// backupRetry is when the scheduler tries again after its own query
// failed (the database was busy or locked).
const backupRetry = 10 * time.Minute

// scheduleOf is a service's effective schedule and its next backup.
func (c *Core) scheduleOf(ctx context.Context, ss svcScope, set store.BackupSetting) (apitypes.BackupSchedule, error) {
	out := apitypes.BackupSchedule{Enabled: true, Hour: int(set.Hour), DefaultHour: true}
	sch, err := c.store.GetBackupSchedule(ctx, ss.svc.ID)
	switch {
	case err == nil:
		out.Enabled = sch.Enabled
		if sch.Hour != nil {
			out.Hour, out.DefaultHour = int(*sch.Hour), false
		}
	case !store.IsNotFound(err):
		return out, err
	}
	if !out.Enabled || ss.svc.ArchivedAt != nil {
		return out, nil
	}
	since := ss.svc.CreatedAt
	rows, err := c.store.ListServiceBackups(ctx, store.ListServiceBackupsParams{ServiceID: ss.svc.ID, Limit: serviceBackupRuns})
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		if r.Operation == "backup" && r.Trigger == "schedule" {
			since = later(since, r.StartedAt)
			break
		}
	}
	n := now()
	next := later(backup.NextDue(out.Hour, since, n, time.Local), n)
	out.NextAt = &next
	return out, nil
}

// UpdateBackupSchedule turns a service's scheduled backups on or off or
// moves them to another hour.
func (c *Core) UpdateBackupSchedule(ctx context.Context, a Actor, ref ServiceRef, req apitypes.UpdateBackupScheduleRequest) (apitypes.ServiceBackups, error) {
	// Backups are the platform's (ADR 0027): operators set schedules.
	if err := a.requireOperator("changing a backup schedule"); err != nil {
		return apitypes.ServiceBackups{}, err
	}
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "changing a backup schedule")
	if err != nil {
		return apitypes.ServiceBackups{}, err
	}
	if _, _, err := c.backupKindOf(ctx, ss); err != nil {
		return apitypes.ServiceBackups{}, err
	}
	params := store.UpsertBackupScheduleParams{ServiceID: ss.svc.ID, Enabled: true}
	if cur, err := c.store.GetBackupSchedule(ctx, ss.svc.ID); err == nil {
		params.Enabled, params.Hour = cur.Enabled, cur.Hour
	} else if !store.IsNotFound(err) {
		return apitypes.ServiceBackups{}, err
	}
	if req.Enabled != nil {
		params.Enabled = *req.Enabled
	}
	switch {
	case req.DefaultHour && req.Hour != nil:
		return apitypes.ServiceBackups{}, invalid("set hour or default_hour, not both")
	case req.DefaultHour:
		params.Hour = nil
	case req.Hour != nil:
		if *req.Hour < 0 || *req.Hour > 23 {
			return apitypes.ServiceBackups{}, invalid("hour must be between 0 and 23")
		}
		params.Hour = ptr(int64(*req.Hour))
	}
	if err := c.store.UpsertBackupSchedule(ctx, params); err != nil {
		return apitypes.ServiceBackups{}, err
	}
	summary := "turned scheduled backups off"
	if params.Enabled {
		summary = "scheduled daily backups at the install's hour"
		if params.Hour != nil {
			summary = fmt.Sprintf("scheduled daily backups at %02d:00", *params.Hour)
		}
	}
	c.audit(ctx, a, "backup.schedule", &ss.project, &ss.env, ss.String(), summary)
	c.kickBackups()
	return c.serviceBackups(ctx, ss)
}

// scheduleBackups is the backup scheduler (ADR 0019): one goroutine and
// one timer. Each pass runs the backups that are due, one at a time, then
// sleeps until the next one is due, or until kicked (kickBackups: a
// schedule or the settings changed, a release went active, a server
// reconnected, a backup finished). The control plane's own daily backup
// (ADR 0021) keeps the timer armed once a day even with no stateful
// services. Services on offline servers are skipped; their server
// reconnecting kicks a pass that runs what is overdue.
func (c *Core) scheduleBackups() {
	defer c.wg.Done()
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		next, err := c.backupPass(c.ctx)
		if c.ctx.Err() != nil {
			return
		}
		if err != nil {
			c.log.Error("backup scheduler", "err", err)
			next = now().Add(backupRetry)
		}
		var wake <-chan time.Time
		if !next.IsZero() {
			timer.Reset(time.Until(next))
			wake = timer.C
		}
		select {
		case <-c.ctx.Done():
			timer.Stop()
			return
		case <-c.backups.kick:
			timer.Stop()
		case <-wake:
		}
	}
}

// backupPass runs every scheduled backup that is due, the services' and
// then the control plane's own (ADR 0021), and returns when the next one
// is due (zero: none scheduled).
func (c *Core) backupPass(ctx context.Context) (time.Time, error) {
	next, err := c.serviceBackupPass(ctx)
	if err != nil {
		return next, err
	}
	cp, err := c.controlPlaneBackupPass(ctx)
	if next.IsZero() || (!cp.IsZero() && cp.Before(next)) {
		next = cp
	}
	return next, err
}

// serviceBackupPass runs every scheduled service backup that is due
// (sequentially, so a small server never runs two dumps at once) and
// returns when the next one is due (zero: none scheduled). A backup that
// couldn't even start is tried once per pass, so the pass always ends.
func (c *Core) serviceBackupPass(ctx context.Context) (time.Time, error) {
	tried := map[int64]bool{}
	for ctx.Err() == nil {
		due, next, err := c.dueBackups(ctx)
		due = slices.DeleteFunc(due, func(id int64) bool { return tried[id] })
		if err != nil || len(due) == 0 {
			return next, err
		}
		for _, id := range due {
			if ctx.Err() != nil {
				break
			}
			tried[id] = true
			c.scheduledBackup(ctx, id)
		}
	}
	return time.Time{}, nil
}

// dueBackups lists the services whose scheduled backup is due now and
// can run (server online, nothing running for them), and the earliest
// later due time. Three queries, however many services.
func (c *Core) dueBackups(ctx context.Context) (due []int64, next time.Time, err error) {
	cands, err := c.store.ListBackupCandidates(ctx)
	if err != nil || len(cands) == 0 {
		return nil, time.Time{}, err
	}
	set, err := c.backupSettings(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	latest, err := c.store.ListLatestScheduledBackups(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	last := make(map[int64]time.Time, len(latest))
	for _, b := range latest {
		last[b.ServiceID] = b.StartedAt
	}
	n := now()
	for _, cand := range cands {
		if !cand.Enabled {
			continue
		}
		hour := int(set.Hour)
		if cand.Hour != nil {
			hour = int(*cand.Hour)
		}
		at := backup.NextDue(hour, later(cand.CreatedAt, last[cand.ID]), n, time.Local)
		if at.After(n) {
			if next.IsZero() || at.Before(next) {
				next = at
			}
			continue
		}
		// Due now. Offline servers wait for their reconnect kick, running
		// ones for the kick their backup's end sends.
		if c.online(cand.ServerID) && !c.serviceBusy(cand.ID) {
			due = append(due, cand.ID)
		}
	}
	return due, next, nil
}

// later returns the later of two times.
func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// serviceBusy reports whether a backup or restore of a service is running.
func (c *Core) serviceBusy(serviceID int64) bool {
	c.backups.mu.Lock()
	defer c.backups.mu.Unlock()
	return c.backups.running[serviceID]
}

// scheduledBackup records and runs one scheduled backup. A failure to
// even start it (the service went away) is recorded as a failed attempt,
// so the scheduler doesn't retry it until tomorrow.
func (c *Core) scheduledBackup(ctx context.Context, serviceID int64) {
	ss, err := c.scopeOf(ctx, serviceID)
	if err != nil {
		c.log.Warn("scheduled backup", "service", serviceID, "err", err)
		return
	}
	row, err := c.startBackup(ctx, System, ss, "schedule")
	if err != nil {
		c.log.Warn("scheduled backup", "service", ss.String(), "err", err)
		return
	}
	c.runBackup(ctx, ss, row)
}
