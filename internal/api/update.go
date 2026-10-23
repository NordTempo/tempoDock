package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// registerUpdate registers updating Dock from the console (ADR 0028).
func registerUpdate(s *Server, a huma.API) {
	tags := []string{"update"}
	authed(s, a, huma.Operation{
		OperationID: "get-update", Method: http.MethodGet, Path: Prefix + "/update",
		Summary: "Check for a newer Dock", Tags: tags,
		Description: "The version this control plane runs, the releases newer than it with their notes, each server's version, and the update running or last finished. The release check is reused for 15 minutes; refresh=true checks now. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Refresh bool `query:"refresh" doc:"Check the release source now instead of reusing the last check"`
	}) (*out[apitypes.UpdateStatus], error) {
		return reply(s.core.UpdateStatus(ctx, actor, in.Refresh))
	})
	authed(s, a, huma.Operation{
		OperationID: "start-update", Method: http.MethodPost, Path: Prefix + "/update/apply",
		Summary: "Update Dock", Tags: tags, DefaultStatus: http.StatusAccepted,
		Description: "Updates Dock to a version (the newest release when left out): downloads and verifies it, then the control plane updates itself through its root updater (a backup first, rolled back if the new version isn't healthy), then every node behind it over its link. Answers once it is under way; update events follow it. 409 while an update runs. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body *apitypes.StartUpdateRequest
	}) (*out[apitypes.UpdateStatus], error) {
		var req apitypes.StartUpdateRequest
		if in.Body != nil {
			req = *in.Body
		}
		return reply(s.core.StartUpdate(ctx, actor, req))
	})
}
