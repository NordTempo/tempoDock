package core

import (
	"slices"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// An org's own role bundles built-in roles and permissions under a name
// (ADR 0080): granted to a group, it decides like a built-in one; edited,
// its holders hold the new parts from their next request; in use, it
// can't be removed.
func TestCustomRoles(t *testing.T) {
	h := newHarness(t)
	h.enforce()
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.deploy(web, "app:1")
	admin := h.user("ada@example.com", apitypes.ScopeAdmin)
	ola := h.user("ola@example.com", apitypes.ScopeRead)
	if _, err := h.c.RemoveGroupMember(h.ctx, admin, "Viewers", ola.Email); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []apitypes.RoleRequest{
		{Name: "deploy:edit", Includes: []string{"deploy:view"}},
		{Name: "Approver", Includes: []string{"deploy:view"}},
		{Name: "Nothing"},
		{Name: "Typo", Permissions: []string{"errors.manag"}},
		{Name: "All", Includes: []string{"full"}},
	} {
		if _, err := h.c.CreateRole(h.ctx, admin, bad); CodeOf(err) != apitypes.CodeValidation {
			t.Errorf("role %+v: %v", bad, err)
		}
	}
	rm, err := h.c.CreateRole(h.ctx, admin, apitypes.RoleRequest{Name: "Release manager", Includes: []string{"deploy:edit"}, Permissions: []string{"errors.manage"}})
	if err != nil || !slices.Contains(rm.Holds, "service.operate") || !slices.Contains(rm.Holds, "errors.manage") || slices.Contains(rm.Holds, "variables.write") {
		t.Fatalf("Release manager = %+v, %v", rm, err)
	}
	if _, err := h.c.CreateGroup(h.ctx, admin, apitypes.CreateGroupRequest{Name: "Leads"}); err != nil {
		t.Fatal(err)
	}
	g, err := h.c.GrantGroup(h.ctx, admin, "Leads", apitypes.GrantRequest{Role: "release manager", Project: web.Project})
	if err != nil || len(g.Grants) != 1 || g.Grants[0].Role != "Release manager" || !g.Grants[0].Custom {
		t.Fatalf("Leads = %+v, %v", g, err)
	}
	if _, err := h.c.AddGroupMember(h.ctx, admin, "Leads", ola.Email); err != nil {
		t.Fatal(err)
	}
	ola = h.in(userActor(h.userRow(ola.UserID)), "")
	if _, err := h.c.Restart(h.ctx, ola, web); err != nil {
		t.Fatalf("a Release manager restarts: %v", err)
	}

	// Edited down to viewing: Ola's next request can't restart.
	if _, err := h.c.UpdateRole(h.ctx, admin, "Release manager", apitypes.RoleRequest{Name: "Release viewer", Includes: []string{"deploy:view"}}); err != nil {
		t.Fatal(err)
	}
	ola = h.in(userActor(h.userRow(ola.UserID)), "")
	if _, err := h.c.Restart(h.ctx, ola, web); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("after the edit: %v", err)
	}
	roles, err := h.c.Roles(h.ctx, admin)
	if err != nil || len(roles) != 1 || roles[0].Name != "Release viewer" || !slices.Equal(roles[0].Groups, []string{"Leads"}) {
		t.Fatalf("roles = %+v, %v", roles, err)
	}
	if err := h.c.DeleteRole(h.ctx, admin, "Release viewer"); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("removing a role in use: %v", err)
	}
	if err := h.c.RevokeGrant(h.ctx, admin, g.Grants[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := h.c.DeleteRole(h.ctx, admin, "Release viewer"); err != nil {
		t.Fatal(err)
	}
}
