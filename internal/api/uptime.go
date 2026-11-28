package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// registerUptime registers a service's uptime checks and the
// organization's outside checks (ADR 0055).
func registerUptime(s *Server, a huma.API) {
	tags := []string{"uptime"}
	authed(s, a, huma.Operation{
		OperationID: "list-uptime-checks", Method: http.MethodGet, Path: servicePathT + "/uptime-checks",
		Summary:     "List uptime checks",
		Description: "With each check's state, last probe, the server that probes it, uptime over 24 hours, 7 and 30 days, the last day in 30-minute buckets and its latest incidents.",
		Tags:        tags,
	}, func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[[]apitypes.UptimeCheck], error) {
		return reply(s.core.ListUptimeChecks(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "add-uptime-check", Method: http.MethodPost, Path: servicePathT + "/uptime-checks",
		Summary:     "Add an uptime check",
		Description: "A URL on one of the service's domains, probed with GET from another server (the control plane when there is none). Two failed probes in a row open an uptime_down alert. At most 10 per service. Scope: admin.",
		Tags:        tags, DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.AddUptimeCheckRequest
	}) (*out[apitypes.UptimeCheck], error) {
		return reply(s.core.AddUptimeCheck(ctx, actor, in.ref(), in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "check-domains", Method: http.MethodPost, Path: servicePathT + "/uptime-checks/domains",
		Summary:     "Check every domain",
		Description: "Adds an uptime check of https://<domain>/ for each of the service's verified domains that has none, marked auto (ADR 0059), and returns the service's checks. Scope: admin.",
		Tags:        tags,
	}, func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[[]apitypes.UptimeCheck], error) {
		return reply(s.core.CheckDomains(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "update-uptime-check", Method: http.MethodPatch, Path: servicePathT + "/uptime-checks/{id}",
		Summary:     "Change an uptime check",
		Description: "Its interval or expected status, in place: the history stays and the new schedule applies at once. Scope: admin.",
		Tags:        tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		ID   int64 `path:"id"`
		Body apitypes.UpdateUptimeCheckRequest
	}) (*out[apitypes.UptimeCheck], error) {
		return reply(s.core.UpdateUptimeCheck(ctx, actor, in.ref(), in.ID, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "remove-uptime-check", Method: http.MethodDelete, Path: servicePathT + "/uptime-checks/{id}",
		Summary: "Remove an uptime check", Description: "With its history; its open alert resolves. Scope: admin.",
		Tags: tags, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		ID int64 `path:"id"`
	}) (*struct{}, error) {
		return nil, s.core.RemoveUptimeCheck(ctx, actor, in.ref(), in.ID)
	})
	authed(s, a, huma.Operation{
		OperationID: "list-outside-uptime-checks", Method: http.MethodGet, Path: Prefix + "/outside-uptime-checks",
		Summary:     "List outside uptime checks",
		Description: "The organization's checks of sites outside Dock, for status page components: each with its results, as a service's checks, and the components that show it (used_by).",
		Tags:        tags,
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.UptimeCheck], error) {
		return reply(s.core.ListOutsideUptimeChecks(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "add-outside-uptime-check", Method: http.MethodPost, Path: Prefix + "/outside-uptime-checks",
		Summary:     "Add an outside uptime check",
		Description: "A URL of a public site outside Dock (ports 80 and 443), probed with GET from one of the servers every 60 seconds or more. Two failed probes in a row open an uptime_down alert to the organization's channels, whether or not a status page shows it. Counts toward the organization's 50 checks. Organization admins.",
		Tags:        tags, DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body apitypes.AddUptimeCheckRequest
	}) (*out[apitypes.UptimeCheck], error) {
		return reply(s.core.AddOutsideUptimeCheck(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "update-outside-uptime-check", Method: http.MethodPatch, Path: Prefix + "/outside-uptime-checks/{id}",
		Summary:     "Change an outside uptime check",
		Description: "Its interval (60 to 3600 seconds) or expected status, in place. Organization admins.",
		Tags:        tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ID   int64 `path:"id"`
		Body apitypes.UpdateUptimeCheckRequest
	}) (*out[apitypes.UptimeCheck], error) {
		return reply(s.core.UpdateOutsideUptimeCheck(ctx, actor, in.ID, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "remove-outside-uptime-check", Method: http.MethodDelete, Path: Prefix + "/outside-uptime-checks/{id}",
		Summary:     "Remove an outside uptime check",
		Description: "With its history; its open alert resolves and the status page components that show it (used_by) lose it as a source. Organization admins.",
		Tags:        tags, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ID int64 `path:"id"`
	}) (*struct{}, error) {
		return nil, s.core.RemoveOutsideUptimeCheck(ctx, actor, in.ID)
	})
}
