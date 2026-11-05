package core

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Scheduled jobs (ADR 0054): named cron schedules on a service. A job's
// run is a run like `dock run`'s (runs.go) with the job's command, no
// volumes and the system as its actor; jobs_schedule.go starts them.

// jobName is a job's name: lower case, digits and dashes.
var jobName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// jobState is the scheduler's kick, whether it runs, and the runs in
// flight per job (by ID), for overlap. Entries go when their runs end.
type jobState struct {
	kick       chan struct{}
	mu         sync.Mutex
	scheduling bool
	running    map[int64]int
}

func newJobState() jobState {
	return jobState{kick: make(chan struct{}, 1), running: map[int64]int{}}
}

// holdJob counts a run of a job in flight; the returned func ends it.
// A job with overlap skip that still runs is errStillRunning: the check
// and the count are under one lock, so a run now and a scheduled run
// racing each other can't both start.
func (c *Core) holdJob(job store.ServiceJob) (func(), error) {
	id := job.ID
	c.jobs.mu.Lock()
	if apitypes.JobOverlap(job.Overlap) == apitypes.OverlapSkip && c.jobs.running[id] > 0 {
		c.jobs.mu.Unlock()
		return nil, errStillRunning
	}
	c.jobs.running[id]++
	c.jobs.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			c.jobs.mu.Lock()
			if c.jobs.running[id]--; c.jobs.running[id] <= 0 {
				delete(c.jobs.running, id)
			}
			c.jobs.mu.Unlock()
		})
	}, nil
}

// jobRunning reports whether a run of the job is in flight.
func (c *Core) jobRunning(id int64) bool {
	c.jobs.mu.Lock()
	defer c.jobs.mu.Unlock()
	return c.jobs.running[id] > 0
}

// errStillRunning is a job due (or run now) while it still runs and its
// overlap is skip.
var errStillRunning = errors.New("the run before it was still running")

// startJob starts a run of a job in the background and returns it as it
// started, or why it couldn't start: still running (overlap skip), no active
// release, the service's limit of runs. A scheduled run that can't start
// is recorded as skipped with the reason.
func (c *Core) startJob(ctx context.Context, jobID, serviceID int64, trigger apitypes.RunTrigger, a Actor) (apitypes.CommandRun, error) {
	ss, err := c.scopeOf(ctx, serviceID)
	if err != nil {
		return apitypes.CommandRun{}, err
	}
	job, err := c.store.GetServiceJobByID(ctx, jobID)
	if err != nil {
		return apitypes.CommandRun{}, err
	}
	if trigger == apitypes.TriggerSchedule && !job.Enabled {
		// Disabled after the pass listed it: nothing to run or record.
		return apitypes.CommandRun{}, errors.New("the job was disabled")
	}
	if trigger == apitypes.TriggerSchedule {
		// It came due: open pages move on to its next time.
		_, _ = c.publishJob(ctx, ss, job)
	}
	var r *Run
	switch {
	case trigger == apitypes.TriggerSchedule && ss.svc.StoppedAt != nil:
		// Stopped means "don't run this", its schedule too (ADR 0050);
		// running it now is still asked for explicitly.
		err = conflict("%s is stopped; `dock service start` runs its jobs again", ss.svc.Name)
	default:
		r, err = c.openRun(ctx, a, ss, runSpec{
			command: job.Command, timeout: int(job.TimeoutSeconds), volumes: apitypes.VolumesNone, trigger: trigger, job: &job,
		})
	}
	if err != nil {
		if trigger == apitypes.TriggerSchedule {
			c.skipJob(ctx, ss, job, err)
		}
		return apitypes.CommandRun{}, err
	}
	info := r.Info() // before it runs: the run's end rewrites it
	c.wg.Go(func() { r.Wait(c.ctx, nil) })
	return info, nil
}

// skipJob records a scheduled run that didn't start, with why.
func (c *Core) skipJob(ctx context.Context, ss svcScope, job store.ServiceJob, why error) {
	at := now()
	var relID *int64
	if rel, err := c.store.GetActiveRelease(ctx, ss.svc.ID); err == nil {
		relID = &rel.ID
	}
	row, err := c.store.InsertSkippedRun(ctx, store.InsertSkippedRunParams{
		ServiceID: ss.svc.ID, ReleaseID: relID, JobID: &job.ID, Trigger: string(apitypes.TriggerSchedule), Command: job.Command,
		TimeoutSeconds: job.TimeoutSeconds, Error: errText(why), Actor: System.Name, ActorKind: System.Kind, StartedAt: at, FinishedAt: &at,
	})
	if err != nil {
		c.log.Error("record a skipped job run", "job", job.ID, "err", err)
		return
	}
	c.trimJobRuns(ctx, job.ID)
	rel, _ := c.runRelease(ctx, row)
	c.publishRun(ss, rel, row, job.Name)
}

// errText is an error's message for a run's record: core's errors say
// what to do next already.
func errText(err error) string {
	return strings.TrimRight(err.Error(), ".")
}

// trimJobRuns keeps a job's newest apitypes.KeepJobRuns runs.
func (c *Core) trimJobRuns(ctx context.Context, jobID int64) {
	if err := c.store.TrimJobRuns(ctx, store.TrimJobRunsParams{JobID: &jobID, Keep: apitypes.KeepJobRuns}); err != nil {
		c.log.Error("trim job runs", "job", jobID, "err", err)
	}
}

// jobEnded is a job run's end: alert on a failure, resolve on a success.
// It doesn't wake the scheduler: the pass that started the run already
// recorded its due time and timed the next one.
func (c *Core) jobEnded(ss svcScope, job store.ServiceJob, row store.ServiceRun) {
	ctx := context.WithoutCancel(c.ctx)
	if _, err := c.store.GetServiceJobByID(ctx, job.ID); store.IsNotFound(err) {
		// Removed while it ran (a dock.toml without it): an alert now
		// would name a job that is gone and never resolve.
		return
	}
	key := jobAlertKey(ss, job)
	switch apitypes.RunStatus(row.Status) {
	case apitypes.RunSucceeded:
		c.resolveAlert(ctx, key, now())
	case apitypes.RunFailed, apitypes.RunTimedOut:
		how := row.Error
		if row.ExitCode != nil {
			how = fmt.Sprintf("exited with code %d", *row.ExitCode)
		}
		svc := serviceKey(ss.project.Name, ss.env.Name, ss.svc.Name)
		c.raiseAlert(ctx, alertSignal{
			Key: key, serviceID: ss.svc.ID, projectID: ss.project.ID, Kind: apitypes.AlertJobFailed, Severity: apitypes.SeverityWarning,
			Title:   fmt.Sprintf("Job %s of %s failed", job.Name, svc),
			Message: fmt.Sprintf("Run %d (`%s`) %s. `dock job history %s -s %s` shows its output.", row.ID, cleanLine(job.Command, 120), how, job.Name, ss.svc.Name),
			Project: ss.project.Name, Environment: ss.env.Name, Service: ss.svc.Name,
		}, now())
	}
}

// jobAlertKey names a job's open job_failed alert.
func jobAlertKey(ss svcScope, job store.ServiceJob) string {
	return "job:" + alertServiceKey(ss.svc.ID, ss.project.Name, ss.env.Name, ss.svc.Name) + ":" + job.Name
}

// checkJob validates a job's settings and fills in the defaults.
func checkJob(name string, req *apitypes.SetJobRequest) error {
	if !jobName.MatchString(name) {
		return invalid("a job's name is lower-case letters, digits and dashes, at most 40, e.g. nightly-cleanup")
	}
	if _, err := checkCommand(req.Command); err != nil {
		return err
	}
	req.Command = strings.TrimSpace(req.Command)
	if req.Timezone == "" {
		req.Timezone = "UTC"
	}
	if _, _, err := parseJobSchedule(req.Schedule, req.Timezone); err != nil {
		return invalid("schedule %q in %s: %v", req.Schedule, req.Timezone, err)
	}
	if req.Overlap == "" {
		req.Overlap = apitypes.OverlapSkip
	}
	if req.Overlap != apitypes.OverlapSkip && req.Overlap != apitypes.OverlapAllow {
		return invalid("overlap must be skip or allow")
	}
	t, err := checkTimeout(req.TimeoutSeconds)
	req.TimeoutSeconds = t
	return err
}

// SetJob creates a job on a service or replaces one by name. Jobs from
// dock.toml are changed there; the file is what deploys them.
func (c *Core) SetJob(ctx context.Context, a Actor, ref ServiceRef, name string, req apitypes.SetJobRequest) (apitypes.Job, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "setting a job")
	if err != nil {
		return apitypes.Job{}, err
	}
	if err := checkJob(name, &req); err != nil {
		return apitypes.Job{}, err
	}
	if cur, err := c.store.GetServiceJob(ctx, store.GetServiceJobParams{ServiceID: ss.svc.ID, Name: name}); err == nil {
		if apitypes.JobSource(cur.Source) == apitypes.JobFromRepo {
			return apitypes.Job{}, conflict("job %s comes from %s's dock.toml; change it there and deploy", name, ss.svc.Name)
		}
	} else if !store.IsNotFound(err) {
		return apitypes.Job{}, err
	} else if jobs, err := c.store.ListServiceJobs(ctx, ss.svc.ID); err != nil {
		return apitypes.Job{}, err
	} else if len(jobs) >= apitypes.MaxJobsPerService {
		return apitypes.Job{}, conflict("%s has %d jobs, the most a service may have; remove one first", ss.svc.Name, len(jobs))
	}
	enabled := req.Enabled == nil || *req.Enabled
	at := now()
	row, err := c.store.UpsertServiceJob(ctx, store.UpsertServiceJobParams{
		ServiceID: ss.svc.ID, Name: name, Schedule: strings.TrimSpace(req.Schedule), Timezone: req.Timezone, Command: req.Command,
		TimeoutSeconds: int64(req.TimeoutSeconds), Overlap: string(req.Overlap), Enabled: enabled, Source: string(apitypes.JobFromAPI),
		CreatedBy: a.Name, CreatedAt: at, UpdatedAt: at,
	})
	if err != nil {
		return apitypes.Job{}, err
	}
	c.audit(ctx, a, "job.set", &ss.project, &ss.env, ss.String()+"/"+name, fmt.Sprintf("set job %s: `%s` at %s (%s)", name, cleanLine(req.Command, runAuditCommand), row.Schedule, row.Timezone))
	c.kickJobs()
	return c.publishJob(ctx, ss, row)
}

// SetJobEnabled turns a job on or off, dock.toml's too.
func (c *Core) SetJobEnabled(ctx context.Context, a Actor, ref ServiceRef, name string, enabled bool) (apitypes.Job, error) {
	ss, job, err := c.jobFor(ctx, a, ref, name, apitypes.ScopeDeploy, "turning a job on or off")
	if err != nil {
		return apitypes.Job{}, err
	}
	if err := c.store.SetServiceJobEnabled(ctx, store.SetServiceJobEnabledParams{Enabled: enabled, UpdatedAt: now(), ID: job.ID}); err != nil {
		return apitypes.Job{}, err
	}
	verb := "disabled"
	if enabled {
		verb = "enabled"
	}
	c.audit(ctx, a, "job."+strings.TrimSuffix(verb, "d"), &ss.project, &ss.env, ss.String()+"/"+name, verb+" job "+name)
	if !enabled {
		c.resolveAlert(ctx, jobAlertKey(ss, job), now())
	}
	c.kickJobs()
	row, err := c.store.GetServiceJobByID(ctx, job.ID)
	if err != nil {
		return apitypes.Job{}, err
	}
	return c.publishJob(ctx, ss, row)
}

// RemoveJob removes a job and its history. Jobs from dock.toml go when
// the file no longer has them.
func (c *Core) RemoveJob(ctx context.Context, a Actor, ref ServiceRef, name string) error {
	ss, job, err := c.jobFor(ctx, a, ref, name, apitypes.ScopeDeploy, "removing a job")
	if err != nil {
		return err
	}
	if apitypes.JobSource(job.Source) == apitypes.JobFromRepo {
		return conflict("job %s comes from %s's dock.toml; remove it there and deploy, or disable it", name, ss.svc.Name)
	}
	if c.jobRunning(job.ID) {
		return conflict("job %s is running; remove it once the run ended", name)
	}
	if err := c.deleteJob(ctx, job.ID); err != nil {
		return err
	}
	c.audit(ctx, a, "job.remove", &ss.project, &ss.env, ss.String()+"/"+name, "removed job "+name)
	c.resolveAlert(ctx, jobAlertKey(ss, job), now())
	c.publish(ss, apitypes.Event{Type: apitypes.EventJob, Job: &apitypes.Job{Name: name}, Message: "removed"})
	c.kickJobs()
	return nil
}

// deleteJob removes a job and its runs.
func (c *Core) deleteJob(ctx context.Context, id int64) error {
	return c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.DeleteServiceJobRuns(ctx, &id); err != nil {
			return err
		}
		return q.DeleteServiceJob(ctx, id)
	})
}

// RunJobNow runs a job now, outside its schedule, and answers with the
// run, running. A job still running with overlap skip is a conflict.
func (c *Core) RunJobNow(ctx context.Context, a Actor, ref ServiceRef, name string) (apitypes.CommandRun, error) {
	ss, job, err := c.jobFor(ctx, a, ref, name, apitypes.ScopeDeploy, "running a job")
	if err != nil {
		return apitypes.CommandRun{}, err
	}
	info, err := c.startJob(ctx, job.ID, ss.svc.ID, apitypes.TriggerManual, a)
	if errors.Is(err, errStillRunning) {
		return apitypes.CommandRun{}, conflict("job %s is still running; wait for it to end", name)
	}
	if err != nil {
		return apitypes.CommandRun{}, err
	}
	c.audit(ctx, a, "job.run", &ss.project, &ss.env, ss.String()+"/"+name, fmt.Sprintf("ran job %s now (run %d)", name, info.ID))
	return info, nil
}

// ListJobs returns a service's jobs with their next time and latest run.
// Scope read: three queries, however many jobs.
func (c *Core) ListJobs(ctx context.Context, a Actor, ref ServiceRef) ([]apitypes.Job, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "listing a service's jobs")
	if err != nil {
		return nil, err
	}
	rows, err := c.store.ListServiceJobs(ctx, ss.svc.ID)
	if err != nil {
		return nil, err
	}
	latest, err := c.store.ListLatestJobRuns(ctx, ss.svc.ID)
	if err != nil {
		return nil, err
	}
	last := make(map[int64]store.ListLatestJobRunsRow, len(latest))
	for _, r := range latest {
		if r.JobID != nil {
			last[*r.JobID] = r
		}
	}
	out := make([]apitypes.Job, 0, len(rows))
	for _, row := range rows {
		j := c.toJob(ss, row)
		if r, ok := last[row.ID]; ok {
			run := apitypes.CommandRun{
				ID: r.ID, Project: ss.project.Name, Environment: ss.env.Name, Service: ss.svc.Name, Job: row.Name,
				Status: apitypes.RunStatus(r.Status), StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
			}
			if r.ExitCode != nil {
				run.ExitCode = ptr(int(*r.ExitCode))
			}
			j.LastRun = &run
		}
		out = append(out, j)
	}
	return out, nil
}

// JobRuns returns a job's runs, newest first, without their output (a
// run's own read, GetRun, has it). Scope read.
func (c *Core) JobRuns(ctx context.Context, a Actor, ref ServiceRef, name string) ([]apitypes.CommandRun, error) {
	ss, job, err := c.jobFor(ctx, a, ref, name, apitypes.ScopeRead, "reading a job's runs")
	if err != nil {
		return nil, err
	}
	rows, err := c.store.ListJobRuns(ctx, store.ListJobRunsParams{JobID: &job.ID, Limit: apitypes.KeepJobRuns})
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.CommandRun, 0, len(rows))
	for _, row := range rows {
		run := c.toRun(ss, store.Release{Number: row.ReleaseNumber}, store.ServiceRun{
			ID: row.ID, ServiceID: row.ServiceID, ReleaseID: row.ReleaseID, Trigger: row.Trigger, Command: row.Command, Tty: row.Tty,
			Volumes: row.Volumes, TimeoutSeconds: row.TimeoutSeconds, Status: row.Status, ExitCode: row.ExitCode, Error: row.Error,
			Actor: row.Actor, ActorKind: row.ActorKind, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, JobID: row.JobID,
		})
		run.Job = name
		out = append(out, run)
	}
	return out, nil
}

// jobFor loads a service and one of its jobs, checking need.
func (c *Core) jobFor(ctx context.Context, a Actor, ref ServiceRef, name string, need apitypes.Scope, action string) (svcScope, store.ServiceJob, error) {
	ss, err := c.serviceFor(ctx, a, ref, need, action)
	if err != nil {
		return ss, store.ServiceJob{}, err
	}
	job, err := c.store.GetServiceJob(ctx, store.GetServiceJobParams{ServiceID: ss.svc.ID, Name: name})
	if store.IsNotFound(err) {
		return ss, job, notFound("job %s of %s not found; `dock job list -s %s` shows its jobs", name, ss.svc.Name, ss.svc.Name)
	}
	return ss, job, err
}

// toJob converts a job row, with its next due time.
func (c *Core) toJob(ss svcScope, row store.ServiceJob) apitypes.Job {
	j := apitypes.Job{
		Name: row.Name, Schedule: row.Schedule, Timezone: row.Timezone, Command: row.Command,
		TimeoutSeconds: int(row.TimeoutSeconds), Overlap: apitypes.JobOverlap(row.Overlap), Enabled: row.Enabled,
		Source: apitypes.JobSource(row.Source), CreatedBy: row.CreatedBy, UpdatedAt: row.UpdatedAt,
	}
	if row.Enabled && ss.svc.ArchivedAt == nil {
		if s, loc, err := parseJobSchedule(row.Schedule, row.Timezone); err == nil {
			if next := s.Next(jobBase(row.UpdatedAt, row.LastDueAt).In(loc)); !next.IsZero() {
				// A time already passed is due now: the scheduler runs it.
				next = later(next, now())
				j.NextAt = &next
			}
		}
	}
	return j
}

// publishJob sends a job event and returns the job.
func (c *Core) publishJob(ctx context.Context, ss svcScope, row store.ServiceJob) (apitypes.Job, error) {
	j := c.toJob(ss, row)
	c.publish(ss, apitypes.Event{Type: apitypes.EventJob, Job: &j})
	return j, nil
}
