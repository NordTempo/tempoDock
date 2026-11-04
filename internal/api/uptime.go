package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// registerUptime registers a service's uptime checks (ADR 0052).
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
		OperationID: "remove-uptime-check", Method: http.MethodDelete, Path: servicePathT + "/uptime-checks/{id}",
		Summary: "Remove an uptime check", Description: "With its history; its open alert resolves. Scope: admin.",
		Tags: tags, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		ID int64 `path:"id"`
	}) (*struct{}, error) {
		return nil, s.core.RemoveUptimeCheck(ctx, actor, in.ref(), in.ID)
	})
}
