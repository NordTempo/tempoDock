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
