package core

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// The platform's data access default applies where an org set nothing,
// and its ceiling caps org rules (refused when set, capped where older
// ones apply) but not org admins. Only operators change them.
func TestPlatformDataPolicyDefaultAndCeiling(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{})
	op := h.operator("op@x")
	dev := h.user("dev@x", apitypes.ScopeDeploy)
	es, err := h.c.envFor(h.ctx, testSystem, EnvRef{"shop", "production"}, apitypes.ScopeRead, "test")
	if err != nil {
		t.Fatal(err)
	}
	perm := func(a Actor) apitypes.DataPermission {
		t.Helper()
		p, err := h.c.dataPermission(h.ctx, a, es)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if got := perm(dev).Level; got != apitypes.LevelNone {
		t.Fatalf("deploy users in production = %s, want the built-in none", got)
	}

	read := apitypes.DataPolicyRule{Subject: apitypes.SubjectDeployUsers, Kind: apitypes.KindProduction, Level: apitypes.LevelRead, Sensitive: apitypes.SensitiveMasked}
	if _, err := h.c.SetPlatformDataPolicy(h.ctx, dev, apitypes.PlatformDataPolicy{Defaults: []apitypes.DataPolicyRule{read}}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a member set the platform's policy: %v", err)
	}
	if _, err := h.c.SetPlatformDataPolicy(h.ctx, op, apitypes.PlatformDataPolicy{Defaults: []apitypes.DataPolicyRule{read}}); err != nil {
		t.Fatal(err)
	}
	if got := perm(dev).Level; got != apitypes.LevelRead {
		t.Fatalf("deploy users in production = %s, want the platform's default read", got)
	}

	// An org rule giving write, then a ceiling of read: the rule is
	// capped where it applies, a new one above it is refused, and the
	// org's admin isn't capped.
	write := read
	write.Level = apitypes.LevelWrite
	if _, err := h.c.SetDataPolicy(h.ctx, testSystem, EnvRef{}, apitypes.UpdateDataPolicyRequest{Rules: []apitypes.DataPolicyRule{write}}); err != nil {
		t.Fatal(err)
	}
	if got := perm(dev).Level; got != apitypes.LevelWrite {
		t.Fatalf("with the org's rule = %s, want write", got)
	}
	if _, err := h.c.SetPlatformDataPolicy(h.ctx, op, apitypes.PlatformDataPolicy{Ceilings: []apitypes.DataPolicyRule{read}}); err != nil {
		t.Fatal(err)
	}
	if got := perm(dev).Level; got != apitypes.LevelRead {
		t.Fatalf("under the ceiling = %s, want read", got)
	}
	if _, err := h.c.SetDataPolicy(h.ctx, testSystem, EnvRef{Project: "shop"}, apitypes.UpdateDataPolicyRequest{Rules: []apitypes.DataPolicyRule{write}}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("a project rule above the ceiling was accepted: %v", err)
	}
	admin := h.user("ada@x", apitypes.ScopeAdmin)
	if got := perm(admin).Level; got != apitypes.LevelWrite {
		t.Fatalf("an org admin = %s, want write: the ceiling is for rules", got)
	}
	// A default above its ceiling is refused.
	if _, err := h.c.SetPlatformDataPolicy(h.ctx, op, apitypes.PlatformDataPolicy{Defaults: []apitypes.DataPolicyRule{write}, Ceilings: []apitypes.DataPolicyRule{read}}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("a default above its ceiling was accepted: %v", err)
	}
	h.restart() // kept in the store, loaded on start
	if got := perm(dev).Level; got != apitypes.LevelRead {
		t.Fatalf("after a restart = %s, want read", got)
	}
}

// Users are the platform's: operators list every account with its orgs,
// and choose who operates the platform, keeping one who can sign in.
func TestAccountsAndOperators(t *testing.T) {
	h := newHarness(t)
	op := h.operator("op@x")
	h.member("ada@x", map[string]apitypes.Scope{"test": apitypes.ScopeAdmin, "acme": apitypes.ScopeRead})
	accounts, err := h.c.ListAccounts(h.ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	var ada apitypes.Account
	for _, a := range accounts {
		if a.Email == "ada@x" {
			ada = a
		}
	}
	if len(ada.Orgs) != 2 || ada.Orgs[1].Org != "test" || !ada.Orgs[1].Owner || ada.Orgs[0].Owner {
		t.Fatalf("ada = %+v; want acme (read) and test (owner)", ada)
	}
	if _, err := h.c.ListAccounts(h.ctx, h.user("dev@x", apitypes.ScopeAdmin)); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("an org admin listed every account: %v", err)
	}

	op.VerifiedAt = now()
	if _, err := h.c.SetOperator(h.ctx, op, op.UserID, false); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("the last operator stepped down: %v", err)
	}
	if acc, err := h.c.SetOperator(h.ctx, op, ada.ID, true); err != nil || !acc.Operator {
		t.Fatalf("make ada an operator = %+v, %v", acc, err)
	}
	if acc, err := h.c.SetOperator(h.ctx, op, op.UserID, false); err != nil || acc.Operator {
		t.Fatalf("step down with another operator = %+v, %v", acc, err)
	}
}

// An org's defaults for new sites become a new site's settings; existing
// sites keep theirs. The org's admins set them.
func TestOrgSiteDefaults(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{})
	es, err := h.c.envFor(h.ctx, testSystem, EnvRef{"shop", "production"}, apitypes.ScopeRead, "test")
	if err != nil {
		t.Fatal(err)
	}
	newSite := func(name string) apitypes.SiteSettings {
		t.Helper()
		var s store.Site
		if err := h.store.Tx(h.ctx, func(q *store.Queries) error {
			var err error
			s, err = h.c.createSite(h.ctx, q, es.project.ID, es.env.ID, name, "test", now())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return siteOverrides(s)
	}
	if got := newSite("before"); got.EventRetentionDays != nil {
		t.Fatalf("a site before org defaults = %+v", got)
	}
	days := 30
	if _, err := h.c.SetOrgSiteDefaults(h.ctx, h.user("dev@x", apitypes.ScopeDeploy), apitypes.SiteSettings{EventRetentionDays: &days}, nil); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a deploy member set the org's defaults: %v", err)
	}
	d, err := h.c.SetOrgSiteDefaults(h.ctx, testSystem, apitypes.SiteSettings{EventRetentionDays: &days}, nil)
	if err != nil || *d.Effective.EventRetentionDays != 30 || d.Effective.ErrorRetentionDays == nil {
		t.Fatalf("org defaults = %+v, %v", d, err)
	}
	if got := newSite("after"); got.EventRetentionDays == nil || *got.EventRetentionDays != 30 {
		t.Fatalf("a new site = %+v; want the org's 30 days", got)
	}
}
