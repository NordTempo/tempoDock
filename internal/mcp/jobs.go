package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/mcpkit"
)

// Scheduled jobs for agents (ADR 0052): list, set, remove and run a
// service's cron jobs, and read their history.

// JobInput names one of a service's jobs.
type JobInput struct {
	ServiceInput
	Job string `json:"job" doc:"Job name, e.g. nightly-cleanup; list_jobs lists them."`
}

// addJobTools registers the job tools.
func (h *handler) addJobTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "list_jobs", "List a service's jobs",
		"A service's scheduled jobs: name, cron schedule and timezone, command, next time, latest run, and whether it comes from dock.toml.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct{ ServiceInput }) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			jobs, err := h.core.ListJobs(ctx, c.Actor, ref)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			names := make([]string, len(jobs))
			for i, j := range jobs {
				names[i] = j.Name
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("%d jobs on %s: %s.", len(jobs), ref.Service, list(names)), Data: jobs}, nil
		})

	mcpkit.AddTool(s, "set_job", "Set a job",
		"Create a scheduled job on a service, or replace one by name: a cron expression (minute hour day-of-month month day-of-week, or @hourly/@daily/@weekly/@monthly/@yearly) in a timezone, "+
			"and a command run like run_command in the service's active image. A job still running when it comes due again is skipped unless overlap is allow. Jobs from dock.toml are changed in the file. Needs deploy.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			JobInput
			Schedule       string `json:"schedule" doc:"Cron expression, e.g. \"0 3 * * *\" or @hourly."`
			Timezone       string `json:"timezone,omitempty" doc:"IANA timezone the schedule is read in, e.g. Europe/Oslo. Default UTC."`
			Command        string `json:"command" doc:"The command, run with /bin/sh -c."`
			TimeoutSeconds int    `json:"timeout_seconds,omitempty" doc:"A run is stopped after this long. Default 3600, at most 86400."`
			Overlap        string `json:"overlap,omitempty" enum:"skip,allow" doc:"When it comes due while still running: skip (default) or allow."`
			Enabled        *bool  `json:"enabled,omitempty" doc:"Default true."`
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			j, err := h.core.SetJob(ctx, c.Actor, ref, in.Job, apitypes.SetJobRequest{
				Schedule: in.Schedule, Timezone: in.Timezone, Command: in.Command, TimeoutSeconds: in.TimeoutSeconds,
				Overlap: apitypes.JobOverlap(in.Overlap), Enabled: in.Enabled,
			})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			next := "disabled"
			if j.NextAt != nil {
				next = "next at " + j.NextAt.Format(time.RFC3339)
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("Job %s set: %s (%s), %s.", j.Name, j.Schedule, j.Timezone, next), Data: j}, nil
		})

	mcpkit.AddTool(s, "remove_job", "Remove a job",
		"Remove a scheduled job and its history. Jobs from dock.toml go when the file drops them. Needs deploy.",
		mcpkit.Destructive, func(ctx context.Context, c *call, in JobInput) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			if err := h.core.RemoveJob(ctx, c.Actor, ref, in.Job); err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("Job %s removed from %s.", in.Job, ref.Service)}, nil
		})

	mcpkit.AddTool(s, "run_job", "Run a job now",
		"Run a scheduled job now, outside its schedule, and wait for it to end. Returns the status, exit code and the last 16 KB of output. Needs deploy.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			JobInput
			WaitSeconds *int `json:"wait_seconds,omitempty" doc:"How long to wait for the end, in seconds. Default 300, at most 900; the run goes on after."`
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			wait := defaultRunWait
			if in.WaitSeconds != nil {
				wait = *in.WaitSeconds
			}
			if wait < 0 || wait > maxRunWait {
				return mcpkit.Reply{}, mcpkit.Invalid("wait_seconds must be between 0 and %d", maxRunWait)
			}
			run, err := h.core.RunJobNow(ctx, c.Actor, ref, in.Job)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			if run, err = h.core.WaitRun(ctx, c.Actor, ref, run.ID, time.Duration(wait)*time.Second); err != nil {
				return mcpkit.Reply{}, err
			}
			summary := runSummary(run)
			if run.Status == apitypes.RunRunning {
				summary = fmt.Sprintf("Run %d of job %s is still running; job_history shows its end.", run.ID, in.Job)
			}
			return mcpkit.Reply{Summary: summary, Data: run}, nil
		})

	mcpkit.AddTool(s, "job_history", "A job's history",
		"A scheduled job's latest runs, newest first: status (skipped ones with why), exit code, trigger, when. Without output; run_job returns that for its run.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in JobInput) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			runs, err := h.core.JobRuns(ctx, c.Actor, ref, in.Job)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			var failed []string
			for _, r := range runs {
				if r.Status == apitypes.RunFailed || r.Status == apitypes.RunTimedOut {
					failed = append(failed, fmt.Sprint(r.ID))
				}
			}
			summary := fmt.Sprintf("%d runs of job %s.", len(runs), in.Job)
			if len(failed) > 0 {
				summary += " Failed: " + strings.Join(failed, ", ") + "."
			}
			return mcpkit.Reply{Summary: summary, Data: runs}, nil
		})
}
