package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// registerAppTemplates registers the app template catalog and using a
// template in an environment (ADR 0081).
func registerAppTemplates(s *Server, a huma.API) {
	tags := []string{"templates"}
	authed(s, a, huma.Operation{
		OperationID: "list-app-templates", Method: http.MethodGet, Path: Prefix + "/app-templates",
		Summary: "List app templates", Tags: tags,
		Description: "The catalog: apps (one or more services each) and the database templates. Use one with use-app-template.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.AppTemplate], error) {
		return reply(s.core.ListAppTemplates(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-app-template", Method: http.MethodGet, Path: Prefix + "/app-templates/{template}",
		Summary: "Get an app template", Tags: tags,
		Description: "Its services, inputs and notes.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Template string `path:"template" doc:"Template name"`
	}) (*out[apitypes.AppTemplate], error) {
		return reply(s.core.GetAppTemplate(ctx, actor, in.Template))
	})
	authed(s, a, huma.Operation{
		OperationID: "use-app-template", Method: http.MethodPost, Path: envPathT + "/app-templates/{template}",
		Summary: "Use an app template", Tags: tags,
		Description: "Creates the template's services in the environment and deploys them: the main one named `name` (default the template's name), the others NAME-<part>. " +
			"Other staged changes in the environment stay staged. With `plan`, only says what it would create. Scope: deploy, or read with `plan`.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		EnvPath
		Template string `path:"template" doc:"Template name"`
		Body     apitypes.UseTemplateRequest
	}) (*out[apitypes.UseTemplateResult], error) {
		return reply(s.core.UseAppTemplate(ctx, actor, in.ref(), in.Template, in.Body))
	})
}
