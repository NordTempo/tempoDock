package tempobase

import (
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
)

// The first admin needs the setup code, which works once.
func TestSetupCode(t *testing.T) {
	h := newHarness(t)
	code, ok, err := h.StartSetup(h.ctx)
	if err != nil || !ok || len(code) < 20 {
		t.Fatalf("StartSetup = %q, %v, %v", code, ok, err)
	}
	req := tbtypes.SetupRequest{Code: "wrong", Email: "admin@example.com", Name: "Admin", Password: testPassword}
	_, _, _, err = h.Setup(h.ctx, req, "10.0.0.1")
	wantCode(t, err, apitypes.CodeUnauthorized)

	req.Code = code
	secret, _, act, err := h.Setup(h.ctx, req, "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if !act.IsAdmin() || !strings.HasPrefix(secret, sessionPrefix) {
		t.Fatalf("setup made %+v with session %q", act, secret[:4])
	}
	if _, err := h.AuthenticateSession(h.ctx, secret); err != nil {
		t.Fatal(err)
	}
	req.Email = "second@example.com"
	_, _, _, err = h.Setup(h.ctx, req, "10.0.0.1")
	wantCode(t, err, apitypes.CodeConflict)
	if _, ok, _ := h.StartSetup(h.ctx); ok {
		t.Fatal("StartSetup made a code with an admin present")
	}
}

// A person created with `tempobase admin create-user` after start spends
// the setup code: it can't create a second, unrelated admin.
func TestSetupAfterAdminCommand(t *testing.T) {
	h := newHarness(t)
	code, _, _ := h.StartSetup(h.ctx)
	h.user("scripted@example.com", tbtypes.RoleAdmin)
	_, _, _, err := h.Setup(h.ctx, tbtypes.SetupRequest{Code: code, Email: "late@example.com", Name: "Late", Password: testPassword}, "10.0.0.1")
	wantCode(t, err, apitypes.CodeConflict)
	if _, err := h.store.GetUserByEmail(h.ctx, "late@example.com"); err == nil {
		t.Fatal("setup created a second admin")
	}
}

// Sessions last 30 days without sliding, end with a disabled account, and
// a password change ends every other session.
func TestSessions(t *testing.T) {
	h := newHarness(t)
	h.admin()
	viewer := h.user("viewer@example.com", tbtypes.RoleViewer)

	_, _, _, err := h.Login(h.ctx, "viewer@example.com", "wrong password!", "10.0.0.2")
	wantCode(t, err, apitypes.CodeUnauthorized)
	_, _, _, err = h.Login(h.ctx, "nobody@example.com", testPassword, "10.0.0.6")
	wantCode(t, err, apitypes.CodeUnauthorized)

	first, expires, act, err := h.Login(h.ctx, "VIEWER@example.com", testPassword, "10.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	if act.UserID != viewer.UserID || !expires.Equal(h.clock.Add(SessionTTL)) {
		t.Fatalf("login = %+v until %v", act, expires)
	}
	second, _, _, err := h.Login(h.ctx, "viewer@example.com", testPassword, "10.0.0.2")
	if err != nil {
		t.Fatal(err)
	}

	// A password change keeps only the session it returns.
	_, _, err = h.ChangePassword(h.ctx, viewer, apitypes.ChangePasswordRequest{CurrentPassword: "not it at all", NewPassword: "another long password"}, "10.0.0.7")
	wantCode(t, err, apitypes.CodeValidation)
	third, _, err := h.ChangePassword(h.ctx, viewer, apitypes.ChangePasswordRequest{CurrentPassword: testPassword, NewPassword: "another long password"}, "10.0.0.7")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{first, second} {
		_, err := h.AuthenticateSession(h.ctx, s)
		wantCode(t, err, apitypes.CodeUnauthorized)
	}
	if _, err := h.AuthenticateSession(h.ctx, third); err != nil {
		t.Fatal(err)
	}

	// Expired: refused and deleted.
	h.clock = h.clock.Add(SessionTTL + time.Second)
	_, err = h.AuthenticateSession(h.ctx, third)
	wantCode(t, err, apitypes.CodeUnauthorized)
	if _, err := h.store.GetSessionUser(h.ctx, hashSecret(third)); err == nil {
		t.Fatal("expired session was kept")
	}

	// Disabled: refused at once.
	fresh, _, _, err := h.Login(h.ctx, "viewer@example.com", "another long password", "10.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.UpdateUser(h.ctx, System, viewer.UserID, tbtypes.UpdateUserRequest{Disabled: new(true)}); err != nil {
		t.Fatal(err)
	}
	_, err = h.AuthenticateSession(h.ctx, fresh)
	wantCode(t, err, apitypes.CodeUnauthorized)
}

// Sign-in attempts are limited per email and client.
func TestLoginRateLimit(t *testing.T) {
	h := newHarness(t)
	h.user("viewer@example.com", tbtypes.RoleViewer)
	var err error
	for range 6 {
		_, _, _, err = h.Login(h.ctx, "viewer@example.com", "wrong password!", "10.0.0.3")
	}
	wantCode(t, err, apitypes.CodeRateLimited)
}

// Tokens are agents capped by their owner, stop when revoked, expired or
// their owner is disabled, and are limited to the connections chosen.
func TestTokens(t *testing.T) {
	h := newHarness(t)
	h.admin()
	editor := h.user("editor@example.com", tbtypes.RoleEditor)
	h.connection("orders", false)
	h.connection("billing", true)

	created, err := h.CreateToken(h.ctx, editor, tbtypes.CreateTokenRequest{Name: "agent", Connections: []string{"orders"}, ExpiresInDays: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Secret, tokenPrefix) || created.Token.Prefix != created.Secret[:tokenPrefixLen] {
		t.Fatalf("token %+v", created.Token)
	}
	tok, err := h.AuthenticateToken(h.ctx, created.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Kind != "token" || tok.Role != tbtypes.RoleEditor || tok.IsAdmin() || len(tok.TokenConnections) != 1 {
		t.Fatalf("token actor %+v", tok)
	}
	me, err := h.Me(h.ctx, tok)
	if err != nil || len(me.TokenConnections) != 1 || me.TokenConnections[0] != "orders" {
		t.Fatalf("Me = %+v, %v", me, err)
	}
	_, err = h.CreateToken(h.ctx, tok, tbtypes.CreateTokenRequest{Name: "child"})
	wantCode(t, err, apitypes.CodeForbidden)
	_, err = h.CreateToken(h.ctx, editor, tbtypes.CreateTokenRequest{Name: "x", Connections: []string{"missing"}})
	wantCode(t, err, apitypes.CodeNotFound)

	// Someone else's token doesn't exist for them.
	viewer := h.user("viewer@example.com", tbtypes.RoleViewer)
	err = h.RevokeToken(h.ctx, viewer, created.Token.ID)
	wantCode(t, err, apitypes.CodeNotFound)

	h.clock = h.clock.Add(25 * time.Hour)
	_, err = h.AuthenticateToken(h.ctx, created.Secret)
	wantCode(t, err, apitypes.CodeUnauthorized)

	forever, err := h.CreateToken(h.ctx, editor, tbtypes.CreateTokenRequest{Name: "forever"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.UpdateUser(h.ctx, System, editor.UserID, tbtypes.UpdateUserRequest{Disabled: new(true)}); err != nil {
		t.Fatal(err)
	}
	_, err = h.AuthenticateToken(h.ctx, forever.Secret)
	wantCode(t, err, apitypes.CodeUnauthorized)
	if _, err := h.UpdateUser(h.ctx, System, editor.UserID, tbtypes.UpdateUserRequest{Disabled: new(false)}); err != nil {
		t.Fatal(err)
	}
	if err := h.RevokeToken(h.ctx, editor, forever.Token.ID); err != nil {
		t.Fatal(err)
	}
	_, err = h.AuthenticateToken(h.ctx, forever.Secret)
	wantCode(t, err, apitypes.CodeUnauthorized)
}

// Invite and reset links work once, before they expire.
func TestLinks(t *testing.T) {
	h := newHarness(t)
	admin := h.admin()

	inv, err := h.CreateInvite(h.ctx, admin, tbtypes.CreateInviteRequest{Email: "new@example.com", Role: tbtypes.RoleEditor}, "https://db.example.com")
	if err != nil {
		t.Fatal(err)
	}
	base, code, ok := strings.Cut(inv.URL, "#")
	if !ok || base != "https://db.example.com/link" {
		t.Fatalf("invite url %q", base)
	}
	info, err := h.LinkInfo(h.ctx, code)
	if err != nil || info.Kind != tbtypes.LinkInvite || info.Email != "new@example.com" {
		t.Fatalf("LinkInfo = %+v, %v", info, err)
	}
	open, _ := h.ListInvites(h.ctx, admin)
	if len(open) != 1 || open[0].URL != "" {
		t.Fatalf("open invites %+v", open)
	}
	_, _, act, err := h.UseLink(h.ctx, code, tbtypes.UseLinkRequest{Name: "New", Password: testPassword}, "10.0.0.4")
	if err != nil {
		t.Fatal(err)
	}
	if act.Role != tbtypes.RoleEditor || act.Email != "new@example.com" {
		t.Fatalf("invite made %+v", act)
	}
	_, _, _, err = h.UseLink(h.ctx, code, tbtypes.UseLinkRequest{Name: "Again", Password: testPassword}, "10.0.0.4")
	wantCode(t, err, apitypes.CodeExpired)

	// A reset link sets a new password and ends the person's sessions.
	session, _, _, err := h.Login(h.ctx, "new@example.com", testPassword, "10.0.0.4")
	if err != nil {
		t.Fatal(err)
	}
	reset, err := h.ResetLink(h.ctx, admin, act.UserID, "https://db.example.com")
	if err != nil {
		t.Fatal(err)
	}
	_, code, _ = strings.Cut(reset.URL, "#")
	if _, _, _, err := h.UseLink(h.ctx, code, tbtypes.UseLinkRequest{Password: "a new long password"}, "10.0.0.4"); err != nil {
		t.Fatal(err)
	}
	_, err = h.AuthenticateSession(h.ctx, session)
	wantCode(t, err, apitypes.CodeUnauthorized)
	if _, _, _, err := h.Login(h.ctx, "new@example.com", "a new long password", "10.0.0.5"); err != nil {
		t.Fatal(err)
	}

	// Expired.
	late, err := h.ResetLink(h.ctx, admin, act.UserID, "https://db.example.com")
	if err != nil {
		t.Fatal(err)
	}
	h.clock = h.clock.Add(ResetTTL)
	_, code, _ = strings.Cut(late.URL, "#")
	_, err = h.LinkInfo(h.ctx, code)
	wantCode(t, err, apitypes.CodeExpired)
}

// tempoBase always keeps an admin who can sign in, and admins don't
// change their own role.
func TestKeepsAnAdmin(t *testing.T) {
	h := newHarness(t)
	admin := h.admin()
	_, err := h.UpdateUser(h.ctx, admin, admin.UserID, tbtypes.UpdateUserRequest{Role: new(tbtypes.RoleViewer)})
	wantCode(t, err, apitypes.CodeValidation)
	_, err = h.UpdateUser(h.ctx, System, admin.UserID, tbtypes.UpdateUserRequest{Disabled: new(true)})
	wantCode(t, err, apitypes.CodeConflict)
	err = h.DeleteUser(h.ctx, System, admin.UserID)
	wantCode(t, err, apitypes.CodeConflict)

	other := h.user("other@example.com", tbtypes.RoleAdmin)
	if err := h.DeleteUser(h.ctx, other, admin.UserID); err != nil {
		t.Fatal(err)
	}
	viewer := h.user("viewer@example.com", tbtypes.RoleViewer)
	_, err = h.ListUsers(h.ctx, viewer)
	wantCode(t, err, apitypes.CodeForbidden)
}

// A person sees the name they set, not the email their actions are
// recorded under.
func TestMeShowsTheName(t *testing.T) {
	h := newHarness(t)
	admin := h.admin()
	me, err := h.Me(h.ctx, admin)
	if err != nil || me.Name != "Admin" || me.Email != "admin@example.com" {
		t.Fatalf("me = %+v, %v", me, err)
	}
}
