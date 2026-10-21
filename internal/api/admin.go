package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/platform/internal/apitypes"
	"github.com/NordTempo/platform/internal/core"
)

func registerAdmin(s *Server, a huma.API) {
	tags := []string{"tokens"}
	authed(s, a, huma.Operation{
		OperationID: "list-tokens", Method: http.MethodGet, Path: Prefix + "/tokens",
		Summary: "List tokens", Description: "Scope: admin, unlimited.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.Token], error) {
		return reply(s.core.ListTokens(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "create-token", Method: http.MethodPost, Path: Prefix + "/tokens",
		Summary: "Create a token", Description: "The secret is returned once and never again. Scope: admin, unlimited.",
		Tags: tags, DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, actor core.Actor, in *struct{ Body apitypes.CreateTokenRequest }) (*out[apitypes.CreatedToken], error) {
		return reply(s.core.CreateToken(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "revoke-token", Method: http.MethodDelete, Path: Prefix + "/tokens/{id}",
		Summary: "Revoke a token", Description: "Scope: admin, unlimited.", Tags: tags, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ID int64 `path:"id"`
	}) (*struct{}, error) {
		return nil, s.core.RevokeToken(ctx, actor, in.ID)
	})

	tags = []string{"users"}
	authed(s, a, huma.Operation{
		OperationID: "list-users", Method: http.MethodGet, Path: Prefix + "/users",
		Summary: "List users", Description: "Scope: admin, unlimited.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.User], error) {
		return reply(s.core.ListUsers(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "create-user", Method: http.MethodPost, Path: Prefix + "/users",
		Summary: "Create a user", Description: "Scope: admin, unlimited.", Tags: tags, DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, actor core.Actor, in *struct{ Body apitypes.CreateUserRequest }) (*out[apitypes.User], error) {
		return reply(s.core.CreateUser(ctx, actor, in.Body))
	})

	authed(s, a, huma.Operation{
		OperationID: "list-audit", Method: http.MethodGet, Path: Prefix + "/audit",
		Summary: "Audit log", Description: "Newest first. Tokens limited to a project or environment only see events there.",
		Tags: []string{"audit"},
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Project     string `query:"project"`
		Environment string `query:"environment" doc:"Requires project"`
		Action      string `query:"action" doc:"Action prefix, e.g. release. or domain.add"`
		Before      int64  `query:"before" doc:"Only events with a smaller ID (paging)"`
		Limit       int    `query:"limit" default:"100" minimum:"1" maximum:"500"`
	}) (*out[[]apitypes.AuditEvent], error) {
		return reply(s.core.ListAudit(ctx, actor, core.AuditFilter{
			Project: in.Project, Environment: in.Environment, Action: in.Action, Before: in.Before, Limit: in.Limit,
		}))
	})

	authed(s, a, huma.Operation{
		OperationID: "list-servers", Method: http.MethodGet, Path: Prefix + "/servers",
		Summary: "List servers", Tags: []string{"servers"},
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.Server], error) {
		return reply(s.core.ListServers(ctx, actor))
	})
}
