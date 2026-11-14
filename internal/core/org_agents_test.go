package core

import (
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
