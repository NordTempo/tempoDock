package core

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// grantedPerson is a member in no group but with grants, as an admin gave
// them (ADR 0080, enforce): the review's setups.
func grantedPerson(h *harness, admin Actor, email string, grants ...apitypes.GrantRequest) Actor {
	h.t.Helper()
	p := h.user(email, apitypes.ScopeRead)
	if _, err := h.c.RemoveGroupMember(h.ctx, admin, "Viewers", p.Email); err != nil {
		h.t.Fatal(err)
	}
	for _, g := range grants {
		if _, err := h.c.GrantMember(h.ctx, admin, p.Email, g); err != nil {
			h.t.Fatal(err)
		}
	}
	return h.in(userActor(h.userRow(p.UserID)), "")
}

// The pre-open-source security review's findings in enforce mode: reveal
// on one environment reveals only there; members:edit gives no more
// than its holder has; an Approver for some kinds sees only their
// requests; finer grants hold in what view-only checks let through.
func TestEnforceSecurityReview(t *testing.T) {
	h := newHarness(t)
	h.enforce()
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.deploy(web, "app:1")
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, web.Project, apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	h.service2("web", "staging")
	admin := h.user("ada@example.com", apitypes.ScopeAdmin)

	// HIGH: variables:reveal on staging reveals staging's, not production's.
	rev := grantedPerson(h, admin, "rev@example.com",
		apitypes.GrantRequest{Role: "deploy:view"},
		apitypes.GrantRequest{Role: "variables:reveal", Project: "shop", Environment: "staging"})
	if _, err := h.c.ListVariables(h.ctx, rev, EnvRef{"shop", "production"}, "", true); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("revealing production with reveal on staging: %v", err)
	}
	if _, err := h.c.ListVariables(h.ctx, rev, EnvRef{"shop", "staging"}, "", true); err != nil {
		t.Fatalf("revealing staging: %v", err)
	}
	if _, err := h.c.ListOrgVariables(h.ctx, rev, true); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("revealing the org's variables: %v", err)
	}
	if es, err := h.c.loadEnv(h.ctx, 1, EnvRef{"shop", "production"}); err != nil || rev.revealsIn(es) {
		t.Fatalf("log masking reveals production: %v", err)
	}

	// MEDIUM: Members and access edit doesn't make its holder Full.
	mgr := grantedPerson(h, admin, "mgr@example.com", apitypes.GrantRequest{Role: "members:edit"})
	if _, err := h.c.GrantMember(h.ctx, mgr, mgr.Email, apitypes.GrantRequest{Role: "full"}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("granting itself Full: %v", err)
	}
	if _, err := h.c.AddGroupMember(h.ctx, mgr, "Admins", mgr.Email); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("joining Admins: %v", err)
	}
	if _, err := h.c.SetMemberRole(h.ctx, mgr, mgr.UserID, apitypes.SetMemberRoleRequest{Role: apitypes.ScopeAdmin}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("making itself admin: %v", err)
	}
	if _, err := h.c.CreateToken(h.ctx, mgr, apitypes.CreateTokenRequest{Name: "x", Scope: apitypes.ScopeAdmin}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("minting an admin token: %v", err)
	}
	if _, err := h.c.GrantMember(h.ctx, mgr, rev.Email, apitypes.GrantRequest{Role: "members:view"}); err != nil {
		t.Fatalf("giving what it holds: %v", err)
	}

	// LOW-MEDIUM: an Approver for other kinds doesn't see production's
	// requests.
	appr := grantedPerson(h, admin, "appr@example.com",
		apitypes.GrantRequest{Role: "deploy:view"},
		apitypes.GrantRequest{Role: "alerts:view"},
		apitypes.GrantRequest{Role: "approver", Kinds: []string{"other"}})
	h.lock("production")
	rh := &requestHarness{harness: h, agent: h.token(testSystem, apitypes.CreateTokenRequest{Name: "claude", Scope: apitypes.ScopeDeploy})}
	h.c.SetReplayer("test", func(_ context.Context, _ Actor, _ string, _ json.RawMessage) (json.RawMessage, error) {
		return nil, nil
	})
	le := rh.ask("1", "")
	if _, err := h.c.GetRequest(h.ctx, appr, le.Request); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("an Approver for other kinds read a production request: %v", err)
	}
	if _, err := h.c.GetRequest(h.ctx, admin, le.Request); err != nil {
		t.Fatalf("an admin reads it: %v", err)
	}

	// LOW: logs:view on staging lists staging's errors and gets its build
	// logs, not production's.
	viewer := grantedPerson(h, admin, "vw@example.com",
		apitypes.GrantRequest{Role: "deploy:view"},
		apitypes.GrantRequest{Role: "logs:view", Project: "shop", Environment: "staging"})
	if _, err := h.c.ListErrors(h.ctx, viewer, ErrorFilter{}); err != nil {
		t.Fatalf("listing staging's errors: %v", err)
	}
	prod, err := h.c.loadEnv(h.ctx, 1, EnvRef{"shop", "production"})
	if err != nil {
		t.Fatal(err)
	}
	staging, err := h.c.loadEnv(h.ctx, 1, EnvRef{"shop", "staging"})
	if err != nil {
		t.Fatal(err)
	}
	p := eventPerms[apitypes.EventBuildLog]
	if viewer.holdsAt(p, prod.project.ID, prod.env.ID, envKind(prod.env)) || !viewer.holdsAt(p, staging.project.ID, staging.env.ID, envKind(staging.env)) {
		t.Fatal("build logs and errors follow logs:view where it is")
	}
}
