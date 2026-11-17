package core

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// A token's grants decide everything about it (ADR 0080, 6b), also what
// still speaks in scopes: once an admin changes its grants, its scope is
// what they amount to at its limit's place. Narrowing an admin token to
// viewing lowers its Data panel access and whose access requests it
// lists; adding to a token keeps what its scope gave, and a read token
// given Full access is an admin's.
func TestATokensGrantsDecideItsScope(t *testing.T) {
	h := newHarness(t)
	h.enforce()
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.deploy(web, "app:1")
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	admin := h.user("ada@example.com", apitypes.ScopeAdmin)
	prod := web.Env()
	es, err := h.c.loadEnv(h.ctx, 1, prod)
	if err != nil {
		t.Fatal(err)
	}
	secrets := map[string]string{}
	for name, req := range map[string]apitypes.CreateTokenRequest{
		"boss":    {Scope: apitypes.ScopeAdmin},
		"limited": {Scope: apitypes.ScopeDeploy, Project: "shop", Environment: "staging"},
		"reader":  {Scope: apitypes.ScopeRead},
	} {
		req.Name = name
		tok, err := h.c.createToken(h.ctx, testSystem, req)
		if err != nil {
			t.Fatal(err)
		}
		secrets[name] = tok.Secret
	}
	auth := func(name string) Actor {
		t.Helper()
		a, err := h.c.AuthenticateToken(h.ctx, secrets[name], "")
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	if p, err := h.c.dataPermission(h.ctx, auth("boss"), es); err != nil || !p.Manage || p.Sensitive != apitypes.SensitiveVisible {
		t.Fatalf("an admin token's data access = %+v, %v", p, err)
	}

	// Narrowed to viewing production: every consumer of its scope follows.
	for _, role := range []string{"deploy:view", "alerts:view"} {
		if _, err := h.c.GrantAgent(h.ctx, admin, "boss", apitypes.GrantRequest{Role: role, Project: "shop", Environment: "production"}); err != nil {
			t.Fatal(err)
		}
	}
	acc, err := h.c.AgentAccess(h.ctx, admin, "boss")
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range acc.Grants {
		if g.Role == "full" {
			if err := h.c.RevokeGrant(h.ctx, admin, g.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	boss := auth("boss")
	if boss.Scope != apitypes.ScopeRead || boss.Me().Scope != apitypes.ScopeRead {
		t.Fatalf("a token narrowed to viewing has scope %s", boss.Scope)
	}
	p, err := h.c.dataPermission(h.ctx, boss, es)
	if err != nil {
		t.Fatal(err)
	}
	if p.Manage || p.Approve || p.Sensitive == apitypes.SensitiveVisible || p.Level.Includes(apitypes.LevelPropose) {
		t.Fatalf("its data access in production is still an admin's: level %s, sensitive %s, manage %v", p.Level, p.Sensitive, p.Manage)
	}
	// Access requests: its own, not the org's.
	other := h.token(testSystem, apitypes.CreateTokenRequest{Name: "other", Scope: apitypes.ScopeDeploy})
	h.lock("production")
	_, err = h.c.Restart(h.ctx, other, web)
	if err := h.c.FileRequest(h.ctx, other, err, Call{Via: "test", Op: "restart", Title: "Restart", Args: []byte(`{}`)}); CodeOf(err) != apitypes.CodeEnvironmentLocked {
		t.Fatal(err)
	}
	if rs, err := h.c.ListRequests(h.ctx, boss, RequestFilter{}); err != nil || len(rs) != 0 {
		t.Fatalf("a narrowed token lists the org's access requests: %+v, %v", rs, err)
	}

	// A grant added to a limited token leaves what its scope gave.
	if _, err := h.c.GrantAgent(h.ctx, admin, "limited", apitypes.GrantRequest{Role: "logs:view", Project: "shop", Environment: "production"}); err != nil {
		t.Fatal(err)
	}
	if a := auth("limited"); a.Scope != apitypes.ScopeDeploy {
		t.Fatalf("a deploy token with one more grant has scope %s", a.Scope)
	}
	// Full access on the org makes a read token an admin's.
	if _, err := h.c.GrantAgent(h.ctx, admin, "reader", apitypes.GrantRequest{Role: "full"}); err != nil {
		t.Fatal(err)
	}
	if a := auth("reader"); a.Scope != apitypes.ScopeAdmin {
		t.Fatalf("a read token with Full access has scope %s", a.Scope)
	}
}
