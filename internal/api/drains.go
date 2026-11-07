package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// registerServerDrains registers draining a server: moving every
// environment on it to other servers without downtime (ADR 0062).
func registerServerDrains(s *Server, a huma.API) {
	tags := []string{"servers"}
	authed(s, a, huma.Operation{
		OperationID: "drain-server", Method: http.MethodPost, Path: serverPathT + "/drain",
		Summary: "Drain a server", Tags: tags, DefaultStatus: http.StatusAccepted, Errors: withOffline,
		Description: "Moves every environment on the server (or every one of a kind) to other servers, one at a time, so it can take a kind off, go into maintenance or be removed. " +
			"Each move copies volumes while their services run, then holds requests for a few seconds while the last changes copy and the target starts: nothing is lost and callers see a slow answer, not an error. The old server forwards what still reaches it. " +
			"Targets come from placement unless targets names one per project. With dry_run the answer is the plan and its problems, and nothing changes; a plan with problems doesn't start. " +
			"A move that fails leaves its environment where it was and pauses the drain. Progress is published as server_drain events. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServerPath
		Body apitypes.DrainServerRequest
	}) (*out[apitypes.ServerDrain], error) {
		return reply(s.core.DrainServer(ctx, actor, in.Server, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-server-drain", Method: http.MethodGet, Path: serverPathT + "/drain",
		Summary: "A server's drain", Tags: tags,
		Description: "The running or latest drain: each environment, its target and how far its move got, and the ports the server forwards for services that moved away. 404 if it never drained and forwards nothing. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *ServerPath) (*out[apitypes.ServerDrain], error) {
		return reply(s.core.ServerDrainStatus(ctx, actor, in.Server))
	})
	authed(s, a, huma.Operation{
		OperationID: "resume-server-drain", Method: http.MethodPost, Path: serverPathT + "/drain/resume",
		Summary: "Resume a paused drain", Tags: tags,
		Description: "Tries the environment that failed again, or with skip leaves it where it is, and goes on with the rest. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServerPath
		Body apitypes.ResumeDrainRequest
	}) (*out[apitypes.ServerDrain], error) {
		return reply(s.core.ResumeServerDrain(ctx, actor, in.Server, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "cancel-server-drain", Method: http.MethodPost, Path: serverPathT + "/drain/cancel",
		Summary: "Cancel a drain", Tags: tags,
		Description: "Stops the drain once the move that runs finished; it isn't cut short. Environments not moved yet stay where they are. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *ServerPath) (*out[apitypes.ServerDrain], error) {
		return reply(s.core.CancelServerDrain(ctx, actor, in.Server))
	})
}
