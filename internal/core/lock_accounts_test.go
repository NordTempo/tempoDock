package core

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// TestLockStopsTokensManagingAccounts: while an environment is locked, an
// operator token can't hand out more power (invites, roles, admin tokens;
// no org token runs the platform since ADR 0080);
// without a lock it can, a person in the UI always can, and taking power
// away stays open. Accounts themselves are never a token's
// (TestTokensCantManageAccounts).
func TestLockStopsTokensManagingAccounts(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	op := Actor{Kind: "user", OrgID: 1, OrgSlug: "default", Name: "op@example.com", Scope: apitypes.ScopeAdmin, Platform: apitypes.PlatformOperator, Operator: true}
	token := Actor{Kind: "token", OrgID: 1, OrgSlug: "default", Name: "agent", TokenID: 9, Scope: apitypes.ScopeAdmin, Platform: apitypes.PlatformOperator, Operator: true}
	bob := h.user("bob@example.com", apitypes.ScopeRead)
	h.user("admin@example.com", apitypes.ScopeAdmin) // so bob can step down
	n := 0
	// actions runs every guarded operation as a, each on fresh targets.
	actions := func(a Actor) map[string]error {
		n++
		email := func(s string) string { return s + string(rune('a'+n)) + "@example.com" }
		errs := map[string]error{}
		_, errs["invite"] = h.c.CreateInvite(h.ctx, a, apitypes.CreateInviteRequest{Email: email("invited"), Role: apitypes.ScopeAdmin})
		_, errs["role"] = h.c.SetMemberRole(h.ctx, a, bob.UserID, apitypes.SetMemberRoleRequest{Role: apitypes.ScopeAdmin})
		_, errs["admin token"] = h.c.CreateToken(h.ctx, a, apitypes.CreateTokenRequest{Name: "admin", Scope: apitypes.ScopeAdmin})
		return errs
	}
	expect := func(who string, a Actor, want apitypes.ErrorCode) {
		t.Helper()
		for name, err := range actions(a) {
			if want == "" && err != nil || want != "" && CodeOf(err) != want {
				t.Errorf("%s, %s: %v", who, name, err)
			}
		}
	}

	expect("token, no lock", token, "")
	h.lock("production")
	expect("token, locked", token, apitypes.CodeForbidden)
	expect("user, locked", op, "")

	// Taking power away, and tokens that are locked out anyway, stay open.
	if _, err := h.c.CreateToken(h.ctx, token, apitypes.CreateTokenRequest{Name: "ci", Scope: apitypes.ScopeDeploy}); err != nil {
		t.Fatalf("deploy token: %v", err)
	}
	if _, err := h.c.SetUserDisabled(h.ctx, op, bob.UserID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SetMemberRole(h.ctx, op, bob.UserID, apitypes.SetMemberRoleRequest{Role: apitypes.ScopeRead}); err != nil {
		t.Fatal(err)
	}
	if err := h.c.RemoveMember(h.ctx, token, bob.UserID); err != nil {
		t.Fatalf("remove member: %v", err)
	}
}
