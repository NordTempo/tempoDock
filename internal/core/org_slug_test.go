package core

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// auditedIn counts audit events with action in an org's log (orgID), or
// the platform's (0).
func (h *harness) auditedIn(action string, orgID int64) int {
	h.t.Helper()
	var n int
	if err := h.store.DB.QueryRowContext(h.ctx, `SELECT count(*) FROM audit_events WHERE action = ? AND coalesce(org_id, 0) = ?`, action, orgID).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

// An org's unlimited admins change its slug under the rules for a new
// one; the old slug keeps naming the org for members and its tokens.
func TestChangeOrgSlug(t *testing.T) {
	h := newHarness(t)
	ada := h.member("ada@x", map[string]apitypes.Scope{"acme": apitypes.ScopeAdmin})
	bob := h.member("bob@x", map[string]apitypes.Scope{"acme": apitypes.ScopeDeploy})
	admin := h.in(ada, "acme")
	tok, err := h.c.CreateToken(h.ctx, admin, apitypes.CreateTokenRequest{Name: "ci", Scope: apitypes.ScopeDeploy})
	if err != nil {
		t.Fatal(err)
	}

	for slug, code := range map[string]apitypes.ErrorCode{
		"Bad_Slug": apitypes.CodeValidation, "console": apitypes.CodeValidation, "test": apitypes.CodeConflict,
	} {
		if _, err := h.c.RenameOrg(h.ctx, admin, apitypes.UpdateOrgRequest{Slug: slug}); CodeOf(err) != code {
			t.Errorf("slug %q: %v, want %s", slug, err, code)
		}
	}
	if _, err := h.c.RenameOrg(h.ctx, h.in(bob, "acme"), apitypes.UpdateOrgRequest{Slug: "nordtempo"}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a deploy member changing the slug: %v", err)
	}

	o, err := h.c.RenameOrg(h.ctx, admin, apitypes.UpdateOrgRequest{Name: "NordTempo", Slug: "nordtempo"})
	if err != nil || o.Slug != "nordtempo" || o.Name != "NordTempo" {
		t.Fatalf("rename = %+v, %v", o, err)
	}
	if n := h.auditedIn("org.slug", admin.OrgID); n != 1 {
		t.Errorf("org.slug in the org's log: %d, want 1", n)
	}

	// The old slug names the org, which acts under its new one.
	if in := h.in(bob, "acme"); in.OrgID != admin.OrgID || in.OrgSlug != "nordtempo" || in.Scope != apitypes.ScopeDeploy {
		t.Fatalf("bob in acme = %+v", in)
	}
	if a, err := h.c.AuthenticateToken(h.ctx, tok.Secret, "acme"); err != nil || a.OrgSlug != "nordtempo" {
		t.Fatalf("token with the old slug: %+v, %v", a, err)
	}
	if _, err := h.c.AuthenticateToken(h.ctx, tok.Secret, "test"); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("token in another org: %v", err)
	}

	// Setting the old slug again takes it back; the newer one is old now.
	if _, err := h.c.RenameOrg(h.ctx, h.in(ada, "nordtempo"), apitypes.UpdateOrgRequest{Slug: "acme"}); err != nil {
		t.Fatal(err)
	}
	if in := h.in(bob, "nordtempo"); in.OrgSlug != "acme" {
		t.Fatalf("bob in nordtempo after moving back = %+v", in)
	}
}

// An old slug is released when another org takes it: from then on it
// names that org, and the old org's members and tokens no longer reach
// theirs through it.
func TestTakeReleasedOrgSlug(t *testing.T) {
	h := newHarness(t)
	ada := h.member("ada@x", map[string]apitypes.Scope{"acme": apitypes.ScopeAdmin})
	admin := h.in(ada, "acme")
	tok, err := h.c.CreateToken(h.ctx, admin, apitypes.CreateTokenRequest{Name: "ci", Scope: apitypes.ScopeDeploy})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.RenameOrg(h.ctx, admin, apitypes.UpdateOrgRequest{Slug: "nordtempo"}); err != nil {
		t.Fatal(err)
	}

	carol := h.member("carol@x", nil)
	taken, err := h.c.CreateOrg(h.ctx, carol, apitypes.CreateOrgRequest{Slug: "acme"})
	if err != nil {
		t.Fatalf("creating an org with a released slug: %v", err)
	}
	if in := h.in(carol, "acme"); in.OrgSlug != taken.Slug || in.OrgID == admin.OrgID {
		t.Fatalf("carol in acme = %+v", in)
	}
	if _, err := h.c.actIn(h.ctx, ada, "acme", apitypes.ScopeAdmin); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("ada in the taken slug: %v, want not found", err)
	}
	if _, err := h.c.AuthenticateToken(h.ctx, tok.Secret, "acme"); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("the old org's token in the taken slug: %v", err)
	}

	// Changing a slug to another org's old one takes it the same way.
	if _, err := h.c.RenameOrg(h.ctx, h.in(carol, "acme"), apitypes.UpdateOrgRequest{Slug: "carol"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.RenameOrg(h.ctx, h.in(ada, "nordtempo"), apitypes.UpdateOrgRequest{Slug: "acme"}); err != nil {
		t.Fatalf("taking carol's old slug: %v", err)
	}
	if in := h.in(ada, "acme"); in.OrgID != admin.OrgID {
		t.Fatalf("ada in acme = %+v", in)
	}
	if _, err := h.c.actIn(h.ctx, carol, "acme", apitypes.ScopeAdmin); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("carol in acme, taken back: %v", err)
	}
}

// Operators change any org's slug without being members, audited in the
// org's log and the platform's; others can't use the console's way.
func TestOperatorChangesOrgSlug(t *testing.T) {
	h := newHarness(t)
	ada := h.member("ada@x", map[string]apitypes.Scope{"acme": apitypes.ScopeAdmin})
	acme := h.in(ada, "acme")
	op := h.operator("op@x")

	if _, err := h.c.OperatorRenameOrg(h.ctx, acme, "acme", apitypes.UpdateOrgRequest{Slug: "nordtempo"}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("an org admin through the console: %v", err)
	}
	o, err := h.c.OperatorRenameOrg(h.ctx, op, "acme", apitypes.UpdateOrgRequest{Slug: "nordtempo"})
	if err != nil || o.Slug != "nordtempo" {
		t.Fatalf("operator rename = %+v, %v", o, err)
	}
	if org, plat := h.auditedIn("org.slug", acme.OrgID), h.auditedIn("org.slug", 0); org != 1 || plat != 1 {
		t.Errorf("org.slug audited %d times in the org's log and %d in the platform's; want 1 and 1", org, plat)
	}
	// The console finds the org by its old slug too.
	if _, err := h.c.OperatorRenameOrg(h.ctx, op, "acme", apitypes.UpdateOrgRequest{Name: "NordTempo"}); err != nil {
		t.Fatalf("operator by the old slug: %v", err)
	}
}
