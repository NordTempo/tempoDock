package core

import (
	"errors"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// An org token is an org agent (ADR 0080, 6b): its scope and limit are
// grants from the start, so it does what it did; a grant widens it, and
// a group gives it what the group gives.
func TestOrgAgents(t *testing.T) {
	h := newHarness(t)
	h.enforce()
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.deploy(web, "app:1")
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, web.Project, apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	stagingWeb := h.service2("web", "staging")
	h.deploy(stagingWeb, "app:1")
	admin := h.user("ada@example.com", apitypes.ScopeAdmin)

	ci := h.token(admin, apitypes.CreateTokenRequest{Name: "ci", Scope: apitypes.ScopeDeploy, Project: "shop", Environment: "staging"})
	if _, err := h.c.Restart(h.ctx, ci, stagingWeb); err != nil {
		t.Fatalf("ci restarts staging: %v", err)
	}
	if _, err := h.c.Restart(h.ctx, ci, web); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("ci restarts production: %v", err)
	}
	acc, err := h.c.GrantAgent(h.ctx, admin, "ci", apitypes.GrantRequest{Role: "deploy:operate", Project: "shop", Environment: "production"})
	if err != nil || len(acc.Grants) < 2 {
		t.Fatalf("grant = %+v, %v", acc, err)
	}
	ci2, err := h.c.createToken(h.ctx, admin, apitypes.CreateTokenRequest{Name: "ci2", Scope: apitypes.ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	auth := func() Actor {
		t.Helper()
		a, err := h.c.AuthenticateToken(h.ctx, ci2.Secret, "")
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	if _, err := h.c.Restart(h.ctx, auth(), web); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a read token restarts: %v", err)
	}
	g, err := h.c.SetGroupAgent(h.ctx, admin, "Developers", "ci2", true)
	if err != nil || len(g.Agents) != 1 || g.Agents[0].Name != "ci2" {
		t.Fatalf("Developers = %+v, %v", g, err)
	}
	if _, err := h.c.Restart(h.ctx, auth(), web); err != nil {
		t.Fatalf("a read token in Developers restarts: %v", err)
	}
}

// The organization's agent rules cap its agents (ADR 0080, 6c): Deploy
// view in production stops a token's restart there, asking first turns
// its staging restart into a request for the approvers, and a token that
// bypasses the rules does both.
func TestAgentRules(t *testing.T) {
	h := newHarness(t)
	h.enforce()
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.deploy(web, "app:1")
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, web.Project, apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	stagingWeb := h.service2("web", "staging")
	h.deploy(stagingWeb, "app:1")
	admin := h.user("ada@example.com", apitypes.ScopeAdmin)
	ci, err := h.c.createToken(h.ctx, admin, apitypes.CreateTokenRequest{Name: "ci", Scope: apitypes.ScopeDeploy})
	if err != nil {
		t.Fatal(err)
	}
	auth := func() Actor {
		t.Helper()
		a, err := h.c.AuthenticateToken(h.ctx, ci.Secret, "")
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	if _, err := h.c.SetAgentRules(h.ctx, admin, apitypes.AgentSettings{Settings: []apitypes.AgentSetting{
		{Area: "deploy", Kind: "production", Level: "view"},
		{Area: "deploy", Kind: "other", Level: "all", Ask: true},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.Restart(h.ctx, auth(), web); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("Deploy view in production: %v", err)
	}
	_, err = h.c.Restart(h.ctx, auth(), stagingWeb)
	if le, ok := errors.AsType[*LockedError](err); !ok || !le.rule {
		t.Fatalf("asking first in staging: %v", err)
	}
	if _, err := h.c.SetAgentRulesBypass(h.ctx, admin, "ci", true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.Restart(h.ctx, auth(), web); err != nil {
		t.Fatalf("bypassing the rules: %v", err)
	}
}
