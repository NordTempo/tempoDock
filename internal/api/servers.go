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

// TrafficRangeQuery is the range of a service's traffic statistics.
type TrafficRangeQuery struct {
	Range apitypes.TrafficRange `query:"range" enum:"24h,7d,30d" default:"24h" doc:"24h: a point per minute; 7d and 30d: a point per hour"`
}

const serverPathT = Prefix + "/servers/{server}"

// withOffline adds 503 server_offline to the documented errors.
var withOffline = []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusServiceUnavailable}

func registerServers(s *Server, a huma.API) {
	tags := []string{"servers"}
	authed(s, a, huma.Operation{
		OperationID: "list-servers", Method: http.MethodGet, Path: Prefix + "/servers",
		Summary: "List servers", Description: "Status, version, accepted environment kinds and load of every server, for operators with each server's latest resource sample; others get the servers their organization's environments run on.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.Server], error) {
		return reply(s.core.ListServers(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-server", Method: http.MethodGet, Path: serverPathT,
		Summary: "Get a server", Description: "The server with its latest resource sample, the projects and environments placed on it, and its services with their latest usage. Operators only.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *ServerPath) (*out[apitypes.ServerDetail], error) {
		return reply(s.core.GetServer(ctx, actor, in.Server))
	})
	authed(s, a, huma.Operation{
		OperationID: "add-server", Method: http.MethodPost, Path: Prefix + "/servers",
		Summary: "Add a server", Tags: tags, DefaultStatus: http.StatusCreated,
		Description: "Creates a one-time join token for a new server, valid for an hour, and the install command to run on it. The token is returned once and never again; the server shows as joining until it connects. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct{ Body apitypes.AddServerRequest }) (*out[apitypes.AddedServer], error) {
		return reply(s.core.AddServer(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "update-server", Method: http.MethodPatch, Path: serverPathT,
		Summary: "Update a server", Tags: tags,
		Description: "Which environment kinds the server accepts, whether it takes new placements and builds, and its base domain. Environments already there stay. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServerPath
		Body apitypes.UpdateServerRequest
	}) (*out[apitypes.Server], error) {
		return reply(s.core.UpdateServer(ctx, actor, in.Server, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "revoke-server", Method: http.MethodPost, Path: serverPathT + "/revoke",
		Summary: "Revoke a server", Tags: tags,
		Description: "Forgets the server's key and drops its connection; it can't connect again until it re-joins with a new token. Its apps keep running there. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *ServerPath) (*out[apitypes.Server], error) {
		return reply(s.core.RevokeServer(ctx, actor, in.Server))
	})
	authed(s, a, huma.Operation{
		OperationID: "rotate-server-key", Method: http.MethodPost, Path: serverPathT + "/rotate-key",
		Summary: "Rotate a server's key", Tags: tags, Errors: withOffline,
		Description: "The online server creates a new link key and reconnects with it; the old key stops working once it has. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *ServerPath) (*out[apitypes.Server], error) {
		return reply(s.core.RotateServerKey(ctx, actor, in.Server))
	})
	authed(s, a, huma.Operation{
		OperationID: "remove-server", Method: http.MethodDelete, Path: serverPathT,
		Summary: "Remove a server", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "Only a server no live project has environments on (archived projects' move to the control plane's server), and never the control plane's own. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *ServerPath) (*struct{}, error) {
		return nil, s.core.RemoveServer(ctx, actor, in.Server)
	})
	authed(s, a, huma.Operation{
		OperationID: "server-stats", Method: http.MethodGet, Path: serverPathT + "/stats",
		Summary: "Server statistics", Description: "CPU, memory, disk and load history plus the latest sample. Operators only.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServerPath
		RangeQuery
	}) (*out[apitypes.ServerStats], error) {
		return reply(s.core.ServerStats(ctx, actor, in.Server, in.Range))
	})
	sse(s, a, huma.Operation{
		OperationID: "server-stats-live", Method: http.MethodGet, Path: serverPathT + "/stats/live",
		Summary: "Live server statistics", Tags: tags, Errors: withOffline,
		Description: "A `sample` event every few seconds while connected; the first is the latest minute sample. Sampling stops when the last watcher disconnects. Operators only.",
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
	authed(s, a, huma.Operation{
		OperationID: "service-traffic", Method: http.MethodGet, Path: servicePathT + "/traffic",
		Summary: "Service traffic",
		Description: "Requests to the service's domains, counted from the proxy's access log: points over the range, totals, distinct visitors " +
			"(estimated; bots left out) and the most requested paths, with IDs in paths replaced by `:id`.",
		Tags: []string{"services"},
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		TrafficRangeQuery
	}) (*out[apitypes.ServiceTraffic], error) {
		return reply(s.core.ServiceTraffic(ctx, actor, in.ref(), in.Range))
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
		Description: "Puts the project's environments of a kind on a server, before any exist. The server must accept the kind; it may be disabled for placement. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ProjectPath
		Kind apitypes.EnvironmentKind `path:"kind" enum:"production,non_production"`
		Body apitypes.PinPlacementRequest
	}) (*out[apitypes.Placement], error) {
		return reply(s.core.PinPlacement(ctx, actor, in.Project, in.Kind, in.Body.Server))
	})
}
