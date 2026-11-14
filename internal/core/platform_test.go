package core

import (
	"strings"
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

// loginToken makes a `dock login` token for user a (platform: a
// platform login), as approving one in the browser does, and returns its
// secret.
func (h *harness) loginToken(a Actor, platform bool) string {
	h.t.Helper()
	var kind *string
	prefix := tokenPrefix
	if platform {
		kind, prefix = ptr("login"), platformTokenPrefix
	}
	secret, hash := newSecret(prefix)
	if _, err := h.store.CreateCLILoginToken(h.ctx, store.CreateCLILoginTokenParams{
		Name: "cli: test", Hash: hash, Scope: string(apitypes.ScopeAdmin), CreatedBy: a.Email, UserID: &a.UserID, Platform: kind,
	}); err != nil {
		h.t.Fatal(err)
	}
	return secret
}

// auth authenticates a token's secret, failing the test on an error.
func (h *harness) auth(secret string) Actor {
	h.t.Helper()
	a, err := h.c.AuthenticateToken(h.ctx, secret, "")
	if err != nil {
		h.t.Fatal(err)
	}
	return a
}

// TestPlatformApart: running the platform takes a platform credential
// (ADR 0080). A platform token runs it with its role and never acts in an
// org; a viewer only reads; an operator's org login doesn't run it and
// says to log in to the platform; a platform login follows its person's
// role as it is now; org tokens can't be made operators; and a legacy
// token, once split, is the org's only, with a platform token beside it.
func TestPlatformApart(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	op := h.operator("op@example.com")
	if err := joinOrgMember(h.ctx, h.store.Queries, 1, op.UserID, apitypes.ScopeAdmin); err != nil {
		t.Fatal(err)
	}
	op.Email = "op@example.com"

	opTok, err := h.c.CreatePlatformToken(h.ctx, testSystem, apitypes.CreatePlatformTokenRequest{Name: "ops", Role: apitypes.PlatformOperator})
	if err != nil || !strings.HasPrefix(opTok.Secret, platformTokenPrefix) || opTok.Token.Platform != "operator" {
		t.Fatalf("platform token = %+v, %v", opTok, err)
	}
	platform := h.auth(opTok.Secret)
	if _, err := h.c.ListServers(h.ctx, platform); err != nil {
		t.Fatalf("platform token lists servers: %v", err)
	}
	if _, err := h.c.ListPlacements(h.ctx, platform, "shop"); err != nil {
		t.Fatalf("platform token reads the only org's placements: %v", err)
	}
	_, err = h.c.CreateProject(h.ctx, platform, apitypes.CreateProjectRequest{Name: "blog"})
	if r := refusalOf(t, err); r.DecidedBy != apitypes.RefusedPlatform || r.Need != "org" {
		t.Fatalf("platform token creates a project: %+v", r)
	}

	viewTok, err := h.c.CreatePlatformToken(h.ctx, testSystem, apitypes.CreatePlatformTokenRequest{Name: "watch", Role: apitypes.PlatformViewer})
	if err != nil {
		t.Fatal(err)
	}
	viewer := h.auth(viewTok.Secret)
	if _, err := h.c.ListServers(h.ctx, viewer); err != nil {
		t.Fatalf("viewer lists servers: %v", err)
	}
	_, err = h.c.AddServer(h.ctx, viewer, apitypes.AddServerRequest{Name: "homelab", AcceptsNonProduction: true})
	if r := refusalOf(t, err); r.Need != string(ServersManage) || !strings.Contains(r.Reason, "platform role viewer") {
		t.Fatalf("viewer adds a server: %+v", r)
	}

	// An operator's org login runs no platform; their platform login does.
	orgLogin := h.auth(h.loginToken(op, false))
	_, err = h.c.ListServers(h.ctx, orgLogin)
	if r := refusalOf(t, err); !strings.Contains(r.Fix, "dock login --platform") {
		t.Fatalf("org login lists servers: %+v", r)
	}
	if _, err := h.c.CreateProject(h.ctx, orgLogin, apitypes.CreateProjectRequest{Name: "blog"}); err != nil {
		t.Fatalf("org login creates a project: %v", err)
	}
	platformSecret := h.loginToken(op, true)
	if a := h.auth(platformSecret); !a.Me().PlatformLogin || a.Platform != apitypes.PlatformOperator {
		t.Fatalf("platform login = %+v", a.Me())
	}
	h.exec(`UPDATE users SET platform_role = 'viewer' WHERE id = ?`, op.UserID)
	if _, err := h.c.AddServer(h.ctx, h.auth(platformSecret), apitypes.AddServerRequest{Name: "homelab", AcceptsNonProduction: true}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("platform login of a viewer now adds a server: %v", err)
	}
	h.exec(`UPDATE users SET platform_role = 'operator' WHERE id = ?`, op.UserID)

	// No new org token runs the platform; a legacy one does until split.
	if _, err := h.c.CreateToken(h.ctx, testSystem, apitypes.CreateTokenRequest{Name: "both", Scope: apitypes.ScopeAdmin, Operator: true}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("operator org token: %v", err)
	}
	legacy, err := h.c.createToken(h.ctx, testSystem, apitypes.CreateTokenRequest{Name: "bootstrap", Scope: apitypes.ScopeAdmin, Operator: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.ListServers(h.ctx, h.auth(legacy.Secret)); err != nil {
		t.Fatalf("legacy token lists servers: %v", err)
	}
	split, err := h.c.SplitToken(h.ctx, testSystem, legacy.Token.ID)
	if err != nil || split.Token.Operator || split.Platform.Token.Platform != "operator" {
		t.Fatalf("split = %+v, %v", split, err)
	}
	if _, err := h.c.ListServers(h.ctx, h.auth(legacy.Secret)); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("split org token lists servers: %v", err)
	}
	if _, err := h.c.ListServers(h.ctx, h.auth(split.Platform.Secret)); err != nil {
		t.Fatalf("split's platform token lists servers: %v", err)
	}
	if list, err := h.c.ListPlatformTokens(h.ctx, viewer); err != nil || len(list) != 4 {
		t.Fatalf("platform tokens = %+v, %v", list, err)
	}
}
