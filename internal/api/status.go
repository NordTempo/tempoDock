package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// StatusPagePath addresses an organization's status page.
type StatusPagePath struct {
	Page string `path:"page" doc:"The page's slug"`
}

const statusPagePathT = Prefix + "/status-pages/{page}"

// registerStatusPages registers status pages (ADR 0073): the org's pages
// and their components. The pages themselves are served by StatusPages.
func registerStatusPages(s *Server, a huma.API) {
	tags := []string{"status pages"}
	authed(s, a, huma.Operation{
		OperationID: "list-status-pages", Method: http.MethodGet, Path: Prefix + "/status-pages",
		Summary: "List status pages", Tags: tags,
		Description: "The organization's public status pages, published or not, with their addresses.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.StatusPageSummary], error) {
		return reply(s.core.ListStatusPages(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "create-status-page", Method: http.MethodPost, Path: Prefix + "/status-pages",
		Summary: "Make a status page", Tags: tags, DefaultStatus: http.StatusCreated,
		Description: "Unpublished until published; then served at status-<slug>.<base domain>. At most 5 per organization. Organization admins.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body apitypes.CreateStatusPageRequest
	}) (*out[apitypes.StatusPage], error) {
		return reply(s.core.CreateStatusPage(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-status-page", Method: http.MethodGet, Path: statusPagePathT,
		Summary: "Get a status page", Tags: tags,
		Description: "With each component's live status, sources and the last 90 days. Sources in projects you can't see are hidden.",
	}, func(ctx context.Context, actor core.Actor, in *StatusPagePath) (*out[apitypes.StatusPage], error) {
		return reply(s.core.GetStatusPage(ctx, actor, in.Page))
	})
	authed(s, a, huma.Operation{
		OperationID: "update-status-page", Method: http.MethodPatch, Path: statusPagePathT,
		Summary: "Change a status page", Tags: tags,
		Description: "Its title, whether it shows percentages, and whether it is published. Organization admins.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		StatusPagePath
		Body apitypes.UpdateStatusPageRequest
	}) (*out[apitypes.StatusPage], error) {
		return reply(s.core.UpdateStatusPage(ctx, actor, in.Page, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "delete-status-page", Method: http.MethodDelete, Path: statusPagePathT,
		Summary: "Remove a status page", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "With its components; its address answers 404 at once. Organization admins.",
	}, func(ctx context.Context, actor core.Actor, in *StatusPagePath) (*struct{}, error) {
		return nil, s.core.DeleteStatusPage(ctx, actor, in.Page)
	})
	authed(s, a, huma.Operation{
		OperationID: "set-status-components", Method: http.MethodPut, Path: statusPagePathT + "/components",
		Summary: "Set a status page's components", Tags: tags,
		Description: "The whole list, in order: public names, groups and sources (services as project/environment/service, or uptime check IDs; never previews). Organization admins.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		StatusPagePath
		Body apitypes.SetStatusComponentsRequest
	}) (*out[apitypes.StatusPage], error) {
		return reply(s.core.SetStatusComponents(ctx, actor, in.Page, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "list-status-sources", Method: http.MethodGet, Path: Prefix + "/status-sources",
		Summary: "List what components may show", Tags: tags,
		Description: "The organization's live services outside previews, with their uptime checks: what a status page's components can be built from.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.StatusSourceCandidate], error) {
		return reply(s.core.ListStatusSourceCandidates(ctx, actor))
	})
}
