package core

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// TestGroupsDecide: a person's groups and grants decide what they may do
// (ADR 0080). The contractor of the plan: in a group with Deploy edit on
// shop's staging only, they restart there, can't touch or see production,
// and their effective access says so. Kinds narrow a grant; Approvers
// approve without being admins; the org keeps someone with Full access.
func TestGroupsDecide(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.deploy(web, "app:1")
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, web.Project, apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	stagingWeb := h.service2("web", "staging")
	h.deploy(stagingWeb, "app:1")
	admin := h.user("ada@example.com", apitypes.ScopeAdmin)
	ola := h.user("ola@example.com", apitypes.ScopeRead)

	groups, err := h.c.ListGroups(h.ctx, admin)
	if err != nil || len(groups) != 4 {
		t.Fatalf("seeded groups = %+v, %v", groups, err)
	}

	// Ola leaves Viewers for a Contractors group on shop/staging.
	if _, err := h.c.RemoveGroupMember(h.ctx, admin, "Viewers", ola.Email); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.CreateGroup(h.ctx, admin, apitypes.CreateGroupRequest{Name: "Contractors"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.GrantGroup(h.ctx, admin, "Contractors", apitypes.GrantRequest{Role: "deploy:edit", Project: web.Project, Environment: "staging"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.AddGroupMember(h.ctx, admin, "Contractors", ola.Email); err != nil {
		t.Fatal(err)
	}
	ola = h.in(userActor(h.userRow(ola.UserID)), "")
	if _, err := h.c.Restart(h.ctx, ola, stagingWeb); err != nil {
		t.Fatalf("contractor restarts staging: %v", err)
	}
	_, err = h.c.Restart(h.ctx, ola, web)
	if r := refusalOf(t, err); r.DecidedBy != apitypes.RefusedGrants || r.Need != "service.operate" {
		t.Fatalf("contractor restarts production: %+v", r)
	}
	if _, err := h.c.GetService(h.ctx, ola, web); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("contractor reads production: %v", err)
	}
	acc, err := h.c.EffectiveAccess(h.ctx, admin, ola.Email)
	if err != nil || len(acc.Environments) != 1 || acc.Environments[0].Environment != "staging" || acc.Groups[0] != "Contractors" {
		t.Fatalf("effective access = %+v, %v", acc, err)
	}

	// Kinds: Deploy edit on the org outside production only.
	if _, err := h.c.GrantMember(h.ctx, admin, ola.Email, apitypes.GrantRequest{Role: "deploy:edit", Kinds: []string{"other"}}); err != nil {
		t.Fatal(err)
	}
	ola = h.in(userActor(h.userRow(ola.UserID)), "")
	if _, err := h.c.Restart(h.ctx, ola, web); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("an Other-only grant restarts production: %v", err)
	}

	// Approvers approve without being admins.
	bo := h.user("bo@example.com", apitypes.ScopeRead)
	if _, err := h.c.AddGroupMember(h.ctx, admin, "Approvers", bo.Email); err != nil {
		t.Fatal(err)
	}
	bo = h.in(userActor(h.userRow(bo.UserID)), "")
	if !bo.approves(0, 0) || bo.Scope != apitypes.ScopeRead {
		t.Fatalf("an approver: approves %v, summary %s", bo.approves(0, 0), bo.Scope)
	}

	// Somebody keeps Full access.
	if _, err := h.c.RemoveGroupMember(h.ctx, admin, "Admins", admin.Email); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("removing the last admin from Admins: %v", err)
	}
	if err := h.c.DeleteGroup(h.ctx, admin, "Admins"); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("deleting Admins with the only admin in it: %v", err)
	}
}

// An org from before groups gets them at start, its members in them by
// their role.
func TestSeedAccessForOlderOrgs(t *testing.T) {
	h := newHarness(t)
	o, err := h.store.CreateOrg(h.ctx, store.CreateOrgParams{Slug: "older", Name: "Older"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := h.store.CreateUser(h.ctx, store.CreateUserParams{Email: "dev@older", Name: "dev", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.AddOrgMember(h.ctx, store.AddOrgMemberParams{OrgID: o.ID, UserID: u.ID, Role: "deploy"}); err != nil {
		t.Fatal(err)
	}
	if a := h.in(userActor(u), "older"); a.hasGrants {
		t.Fatal("an org without groups decided by grants")
	}
	h.restart()
	a := h.in(userActor(u), "older")
	if !a.hasGrants || a.Scope != apitypes.ScopeDeploy {
		t.Fatalf("after seeding: grants %v, summary %s", a.hasGrants, a.Scope)
	}
	acc, err := h.c.EffectiveAccess(h.ctx, a, "")
	if err != nil || len(acc.Groups) != 1 || !strings.EqualFold(acc.Groups[0], "Developers") {
		t.Fatalf("effective access = %+v, %v", acc, err)
	}
}

// service2 creates a service in another environment of shop.
func (h *harness) service2(name, env string) ServiceRef {
	h.t.Helper()
	if _, err := h.c.CreateService(h.ctx, testSystem, EnvRef{"shop", env}, apitypes.CreateServiceRequest{Name: name, Image: "app:1"}); err != nil {
		h.t.Fatal(err)
	}
	return ServiceRef{"shop", env, name}
}

// userRow loads a user's row.
func (h *harness) userRow(id int64) store.User {
	h.t.Helper()
	u, err := h.store.GetUser(h.ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return u
}
