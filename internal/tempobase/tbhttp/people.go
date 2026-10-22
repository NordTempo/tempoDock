package tbhttp

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/tempobase"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
)

// IDPath is a numeric ID in a path.
type IDPath struct {
	ID int64 `path:"id"`
}

type updateUserInput struct {
	IDPath
	Body tbtypes.UpdateUserRequest
}

type updateTokenInput struct {
	IDPath
	Body apitypes.UpdateTokenRequest
}

func registerPeople(s *server, a huma.API) {
	authed(s, a, huma.Operation{
		OperationID: "list-users", Method: http.MethodGet, Path: Prefix + "/users",
		Summary: "List people", Description: "Admin only.", Tags: []string{"people"},
	}, func(ctx context.Context, act tempobase.Actor, _ *struct{}) (*out[[]tbtypes.User], error) {
		return reply(s.app.ListUsers(ctx, act))
	})
	authed(s, a, huma.Operation{
		OperationID: "update-user", Method: http.MethodPatch, Path: Prefix + "/users/{id}",
		Summary:     "Change a person's name, role or disabled state",
		Description: "Admin only, not on yourself. Disabling ends their sessions and tokens at once. The last admin stays an admin.",
		Tags:        []string{"people"},
	}, func(ctx context.Context, act tempobase.Actor, in *updateUserInput) (*out[tbtypes.User], error) {
		return reply(s.app.UpdateUser(ctx, act, in.ID, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "delete-user", Method: http.MethodDelete, Path: Prefix + "/users/{id}",
		Summary:     "Delete a person",
		Description: "Admin only, not yourself or the last admin. Their sessions, tokens and saved queries go with them; their audit history stays.",
		Tags:        []string{"people"}, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, act tempobase.Actor, in *IDPath) (*empty, error) {
		return nil, s.app.DeleteUser(ctx, act, in.ID)
	})
	authed(s, a, huma.Operation{
		OperationID: "create-reset-link", Method: http.MethodPost, Path: Prefix + "/users/{id}/reset-link",
		Summary:     "Create a password reset link",
		Description: "Admin only. Copy the url to the person; it works once, for 24 hours, and replaces their earlier reset links.",
		Tags:        []string{"people"},
	}, func(ctx context.Context, act tempobase.Actor, in *IDPath) (*out[tbtypes.Link], error) {
		return reply(s.app.ResetLink(ctx, act, in.ID, clientFrom(ctx).base))
	})
	authed(s, a, huma.Operation{
		OperationID: "list-invites", Method: http.MethodGet, Path: Prefix + "/invites",
		Summary: "List open invites", Description: "Admin only. Without their URLs.", Tags: []string{"people"},
	}, func(ctx context.Context, act tempobase.Actor, _ *struct{}) (*out[[]tbtypes.Link], error) {
		return reply(s.app.ListInvites(ctx, act))
	})
	authed(s, a, huma.Operation{
		OperationID: "create-invite", Method: http.MethodPost, Path: Prefix + "/invites",
		Summary:     "Invite a person",
		Description: "Admin only. Copy the url to the person; it works once, for 7 days.",
		Tags:        []string{"people"},
	}, func(ctx context.Context, act tempobase.Actor, in *struct{ Body tbtypes.CreateInviteRequest }) (*out[tbtypes.Link], error) {
		return reply(s.app.CreateInvite(ctx, act, in.Body, clientFrom(ctx).base))
	})
	authed(s, a, huma.Operation{
		OperationID: "delete-invite", Method: http.MethodDelete, Path: Prefix + "/invites/{id}",
		Summary: "Withdraw an invite", Description: "Admin only.", Tags: []string{"people"}, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, act tempobase.Actor, in *IDPath) (*empty, error) {
		return nil, s.app.DeleteInvite(ctx, act, in.ID)
	})
}

func registerTokens(s *server, a huma.API) {
	authed(s, a, huma.Operation{
		OperationID: "list-tokens", Method: http.MethodGet, Path: Prefix + "/tokens",
		Summary: "List API tokens", Description: "Your own; every token for admins. Never their secrets.", Tags: []string{"tokens"},
	}, func(ctx context.Context, act tempobase.Actor, _ *struct{}) (*out[[]tbtypes.Token], error) {
		return reply(s.app.ListTokens(ctx, act))
	})
	authed(s, a, huma.Operation{
		OperationID: "create-token", Method: http.MethodPost, Path: Prefix + "/tokens",
		Summary:     "Create an API token",
		Description: "For an agent or script, from a session. The secret is shown once. The token follows the policy's tokens rule or its own data access, capped by your role, and may be limited to some connections.",
		Tags:        []string{"tokens"},
	}, func(ctx context.Context, act tempobase.Actor, in *struct{ Body tbtypes.CreateTokenRequest }) (*out[tbtypes.CreatedToken], error) {
		return reply(s.app.CreateToken(ctx, act, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "update-token", Method: http.MethodPatch, Path: Prefix + "/tokens/{id}",
		Summary:     "Change a token's data access",
		Description: "Its owner or an admin. `data: null` follows the policy's tokens rule again.",
		Tags:        []string{"tokens"},
	}, func(ctx context.Context, act tempobase.Actor, in *updateTokenInput) (*out[tbtypes.Token], error) {
		return reply(s.app.UpdateToken(ctx, act, in.ID, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "revoke-token", Method: http.MethodDelete, Path: Prefix + "/tokens/{id}",
		Summary: "Revoke a token", Description: "Its owner or an admin. It stops working at once.",
		Tags: []string{"tokens"}, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, act tempobase.Actor, in *IDPath) (*empty, error) {
		return nil, s.app.RevokeToken(ctx, act, in.ID)
	})
}
