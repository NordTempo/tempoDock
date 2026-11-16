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
	for p := range permCount {
		if !perms.has(p) {
			continue
		}
		held := false
		for _, g := range a.grants {
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

// mayGive refuses giving perms on a place the caller doesn't hold them
// on (covers); what names what is given, place where.
func (a Actor) mayGive(perms Perms, project, env int64, kinds uint8, what, place string) error {
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
	grants, err := c.store.ListOrgGrants(ctx, store.ListOrgGrantsParams{OrgID: a.OrgID, Now: ptr(now().Unix())})
	if err != nil {
		return err
	}
	for _, gr := range grants {
		if gr.GroupID == nil || *gr.GroupID != g.ID {
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
		if err := a.mayGive(perms, deref(gr.ProjectID), deref(gr.EnvironmentID), kinds, "group "+g.Name, place); err != nil {
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
