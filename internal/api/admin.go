package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
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
		Summary: "List users", Description: "With each user's live sessions and usable tokens. Scope: admin, unlimited.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.User], error) {
		return reply(s.core.ListUsers(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "create-user", Method: http.MethodPost, Path: Prefix + "/users",
		Summary: "Create a user", Description: "Scope: admin, unlimited.", Tags: tags, DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, actor core.Actor, in *struct{ Body apitypes.CreateUserRequest }) (*out[apitypes.User], error) {
		return reply(s.core.CreateUser(ctx, actor, in.Body))
	})
	userIDOp := func(op huma.Operation, disable bool) {
		authed(s, a, op, func(ctx context.Context, actor core.Actor, in *struct {
			ID int64 `path:"id"`
		}) (*out[apitypes.User], error) {
			return reply(s.core.SetUserDisabled(ctx, actor, in.ID, disable))
		})
	}
	userIDOp(huma.Operation{
		OperationID: "disable-user", Method: http.MethodPost, Path: Prefix + "/users/{id}/disable",
		Summary: "Disable a user", Tags: tags,
		Description: "The user can't log in, their sessions end and their `dock login` tokens are revoked now. Tokens they created by hand keep working until revoked. Scope: admin, unlimited.",
	}, true)
	userIDOp(huma.Operation{
		OperationID: "enable-user", Method: http.MethodPost, Path: Prefix + "/users/{id}/enable",
		Summary: "Enable a user", Description: "Lets a disabled user log in again. Scope: admin, unlimited.", Tags: tags,
	}, false)
	authed(s, a, huma.Operation{
		OperationID: "reset-password", Method: http.MethodPost, Path: Prefix + "/users/{id}/reset-password",
		Summary: "Reset a user's password", Tags: tags,
		Description: "Sets the given password, or generates one and returns it once. The user's sessions end and their `dock login` tokens are revoked. Not for your own account (use change-password). Scope: admin, unlimited.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ID   int64 `path:"id"`
		Body *apitypes.ResetPasswordRequest
	}) (*out[apitypes.ResetPasswordResult], error) {
		var req apitypes.ResetPasswordRequest
		if in.Body != nil {
			req = *in.Body
		}
		return reply(s.core.ResetPassword(ctx, actor, in.ID, req))
	})
	authed(s, a, huma.Operation{
		OperationID: "delete-user", Method: http.MethodDelete, Path: Prefix + "/users/{id}",
		Summary: "Delete a user", Description: "Ends their sessions and revokes their `dock login` tokens. Their audit history and the tokens they created by hand stay. Scope: admin, unlimited.",
		Tags: tags, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ID int64 `path:"id"`
	}) (*struct{}, error) {
		return nil, s.core.DeleteUser(ctx, actor, in.ID)
	})

	authed(s, a, huma.Operation{
		OperationID: "list-audit", Method: http.MethodGet, Path: Prefix + "/audit",
		Summary: "Audit log", Description: "Newest first. Tokens limited to a project or environment only see events there.",
		Tags: []string{"audit"},
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Project     string `query:"project"`
		Environment string `query:"environment" doc:"Requires project"`
		Service     string `query:"service" doc:"Only events targeting this service; requires project and environment"`
		Action      string `query:"action" doc:"Action prefix, e.g. release. or domain.add"`
		Before      int64  `query:"before" doc:"Only events with a smaller ID (paging)"`
		Limit       int    `query:"limit" default:"100" minimum:"1" maximum:"500"`
	}) (*out[[]apitypes.AuditEvent], error) {
		return reply(s.core.ListAudit(ctx, actor, core.AuditFilter{
			Project: in.Project, Environment: in.Environment, Service: in.Service, Action: in.Action, Before: in.Before, Limit: in.Limit,
		}))
	})

}
