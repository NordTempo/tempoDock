package core

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// A promotion carries the image and nothing of the source's variables,
// references included: the target runs with its own.
func TestPromoteLeavesVariablesBehind(t *testing.T) {
	h := newHarness(t)
	prod := h.service("web", apitypes.CreateServiceRequest{})
	if _, err := h.c.SetOrgVariable(h.ctx, testSystem, "STRIPE_KEY", "sk_live_x"); err != nil {
		t.Fatal(err)
	}
	h.set(prod.Env(), "web", map[string]string{"PAY": "${{org.STRIPE_KEY}}", "PIN": "4711"}, true)
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	staging := ServiceRef{"shop", "staging", "web"}
	if _, err := h.c.CreateService(h.ctx, testSystem, staging.Env(), apitypes.CreateServiceRequest{Name: "web"}); err != nil {
		t.Fatal(err)
	}
	h.set(staging.Env(), "web", map[string]string{"OWN": "1"}, true)
	h.deploy(prod, "app:1")
	res, err := h.c.Promote(h.ctx, testSystem, "shop", apitypes.PromoteRequest{From: "production", To: "staging"})
	if err != nil || len(res.Promoted) != 1 {
		t.Fatalf("promote: %+v, %v", res, err)
	}
	if got := h.revealed(staging); len(got) != 1 || got["OWN"] != "1" {
		t.Errorf("staging's variables after a promotion from production = %v, want its own only", got)
	}
}

// A deploy token limited to a non-production environment deploys there,
// but can't promote what it deployed into production: it is forbidden and
// production gets no release.
func TestEnvironmentTokenCantPromoteIntoProduction(t *testing.T) {
	h := newHarness(t)
	prod := h.service("web", apitypes.CreateServiceRequest{})
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	staging := ServiceRef{"shop", "staging", "web"}
	if _, err := h.c.CreateService(h.ctx, testSystem, staging.Env(), apitypes.CreateServiceRequest{Name: "web"}); err != nil {
		t.Fatal(err)
	}
	tok, err := h.c.CreateToken(h.ctx, testSystem, apitypes.CreateTokenRequest{Name: "ci", Scope: apitypes.ScopeDeploy, Project: "shop", Environment: "staging"})
	if err != nil {
		t.Fatal(err)
	}
	ci, err := h.c.AuthenticateToken(h.ctx, tok.Secret, "")
	if err != nil {
		t.Fatal(err)
	}

	rel, err := h.c.DeployImage(h.ctx, ci, staging, apitypes.DeployImageRequest{Image: "app:1"})
	if err != nil {
		t.Fatalf("deploying to its own environment: %v", err)
	}
	if r := h.wait(staging, rel.Number); r.Status != apitypes.ReleaseActive {
		t.Fatalf("staging release: %s (%s)", r.Status, r.Error)
	}

	_, err = h.c.Promote(h.ctx, ci, "shop", apitypes.PromoteRequest{From: "staging", To: "production"})
	if CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("promoting into production: got %v, want forbidden", err)
	}
	if rels, err := h.c.ListReleases(h.ctx, testSystem, prod, 10); err != nil || len(rels) != 0 {
		t.Fatalf("production releases = %+v, %v; want none", rels, err)
	}
}
