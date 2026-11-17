package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Nobody gives more than they hold (ADR 0080): granting a role, putting
// someone in a group, changing a member's role, inviting with one,
// minting a token or widening a custom role needs the caller to hold
// every permission it gives, at least as widely as it reaches. So
// managing members (members.manage) is not a way to Full access.

// covers reports whether a holds every permission in perms on a place as
// wide as (project, env, kinds): project 0 the org, env 0 a project;
// kinds the environment kinds the place covers (an environment's own).
// Actors grants don't decide give what their scope holds (an admin
// anything, as before) inside their token's limit.
func (a Actor) covers(perms Perms, project, env int64, kinds uint8) bool {
	if !a.hasGrants {
		return a.limitCovers(project, env) && (rank(a.Scope) >= rank(apitypes.ScopeAdmin) || perms&^seededPerms(a.Scope) == 0)
	}
	return entriesCover(a.grants, perms, project, env, kinds)
}

// entriesCover reports whether grants hold every permission in perms on a
// place as wide as (project, env, kinds); see covers.
func entriesCover(grants []grantEntry, perms Perms, project, env int64, kinds uint8) bool {
	for p := range permCount {
		if !perms.has(p) {
			continue
		}
		held := false
		for _, g := range grants {
			if g.perms.has(p) && (g.project == 0 || project != 0 && g.project == project) &&
				(g.env == 0 || env != 0 && g.env == env) && g.kinds&kinds == kinds {
				held = true
				break
			}
		}
		if !held {
			return false
		}
	}
	return true
}

// mustAsk is what of perms an agent asks first for (its person's agent
// setting, the org's agent rules) in any of the environment kinds a place
// covers; none for everyone else.
func (a Actor) mustAsk(perms Perms, kinds uint8) Perms {
	if a.agent == nil || !a.hasGrants {
		return 0
	}
	var out Perms
	for i, k := range agentKinds {
		if kinds&k != 0 {
			out |= perms & (a.agent.ask[i] | a.agent.ruleAsk[i])
		}
	}
	return out
}

// mayGive refuses giving perms on a place the caller doesn't hold them
// on (covers), or holds them only after asking (mustAsk); what names
// what is given, place where.
func (a Actor) mayGive(perms Perms, project, env int64, kinds uint8, what, place string) error {
	if asks := a.mustAsk(perms, kinds); asks != 0 {
		// What an agent uses only after asking isn't its own to hand on:
		// whoever got it wouldn't ask.
		var names []string
		for p := range permCount {
			if asks.has(p) {
				names = append(names, p.String())
			}
		}
		return a.refuse(apitypes.Refusal{
			DecidedBy: apitypes.RefusedPerson, Action: "giving " + what, Need: "person", Place: place,
			Reason: fmt.Sprintf("%s can't give %s on %s: it asks first before it uses %s there, and what it gave wouldn't ask", a, what, place, strings.Join(names, ", ")),
			Fix:    "a person gives it from a signed-in session in the web UI",
		})
	}
	if a.covers(perms, project, env, kinds) {
		return nil
	}
	var missing []string
	for p := range permCount {
		if perms.has(p) && !a.covers(1<<p, project, env, kinds) {
			missing = append(missing, p.String())
		}
	}
	return a.refuse(apitypes.Refusal{
		DecidedBy: apitypes.RefusedGrants, Action: "giving " + what, Need: strings.Join(missing, " "), Place: place,
		Reason: fmt.Sprintf("%s can't give %s on %s: it holds less there (%s); nobody gives more than they hold", a, what, place, strings.Join(missing, ", ")),
		Fix:    "someone who holds it there gives it, like a member of Admins",
	})
}

// mayGiveGroup refuses putting someone in a group whose grants the
// caller couldn't give themselves (mayGive, each at its place).
func (c *Core) mayGiveGroup(ctx context.Context, a Actor, g store.Group) error {
	return c.mayGiveGrants(ctx, a, "group "+g.Name, inGroup(g.ID))
}

// mayTakeGroup refuses taking a group's grants away from someone (out of
// the group, or the group itself) when the caller couldn't give them:
// taking away is held to the same rule as giving, so managing members is
// no way to strip those who hold more.
func (c *Core) mayTakeGroup(ctx context.Context, a Actor, g store.Group) error {
	return c.mayGiveGrants(ctx, a, "group "+g.Name+" (taking it away)", inGroup(g.ID))
}

// inGroup picks a group's grants.
func inGroup(id int64) func(store.ListOrgGrantsRow) bool {
	return func(gr store.ListOrgGrantsRow) bool { return gr.GroupID != nil && *gr.GroupID == id }
}

// mayTakeMember refuses taking away everything a member holds in the org
// (removing them) when the caller couldn't give some of it: their own
// grants and their groups'.
func (c *Core) mayTakeMember(ctx context.Context, a Actor, u store.User) error {
	members, err := c.store.ListGroupMembers(ctx, a.OrgID)
	if err != nil {
		return err
	}
	in := map[int64]bool{}
	for _, m := range members {
		if m.UserID == u.ID {
			in[m.GroupID] = true
		}
	}
	return c.mayGiveGrants(ctx, a, "what "+u.Email+" holds (removing them takes it away)", func(gr store.ListOrgGrantsRow) bool {
		return gr.UserID != nil && *gr.UserID == u.ID || gr.GroupID != nil && in[*gr.GroupID]
	})
}

// roleGive refuses giving a role (a role change, an invite's) or, with
// take, taking it away, when the caller doesn't hold what it means: what
// the role's seeded group holds now, which the org may have changed, or
// with that group removed the role's built-in roles (placeInRoleGroup).
func (c *Core) roleGive(ctx context.Context, a Actor, role apitypes.Scope, take bool) error {
	what := "role " + string(role)
	if take {
		what += " (taking it away)"
	}
	for _, sg := range seededGroups {
		if sg.role != role {
			continue
		}
		g, err := c.store.GetSeededGroup(ctx, store.GetSeededGroupParams{OrgID: a.OrgID, Seeded: &sg.key})
		if err == nil {
			return c.mayGiveGrants(ctx, a, what, inGroup(g.ID))
		}
		if !store.IsNotFound(err) {
			return err
		}
	}
	return a.scopeGive(role, 0, 0, uint8(allKinds), what)
}

// mayGiveGrants refuses giving, or taking away, those of the org's grants
// that pick chooses when the caller couldn't give one of them themselves
// (mayGive, each at its place); what names what is given.
func (c *Core) mayGiveGrants(ctx context.Context, a Actor, what string, pick func(store.ListOrgGrantsRow) bool) error {
	grants, err := c.store.ListOrgGrants(ctx, store.ListOrgGrantsParams{OrgID: a.OrgID, Now: ptr(now().Unix())})
	if err != nil {
		return err
	}
	for _, gr := range grants {
		if !pick(gr) {
			continue
		}
		perms, ok := grantPerms(gr.Role, gr.CustomParts)
		if !ok {
			continue
		}
		kinds := uint8(gr.Kinds)
		if gr.EnvironmentID != nil {
			kind, err := c.envKindOf(ctx, a, *gr.EnvironmentID)
			if err != nil {
				return err
			}
			kinds = kind
		}
		place := grantPlaceOf(toGrant(gr))
		if err := a.mayGive(perms, deref(gr.ProjectID), deref(gr.EnvironmentID), kinds, what, place); err != nil {
			return err
		}
	}
	return nil
}

// scopeGive is what a scope's role gives (its seeded group) on the whole
// org, for role changes, invites and tokens.
func (a Actor) scopeGive(s apitypes.Scope, project, env int64, kinds uint8, what string) error {
	perms := scopePerms(s)
	place := a.orgName()
	if project != 0 {
		place = "its project"
	}
	return a.mayGive(perms, project, env, kinds, what, place)
}

// grantPlaceOf is a grant's place in words.
func grantPlaceOf(g apitypes.Grant) string {
	switch {
	case g.Environment != "":
		return g.Project + "/" + g.Environment
	case g.Project != "":
		return g.Project
	}
	return "the org"
}

// scopeRoles are the built-in roles a scope's seeded group gives.
func scopeRoles(s apitypes.Scope) []string {
	for _, g := range seededGroups {
		if g.role == s {
			return g.roles
		}
	}
	return nil
}

// scopePerms is what a scope's roles hold (scopeRoles).
func scopePerms(s apitypes.Scope) Perms {
	var perms Perms
	for _, r := range scopeRoles(s) {
		p, _ := rolePerms(r)
		perms |= p
	}
	return perms
}
