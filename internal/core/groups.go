package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Groups and grants decide what a person may do in an org (ADR 0080,
// phase 4): the role grants of their groups and their own one-off ones.
// Every org starts with four groups; a person's role (`dock org role`, an
// invite's role) puts them in Admins, Developers or Viewers.

// seededGroup is one of the groups every org starts with.
type seededGroup struct {
	key, name string
	// role is the org role whose members it holds ("" for Approvers).
	role  apitypes.Scope
	roles []string
}

// seededGroups are the groups every org starts with: Developers and
// Viewers hold exactly what today's deploy and read do (the shadow of
// phase 3 checked it on every check), Admins everything.
var seededGroups = []seededGroup{
	{"admins", "Admins", apitypes.ScopeAdmin, []string{"full"}},
	{"developers", "Developers", apitypes.ScopeDeploy, []string{
		"deploy:edit", "variables:edit", "logs:edit", "domains:edit", "status_pages:post", "alerts:resolve", "analytics:edit", "activity:view",
	}},
	{"viewers", "Viewers", apitypes.ScopeRead, []string{
		"deploy:view", "variables:view", "logs:view", "domains:view", "status_pages:view", "alerts:view", "analytics:view", "activity:view",
	}},
	{"approvers", "Approvers", "", []string{"approver"}},
}

// rolePerms is what a built-in role name holds: "deploy:edit",
// "approver", "full". ok is false for a name that isn't one.
func rolePerms(role string) (Perms, bool) {
	key, level, _ := strings.Cut(role, ":")
	return builtinRole(key, level)
}

// seedPerms is a seeded group's permissions.
func seedPerms(key string) Perms {
	for _, g := range seededGroups {
		if g.key != key {
			continue
		}
		var s Perms
		for _, r := range g.roles {
			p, ok := rolePerms(r)
			if !ok {
				panic("seeded group " + key + ": no built-in role " + r)
			}
			s |= p
		}
		return s
	}
	panic("no seeded group " + key)
}

// seedGroups creates an org's four groups with their grants, in q's
// transaction. A new org gets them when it is made; older ones at start
// (seedAccess).
func seedGroups(ctx context.Context, q *store.Queries, orgID int64) error {
	for _, sg := range seededGroups {
		g, err := q.CreateGroup(ctx, store.CreateGroupParams{OrgID: orgID, Name: sg.name, Seeded: &sg.key})
		if err != nil {
			return fmt.Errorf("seed group %s: %w", sg.name, err)
		}
		for _, r := range sg.roles {
			if _, err := q.CreateGrant(ctx, store.CreateGrantParams{OrgID: orgID, GroupID: &g.ID, Role: r, Kinds: allKinds, GrantedBy: "system"}); err != nil {
				return err
			}
		}
	}
	return nil
}

// seedAccess gives every org that has no groups yet (orgs from before
// ADR 0080's phase 4) its four groups, puts its members in them by their
// role, and gives operators who joined for a while Full access until then,
// each org in one transaction. Starting again finds nothing to do.
func (c *Core) seedAccess(ctx context.Context) error {
	orgs, err := c.store.ListOrgsWithoutGroups(ctx)
	if err != nil {
		return err
	}
	for _, orgID := range orgs {
		err := c.store.Tx(ctx, func(q *store.Queries) error {
			if err := seedGroups(ctx, q, orgID); err != nil {
				return err
			}
			members, err := q.ListOrgMemberRoles(ctx, orgID)
			if err != nil {
				return err
			}
			for _, m := range members {
				if m.ExpiresAt != nil {
					// An operator who joined for a while (ADR 0062).
					if _, err := q.CreateGrant(ctx, store.CreateGrantParams{OrgID: orgID, UserID: &m.UserID, Role: "full", Kinds: allKinds,
						ExpiresAt: m.ExpiresAt, Via: ptr("operator"), GrantedBy: "system"}); err != nil {
						return err
					}
					continue
				}
				if err := placeInRoleGroup(ctx, q, orgID, m.UserID, apitypes.Scope(m.Role), "system"); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("seed the groups of org %d: %w", orgID, err)
		}
	}
	return nil
}

// placeInRoleGroup puts a member in the seeded group of their org role,
// out of the other two: what a role change or an invite's role means now.
// If the org removed that group, the role's grants become their own.
func placeInRoleGroup(ctx context.Context, q *store.Queries, orgID, userID int64, role apitypes.Scope, by string) error {
	if err := q.RemoveFromRoleGroups(ctx, store.RemoveFromRoleGroupsParams{UserID: userID, OrgID: orgID}); err != nil {
		return err
	}
	for _, sg := range seededGroups {
		if sg.role != role {
			continue
		}
		g, err := q.GetSeededGroup(ctx, store.GetSeededGroupParams{OrgID: orgID, Seeded: &sg.key})
		if err == nil {
			return q.AddGroupMember(ctx, store.AddGroupMemberParams{GroupID: g.ID, UserID: userID})
		}
		if !store.IsNotFound(err) {
			return err
		}
		for _, r := range sg.roles {
			if _, err := q.CreateGrant(ctx, store.CreateGrantParams{OrgID: orgID, UserID: &userID, Role: r, Kinds: allKinds, Via: ptr("role"), GrantedBy: by}); err != nil {
				return err
			}
		}
		return nil
	}
	return invalid("role %q isn't one; use read, deploy or admin", role)
}

// joinOrgMember makes a person a member of an org with role, in its
// seeded group for that role (in q's transaction).
func joinOrgMember(ctx context.Context, q *store.Queries, orgID, userID int64, role apitypes.Scope) error {
	if err := q.AddOrgMember(ctx, store.AddOrgMemberParams{OrgID: orgID, UserID: userID, Role: string(role)}); err != nil {
		return err
	}
	return placeInRoleGroup(ctx, q, orgID, userID, role, "system")
}

// leaveOrgMember ends a person's membership of an org and what they held
// there, in one transaction.
func (c *Core) leaveOrgMember(ctx context.Context, orgID, userID int64) error {
	return c.store.Tx(ctx, func(q *store.Queries) error {
		if _, err := q.RemoveOrgMember(ctx, store.RemoveOrgMemberParams{OrgID: orgID, UserID: userID}); err != nil {
			return err
		}
		return leaveOrgAccess(ctx, q, orgID, userID)
	})
}

// leaveOrgAccess ends what a person held in an org they left: their
// groups there and their one-off grants.
func leaveOrgAccess(ctx context.Context, q *store.Queries, orgID, userID int64) error {
	if err := q.RemoveFromOrgGroups(ctx, store.RemoveFromOrgGroupsParams{UserID: userID, OrgID: orgID}); err != nil {
		return err
	}
	return q.DeleteUserGrants(ctx, store.DeleteUserGrantsParams{UserID: &userID, OrgID: orgID})
}

// Environment kinds a grant can name (grants.kinds).
const (
	kindProduction uint8 = 1
	kindOther      uint8 = 2
	kindPreviews   uint8 = 4
	allKinds             = int64(kindProduction | kindOther | kindPreviews)
)

// envKind is an environment's kind bit.
func envKind(e store.Environment) uint8 {
	switch {
	case e.Preview:
		return kindPreviews
	case e.Kind == string(apitypes.KindProduction):
		return kindProduction
	}
	return kindOther
}

// loadAccess reads what a person holds in the actor's org (one query) and
// makes it the actor's access, with Scope as its summary: the role whose
// seeded group the person holds all of on the whole org (read when none),
// for what still speaks in roles (the data policy's subjects, the web
// UI). An org not seeded yet keeps deciding by role, and so does everyone
// while the model only watches (shadow): then the grants are kept for the
// shadow to answer from.
func (c *Core) loadAccess(ctx context.Context, a Actor) (Actor, error) {
	rows, err := c.store.ListUserGrants(ctx, store.ListUserGrantsParams{UserID: &a.UserID, OrgID: a.OrgID, Now: ptr(now().Unix())})
	if err != nil {
		return a, err
	}
	if len(rows) == 0 {
		n, err := c.store.CountGroups(ctx, a.OrgID)
		if err != nil || n == 0 {
			return a, err // not seeded: the role decides, as before
		}
	}
	entries := make([]grantEntry, 0, len(rows))
	var org Perms
	for _, r := range rows {
		perms, ok := grantPerms(r.Role, r.Parts)
		if !ok {
			c.log.Warn("a grant names no built-in role; it gives nothing", "role", r.Role, "org", a.OrgID)
			continue
		}
		g := grantEntry{project: deref(r.ProjectID), env: deref(r.EnvironmentID), kinds: uint8(r.Kinds), perms: perms}
		if g.project == 0 && g.env == 0 && g.kinds == uint8(allKinds) {
			org |= perms
		}
		entries = append(entries, g)
	}
	if !c.enforceGrants.Load() {
		// Shadow (access_model.go): the role decides as before; the model
		// answers beside it from these grants.
		a.grants, a.watchGrants = entries, true
		return a, nil
	}
	summary := apitypes.ScopeRead
	switch {
	case org&seedPerms("admins") == seedPerms("admins"):
		summary = apitypes.ScopeAdmin
	case org&seedPerms("developers") == seedPerms("developers"):
		summary = apitypes.ScopeDeploy
	}
	// The role column is what role-setting APIs last wrote; groups changed
	// since may give more or less, so the summary replaces it. (`dock
	// login` tokens, which acted with the role capped at their scope, are
	// all scope admin.)
	a.Scope = summary
	a.grants, a.hasGrants = entries, true
	return a, nil
}

// grantAllows is a person's answer to a check from their grants: the
// permission the check means (actionPerms) held at the place, in an
// environment of that kind (kind 0 at the org or a project). A check
// without one is refused (permOf): their summary role can't stand in, as
// it doesn't say where they hold it.
func (a Actor) grantAllows(need apitypes.Scope, action string, project, env int64, kind uint8) bool {
	perm, ok := permOf(a, actionKey{need, action}, placeOf(project, env))
	if !ok {
		return false
	}
	for _, g := range a.grants {
		if modelAllows(g, perm, project, env, kind) {
			return true
		}
	}
	return false
}

// holdsAnywhere reports whether a person holds a check's permission
// anywhere: the pre-check a loader makes before it knows the place, which
// the check at the place then decides.
func (a Actor) holdsAnywhere(need apitypes.Scope, action string) bool {
	perm, ok := permOf(a, actionKey{need, action}, "the org")
	if !ok {
		return false
	}
	for _, g := range a.grants {
		if g.perms.has(perm) {
			return true
		}
	}
	return false
}

// holdsAt reports whether a person whose grants decide holds p at a place
// (modelAllows: envID 0 a project, projectID 0 the org; kind is the
// environment's); others pass, their scope and limit having decided what
// they see. For filtering what a list or stream shows.
func (a Actor) holdsAt(p Perm, projectID, envID int64, kind uint8) bool {
	if !a.hasGrants {
		return true
	}
	for _, g := range a.grants {
		if modelAllows(g, p, projectID, envID, kind) {
			return true
		}
	}
	return false
}

// envKinds maps the org's environments to their kinds (envKind), for
// checking a person's grants against many items at once (holdsAt): one
// query, and none for actors grants don't decide (nil).
func (c *Core) envKinds(ctx context.Context, a Actor) (map[int64]uint8, error) {
	if !a.hasGrants {
		return nil, nil
	}
	rows, err := c.store.ListOrgEnvironments(ctx, a.OrgID)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]uint8, len(rows))
	for _, e := range rows {
		out[e.ID] = envKind(store.Environment{Kind: e.Kind, Preview: e.Preview})
	}
	return out, nil
}

// envKindOf is one environment's kind for holdsAt; 0 for actors grants
// don't decide, without a query.
func (c *Core) envKindOf(ctx context.Context, a Actor, envID int64) (uint8, error) {
	if !a.hasGrants || envID == 0 {
		return 0, nil
	}
	e, err := c.store.GetEnvironment(ctx, envID)
	if err != nil {
		return 0, err
	}
	return envKind(e), nil
}

// seesProject reports whether the actor may see that project exists: a
// token limited to another one can't, nor a person with no grant that
// shows any of it (env.view).
func (a Actor) seesProject(projectID int64) bool {
	if !a.hasGrants {
		return a.ProjectID == 0 || a.ProjectID == projectID
	}
	for _, g := range a.grants {
		if g.perms.has(PermEnvView) && (g.project == 0 || g.project == projectID) {
			return true
		}
	}
	return false
}

// seesEnv is seesProject for one environment of it.
func (a Actor) seesEnv(projectID, envID int64) bool {
	if !a.hasGrants {
		return (a.ProjectID == 0 || a.ProjectID == projectID) && (a.EnvironmentID == 0 || a.EnvironmentID == envID)
	}
	for _, g := range a.grants {
		if g.perms.has(PermEnvView) && (g.project == 0 || g.project == projectID) && (g.env == 0 || envID == 0 || g.env == envID) {
			return true
		}
	}
	return false
}

// refuseGrant is the refusal when a person's grants don't give a check's
// permission at its place.
func (a Actor) refuseGrant(need apitypes.Scope, action, place string) error {
	where := "here"
	if place != "" {
		where = "in " + place
	}
	perm, ok := permFor(actionKey{need, action})
	if !ok {
		return a.refuse(apitypes.Refusal{
			DecidedBy: apitypes.RefusedGrants, Action: action, Place: place,
			Reason: fmt.Sprintf("Dock can't decide %s %s: the check names no permission, a bug in Dock", action, where),
			Fix:    "tell the server's operator; Dock's log names the check",
		})
	}
	return a.refuse(apitypes.Refusal{
		DecidedBy: apitypes.RefusedGrants, Action: action, Need: perm.String(), Place: place,
		Reason: fmt.Sprintf("%s can't do %s %s: it needs %s, which none of their groups or grants gives there", a, action, where, perm),
		Fix:    fmt.Sprintf("someone who manages the members of %s adds them to a group that has it, in Settings › Members (`dock group add <group> %s`)", a.orgName(), a.who()),
	})
}

// approves reports whether the actor may decide access requests in an
// environment of kind (envKind; approve): a person whose grants give it
// there, otherwise an admin. projectID 0 asks whether anywhere.
func (a Actor) approves(projectID, envID int64, kind uint8) bool {
	if !a.hasGrants {
		return rank(a.Scope) >= rank(apitypes.ScopeAdmin)
	}
	for _, g := range a.grants {
		if projectID == 0 && g.perms.has(PermApprove) || modelAllows(g, PermApprove, projectID, envID, kind) {
			return true
		}
	}
	return false
}

// Holds reports whether the actor holds p anywhere in its org: a person
// whose grants decide, from them; anyone else (tokens, a person while the
// model only watches), from their scope's seeded group. The Data panel's
// permissions follow the data policy, not this. For listing what an actor
// could do (MCP's tools/list); each check still decides at its place.
func (a Actor) Holds(p Perm) bool {
	if dataPerms.has(p) && !a.hasGrants {
		return true
	}
	if !a.hasGrants {
		return seededPerms(a.Scope).has(p)
	}
	for _, g := range a.grants {
		if g.perms.has(p) {
			return true
		}
	}
	return false
}

// ScopeHolds reports whether a scope's seeded group holds p: what a token
// of that scope may do, for checks of what tools declare.
func ScopeHolds(s apitypes.Scope, p Perm) bool { return seededPerms(s).has(p) }
