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

// Scheduled jobs for agents (ADR 0054): list, set, remove and run a
// service's cron jobs, and read their history.

// JobInput names one of a service's jobs.
type JobInput struct {
	ServiceInput
	Job string `json:"job"`
}

// addJobTools registers the job tools.
func (h *handler) addJobTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "list_jobs", "List a service's jobs",
		"A service's scheduled jobs.",
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
		"Create or replace a scheduled job: a cron schedule and a command run in the service's image.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			JobInput
			Schedule       string `json:"schedule" doc:"e.g. \"0 3 * * *\" or @daily."`
			Timezone       string `json:"timezone,omitempty" doc:"IANA name. Default UTC."`
			Command        string `json:"command"`
			TimeoutSeconds int    `json:"timeout_seconds,omitempty" doc:"Default 3600."`
			Overlap        string `json:"overlap,omitempty" enum:"skip,allow"`
			Enabled        *bool  `json:"enabled,omitempty"`
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
		"Remove a scheduled job.",
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
		"Run a scheduled job now and wait.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			JobInput
			WaitSeconds *int `json:"wait_seconds,omitempty" doc:"Default 300, at most 900."`
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
		"A scheduled job's latest runs.",
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
