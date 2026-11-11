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

// IncidentPath addresses an incident by its public ID.
type IncidentPath struct {
	Incident string `path:"incident" doc:"The incident's ID"`
}

const incidentPathT = Prefix + "/incidents/{incident}"

// registerIncidents registers incidents on status pages (ADR 0073,
// phase 2): opened, updated and resolved by people who deploy; removed
// by organization admins.
func registerIncidents(s *Server, a huma.API) {
	tags := []string{"status pages"}
	authed(s, a, huma.Operation{
		OperationID: "open-incident", Method: http.MethodPost, Path: statusPagePathT + "/incidents",
		Summary: "Open an incident", Tags: tags, DefaultStatus: http.StatusCreated,
		Description: "Published at once with its first update, or a draft (state draft) for someone to publish. Its impact sets the components' status while it is open. " +
			"Deploy scope; a token limited to a project may name only components built entirely from it.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		StatusPagePath
		Body apitypes.OpenIncidentRequest
	}) (*out[apitypes.StatusIncident], error) {
		return reply(s.core.OpenIncident(ctx, actor, in.Page, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "schedule-maintenance", Method: http.MethodPost, Path: statusPagePathT + "/maintenance",
		Summary: "Schedule maintenance", Tags: tags, DefaultStatus: http.StatusCreated,
		Description: "A maintenance window over some of the page's components, public at once. Dock starts it at its start and completes it at its end; " +
			"while it is in progress its components read maintenance, their services' alerts are held until it ends and their uptime isn't counted. " +
			"Change its times with PATCH /incidents/{id}; start, complete or cancel it sooner with an update. Deploy scope.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		StatusPagePath
		Body apitypes.ScheduleMaintenanceRequest
	}) (*out[apitypes.StatusIncident], error) {
		return reply(s.core.ScheduleMaintenance(ctx, actor, in.Page, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-incident", Method: http.MethodGet, Path: incidentPathT,
		Summary: "Get an incident", Tags: tags,
		Description: "With its updates, who posted them, and the alert a draft came from.",
	}, func(ctx context.Context, actor core.Actor, in *IncidentPath) (*out[apitypes.StatusIncident], error) {
		return reply(s.core.GetIncident(ctx, actor, in.Incident))
	})
	authed(s, a, huma.Operation{
		OperationID: "update-incident", Method: http.MethodPatch, Path: incidentPathT,
		Summary: "Change an incident", Tags: tags,
		Description: "Its title, the components it names, or a draft's words, without posting an update. Deploy scope.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		IncidentPath
		Body apitypes.UpdateIncidentRequest
	}) (*out[apitypes.StatusIncident], error) {
		return reply(s.core.UpdateIncident(ctx, actor, in.Incident, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "delete-incident", Method: http.MethodDelete, Path: incidentPathT,
		Summary: "Remove an incident", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "From the page and its history, with its updates. Organization admins.",
	}, func(ctx context.Context, actor core.Actor, in *IncidentPath) (*struct{}, error) {
		return nil, s.core.DeleteIncident(ctx, actor, in.Incident)
	})
	authed(s, a, huma.Operation{
		OperationID: "post-incident-update", Method: http.MethodPost, Path: incidentPathT + "/updates",
		Summary: "Post an incident update", Tags: tags, DefaultStatus: http.StatusCreated,
		Description: "Public at once. On a draft it publishes the incident; resolved resolves it. Deploy scope.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		IncidentPath
		Body apitypes.PostIncidentUpdateRequest
	}) (*out[apitypes.StatusIncident], error) {
		return reply(s.core.PostIncidentUpdate(ctx, actor, in.Incident, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "edit-incident-update", Method: http.MethodPatch, Path: incidentPathT + "/updates/{update}",
		Summary: "Edit an incident update", Tags: tags,
		Description: "Fixes its words; the page says it was edited and the audit log keeps what it said. Deploy scope.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		IncidentPath
		Update int64 `path:"update" doc:"The update's ID"`
		Body   apitypes.EditIncidentUpdateRequest
	}) (*out[apitypes.StatusIncident], error) {
		return reply(s.core.EditIncidentUpdate(ctx, actor, in.Incident, in.Update, in.Body))
	})
}
