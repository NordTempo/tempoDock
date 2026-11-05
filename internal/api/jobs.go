package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// Scheduled jobs (ADR 0054): a service's named cron schedules, their runs
// and running one now.

// JobPath names one of a service's jobs.
type JobPath struct {
	ServicePath
	Job string `path:"job" doc:"Job name"`
}

func registerJobs(s *Server, a huma.API) {
	tags := []string{"jobs"}
	jobPath := servicePathT + "/jobs/{job}"
	authed(s, a, huma.Operation{
		OperationID: "list-jobs", Method: http.MethodGet, Path: servicePathT + "/jobs",
		Summary: "List a service's jobs", Tags: tags,
		Description: "Its scheduled jobs by name, each with its next time and latest run.",
	}, func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[[]apitypes.Job], error) {
		return reply(s.core.ListJobs(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-job", Method: http.MethodPut, Path: jobPath,
		Summary: "Set a job", Tags: tags,
		Description: "Creates a scheduled job or replaces it: a cron expression in a timezone and a command, run like `dock run` in the service's active image. " +
			"A changed schedule counts from now. Jobs from dock.toml are changed there (409). Audited as job.set. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		JobPath
		Body apitypes.SetJobRequest
	}) (*out[apitypes.Job], error) {
		return reply(s.core.SetJob(ctx, actor, in.ref(), in.Job, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-job-enabled", Method: http.MethodPut, Path: jobPath + "/enabled",
		Summary: "Turn a job on or off", Tags: tags,
		Description: "Also for jobs from dock.toml. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		JobPath
		Body apitypes.SetJobEnabledRequest
	}) (*out[apitypes.Job], error) {
		return reply(s.core.SetJobEnabled(ctx, actor, in.ref(), in.Job, in.Body.Enabled))
	})
	authed(s, a, huma.Operation{
		OperationID: "remove-job", Method: http.MethodDelete, Path: jobPath,
		Summary: "Remove a job", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "Removes the job and its runs. Jobs from dock.toml go when the file drops them (409). Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *JobPath) (*struct{}, error) {
		return nil, s.core.RemoveJob(ctx, actor, in.ref(), in.Job)
	})
	authed(s, a, huma.Operation{
		OperationID: "run-job", Method: http.MethodPost, Path: jobPath + "/runs",
		Summary: "Run a job now", Tags: tags, DefaultStatus: http.StatusAccepted,
		Description: "Starts a run outside the schedule in the background and answers with it, running; run events follow it. A job still running with overlap skip is a 409. Audited as job.run. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *JobPath) (*out[apitypes.CommandRun], error) {
		return reply(s.core.RunJobNow(ctx, actor, in.ref(), in.Job))
	})
	authed(s, a, huma.Operation{
		OperationID: "list-job-runs", Method: http.MethodGet, Path: jobPath + "/runs",
		Summary: "A job's history", Tags: tags,
		Description: "Its latest runs, newest first, without their output (GET .../runs/{id} has it).",
	}, func(ctx context.Context, actor core.Actor, in *JobPath) (*out[[]apitypes.CommandRun], error) {
		return reply(s.core.JobRuns(ctx, actor, in.ref(), in.Job))
	})
}
