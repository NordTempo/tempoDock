package core

import (
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
