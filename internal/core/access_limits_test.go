package core

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// An admin token limited to one environment acts as an admin there and
// nowhere else, under the shadow model too: it can't hand out access
// that reaches the org (groups, grants, roles, agent rules and their
// bypass), nor reveal the org's variables, which reach every environment.
func TestLimitedAdminTokenStaysInItsLimit(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, web.Project, apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SetOrgVariable(h.ctx, testSystem, "SENTRY_DSN", "https://secret@sentry"); err != nil {
		t.Fatal(err)
	}
	stg := h.token(testSystem, apitypes.CreateTokenRequest{Name: "stg-admin", Scope: apitypes.ScopeAdmin, Project: "shop", Environment: "staging"})
	refused := func(what string, err error) {
		t.Helper()
		if CodeOf(err) != apitypes.CodeForbidden {
			t.Errorf("%s: %v, want forbidden", what, err)
		}
	}
	_, err := h.c.CreateGroup(h.ctx, stg, apitypes.CreateGroupRequest{Name: "mine"})
	refused("creating a group", err)
	_, err = h.c.GrantAgent(h.ctx, stg, "stg-admin", apitypes.GrantRequest{Role: "full"})
	refused("granting itself Full access", err)
	_, err = h.c.SetAgentRulesBypass(h.ctx, stg, "stg-admin", true)
	refused("exempting itself from the agent rules", err)
	_, err = h.c.SetAgentRules(h.ctx, stg, apitypes.AgentSettings{})
	refused("changing the agent rules", err)
	_, err = h.c.CreateRole(h.ctx, stg, apitypes.RoleRequest{Name: "mine", Includes: []string{"full"}})
	refused("creating a role", err)
	_, err = h.c.ListOrgVariables(h.ctx, stg, true)
	refused("revealing the org's variables", err)
	if _, err := h.c.ListVariables(h.ctx, stg, EnvRef{"shop", "staging"}, "", true); err != nil {
		t.Errorf("revealing its own environment's variables: %v", err)
	}

	// An unlimited admin token still manages the org's access.
	org := h.token(testSystem, apitypes.CreateTokenRequest{Name: "org-admin", Scope: apitypes.ScopeAdmin})
	if _, err := h.c.CreateGroup(h.ctx, org, apitypes.CreateGroupRequest{Name: "ops"}); err != nil {
		t.Errorf("an unlimited admin token creating a group: %v", err)
	}
}

// Under enforce, removing a member takes their role away: Members and
// access edit alone can't remove an admin, and the org keeps someone
// with Full access.
func TestRemoveMemberTakesNoMoreThanItHolds(t *testing.T) {
	h := newHarness(t)
	h.enforce()
	admin := h.user("ada@example.com", apitypes.ScopeAdmin)
	// A viewer who may also manage members.
	m := h.user("mgr@example.com", apitypes.ScopeRead)
	if _, err := h.c.GrantMember(h.ctx, admin, m.Email, apitypes.GrantRequest{Role: "members:edit"}); err != nil {
		t.Fatal(err)
	}
	mgr := h.in(userActor(h.userRow(m.UserID)), "")
	if err := h.c.RemoveMember(h.ctx, mgr, admin.UserID); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("members:edit removing the admin: %v, want forbidden", err)
	}
	viewer := h.user("vic@example.com", apitypes.ScopeRead)
	if err := h.c.RemoveMember(h.ctx, mgr, viewer.UserID); err != nil {
		t.Errorf("members:edit removing a viewer: %v", err)
	}
}

// A token may tighten the org's agent rules, which cap it, but never
// loosen them or exempt itself (another token, yes).
func TestTokensOnlyTightenAgentRules(t *testing.T) {
	h := newHarness(t)
	h.enforce()
	tok := h.token(testSystem, apitypes.CreateTokenRequest{Name: "ci", Scope: apitypes.ScopeAdmin})
	view := []apitypes.AgentSetting{{Area: "deploy", Kind: "production", Level: "view"}}
	if _, err := h.c.SetAgentRules(h.ctx, tok, apitypes.AgentSettings{Settings: view}); err != nil {
		t.Fatalf("tightening: %v", err)
	}
	all := []apitypes.AgentSetting{{Area: "deploy", Kind: "production", Level: "all"}}
	if _, err := h.c.SetAgentRules(h.ctx, tok, apitypes.AgentSettings{Settings: all}); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("loosening: %v, want forbidden", err)
	}
	if _, err := h.c.SetAgentRulesBypass(h.ctx, tok, "ci", true); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("exempting itself: %v, want forbidden", err)
	}
	h.token(testSystem, apitypes.CreateTokenRequest{Name: "deployer", Scope: apitypes.ScopeDeploy})
	if _, err := h.c.SetAgentRulesBypass(h.ctx, tok, "deployer", true); err != nil {
		t.Errorf("exempting another token: %v", err)
	}
}

// Under enforce a person who may deploy one environment only has the
// org-wide summary scope read, yet still changes something.
func TestChangesSomewhere(t *testing.T) {
	h := newHarness(t)
	h.enforce()
	admin := h.user("ada@example.com", apitypes.ScopeAdmin)
	h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	dev := grantedPerson(h, admin, "dev@example.com", apitypes.GrantRequest{Role: "deploy:edit", Project: "shop", Environment: "production"})
	if !dev.ChangesSomewhere() {
		t.Errorf("deploy:edit on shop/production: scope %s, changes nowhere", dev.Scope)
	}
	viewer := grantedPerson(h, admin, "vic@example.com", apitypes.GrantRequest{Role: "deploy:view"})
	if viewer.ChangesSomewhere() {
		t.Error("deploy:view changes something")
	}
}

// Under enforce an org token refused for lack of a grant is told how a
// token gets one, not to put a person in a group.
func TestTokenRefusalNamesATokensFix(t *testing.T) {
	h := newHarness(t)
	h.enforce()
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	ci := h.token(testSystem, apitypes.CreateTokenRequest{Name: "ci", Scope: apitypes.ScopeDeploy, Project: "shop"})
	_, err := h.c.ListVariables(h.ctx, ci, web.Env(), "", true)
	r := refusalOf(t, err)
	if !strings.Contains(r.Fix, "dock token grant ci") || strings.Contains(r.Fix, "<email>") {
		t.Errorf("refusal = %+v", r)
	}
}
