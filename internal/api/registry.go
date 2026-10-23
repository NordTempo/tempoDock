package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// registerRegistry registers the platform registry's console endpoints.
func registerRegistry(s *Server, a huma.API) {
	tags := []string{"registry"}
	authed(s, a, huma.Operation{
		OperationID: "get-registry", Method: http.MethodGet, Path: Prefix + "/registry",
		Summary: "Describe the registry", Tags: tags,
		Description: "The platform registry's size on disk, how pruning went since the daemon started, and the services keeping images in it. enabled is false on installs without a registry. Scope: admin, unlimited.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.Registry], error) {
		return reply(s.core.GetRegistry(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "prune-registry", Method: http.MethodPost, Path: Prefix + "/registry/prune",
		Summary: "Prune the registry now", Tags: tags, DefaultStatus: http.StatusAccepted,
		Description: "Deletes the images no release keeps (outside every service's rollback window and not active) and collects their layers, in the background; the registry stops for those seconds. Answers with the registry, pruning. 409 while builds or pulls use the registry or a prune runs. Scope: admin, unlimited.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.Registry], error) {
		return reply(s.core.PruneRegistry(ctx, actor))
	})
}
