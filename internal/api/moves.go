package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// registerMoves registers moving an environment to another server and
// reading how its move went (ADR 0053).
func registerMoves(s *Server, a huma.API) {
	tags := []string{"environments"}
	authed(s, a, huma.Operation{
		OperationID: "move-environment", Method: http.MethodPost, Path: envPathT + "/move",
		Summary: "Move an environment to another server", Tags: tags, DefaultStatus: http.StatusAccepted, Errors: withOffline,
		Description: "Checks everything first and refuses with the reason: the target accepts the environment's kind, both servers are online, the target has the memory and disk, every active release's image can reach it, and domains routed through Cloudflare have a tunnel on it. " +
			"Then answers with the move, running, and works in the background (ADR 0062): volumes copy in passes while their services run; then requests are held for a few seconds while every service stops on the source, the last changes copy and are checked, and the target starts. No write is lost, and the old server forwards what still reaches it. " +
			"A move whose final copy wouldn't fit max_hold fails before anything stops. A failure before the switch rolls back and leaves the environment where it was. Progress is published as environment_move events; environment.moved or environment.move_failed is audited at the end. " +
			"Operators only, and the environment's lock applies.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		EnvPath
		Body apitypes.MoveEnvironmentRequest
	}) (*out[apitypes.EnvironmentMove], error) {
		return reply(s.core.MoveEnvironment(ctx, actor, in.ref(), in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-environment-move", Method: http.MethodGet, Path: envPathT + "/move",
		Summary: "An environment's move", Tags: tags,
		Description: "The running move, or the latest one: its step, each service's progress, the hold and what is left to do by hand. 404 if the environment never moved. Scope: read.",
	}, func(ctx context.Context, actor core.Actor, in *EnvPath) (*out[apitypes.EnvironmentMove], error) {
		return reply(s.core.EnvironmentMoveStatus(ctx, actor, in.ref()))
	})
}
