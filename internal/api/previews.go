package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// registerPreviews serves preview environments per pull request (ADR
// 0056, 0077): a project's previews, a service's and an environment's
// settings, and one preview by its environment.
func registerPreviews(s *Server, a huma.API) {
	tags := []string{"previews"}
	authed(s, a, huma.Operation{
		OperationID: "get-previews", Method: http.MethodGet, Path: projectPathT + "/previews",
		Summary: "A project's previews", Tags: tags,
		Description: "Which services build pull requests (and where from), the numbers of each environment previews are cloned from, and the open previews with their pull request, status and addresses. Scope: read.",
	}, func(ctx context.Context, actor core.Actor, in *ProjectPath) (*out[apitypes.PreviewSettings], error) {
		return reply(s.core.GetPreviews(ctx, actor, in.Project))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-service-previews", Method: http.MethodGet, Path: servicePathT + "/previews",
		Summary: "What a service does in previews", Tags: tags,
		Description: "Whether pull requests into its branch build it in a preview, forks too, which of its variables' literal values previews copy, and whether its volumes start from its newest snapshot (ADR 0077). Scope: read.",
	}, func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[apitypes.ServicePreviews], error) {
		return reply(s.core.GetServicePreviews(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-service-previews", Method: http.MethodPut, Path: servicePathT + "/previews",
		Summary: "Set what a service does in previews", Tags: tags,
		Description: "With build, pull requests to the service's repository, into the branch it deploys, that change its directory get a non-production environment pr-<number> cloned from its environment. " +
			"Only the listed variables' literal values are copied; pull requests from forks only with `allow_forks`. Every field is replaced. Scope: admin.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.SetServicePreviewsRequest
	}) (*out[apitypes.ServicePreviews], error) {
		return reply(s.core.SetServicePreviews(ctx, actor, in.ref(), in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-environment-previews", Method: http.MethodGet, Path: envPathT + "/previews",
		Summary: "The numbers of previews cloned from an environment", Tags: tags,
		Description: "How many may be open at once, when they sleep, and which shared variables' literal values they copy. Scope: read.",
	}, func(ctx context.Context, actor core.Actor, in *EnvPath) (*out[apitypes.EnvironmentPreviews], error) {
		return reply(s.core.GetEnvironmentPreviews(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-environment-previews", Method: http.MethodPut, Path: envPathT + "/previews",
		Summary: "Set the numbers of previews cloned from an environment", Tags: tags,
		Description: "Open previews take the new sleep setting at once. Every field is replaced. Scope: admin.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		EnvPath
		Body apitypes.SetEnvironmentPreviewsRequest
	}) (*out[apitypes.EnvironmentPreviews], error) {
		return reply(s.core.SetEnvironmentPreviews(ctx, actor, in.ref(), in.Body))
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
	authed(s, a, huma.Operation{
		OperationID: "refresh-preview-data", Method: http.MethodPost, Path: envPathT + "/preview/refresh-data",
		Summary: "Copy a preview's databases from its base again", Tags: tags,
		Description: "Each database whose base service copies its data into previews is stopped, emptied and filled again as at the preview's creation (ADR 0092); then the preview's other services restart. The base is only read. Answers at once; the preview's databases show how it goes. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		EnvPath
		Body apitypes.RefreshPreviewDataRequest
	}) (*out[apitypes.Preview], error) {
		return reply(s.core.RefreshPreviewData(ctx, actor, in.ref(), in.Body))
	})
}
