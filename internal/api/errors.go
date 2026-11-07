package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// Error issues (ADR 0062, "One place for errors"): the org's errors from
// its services' logs, Dock Analytics' frontend errors and 5xx spikes,
// grouped by fingerprint per service.

// ErrorPath addresses an error issue.
type ErrorPath struct {
	ID int64 `path:"id" doc:"Error issue ID"`
}

func registerErrors(s *Server, a huma.API) {
	tags := []string{"errors"}
	authed(s, a, huma.Operation{
		OperationID: "list-errors", Method: http.MethodGet, Path: Prefix + "/errors",
		Summary: "List error issues", Tags: tags,
		Description: "The organization's error issues: errors grouped by fingerprint per service, from its containers' logs, frontend errors Dock Analytics collected and 5xx spikes. New first, then regressed, then ongoing, each by last seen; the list holds the most recently seen ones. Facets count the open issues of every service in the environment filter (or every environment), whatever project and service are picked. Scope: read.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Project     string `query:"project" doc:"Only this project's issues"`
		Environment string `query:"environment" doc:"Only this environment's issues (of every project, without project); also filters the facets"`
		Service     string `query:"service" doc:"Only this service's issues; needs project and environment"`
		State       string `query:"state" enum:"open,resolved,all" default:"open" doc:"open: not resolved"`
		Limit       int    `query:"limit" default:"200" minimum:"1" maximum:"500" doc:"How many issues at most"`
	}) (*out[apitypes.ErrorList], error) {
		return reply(s.core.ListErrors(ctx, actor, core.ErrorFilter{
			Project: in.Project, Environment: in.Environment, Service: in.Service, State: in.State, Limit: in.Limit,
		}))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-error", Method: http.MethodGet, Path: Prefix + "/errors/{id}",
		Summary: "Show an error issue", Tags: tags,
		Description: "One error issue with its latest sample (a stack trace, a log excerpt or the 5xx counts, at most 8 KB, secret values masked) and its trend over 24 hours. Scope: read.",
	}, func(ctx context.Context, actor core.Actor, in *ErrorPath) (*out[apitypes.ErrorIssueDetail], error) {
		return reply(s.core.GetError(ctx, actor, in.ID))
	})
	authed(s, a, huma.Operation{
		OperationID: "resolve-error", Method: http.MethodPost, Path: Prefix + "/errors/{id}/resolve",
		Summary: "Resolve an error issue", Tags: tags,
		Description: "Marks an issue resolved and resolves its alerts. If the error is seen again, the issue regresses and alerts. Issues also resolve by themselves after 7 days without an error. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *ErrorPath) (*out[apitypes.ErrorIssue], error) {
		return reply(s.core.ResolveError(ctx, actor, in.ID))
	})
}
