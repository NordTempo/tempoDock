package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// Groups and grants (ADR 0080): an org's groups, their members and role
// grants, one person's one-off grants, and a person's effective access.

// RolePath addresses one of an org's own roles by name.
type RolePath struct {
	Role string `path:"role" doc:"The role's name"`
}

// GroupPath addresses a group by name or ID.
type GroupPath struct {
	Group string `path:"group" doc:"The group's name or ID"`
}

func registerGroups(s *Server, a huma.API) {
	tags := []string{"access"}
	authed(s, a, huma.Operation{
		OperationID: "list-roles", Method: http.MethodGet, Path: Prefix + "/roles",
		Summary: "The built-in roles", Tags: tags,
		Description: "Every area role with its levels and the permissions each level holds, the same in every organization. Grants name them as area:level (deploy:edit), approver or full.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.AreaRole], error) {
		return reply(core.AreaRoles(), nil)
	})
	authed(s, a, huma.Operation{
		OperationID: "list-custom-roles", Method: http.MethodGet, Path: Prefix + "/custom-roles",
		Summary: "The organization's own roles", Tags: tags,
		Description: "Roles made of built-in roles and single permissions under one name, with what they add up to and who holds them. Grants name them by name. Needs members.read.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.Role], error) {
		return reply(s.core.Roles(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "create-custom-role", Method: http.MethodPost, Path: Prefix + "/custom-roles",
		Summary: "Make a role", Tags: tags, DefaultStatus: http.StatusCreated,
		Description: "Built-in roles (includes, like deploy:edit) and permissions (like errors.manage) under a name no built-in role has. Needs roles.manage.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body apitypes.RoleRequest
	}) (*out[apitypes.Role], error) {
		return reply(s.core.CreateRole(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-custom-role", Method: http.MethodGet, Path: Prefix + "/custom-roles/{role}",
		Summary: "One of the organization's roles", Tags: tags, Description: "Needs members.read.",
	}, func(ctx context.Context, actor core.Actor, in *RolePath) (*out[apitypes.Role], error) {
		return reply(s.core.Role(ctx, actor, in.Role))
	})
	authed(s, a, huma.Operation{
		OperationID: "update-custom-role", Method: http.MethodPut, Path: Prefix + "/custom-roles/{role}",
		Summary: "Change a role", Tags: tags,
		Description: "Its name, description and parts, all as given; who holds it holds the new parts from their next request. Needs roles.manage.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		RolePath
		Body apitypes.RoleRequest
	}) (*out[apitypes.Role], error) {
		return reply(s.core.UpdateRole(ctx, actor, in.Role, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "delete-custom-role", Method: http.MethodDelete, Path: Prefix + "/custom-roles/{role}",
		Summary: "Remove a role", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "Refused while a grant names it. Needs roles.manage.",
	}, func(ctx context.Context, actor core.Actor, in *RolePath) (*struct{}, error) {
		return nil, s.core.DeleteRole(ctx, actor, in.Role)
	})
	authed(s, a, huma.Operation{
		OperationID: "get-agent-settings", Method: http.MethodGet, Path: Prefix + "/me/agent-settings",
		Summary: "What your agents may do", Tags: tags,
		Description: "ADR 0080: what your `dock login` tokens may use of your access in this organization, per area role and environment kind, and where they ask you first. agent 0 is all of them; an agent's ID its own narrowing.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Agent int64 `query:"agent" doc:"One agent's narrowing; 0 or none for all your agents"`
	}) (*out[apitypes.AgentSettings], error) {
		return reply(s.core.AgentSettings(ctx, actor, in.Agent))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-agent-settings", Method: http.MethodPut, Path: Prefix + "/me/agent-settings",
		Summary: "Change what your agents may do", Tags: tags,
		Description: "The cells given replace theirs. An agent may tighten its setting; loosening needs you signed in, recently.",
	}, func(ctx context.Context, actor core.Actor, in *struct{ Body apitypes.AgentSettings }) (*out[apitypes.AgentSettings], error) {
		return reply(s.core.SetAgentSettings(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "list-groups", Method: http.MethodGet, Path: Prefix + "/groups",
		Summary: "List groups", Tags: tags,
		Description: "The organization's groups with their members and grants. Needs members.read.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.Group], error) {
		return reply(s.core.ListGroups(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "create-group", Method: http.MethodPost, Path: Prefix + "/groups",
		Summary: "Create a group", Tags: tags, DefaultStatus: http.StatusCreated,
		Description: "An empty group; give it grants and members next. Needs members.manage.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body apitypes.CreateGroupRequest
	}) (*out[apitypes.Group], error) {
		return reply(s.core.CreateGroup(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "rename-group", Method: http.MethodPatch, Path: Prefix + "/groups/{group}",
		Summary: "Rename a group", Tags: tags, Description: "Needs members.manage.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		GroupPath
		Body apitypes.CreateGroupRequest
	}) (*out[apitypes.Group], error) {
		return reply(s.core.RenameGroup(ctx, actor, in.Group, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "delete-group", Method: http.MethodDelete, Path: Prefix + "/groups/{group}",
		Summary: "Remove a group", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "Its members lose what it gave them. Refused if nobody would keep Full access. Needs members.manage.",
	}, func(ctx context.Context, actor core.Actor, in *GroupPath) (*struct{}, error) {
		return nil, s.core.DeleteGroup(ctx, actor, in.Group)
	})
	authed(s, a, huma.Operation{
		OperationID: "add-group-member", Method: http.MethodPut, Path: Prefix + "/groups/{group}/members/{member}",
		Summary: "Put a member in a group", Tags: tags, Description: "member is an email or a user ID. Needs members.manage.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		GroupPath
		Member string `path:"member"`
	}) (*out[apitypes.Group], error) {
		return reply(s.core.AddGroupMember(ctx, actor, in.Group, in.Member))
	})
	authed(s, a, huma.Operation{
		OperationID: "remove-group-member", Method: http.MethodDelete, Path: Prefix + "/groups/{group}/members/{member}",
		Summary: "Take a member out of a group", Tags: tags,
		Description: "Refused if nobody would keep Full access. Needs members.manage.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		GroupPath
		Member string `path:"member"`
	}) (*out[apitypes.Group], error) {
		return reply(s.core.RemoveGroupMember(ctx, actor, in.Group, in.Member))
	})
	authed(s, a, huma.Operation{
		OperationID: "grant-group", Method: http.MethodPost, Path: Prefix + "/groups/{group}/grants",
		Summary: "Give a group a role at a place", Tags: tags,
		Description: "A built-in role on the organization, a project or one environment, optionally for some environment kinds, optionally for a while. Needs members.manage.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		GroupPath
		Body apitypes.GrantRequest
	}) (*out[apitypes.Group], error) {
		return reply(s.core.GrantGroup(ctx, actor, in.Group, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "list-member-grants", Method: http.MethodGet, Path: Prefix + "/grants",
		Summary: "List one-off grants", Tags: tags, Description: "Grants given to one person, not a group. Needs members.read.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.Grant], error) {
		return reply(s.core.MemberGrants(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "grant-member", Method: http.MethodPost, Path: Prefix + "/members/{member}/grants",
		Summary: "Give one member a role at a place", Tags: tags, DefaultStatus: http.StatusCreated,
		Description: "A one-off grant, not through a group. Needs members.manage.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Member string `path:"member"`
		Body   apitypes.GrantRequest
	}) (*out[apitypes.Grant], error) {
		return reply(s.core.GrantMember(ctx, actor, in.Member, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "revoke-grant", Method: http.MethodDelete, Path: Prefix + "/grants/{id}",
		Summary: "End a grant", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "A group's grant or a one-off one. Refused if nobody would keep Full access. Needs members.manage.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ID int64 `path:"id"`
	}) (*struct{}, error) {
		return nil, s.core.RevokeGrant(ctx, actor, in.ID)
	})
	authed(s, a, huma.Operation{
		OperationID: "my-access", Method: http.MethodGet, Path: Prefix + "/access",
		Summary: "What you can do here", Tags: tags,
		Description: "Your effective access: each area role's level on the organization and in each environment, and the grants that give it.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.EffectiveAccess], error) {
		return reply(s.core.EffectiveAccess(ctx, actor, ""))
	})
	authed(s, a, huma.Operation{
		OperationID: "member-access", Method: http.MethodGet, Path: Prefix + "/access/{member}",
		Summary: "What a member can do", Tags: tags,
		Description: "A member's effective access, as /access gives yours. Needs members.read.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Member string `path:"member"`
	}) (*out[apitypes.EffectiveAccess], error) {
		return reply(s.core.EffectiveAccess(ctx, actor, in.Member))
	})
}
