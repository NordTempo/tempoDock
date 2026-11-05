package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// registerPreviews serves preview environments per pull request (ADR
// 0056): a project's settings and open previews, and one preview by its
// environment.
func registerPreviews(s *Server, a huma.API) {
	tags := []string{"previews"}
	authed(s, a, huma.Operation{
		OperationID: "get-previews", Method: http.MethodGet, Path: projectPathT + "/previews",
		Summary: "Preview settings and open previews", Tags: tags,
		Description: "Whether pull requests get preview environments, cloned from which environment, the limits, and the open previews with their pull request, status and addresses. Scope: read.",
	}, func(ctx context.Context, actor core.Actor, in *ProjectPath) (*out[apitypes.PreviewSettings], error) {
		return reply(s.core.GetPreviews(ctx, actor, in.Project))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-previews", Method: http.MethodPut, Path: projectPathT + "/previews",
		Summary: "Turn previews on or change them", Tags: tags,
		Description: "Pull requests to a repository the base environment's services deploy, into the branch they deploy, get a non-production environment pr-<number> cloned from the base. " +
			"Only the listed variables' literal values are copied; pull requests from forks only with `allow_forks`. Scope: admin, whole project.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ProjectPath
		Body apitypes.SetPreviewsRequest
	}) (*out[apitypes.PreviewSettings], error) {
		return reply(s.core.SetPreviews(ctx, actor, in.Project, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "disable-previews", Method: http.MethodDelete, Path: projectPathT + "/previews",
		Summary: "Turn previews off", Tags: tags,
		Description: "New pull requests get no preview. Open previews stay until their pull requests close. Scope: admin, whole project.",
	}, func(ctx context.Context, actor core.Actor, in *ProjectPath) (*out[apitypes.PreviewSettings], error) {
		return reply(s.core.DisablePreviews(ctx, actor, in.Project))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-preview", Method: http.MethodGet, Path: envPathT + "/preview",
		Summary: "A preview environment's pull request and status", Tags: tags,
		Description: "Not found unless the environment is a preview. Scope: read.",
	}, func(ctx context.Context, actor core.Actor, in *EnvPath) (*out[apitypes.Preview], error) {
		return reply(s.core.GetPreview(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "close-preview", Method: http.MethodPost, Path: envPathT + "/preview/close",
		Summary: "Close a preview", Tags: tags,
		Description: "Archives the preview environment as its pull request closing would: containers, network, volumes and addresses are freed. The pull request's next event makes a new one. Scope: deploy, whole project.",
	}, func(ctx context.Context, actor core.Actor, in *EnvPath) (*out[apitypes.Preview], error) {
		return reply(s.core.ClosePreview(ctx, actor, in.ref()))
	})
}
