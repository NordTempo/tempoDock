package core

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// An org's own roles (ADR 0080, phase 5): built-in roles and single
// permissions under one name ("Release manager" = Deploy edit + Variables
// edit + errors.manage), granted to groups and people like a built-in
// role. A role keeps the built-in roles it includes by name, so what they
// hold in a later Dock reaches it.

// permByName maps a permission's stable name to it.
var permByName = func() map[string]Perm {
	m := make(map[string]Perm, permCount)
	for p := range permCount {
		m[p.String()] = p
	}
	return m
}()

// partsPerms is what a custom role's parts hold: each a built-in role or
// a permission's name. A name this Dock doesn't know gives nothing.
func partsPerms(parts string) Perms {
	var s Perms
	for _, part := range strings.Fields(parts) {
		if p, ok := rolePerms(part); ok {
			s |= p
		} else if p, ok := permByName[part]; ok {
			s |= 1 << p
		}
	}
	return s
}

// grantPerms is what a grant holds: its custom role's parts when it names
// one (parts not nil), else its built-in role.
func grantPerms(role string, parts *string) (Perms, bool) {
	if parts != nil {
		return partsPerms(*parts), true
	}
	return rolePerms(role)
}

// roleParts checks a role request's built-in roles and permissions and
// joins them as stored.
func roleParts(req apitypes.RoleRequest) (string, error) {
	var parts []string
	for _, r := range req.Includes {
		r = strings.TrimSpace(r)
		if r == "full" {
			return "", invalid("Full access is a role of its own: grant full instead of a role that includes it")
		}
		if _, ok := rolePerms(r); !ok {
			return "", invalid("%q isn't a built-in role; `dock role list` lists them, like deploy:edit", r)
		}
		if !slices.Contains(parts, r) {
			parts = append(parts, r)
		}
	}
	for _, p := range req.Permissions {
		p = strings.TrimSpace(p)
		if _, ok := permByName[p]; !ok {
			return "", invalid("%q isn't a permission; `dock role list` shows each built-in role's, like errors.manage", p)
		}
		if !slices.Contains(parts, p) {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return "", invalid("a role needs at least one built-in role (--include) or permission (--permission)")
	}
	return strings.Join(parts, " "), nil
}

// roleName checks a custom role's name: not a built-in one's, and short.
func roleName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", invalid("a role needs a name, like \"Release manager\"")
	case len(name) > 64:
		return "", invalid("a role's name is at most 64 characters")
	case strings.Contains(name, ":"):
		return "", invalid("a role's name can't have a colon; those are the built-in roles' (deploy:edit)")
	}
	if _, ok := rolePerms(strings.ToLower(name)); ok {
		return "", invalid("%q is a built-in role's name; pick another", name)
	}
	return name, nil
}

// toRole is a custom role as the API shows it, with who holds it.
func toRole(r store.Role, uses []store.RoleUsesRow) apitypes.Role {
	out := apitypes.Role{ID: r.ID, Name: r.Name, Description: r.Description, Includes: []string{}, Permissions: []string{}, Holds: []string{}, Groups: []string{}, People: []string{}}
	for _, part := range strings.Fields(r.Parts) {
		if _, ok := rolePerms(part); ok {
			out.Includes = append(out.Includes, part)
		} else {
			out.Permissions = append(out.Permissions, part)
		}
	}
	held := partsPerms(r.Parts)
	for p := range permCount {
		if held.has(p) {
			out.Holds = append(out.Holds, p.String())
		}
	}
	for _, u := range uses {
		if u.RoleID == nil || *u.RoleID != r.ID {
			continue
		}
		if u.GroupName != nil && !slices.Contains(out.Groups, *u.GroupName) {
			out.Groups = append(out.Groups, *u.GroupName)
		}
		if u.UserEmail != nil && !slices.Contains(out.People, *u.UserEmail) {
			out.People = append(out.People, *u.UserEmail)
		}
	}
	return out
}

// Roles lists the org's own roles, with who holds each. Two queries.
func (c *Core) Roles(ctx context.Context, a Actor) ([]apitypes.Role, error) {
	if err := a.require(apitypes.ScopeAdmin, "reading roles"); err != nil {
		return nil, err
	}
	rows, err := c.store.ListRoles(ctx, a.OrgID)
	if err != nil {
		return nil, err
	}
	uses, err := c.store.RoleUses(ctx, a.OrgID)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.Role, 0, len(rows))
	for _, r := range rows {
		out = append(out, toRole(r, uses))
	}
	return out, nil
}

// roleFor loads one of the org's roles by name, for an admin action.
func (c *Core) roleFor(ctx context.Context, a Actor, name, action string) (store.Role, error) {
	if err := a.require(apitypes.ScopeAdmin, action); err != nil {
		return store.Role{}, err
	}
	r, err := c.store.GetRoleByName(ctx, store.GetRoleByNameParams{OrgID: a.OrgID, Name: strings.TrimSpace(name)})
	if store.IsNotFound(err) {
		return r, notFound("role %q not found in %s; `dock role list` lists them", name, a.OrgSlug)
	}
	return r, err
}

// oneRole is a role with who holds it.
func (c *Core) oneRole(ctx context.Context, a Actor, r store.Role) (apitypes.Role, error) {
	uses, err := c.store.RoleUses(ctx, a.OrgID)
	if err != nil {
		return apitypes.Role{}, err
	}
	return toRole(r, uses), nil
}

// Role is one of the org's roles.
func (c *Core) Role(ctx context.Context, a Actor, name string) (apitypes.Role, error) {
	r, err := c.roleFor(ctx, a, name, "reading roles")
	if err != nil {
		return apitypes.Role{}, err
	}
	return c.oneRole(ctx, a, r)
}

// CreateRole makes one of the org's own roles.
func (c *Core) CreateRole(ctx context.Context, a Actor, req apitypes.RoleRequest) (apitypes.Role, error) {
	if err := a.require(apitypes.ScopeAdmin, "managing roles"); err != nil {
		return apitypes.Role{}, err
	}
	if err := a.requireRecent("managing roles"); err != nil {
		return apitypes.Role{}, err
	}
	name, err := roleName(req.Name)
	if err != nil {
		return apitypes.Role{}, err
	}
	parts, err := roleParts(req)
	if err != nil {
		return apitypes.Role{}, err
	}
	if _, err := c.store.GetRoleByName(ctx, store.GetRoleByNameParams{OrgID: a.OrgID, Name: name}); err == nil {
		return apitypes.Role{}, conflict("%s already has a role %q; edit it with `dock role edit`", a.orgName(), name)
	}
	r, err := c.store.CreateRole(ctx, store.CreateRoleParams{OrgID: a.OrgID, Name: name, Description: strings.TrimSpace(req.Description), Parts: parts, CreatedBy: a.Name})
	if err != nil {
		return apitypes.Role{}, err
	}
	c.audit(ctx, a, "role.create", nil, nil, "role "+name, fmt.Sprintf("made role %s: %s", name, parts))
	return toRole(r, nil), nil
}

// UpdateRole changes one of the org's roles: its name, description and
// parts, all as given. Who holds it holds the new parts from their next
// request.
func (c *Core) UpdateRole(ctx context.Context, a Actor, name string, req apitypes.RoleRequest) (apitypes.Role, error) {
	r, err := c.roleFor(ctx, a, name, "managing roles")
	if err != nil {
		return apitypes.Role{}, err
	}
	if err := a.requireRecent("managing roles"); err != nil {
		return apitypes.Role{}, err
	}
	newName, err := roleName(req.Name)
	if err != nil {
		return apitypes.Role{}, err
	}
	parts, err := roleParts(req)
	if err != nil {
		return apitypes.Role{}, err
	}
	if !strings.EqualFold(newName, r.Name) {
		if _, err := c.store.GetRoleByName(ctx, store.GetRoleByNameParams{OrgID: a.OrgID, Name: newName}); err == nil {
			return apitypes.Role{}, conflict("%s already has a role %q", a.orgName(), newName)
		}
	}
	if err := c.store.UpdateRole(ctx, store.UpdateRoleParams{Name: newName, Description: strings.TrimSpace(req.Description), Parts: parts, ID: r.ID, OrgID: a.OrgID}); err != nil {
		return apitypes.Role{}, err
	}
	c.audit(ctx, a, "role.update", nil, nil, "role "+newName, fmt.Sprintf("changed role %s: %s", newName, parts))
	r.Name, r.Description, r.Parts = newName, strings.TrimSpace(req.Description), parts
	return c.oneRole(ctx, a, r)
}

// DeleteRole removes one of the org's roles no grant names.
func (c *Core) DeleteRole(ctx context.Context, a Actor, name string) error {
	r, err := c.roleFor(ctx, a, name, "managing roles")
	if err != nil {
		return err
	}
	if err := a.requireRecent("managing roles"); err != nil {
		return err
	}
	role, err := c.oneRole(ctx, a, r)
	if err != nil {
		return err
	}
	if holders := append(role.Groups, role.People...); len(holders) > 0 {
		return conflict("role %s is still granted to %s; end those grants first (`dock group show`, `dock grant list`)", r.Name, strings.Join(holders, ", "))
	}
	if err := c.store.DeleteRole(ctx, store.DeleteRoleParams{ID: r.ID, OrgID: a.OrgID}); err != nil {
		return err
	}
	c.audit(ctx, a, "role.delete", nil, nil, "role "+r.Name, "removed role "+r.Name)
	return nil
}
