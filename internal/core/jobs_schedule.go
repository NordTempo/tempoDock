package core

import (
	"context"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cron"
	"github.com/NordTempo/tempoDock/internal/store"
)

// jobRetry is when the job scheduler tries again after its own query
// failed (the database was busy or locked).
const jobRetry = time.Minute

// maxCatchUp bounds the walk to a job's latest missed due time after a
// long downtime (a job every minute, down for a year, is 525,600 steps).
const maxCatchUp = 1 << 20

// kickJobs wakes the job scheduler for a new pass, starting it if it
// isn't running: a job changed, a run of one ended, a server reconnected
// or a release went active, and once at start-up. A kick after Close
// does nothing.
func (c *Core) kickJobs() {
	c.jobs.mu.Lock()
	defer c.jobs.mu.Unlock()
	switch {
	case c.jobs.scheduling:
		select {
		case c.jobs.kick <- struct{}{}:
		default:
		}
	case c.ctx.Err() == nil:
		c.jobs.scheduling = true
		c.wg.Add(1)
		go c.scheduleJobs()
	}
}

// scheduleJobs is the job scheduler (ADR 0052): one goroutine and one
// timer for every job. Each pass starts the jobs that are due and sleeps
// until the next one is due, or until kicked (kickJobs). With nothing to
// wait for (no enabled job, or only ones waiting for an offline server)
// it exits, so an install without jobs has no goroutine and no timer; the
// next kick starts it again. Jobs on offline servers wait for their
// server's reconnect kick, then run once.
func (c *Core) scheduleJobs() {
	defer c.wg.Done()
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		next, err := c.jobPass(c.ctx)
		if c.ctx.Err() != nil {
			return
		}
		if err != nil {
			c.log.Error("job scheduler", "err", err)
			next = now().Add(jobRetry)
		}
		if next.IsZero() {
			if c.stopScheduling() {
				return
			}
			// A kick came in during the pass (a job set or enabled) and
			// stopScheduling took it: pass again now, don't wait for it.
			continue
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
		case <-c.jobs.kick:
			timer.Stop()
		case <-wake:
		}
	}
}

// stopScheduling marks the scheduler stopped unless a kick came in since
// its pass; then it takes the kick and the caller passes again. Kicks and
// this check are under one lock, so none is lost.
func (c *Core) stopScheduling() bool {
	c.jobs.mu.Lock()
	defer c.jobs.mu.Unlock()
	select {
	case <-c.jobs.kick:
		return false
	default:
		c.jobs.scheduling = false
		return true
	}
}

// jobPass starts every job that is due and returns when the next one is
// due (zero: none). One query, however many jobs; each due job is
// recorded as handled for its due time before it starts, so a crash
// never runs it twice for the same time.
func (c *Core) jobPass(ctx context.Context) (time.Time, error) {
	rows, err := c.store.ListSchedulableJobs(ctx)
	if err != nil {
		return time.Time{}, err
	}
	n := now()
	var next time.Time
	soonest := func(t time.Time) {
		if !t.IsZero() && (next.IsZero() || t.Before(next)) {
			next = t
		}
	}
	for _, j := range rows {
		sched, loc, err := parseJobSchedule(j.Schedule, j.Timezone)
		if err != nil {
			// Checked when it was set; a timezone the host lost since.
			c.log.Warn("job with a bad schedule", "job", j.ID, "err", err)
			continue
		}
		due := sched.Next(jobBase(j.UpdatedAt, j.LastDueAt).In(loc))
		if due.IsZero() {
			continue
		}
		if due.After(n) {
			soonest(due)
			continue
		}
		if !c.online(j.ServerID) {
			continue // its server's reconnect kicks a pass
		}
		slot := latestDue(sched, due, n)
		if err := c.store.SetServiceJobDue(ctx, store.SetServiceJobDueParams{LastDueAt: &slot, ID: j.ID}); err != nil {
			return next, err
		}
		// One that couldn't start is recorded as skipped, with why.
		if _, err := c.startJob(ctx, j.ID, j.ServiceID, apitypes.TriggerSchedule, System); err != nil {
			c.log.Info("scheduled job skipped", "job", j.ID, "err", err)
		}
		soonest(sched.Next(slot))
	}
	return next, nil
}

// jobBase is the time a job's due times count from: when it last ran
// (or skipped) for one, or when its schedule was set, the later.
func jobBase(updated time.Time, lastDue *time.Time) time.Time {
	if lastDue != nil && lastDue.After(updated) {
		return *lastDue
	}
	return updated
}

// latestDue walks from a due time to the latest one not after t: missed
// times collapse into one run.
func latestDue(s cron.Schedule, due, t time.Time) time.Time {
	for range maxCatchUp {
		n := s.Next(due)
		if n.IsZero() || n.After(t) {
			break
		}
		due = n
	}
	return due
}

// parseJobSchedule parses a job's expression and timezone.
func parseJobSchedule(expr, tz string) (cron.Schedule, *time.Location, error) {
	s, err := cron.Parse(expr)
	if err != nil {
		return s, nil, err
	}
	loc, err := jobLocation(tz)
	return s, loc, err
}

// locations caches the timezones jobs use: loading one reads and parses
// its tzdata, on every pass otherwise. Bounded by the IANA names (about
// 600), which are all a lookup can store.
var locations sync.Map

// jobLocation loads a timezone, once.
func jobLocation(name string) (*time.Location, error) {
	if loc, ok := locations.Load(name); ok {
		return loc.(*time.Location), nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, err
	}
	locations.Store(name, loc)
	return loc, nil
}
