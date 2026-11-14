package core

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Managing groups and grants (ADR 0080, phase 4): an org's admins (who
// hold members.manage) make groups, put people in them and give groups or
// one person roles at places. The org always keeps someone with Full
// access.

// AreaRoles lists the built-in roles with their levels and permissions,
// the same in every org.
func AreaRoles() []apitypes.AreaRole {
	out := make([]apitypes.AreaRole, 0, len(areaRoles))
	for _, r := range areaRoles {
		ar := apitypes.AreaRole{Key: r.key, Name: r.name}
		var held Perms
		for _, l := range r.levels {
			held |= l.adds
			role := r.key
			if l.name != "" {
				role += ":" + l.name
			}
			var names []string
			for p := range permCount {
				if held.has(p) {
					names = append(names, p.String())
				}
			}
			ar.Levels = append(ar.Levels, apitypes.RoleLevel{Name: l.name, Role: role, Permissions: names})
		}
		out = append(out, ar)
	}
	return out
}

// ListGroups returns the org's groups with their members and grants, in
// three queries. Needs members.read.
func (c *Core) ListGroups(ctx context.Context, a Actor) ([]apitypes.Group, error) {
	if err := a.require(apitypes.ScopeAdmin, "reading groups"); err != nil {
		return nil, err
	}
	groups, err := c.store.ListGroups(ctx, a.OrgID)
	if err != nil {
		return nil, err
	}
	members, err := c.store.ListGroupMembers(ctx, a.OrgID)
	if err != nil {
		return nil, err
	}
	grants, err := c.store.ListOrgGrants(ctx, store.ListOrgGrantsParams{OrgID: a.OrgID, Now: ptr(now().Unix())})
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.Group, len(groups))
	for i, g := range groups {
		out[i] = apitypes.Group{ID: g.ID, Name: g.Name, Seeded: deref(g.Seeded), Members: []apitypes.GroupMember{}, Grants: []apitypes.Grant{}}
		for _, m := range members {
			if m.GroupID == g.ID {
				out[i].Members = append(out[i].Members, apitypes.GroupMember{UserID: m.UserID, Email: m.Email, Name: m.Name})
			}
		}
		for _, gr := range grants {
			if gr.GroupID != nil && *gr.GroupID == g.ID {
				out[i].Grants = append(out[i].Grants, toGrant(gr))
			}
		}
	}
	return out, nil
}

// toGrant converts a grant row with its places' names.
func toGrant(r store.ListOrgGrantsRow) apitypes.Grant {
	g := apitypes.Grant{ID: r.ID, Role: r.Role, Project: deref(r.ProjectName), Environment: deref(r.EnvironmentName), Kinds: grantKindNames(uint8(r.Kinds)), Via: deref(r.Via)}
	if r.ExpiresAt != nil {
		g.ExpiresAt = ptr(time.Unix(*r.ExpiresAt, 0).UTC())
	}
	return g
}

// grantKindNames are a kinds bit set's names; nil for all three.
func grantKindNames(k uint8) []string {
	if k == uint8(allKinds) {
		return nil
	}
	var out []string
	for _, x := range []struct {
		bit  uint8
		name string
	}{{kindProduction, "production"}, {kindOther, "other"}, {kindPreviews, "previews"}} {
		if k&x.bit != 0 {
			out = append(out, x.name)
		}
	}
	return out
}

// kindBits is a kinds list's bit set; all three for none.
func kindBits(names []string) (int64, error) {
	if len(names) == 0 {
		return allKinds, nil
	}
	var k uint8
	for _, n := range names {
		switch n {
		case "production":
			k |= kindProduction
		case "other", "non_production":
			k |= kindOther
		case "previews":
			k |= kindPreviews
		default:
			return 0, invalid("environment kind %q isn't one; use production, other or previews", n)
		}
	}
	return int64(k), nil
}

// groupFor loads one of the org's groups by ID or name, after a manages
// members, recently signed in: groups give power, like roles.
func (c *Core) groupFor(ctx context.Context, a Actor, ref, action string) (store.Group, error) {
	if err := a.require(apitypes.ScopeAdmin, action); err != nil {
		return store.Group{}, err
	}
	if err := a.requireRecent(action); err != nil {
		return store.Group{}, err
	}
	var g store.Group
	var err error
	if id, perr := parseRefID(ref); perr == nil {
		g, err = c.store.GetGroup(ctx, store.GetGroupParams{ID: id, OrgID: a.OrgID})
	} else {
		g, err = c.store.GetGroupByName(ctx, store.GetGroupByNameParams{OrgID: a.OrgID, Name: ref})
	}
	if store.IsNotFound(err) {
		return g, notFound("group %q not found in %s; `dock group list` lists them", ref, a.OrgSlug)
	}
	return g, err
}

// parseRefID reads a numeric ID given where a name could be.
func parseRefID(s string) (int64, error) {
	var id int64
	_, err := fmt.Sscan(s, &id)
	if err != nil || id <= 0 || fmt.Sprint(id) != s {
		return 0, fmt.Errorf("not an ID: %q", s)
	}
	return id, nil
}

// validGroupName checks a group's name: 1 to 64 printable characters.
func validGroupName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 || strings.ContainsAny(name, "\n\r\t") {
		return "", invalid("a group's name is 1 to 64 characters on one line")
	}
	return name, nil
}

// CreateGroup makes an empty group. Needs members.manage.
func (c *Core) CreateGroup(ctx context.Context, a Actor, req apitypes.CreateGroupRequest) (apitypes.Group, error) {
	if err := a.require(apitypes.ScopeAdmin, "managing groups"); err != nil {
		return apitypes.Group{}, err
	}
	if err := a.requireRecent("managing groups"); err != nil {
		return apitypes.Group{}, err
	}
	name, err := validGroupName(req.Name)
	if err != nil {
		return apitypes.Group{}, err
	}
	g, err := c.store.CreateGroup(ctx, store.CreateGroupParams{OrgID: a.OrgID, Name: name})
	if store.IsUnique(err) {
		return apitypes.Group{}, conflict("%s has a group named %q already", a.OrgSlug, name)
	}
	if err != nil {
		return apitypes.Group{}, err
	}
	c.audit(ctx, a, "group.create", nil, nil, "group "+name, "created group "+name)
	return apitypes.Group{ID: g.ID, Name: g.Name, Members: []apitypes.GroupMember{}, Grants: []apitypes.Grant{}}, nil
}

// RenameGroup renames a group, a seeded one too.
func (c *Core) RenameGroup(ctx context.Context, a Actor, ref string, req apitypes.CreateGroupRequest) (apitypes.Group, error) {
	g, err := c.groupFor(ctx, a, ref, "managing groups")
	if err != nil {
		return apitypes.Group{}, err
	}
	name, err := validGroupName(req.Name)
	if err != nil {
		return apitypes.Group{}, err
	}
	if err := c.store.RenameGroup(ctx, store.RenameGroupParams{Name: name, ID: g.ID, OrgID: a.OrgID}); store.IsUnique(err) {
		return apitypes.Group{}, conflict("%s has a group named %q already", a.OrgSlug, name)
	} else if err != nil {
		return apitypes.Group{}, err
	}
	c.audit(ctx, a, "group.rename", nil, nil, "group "+name, fmt.Sprintf("renamed group %s to %s", g.Name, name))
	return c.group(ctx, a, g.ID)
}

// group is one group as ListGroups shows it.
func (c *Core) group(ctx context.Context, a Actor, id int64) (apitypes.Group, error) {
	all, err := c.ListGroups(ctx, a)
	if err != nil {
		return apitypes.Group{}, err
	}
	for _, g := range all {
		if g.ID == id {
			return g, nil
		}
	}
	return apitypes.Group{}, notFound("group %d not found", id)
}

// keepFullAccess refuses a change, in its transaction, that would leave
// the org with nobody holding Full access on all of it without an end.
func keepFullAccess(ctx context.Context, q *store.Queries, a Actor) error {
	n, err := q.CountFullAccess(ctx, a.OrgID)
	if err != nil {
		return err
	}
	if n == 0 {
		return conflict("%s would have nobody with Full access; give someone else Full access first (Admins, or a Full access grant)", a.OrgSlug)
	}
	return nil
}

// DeleteGroup removes a group: its members lose what it gave them.
func (c *Core) DeleteGroup(ctx context.Context, a Actor, ref string) error {
	g, err := c.groupFor(ctx, a, ref, "managing groups")
	if err != nil {
		return err
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.DeleteGroup(ctx, store.DeleteGroupParams{ID: g.ID, OrgID: a.OrgID}); err != nil {
			return err
		}
		return keepFullAccess(ctx, q, a)
	})
	if err != nil {
		return err
	}
	c.audit(ctx, a, "group.delete", nil, nil, "group "+g.Name, "removed group "+g.Name)
	return nil
}

// memberByRef finds a member of the org by email or ID.
func (c *Core) memberByRef(ctx context.Context, a Actor, ref string) (store.User, error) {
	var u store.User
	var err error
	if id, perr := parseRefID(ref); perr == nil {
		u, err = c.store.GetUser(ctx, id)
	} else {
		u, err = c.store.GetUserByEmail(ctx, strings.TrimSpace(ref))
	}
	if err == nil {
		_, err = c.store.GetMemberRole(ctx, store.GetMemberRoleParams{OrgID: a.OrgID, UserID: u.ID})
	}
	if store.IsNotFound(err) {
		return u, notFound("%q isn't a member of %s; invite them first (`dock org invite`)", ref, a.OrgSlug)
	}
	return u, err
}

// AddGroupMember puts a member of the org in a group.
func (c *Core) AddGroupMember(ctx context.Context, a Actor, ref, who string) (apitypes.Group, error) {
	g, err := c.groupFor(ctx, a, ref, "managing groups")
	if err != nil {
		return apitypes.Group{}, err
	}
	if err := c.checkAccountLock(ctx, a, "changing groups", false); err != nil {
		return apitypes.Group{}, err
	}
	u, err := c.memberByRef(ctx, a, who)
	if err != nil {
		return apitypes.Group{}, err
	}
	if err := c.store.AddGroupMember(ctx, store.AddGroupMemberParams{GroupID: g.ID, UserID: u.ID}); err != nil {
		return apitypes.Group{}, err
	}
	c.audit(ctx, a, "group.add", nil, nil, u.Email, fmt.Sprintf("added %s to group %s", u.Email, g.Name))
	return c.group(ctx, a, g.ID)
}

// RemoveGroupMember takes a person out of a group.
func (c *Core) RemoveGroupMember(ctx context.Context, a Actor, ref, who string) (apitypes.Group, error) {
	g, err := c.groupFor(ctx, a, ref, "managing groups")
	if err != nil {
		return apitypes.Group{}, err
	}
	u, err := c.memberByRef(ctx, a, who)
	if err != nil {
		return apitypes.Group{}, err
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		n, err := q.RemoveGroupMember(ctx, store.RemoveGroupMemberParams{GroupID: g.ID, UserID: u.ID})
		if err != nil {
			return err
		}
		if n == 0 {
			return notFound("%s isn't in group %s", u.Email, g.Name)
		}
		return keepFullAccess(ctx, q, a)
	})
	if err != nil {
		return apitypes.Group{}, err
	}
	c.audit(ctx, a, "group.remove", nil, nil, u.Email, fmt.Sprintf("took %s out of group %s", u.Email, g.Name))
	return c.group(ctx, a, g.ID)
}

// grantParams checks a grant request: a built-in role, a place in the
// org, kinds only for a grant wider than one environment.
func (c *Core) grantParams(ctx context.Context, a Actor, req apitypes.GrantRequest) (store.CreateGrantParams, string, error) {
	p := store.CreateGrantParams{OrgID: a.OrgID, Role: strings.TrimSpace(req.Role), GrantedBy: a.Name}
	if _, ok := rolePerms(p.Role); !ok {
		return p, "", invalid("role %q isn't a built-in one; `dock role list` lists them, like deploy:edit", req.Role)
	}
	kinds, err := kindBits(req.Kinds)
	if err != nil {
		return p, "", err
	}
	p.Kinds = kinds
	place := "the org"
	switch {
	case req.Environment != "" && req.Project == "":
		return p, "", invalid("an environment needs its project too")
	case req.Environment != "":
		if len(req.Kinds) > 0 {
			return p, "", invalid("kinds are for a grant on the org or a project; one environment has its own")
		}
		es, err := c.loadEnv(ctx, a.OrgID, EnvRef{req.Project, req.Environment})
		if err != nil {
			return p, "", err
		}
		p.ProjectID, p.EnvironmentID, place = &es.project.ID, &es.env.ID, es.project.Name+"/"+es.env.Name
	case req.Project != "":
		pr, err := c.loadProject(ctx, a.OrgID, req.Project)
		if err != nil {
			return p, "", err
		}
		p.ProjectID, place = &pr.ID, pr.Name
	}
	if req.ForHours > 0 {
		p.ExpiresAt = ptr(now().Add(time.Duration(req.ForHours) * time.Hour).Unix())
	}
	if len(req.Kinds) > 0 {
		place += " (" + strings.Join(req.Kinds, ", ") + ")"
	}
	return p, place, nil
}

// GrantGroup gives a group a role at a place.
func (c *Core) GrantGroup(ctx context.Context, a Actor, ref string, req apitypes.GrantRequest) (apitypes.Group, error) {
	g, err := c.groupFor(ctx, a, ref, "managing groups")
	if err != nil {
		return apitypes.Group{}, err
	}
	if err := c.checkAccountLock(ctx, a, "changing groups", false); err != nil {
		return apitypes.Group{}, err
	}
	p, place, err := c.grantParams(ctx, a, req)
	if err != nil {
		return apitypes.Group{}, err
	}
	p.GroupID = &g.ID
	if _, err := c.store.CreateGrant(ctx, p); err != nil {
		return apitypes.Group{}, err
	}
	c.audit(ctx, a, "group.grant", nil, nil, "group "+g.Name, fmt.Sprintf("gave group %s %s on %s", g.Name, p.Role, place))
	return c.group(ctx, a, g.ID)
}

// RevokeGrant ends a grant, a group's or a person's.
func (c *Core) RevokeGrant(ctx context.Context, a Actor, id int64) error {
	if err := a.require(apitypes.ScopeAdmin, "managing groups"); err != nil {
		return err
	}
	if err := a.requireRecent("managing groups"); err != nil {
		return err
	}
	g, err := c.store.GetGrant(ctx, store.GetGrantParams{ID: id, OrgID: a.OrgID})
	if store.IsNotFound(err) {
		return notFound("grant %d not found in %s", id, a.OrgSlug)
	}
	if err != nil {
		return err
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.DeleteGrant(ctx, store.DeleteGrantParams{ID: id, OrgID: a.OrgID}); err != nil {
			return err
		}
		return keepFullAccess(ctx, q, a)
	})
	if err != nil {
		return err
	}
	c.audit(ctx, a, "access.revoke", nil, nil, "grant "+fmt.Sprint(id), fmt.Sprintf("ended grant %d (%s)", id, g.Role))
	return nil
}

// GrantMember gives one member a role at a place: a one-off grant.
func (c *Core) GrantMember(ctx context.Context, a Actor, who string, req apitypes.GrantRequest) (apitypes.Grant, error) {
	if err := a.require(apitypes.ScopeAdmin, "managing groups"); err != nil {
		return apitypes.Grant{}, err
	}
	if err := a.requireRecent("managing groups"); err != nil {
		return apitypes.Grant{}, err
	}
	if err := c.checkAccountLock(ctx, a, "changing groups", false); err != nil {
		return apitypes.Grant{}, err
	}
	u, err := c.memberByRef(ctx, a, who)
	if err != nil {
		return apitypes.Grant{}, err
	}
	p, place, err := c.grantParams(ctx, a, req)
	if err != nil {
		return apitypes.Grant{}, err
	}
	p.UserID = &u.ID
	row, err := c.store.CreateGrant(ctx, p)
	if err != nil {
		return apitypes.Grant{}, err
	}
	c.audit(ctx, a, "access.grant", nil, nil, u.Email, fmt.Sprintf("gave %s %s on %s", u.Email, p.Role, place))
	out := apitypes.Grant{ID: row.ID, Role: row.Role, Project: req.Project, Environment: req.Environment, Kinds: grantKindNames(uint8(row.Kinds)), User: u.Email}
	if row.ExpiresAt != nil {
		out.ExpiresAt = ptr(time.Unix(*row.ExpiresAt, 0).UTC())
	}
	return out, nil
}

// MemberGrants lists the org's one-off grants, with whom each is for.
func (c *Core) MemberGrants(ctx context.Context, a Actor) ([]apitypes.Grant, error) {
	if err := a.require(apitypes.ScopeAdmin, "reading groups"); err != nil {
		return nil, err
	}
	rows, err := c.store.ListOrgGrants(ctx, store.ListOrgGrantsParams{OrgID: a.OrgID, Now: ptr(now().Unix())})
	if err != nil {
		return nil, err
	}
	members, err := c.store.ListOrgMembers(ctx, store.ListOrgMembersParams{Now: now(), OrgID: a.OrgID})
	if err != nil {
		return nil, err
	}
	out := []apitypes.Grant{}
	for _, r := range rows {
		if r.UserID == nil {
			continue
		}
		g := toGrant(r)
		if i := slices.IndexFunc(members, func(m store.ListOrgMembersRow) bool { return m.User.ID == *r.UserID }); i >= 0 {
			g.User = members[i].User.Email
		}
		out = append(out, g)
	}
	return out, nil
}

// EffectiveAccess is what a member can do, worked out from their groups
// and one-off grants: the level of each area role on the org and in each
// environment, with the grants that give it. Anyone may read their own;
// others' needs members.read. Four queries, whatever the org's size.
func (c *Core) EffectiveAccess(ctx context.Context, a Actor, who string) (apitypes.EffectiveAccess, error) {
	var u store.User
	var err error
	if who == "" || who == a.Email {
		if err := a.require(apitypes.ScopeRead, "reading your access"); err != nil {
			return apitypes.EffectiveAccess{}, err
		}
		if a.UserID == 0 {
			return apitypes.EffectiveAccess{}, invalid("a token's access is its scope and limit (`dock whoami`); effective access is a person's")
		}
		u, err = c.store.GetUser(ctx, a.UserID)
	} else {
		if err := a.require(apitypes.ScopeAdmin, "reading groups"); err != nil {
			return apitypes.EffectiveAccess{}, err
		}
		u, err = c.memberByRef(ctx, a, who)
	}
	if err != nil {
		return apitypes.EffectiveAccess{}, err
	}
	grants, err := c.store.ListOrgGrants(ctx, store.ListOrgGrantsParams{OrgID: a.OrgID, Now: ptr(now().Unix())})
	if err != nil {
		return apitypes.EffectiveAccess{}, err
	}
	groups, err := c.store.ListGroups(ctx, a.OrgID)
	if err != nil {
		return apitypes.EffectiveAccess{}, err
	}
	members, err := c.store.ListGroupMembers(ctx, a.OrgID)
	if err != nil {
		return apitypes.EffectiveAccess{}, err
	}
	envs, err := c.store.ListOrgEnvironments(ctx, a.OrgID)
	if err != nil {
		return apitypes.EffectiveAccess{}, err
	}
	in := map[int64]string{} // the person's groups, by ID
	for _, m := range members {
		if m.UserID == u.ID {
			for _, g := range groups {
				if g.ID == m.GroupID {
					in[g.ID] = g.Name
				}
			}
		}
	}
	// Their grants, each with where it comes from.
	type held struct {
		g    grantEntry
		from string
	}
	var mine []held
	for _, r := range grants {
		var who string
		switch {
		case r.UserID != nil && *r.UserID == u.ID:
			who = "their own grant"
		case r.GroupID != nil && in[*r.GroupID] != "":
			who = in[*r.GroupID]
		default:
			continue
		}
		perms, ok := rolePerms(r.Role)
		if !ok {
			continue
		}
		on := "the org"
		if gr := toGrant(r); gr.Environment != "" {
			on = gr.Project + "/" + gr.Environment
		} else if gr.Project != "" {
			on = gr.Project
		}
		mine = append(mine, held{grantEntry{project: deref(r.ProjectID), env: deref(r.EnvironmentID), kinds: uint8(r.Kinds), perms: perms},
			fmt.Sprintf("%s: %s on %s", who, r.Role, on)})
	}
	// levels says each area role's level for what a place holds.
	levels := func(covers func(grantEntry) bool, orgOnly bool) []apitypes.RoleAt {
		var hold Perms
		for _, h := range mine {
			if covers(h.g) {
				hold |= h.g.perms
			}
		}
		out := []apitypes.RoleAt{}
		for _, r := range areaRoles {
			if r.key == "full" || r.key == "approver" && orgOnly {
				continue
			}
			var cum Perms
			level := ""
			for _, l := range r.levels {
				cum |= l.adds
				if l.adds == 0 || hold&cum != cum {
					break
				}
				level = cmp.Or(l.name, "yes")
			}
			if level == "" || orgOnly != isOrgRole(r) {
				continue
			}
			var from []string
			for i, h := range mine {
				if covers(h.g) && h.g.perms&r.levels[0].adds != 0 {
					from = append(from, mine[i].from)
				}
			}
			out = append(out, apitypes.RoleAt{Role: r.key, Level: level, From: from})
		}
		return out
	}
	out := apitypes.EffectiveAccess{User: u.Email, Groups: []string{}, Environments: []apitypes.EnvironmentAccess{}}
	for _, name := range in {
		out.Groups = append(out.Groups, name)
	}
	slices.Sort(out.Groups)
	out.Org = levels(func(g grantEntry) bool { return g.project == 0 && g.env == 0 && g.kinds == uint8(allKinds) }, true)
	for _, e := range envs {
		kind := envKind(store.Environment{Kind: e.Kind, Preview: e.Preview})
		roles := levels(func(g grantEntry) bool {
			return (g.project == 0 || g.project == e.ProjectID) && (g.env == 0 || g.env == e.ID) && g.kinds&kind != 0
		}, false)
		if len(roles) == 0 {
			continue
		}
		out.Environments = append(out.Environments, apitypes.EnvironmentAccess{
			Project: e.ProjectName, Environment: e.Name, Kind: grantKindNames(kind)[0], Roles: roles,
		})
	}
	return out, nil
}

// isOrgRole reports whether an area role's permissions are the org's
// (members, org settings, usage): shown once for the org, not per
// environment.
func isOrgRole(r areaRole) bool {
	return permTable[r.levels[0].adds.first()].level == PermOrg
}

// first is the set's lowest permission.
func (s Perms) first() Perm {
	for p := range permCount {
		if s.has(p) {
			return p
		}
	}
	return 0
}
