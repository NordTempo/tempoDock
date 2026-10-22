package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// ServerPath addresses a server.
type ServerPath struct {
	Server string `path:"server" doc:"Server name"`
}

// RangeQuery selects how much statistics history to return.
type RangeQuery struct {
	Range apitypes.StatsRange `query:"range" enum:"1h,24h,7d" default:"1h" doc:"1h and 24h: a point per minute; 7d: a point per 10 minutes"`
}

const serverPathT = Prefix + "/servers/{server}"

// withOffline adds 503 server_offline to the documented errors.
var withOffline = []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusServiceUnavailable}

func registerServers(s *Server, a huma.API) {
	tags := []string{"servers"}
	authed(s, a, huma.Operation{
		OperationID: "list-servers", Method: http.MethodGet, Path: Prefix + "/servers",
		Summary: "List servers", Description: "Status, version, accepted environment kinds and load of every server. Unlimited admins also get each server's latest resource sample.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.Server], error) {
		return reply(s.core.ListServers(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-server", Method: http.MethodGet, Path: serverPathT,
		Summary: "Get a server", Description: "The server with its latest resource sample, the projects and environments placed on it, and its services with their latest usage. Scope: admin, unlimited.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *ServerPath) (*out[apitypes.ServerDetail], error) {
		return reply(s.core.GetServer(ctx, actor, in.Server))
	})
	authed(s, a, huma.Operation{
		OperationID: "update-server", Method: http.MethodPatch, Path: serverPathT,
		Summary: "Update a server", Tags: tags,
		Description: "Which environment kinds the server accepts, and whether it takes new placements. Environments already there stay. Scope: admin, unlimited.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServerPath
		Body apitypes.UpdateServerRequest
	}) (*out[apitypes.Server], error) {
		return reply(s.core.UpdateServer(ctx, actor, in.Server, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "server-stats", Method: http.MethodGet, Path: serverPathT + "/stats",
		Summary: "Server statistics", Description: "CPU, memory, disk and load history plus the latest sample. Scope: admin, unlimited.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServerPath
		RangeQuery
	}) (*out[apitypes.ServerStats], error) {
		return reply(s.core.ServerStats(ctx, actor, in.Server, in.Range))
	})
	sse(s, a, huma.Operation{
		OperationID: "server-stats-live", Method: http.MethodGet, Path: serverPathT + "/stats/live",
		Summary: "Live server statistics", Tags: tags, Errors: withOffline,
		Description: "A `sample` event every few seconds while connected; the first is the latest minute sample. Sampling stops when the last watcher disconnects. Scope: admin, unlimited.",
	}, []string{"sample"}, sseFormat[apitypes.ServerUsage]{
		name: func(apitypes.ServerUsage) string { return "sample" },
	}, func(ctx context.Context, actor core.Actor, in *ServerPath) (core.Stream[apitypes.ServerUsage], error) {
		return s.core.ServerStatsLive(ctx, actor, in.Server)
	})

	authed(s, a, huma.Operation{
		OperationID: "service-stats", Method: http.MethodGet, Path: servicePathT + "/stats",
		Summary: "Service statistics", Description: "CPU, memory and IO history of the service (across releases) plus the latest sample.",
		Tags: []string{"services"},
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		RangeQuery
	}) (*out[apitypes.ServiceStats], error) {
		return reply(s.core.ServiceStats(ctx, actor, in.ref(), in.Range))
	})
	sse(s, a, huma.Operation{
		OperationID: "service-stats-live", Method: http.MethodGet, Path: servicePathT + "/stats/live",
		Summary: "Live service statistics", Tags: []string{"services"}, Errors: withOffline,
		Description: "A `sample` event every few seconds while the service has a running container; the first is the latest minute sample. Sampling stops when the last watcher disconnects.",
	}, []string{"sample"}, sseFormat[apitypes.ServiceUsage]{
		name: func(apitypes.ServiceUsage) string { return "sample" },
	}, func(ctx context.Context, actor core.Actor, in *ServicePath) (core.Stream[apitypes.ServiceUsage], error) {
		return s.core.ServiceStatsLive(ctx, actor, in.ref())
	})

	authed(s, a, huma.Operation{
		OperationID: "list-placements", Method: http.MethodGet, Path: projectPathT + "/placements",
		Summary: "Project placements", Description: "Which server the project's production and non-production environments live on.",
		Tags: []string{"projects"},
	}, func(ctx context.Context, actor core.Actor, in *ProjectPath) (*out[[]apitypes.Placement], error) {
		return reply(s.core.ListPlacements(ctx, actor, in.Project))
	})
	authed(s, a, huma.Operation{
		OperationID: "pin-placement", Method: http.MethodPut, Path: projectPathT + "/placements/{kind}",
		Summary: "Pin a project to a server", Tags: []string{"projects"},
		Description: "Puts the project's environments of a kind on a server, before any exist. The server must accept the kind; it may be disabled for placement. Scope: admin, unlimited.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ProjectPath
		Kind apitypes.EnvironmentKind `path:"kind" enum:"production,non_production"`
		Body apitypes.PinPlacementRequest
	}) (*out[apitypes.Placement], error) {
		return reply(s.core.PinPlacement(ctx, actor, in.Project, in.Kind, in.Body.Server))
	})
}
