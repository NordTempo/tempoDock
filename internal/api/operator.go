package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// The platform console's organizations (ADR 0062): operators list every
// org with its size and use, and administer one without being a member.
// Content (variables, logs, data, code) is not reachable here; Join as
// operator is the audited way in.

// ConsoleOrgPath addresses an org on the console.
type ConsoleOrgPath struct {
	Org string `path:"org" doc:"The organization's slug"`
}

// ConsoleMemberPath addresses a member of an org on the console.
type ConsoleMemberPath struct {
	ConsoleOrgPath
	User int64 `path:"user" doc:"The member's user ID"`
}

const consoleOrgT = Prefix + "/console/orgs/{org}"

func registerOperator(s *Server, a huma.API) {
	tags := []string{"console"}
	authed(s, a, huma.Operation{
		OperationID: "list-console-orgs", Method: http.MethodGet, Path: Prefix + "/console/orgs",
		Summary: "List every organization", Tags: tags,
		Description: "Every organization with its owner and admins, members, projects and services, status and resource use (CPU, memory, volumes, backups, registry images, bandwidth), biggest first. Volumes come from the servers' last disk readings; with read_disks, servers not read in the last minute are read again in the background, and the result arrives as `disk` events. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ReadDisks bool `query:"read_disks" doc:"Read stale servers' disks again, for fresh volume sizes"`
	}) (*out[[]apitypes.OperatorOrg], error) {
		return reply(s.core.OperatorOrgs(ctx, actor, in.ReadDisks))
	})
	authed(s, a, huma.Operation{
		OperationID: "rename-console-org", Method: http.MethodPatch, Path: consoleOrgT,
		Summary: "Rename an organization", Tags: tags,
		Description: "Changes any organization's display name; its slug stays. Audited in its log and the platform's. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ConsoleOrgPath
		Body apitypes.UpdateOrgRequest
	}) (*out[apitypes.Org], error) {
		return reply(s.core.OperatorRenameOrg(ctx, actor, in.Org, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "delete-console-org", Method: http.MethodDelete, Path: consoleOrgT,
		Summary: "Delete an organization", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "confirm must be the organization's slug. One with projects keeps their history: they are archived (containers stopped, volumes and releases kept), its members, invites and variables removed, its tokens revoked, and it is hidden. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ConsoleOrgPath
		Confirm string `query:"confirm" doc:"The organization's slug, typed to confirm"`
	}) (*struct{}, error) {
		return nil, s.core.OperatorDeleteOrg(ctx, actor, in.Org, in.Confirm)
	})
	for _, op := range []struct {
		id, path, summary, desc string
		suspend                 bool
	}{
		{"suspend-console-org", "/suspend", "Suspend an organization", "Its apps keep running; every deploy and change in it is refused, and its members and tokens may only read, until it is unsuspended. Operators only.", true},
		{"unsuspend-console-org", "/unsuspend", "Unsuspend an organization", "Deploys and changes work again. Operators only.", false},
	} {
		authed(s, a, huma.Operation{
			OperationID: op.id, Method: http.MethodPost, Path: consoleOrgT + op.path,
			Summary: op.summary, Description: op.desc, Tags: tags,
		}, func(ctx context.Context, actor core.Actor, in *ConsoleOrgPath) (*out[apitypes.OperatorOrg], error) {
			return reply(s.core.SuspendOrg(ctx, actor, in.Org, op.suspend))
		})
	}
	authed(s, a, huma.Operation{
		OperationID: "stop-console-org", Method: http.MethodPost, Path: consoleOrgT + "/stop",
		Summary: "Stop an organization's services", Tags: tags,
		Description: "Stops every running service, as `dock stop` does: domains show the 503 page, volumes and history stay, starting or deploying one runs it again. Services that couldn't stop are listed. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *ConsoleOrgPath) (*out[apitypes.StoppedOrgServices], error) {
		return reply(s.core.StopOrgServices(ctx, actor, in.Org))
	})
	authed(s, a, huma.Operation{
		OperationID: "join-console-org", Method: http.MethodPost, Path: consoleOrgT + "/join",
		Summary: "Join an organization as operator", Tags: tags,
		Description: "Makes you an admin of the organization for a while (1 to 24 hours), to see its content when needed. Its admins see it in their activity log, and the membership ends by itself. People only, not tokens. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ConsoleOrgPath
		Body apitypes.JoinOrgRequest
	}) (*out[apitypes.OrgMembershipGrant], error) {
		return reply(s.core.JoinOrgAsOperator(ctx, actor, in.Org, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "list-console-org-members", Method: http.MethodGet, Path: consoleOrgT + "/members",
		Summary: "List an organization's members", Tags: tags,
		Description: "Members with their role; an operator who joined for a while shows with when it ends. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *ConsoleOrgPath) (*out[[]apitypes.User], error) {
		return reply(s.core.OperatorMembers(ctx, actor, in.Org))
	})
	authed(s, a, huma.Operation{
		OperationID: "add-console-org-member", Method: http.MethodPost, Path: consoleOrgT + "/members",
		Summary: "Add a member to an organization", Tags: tags, DefaultStatus: http.StatusCreated,
		Description: "Adds an existing user with a role: how an organization without an admin gets one. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ConsoleOrgPath
		Body apitypes.OperatorMemberRequest
	}) (*out[apitypes.User], error) {
		return reply(s.core.OperatorAddMember(ctx, actor, in.Org, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-console-org-member", Method: http.MethodPut, Path: consoleOrgT + "/members/{user}",
		Summary: "Change a member's role", Tags: tags,
		Description: "The organization keeps an admin of its own. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ConsoleMemberPath
		Body apitypes.OperatorMemberRequest
	}) (*out[apitypes.User], error) {
		return reply(s.core.OperatorSetMemberRole(ctx, actor, in.Org, in.User, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "remove-console-org-member", Method: http.MethodDelete, Path: consoleOrgT + "/members/{user}",
		Summary: "Remove a member", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "Their account stays. The organization keeps an admin of its own. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *ConsoleMemberPath) (*struct{}, error) {
		return nil, s.core.OperatorRemoveMember(ctx, actor, in.Org, in.User)
	})
}
