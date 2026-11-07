package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// The org the caller acts in (ADR 0027): its name, members, invites,
// variables and registry credentials (ADR 0051), under /org.
func registerOrgSettings(s *Server, a huma.API) {
	tags := []string{"organizations"}
	orgPath := Prefix + "/org"
	authed(s, a, huma.Operation{
		OperationID: "get-org", Method: http.MethodGet, Path: orgPath,
		Summary: "The organization you act in", Description: "With your role in it. Scope: read.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.Org], error) {
		return reply(s.core.GetOrg(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "update-org", Method: http.MethodPatch, Path: orgPath,
		Summary: "Rename the organization or change its slug", Description: "Changes its name, its slug (its address), or both. The old slug keeps naming the organization until another one takes it. Scope: admin, unlimited.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct{ Body apitypes.UpdateOrgRequest }) (*out[apitypes.Org], error) {
		return reply(s.core.RenameOrg(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "delete-org", Method: http.MethodDelete, Path: orgPath,
		Summary: "Delete the organization", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "Only without projects (archived ones count). Its members, invites, variables, registry credentials and data policy go and its tokens are revoked; its audit log stays. Scope: admin, unlimited.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*struct{}, error) {
		return nil, s.core.DeleteOrg(ctx, actor)
	})
	authed(s, a, huma.Operation{
		OperationID: "leave-org", Method: http.MethodPost, Path: orgPath + "/leave",
		Summary: "Leave the organization", Description: "Users only. The last admin can't leave.", Tags: tags, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*struct{}, error) {
		return nil, s.core.LeaveOrg(ctx, actor)
	})

	authed(s, a, huma.Operation{
		OperationID: "list-org-members", Method: http.MethodGet, Path: orgPath + "/members",
		Summary: "List members", Description: "With their role, live sessions and usable tokens. Scope: admin, unlimited.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.User], error) {
		return reply(s.core.ListUsers(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-member-role", Method: http.MethodPatch, Path: orgPath + "/members/{id}",
		Summary: "Change a member's role", Description: "The organization always keeps an admin. Scope: admin, unlimited.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ID   int64 `path:"id"`
		Body apitypes.SetMemberRoleRequest
	}) (*out[apitypes.User], error) {
		return reply(s.core.SetMemberRole(ctx, actor, in.ID, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "remove-member", Method: http.MethodDelete, Path: orgPath + "/members/{id}",
		Summary: "Remove a member", Description: "Their account stays. Not for yourself: leave instead. Scope: admin, unlimited.",
		Tags: tags, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ID int64 `path:"id"`
	}) (*struct{}, error) {
		return nil, s.core.RemoveMember(ctx, actor, in.ID)
	})

	authed(s, a, huma.Operation{
		OperationID: "list-invites", Method: http.MethodGet, Path: orgPath + "/invites",
		Summary: "List open invites", Description: "Newest first, expired ones too until revoked. Scope: admin, unlimited.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.Invite], error) {
		return reply(s.core.ListInvites(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "create-invite", Method: http.MethodPost, Path: orgPath + "/invites",
		Summary: "Invite someone", Tags: tags, DefaultStatus: http.StatusCreated,
		Description: "The link's code is returned once. It works once, for 7 days, and only for the email: a user signed in with it, or someone creating its account from the link. Scope: admin, unlimited.",
	}, func(ctx context.Context, actor core.Actor, in *struct{ Body apitypes.CreateInviteRequest }) (*out[apitypes.CreatedInvite], error) {
		return reply(s.core.CreateInvite(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "revoke-invite", Method: http.MethodDelete, Path: orgPath + "/invites/{id}",
		Summary: "Revoke an invite", Description: "Scope: admin, unlimited.", Tags: tags, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ID int64 `path:"id"`
	}) (*struct{}, error) {
		return nil, s.core.RevokeInvite(ctx, actor, in.ID)
	})
	public(a, huma.Operation{
		OperationID: "get-invite", Method: http.MethodGet, Path: Prefix + "/invites/{code}",
		Summary: "Look at an invite", Tags: tags,
		Description: "For whoever holds its link, signed in or not (ADR 0060): the organization, the role, the email invited, who invited and until when. A code that is unknown, used, revoked or expired is 404. Rate limited per client IP like logins.",
		Errors:      []int{http.StatusNotFound, http.StatusTooManyRequests},
	}, func(ctx context.Context, in *inviteInput) (*out[apitypes.InviteInfo], error) {
		info, err := s.core.Invite(ctx, in.Code, in.client.ip)
		if err != nil {
			return nil, s.problem(err)
		}
		return reply(info, nil)
	})
	public(a, huma.Operation{
		OperationID: "invite-signup", Method: http.MethodPost, Path: Prefix + "/invites/{code}/signup",
		Summary: "Create an account from an invite", Tags: tags,
		Description: "For someone invited without an account (ADR 0060): creates the account for the invite's email with this name and password (at least 12 characters), accepts the invite and signs in, setting the session cookie as a login does. Refused (409) when the email has an account (sign in and accept instead) and (403) when the organization requires its single sign-on. Rate limited like logins.",
		Errors:      []int{http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusTooManyRequests},
	}, func(ctx context.Context, in *inviteSignUpInput) (*loginOutput, error) {
		res, err := s.core.SignUpWithInvite(ctx, in.Code, in.Body, in.client.ip)
		if err != nil {
			return nil, s.problem(err)
		}
		return in.client.signedIn(res), nil
	})
	authed(s, a, huma.Operation{
		OperationID: "accept-invite", Method: http.MethodPost, Path: Prefix + "/invites/{code}/accept",
		Summary: "Accept an invite", Description: "Makes you a member with the invite's role, if it is open and for your email.", Tags: tags,
		Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusGone},
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Code string `path:"code"`
	}) (*out[apitypes.Org], error) {
		return reply(s.core.AcceptInvite(ctx, actor, in.Code))
	})

	tags = []string{"variables"}
	authed(s, a, huma.Operation{
		OperationID: "list-org-variables", Method: http.MethodGet, Path: orgPath + "/variables",
		Summary: "List organization variables", Tags: tags,
		Description: "Variables any project of the organization may reference as `${{org.KEY}}`, with where each is used. Values only with `reveal=true` (admin). Scope: read.",
	}, func(ctx context.Context, actor core.Actor, in *RevealQuery) (*out[[]apitypes.OrgVariable], error) {
		return reply(s.core.ListOrgVariables(ctx, actor, in.Reveal))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-org-variable", Method: http.MethodPut, Path: orgPath + "/variables/{key}",
		Summary: "Set an organization variable", Tags: tags,
		Description: "Services that reference it get the new value on their next release; redeploy starts those. Scope: admin, unlimited.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Key  string `path:"key"`
		Body apitypes.SetOrgVariableRequest
	}) (*out[apitypes.OrgVariable], error) {
		return reply(s.core.SetOrgVariable(ctx, actor, in.Key, in.Body.Value))
	})
	authed(s, a, huma.Operation{
		OperationID: "delete-org-variable", Method: http.MethodDelete, Path: orgPath + "/variables/{key}",
		Summary: "Remove an organization variable", Description: "Only one nothing references. Scope: admin, unlimited.",
		Tags: tags, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Key string `path:"key"`
	}) (*struct{}, error) {
		return nil, s.core.DeleteOrgVariable(ctx, actor, in.Key)
	})
	authed(s, a, huma.Operation{
		OperationID: "redeploy-org-variable", Method: http.MethodPost, Path: orgPath + "/variables/{key}/redeploy",
		Summary: "Redeploy the services that use an organization variable", Tags: tags,
		Description: "A config release for every running service that references it, directly or through its environment's shared variables. Services you can't deploy, not running or deploying already are skipped with the reason. Scope: deploy on each.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Key string `path:"key"`
	}) (*out[apitypes.OrgVariableRollout], error) {
		return reply(s.core.RedeployOrgVariable(ctx, actor, in.Key))
	})

	tags = []string{"registry"}
	authed(s, a, huma.Operation{
		OperationID: "list-registry-credentials", Method: http.MethodGet, Path: orgPath + "/registry-credentials",
		Summary: "List registry credentials", Tags: tags,
		Description: "How the organization signs in to private image registries, by host. Passwords and tokens are never returned. Scope: admin, unlimited.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.RegistryCredential], error) {
		return reply(s.core.ListRegistryCredentials(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-registry-credential", Method: http.MethodPut, Path: orgPath + "/registry-credentials/{host}",
		Summary: "Set a registry credential", Tags: tags,
		Description: "Adds or replaces the username and password (or token) deploys sign in with to pull images from the registry host, like `ghcr.io` or `registry.example.com:5000`; Docker Hub is `docker.io`. Takes effect on the next pull. Scope: admin, unlimited.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Host string `path:"host" example:"ghcr.io"`
		Body apitypes.SetRegistryCredentialRequest
	}) (*out[apitypes.RegistryCredential], error) {
		return reply(s.core.SetRegistryCredential(ctx, actor, in.Host, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "delete-registry-credential", Method: http.MethodDelete, Path: orgPath + "/registry-credentials/{host}",
		Summary: "Remove a registry credential", Description: "Pulls from the host are anonymous again. Scope: admin, unlimited.",
		Tags: tags, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Host string `path:"host"`
	}) (*struct{}, error) {
		return nil, s.core.DeleteRegistryCredential(ctx, actor, in.Host)
	})
}

// inviteInput is an invite link's code, and how the request arrived (for
// rate limits).
type inviteInput struct {
	Code   string `path:"code"`
	client client
}

// Resolve records how the request arrived.
func (in *inviteInput) Resolve(ctx huma.Context) []error {
	in.client = clientOf(ctx)
	return nil
}

// inviteSignUpInput creates an account from an invite link.
type inviteSignUpInput struct {
	Code   string `path:"code"`
	Body   apitypes.InviteSignUpRequest
	client client
}

// Resolve records how the request arrived.
func (in *inviteSignUpInput) Resolve(ctx huma.Context) []error {
	in.client = clientOf(ctx)
	return nil
}
