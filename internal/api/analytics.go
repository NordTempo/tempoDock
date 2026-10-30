package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// registerAnalytics registers Dock Analytics' platform settings (ADR
// 0037): which server runs the store, and whether it answers.
func registerAnalytics(s *Server, a huma.API) {
	tags := []string{"analytics"}
	authed(s, a, huma.Operation{
		OperationID: "get-analytics", Method: http.MethodGet, Path: Prefix + "/analytics",
		Summary: "Show Dock Analytics' state", Tags: tags,
		Description: "Whether analytics is on, which server runs the store, and whether the store answers (asked now). Operators only.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.AnalyticsStatus], error) {
		return reply(s.core.AnalyticsStatus(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-analytics-server", Method: http.MethodPut, Path: Prefix + "/analytics/server",
		Summary: "Choose the analytics server", Tags: tags,
		Description: "Runs the analytics store on the named server (empty: the control plane's own) and turns analytics on. Every server keeps its events in its spool until the store has them. A store on another server stops; its data stays on that server's volume, and the new store starts empty. Operators only.",
		Errors:      []int{http.StatusNotFound},
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body apitypes.SetAnalyticsServerRequest
	}) (*out[apitypes.AnalyticsStatus], error) {
		return reply(s.core.SetAnalyticsServer(ctx, actor, in.Body.Server))
	})
}
