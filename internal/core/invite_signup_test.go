package core

import (
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Someone invited without an account reads the invite signed out, creates
// the account for its email from it, and is signed in as a member with
// its role; the code then works no more.
func TestInviteSignUp(t *testing.T) {
	h := newHarness(t)
	admin := h.in(h.member("ada@x", map[string]apitypes.Scope{"test": apitypes.ScopeAdmin}), "test")
	invite := func(email string) string {
		t.Helper()
		inv, err := h.c.CreateInvite(h.ctx, admin, apitypes.CreateInviteRequest{Email: email, Role: apitypes.ScopeDeploy})
		if err != nil {
			t.Fatal(err)
		}
		return inv.Code
	}
	const ip = "192.0.2.7"
	code := invite("New@x")

	info, err := h.c.Invite(h.ctx, code, ip)
	if err != nil || info.OrgName == "" || info.Email != "New@x" || info.Role != apitypes.ScopeDeploy || info.InvitedBy != "ada@x" {
		t.Fatalf("invite signed out = %+v, %v", info, err)
	}
	if _, err := h.c.Invite(h.ctx, invitePrefix+"wrong", ip); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("wrong code: %v", err)
	}
	if _, err := h.c.SignUpWithInvite(h.ctx, invitePrefix+"wrong", apitypes.InviteSignUpRequest{Name: "New", Password: "long enough password"}, ip); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("sign-up with a wrong code: %v", err)
	}
	for _, bad := range []apitypes.InviteSignUpRequest{
		{Name: "New", Password: "short"},
		{Name: "  ", Password: "long enough password"},
		{Name: strings.Repeat("n", 65), Password: "long enough password"},
	} {
		if _, err := h.c.SignUpWithInvite(h.ctx, code, bad, ip); CodeOf(err) != apitypes.CodeValidation {
			t.Fatalf("sign-up with %+v: %v", bad, err)
		}
	}

	res, err := h.c.SignUpWithInvite(h.ctx, code, apitypes.InviteSignUpRequest{Name: "  New Person ", Password: "long enough password"}, ip)
	if err != nil || res.Pending || res.Secret == "" {
		t.Fatalf("sign-up = %+v, %v", res, err)
	}
	a, err := h.c.AuthenticateSession(h.ctx, res.Secret, "", false)
	if err != nil || a.Email != "New@x" || a.DisplayName != "New Person" || a.OrgSlug != "test" || a.Scope != apitypes.ScopeDeploy {
		t.Fatalf("new session = %+v, %v", a, err)
	}
	if _, err := h.c.Login(h.ctx, "new@x", "long enough password", ip); err != nil {
		t.Fatalf("logging in with the new password: %v", err)
	}
	if _, err := h.c.Invite(h.ctx, code, ip); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("a used invite: %v", err)
	}
	if _, err := h.c.SignUpWithInvite(h.ctx, code, apitypes.InviteSignUpRequest{Name: "Again", Password: "long enough password"}, ip); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("signing up twice: %v", err)
	}
	if h.audited("user.create") != 1 || h.audited("org.join") != 1 {
		t.Fatalf("audited user.create %d and org.join %d times; want once each", h.audited("user.create"), h.audited("org.join"))
	}

	// An email with an account signs in and accepts instead.
	h.member("bob@x", nil)
	if _, err := h.c.SignUpWithInvite(h.ctx, invite("bob@x"), apitypes.InviteSignUpRequest{Name: "Bob", Password: "long enough password"}, ip); CodeOf(err) != apitypes.CodeConflict || !strings.Contains(err.Error(), "sign in") {
		t.Fatalf("an existing account: %v", err)
	}

	late := invite("late@x")
	if _, err := h.store.DB.ExecContext(h.ctx, "UPDATE org_invites SET expires_at = ? WHERE email = ?", time.Now().Add(-time.Minute).UTC(), "late@x"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.Invite(h.ctx, late, ip); CodeOf(err) != apitypes.CodeNotFound || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("an expired invite: %v", err)
	}
	if _, err := h.c.SignUpWithInvite(h.ctx, late, apitypes.InviteSignUpRequest{Name: "Late", Password: "long enough password"}, ip); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("signing up with an expired invite: %v", err)
	}

}

// A user changes their own display name; tokens can't, and the name is
// checked.
func TestUpdateMe(t *testing.T) {
	h := newHarness(t)
	ada := h.in(h.member("ada@x", map[string]apitypes.Scope{"test": apitypes.ScopeRead}), "test")
	me, err := h.c.UpdateMe(h.ctx, ada, apitypes.UpdateMeRequest{Name: "  Ada Lovelace  "})
	if err != nil || me.DisplayName != "Ada Lovelace" || me.Email != "ada@x" {
		t.Fatalf("update me = %+v, %v", me, err)
	}
	if u, _ := h.store.GetUserByEmail(h.ctx, "ada@x"); u.Name != "Ada Lovelace" {
		t.Fatalf("stored name = %q", u.Name)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("é", 65), "two\nlines"} {
		if _, err := h.c.UpdateMe(h.ctx, ada, apitypes.UpdateMeRequest{Name: bad}); CodeOf(err) != apitypes.CodeValidation {
			t.Fatalf("name %q: %v", bad, err)
		}
	}
	if _, err := h.c.UpdateMe(h.ctx, testSystem, apitypes.UpdateMeRequest{Name: "Robot"}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("a token renaming itself: %v", err)
	}
	if h.audited("user.name") != 1 {
		t.Fatal("the rename isn't audited")
	}
}
