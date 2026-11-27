package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// The deploy queue (ADR 0086, amended 2026-11-27): what each builder
// builds and what waits behind it, for the org and for the console.
// Pages keep it live from deploy_queue and release events.
func registerDeployQueue(s *Server, a huma.API) {
	tags := []string{"releases"}
	authed(s, a, huma.Operation{
		OperationID: "get-deploy-queue", Method: http.MethodGet, Path: Prefix + "/deploy-queue",
		Summary: "Read the deploy queue", Tags: tags,
		Description: "Per builder, the release it builds and the ones waiting for it in order, then the releases deploying that aren't at a builder. Another organization's release, or one you can't read, shows as hidden in its place. Builders are labelled, not named. Cancel one with POST .../releases/{number}/cancel. Scope: read.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.DeployQueue], error) {
		return reply(s.core.DeployQueue(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-console-deploy-queue", Method: http.MethodGet, Path: Prefix + "/console/deploy-queue",
		Summary: "Read every organization's deploy queue", Tags: tags,
		Description: "The deploy queue of every organization, builders named, each release with its organization and ID. Commits show by SHA only. Platform viewers and operators.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.DeployQueue], error) {
		return reply(s.core.OperatorDeployQueue(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "cancel-console-release", Method: http.MethodPost, Path: Prefix + "/console/releases/{id}/cancel",
		Summary: "Cancel any organization's deploy", Tags: tags,
		Description: "Cancels a deploy by release ID, as POST .../releases/{number}/cancel does, to free a builder without joining the organization. Recorded in the organization's log and the platform's. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ID int64 `path:"id" doc:"Release ID, from the console's deploy queue"`
	}) (*out[apitypes.Release], error) {
		return reply(s.core.OperatorCancelRelease(ctx, actor, in.ID))
	})
}
