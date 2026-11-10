package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// registerDiagnoses adds a service's deploy diagnoses (ADR 0071).
func registerDiagnoses(s *Server, a huma.API) {
	tags := []string{"releases"}
	authed(s, a, huma.Operation{
		OperationID: "list-diagnoses", Method: http.MethodGet, Path: servicePathT + "/diagnoses",
		Summary: "List diagnoses", Tags: tags,
		Description: "A service's deploy diagnoses, newest first, without their bundles: one per failed release, captured when it failed. Scope: read.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Release int64 `query:"release" minimum:"0" doc:"Only this release's"`
		Limit   int   `query:"limit" default:"20" minimum:"1" maximum:"20"`
	}) (*out[[]apitypes.DiagnosisSummary], error) {
		return reply(s.core.ListDiagnoses(ctx, actor, in.ref(), in.Release, in.Limit))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-diagnosis", Method: http.MethodGet, Path: servicePathT + "/diagnoses/{id}",
		Summary: "Get a diagnosis", Tags: tags,
		Description: "One failure's bundle: the likely cause and what was ruled out, the failure, the health check, what changed since the last good release, the pre-deploy command, the last lines of the container and the build, error issues and resources around it. Secret values in log lines are masked unless the caller can reveal them. Scope: read.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		ID int64 `path:"id" minimum:"1"`
	}) (*out[apitypes.Diagnosis], error) {
		return reply(s.core.GetDiagnosis(ctx, actor, in.ref(), in.ID))
	})
}
