package core

import (
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// cliUser creates a user and returns them as a signed-in actor.
func (h *harness) cliUser(email string, role apitypes.Scope) Actor {
	h.t.Helper()
	u, err := h.c.CreateUser(h.ctx, testSystem, apitypes.CreateUserRequest{Email: email, Name: "x", Password: "a long enough password", Role: role})
	if err != nil {
		h.t.Fatal(err)
	}
	su, err := h.store.GetUser(h.ctx, u.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	a, err := h.c.actIn(h.ctx, userActor(su), "", apitypes.ScopeAdmin)
	if err != nil {
		h.t.Fatal(err)
	}
	return a
}

func (h *harness) startCLILogin() apitypes.CLILoginStart {
	h.t.Helper()
	s, err := h.c.StartCLILogin(h.ctx, apitypes.CLILoginStartRequest{ClientName: "dock CLI", Hostname: "laptop\x1b[31m", OS: "linux/amd64"}, "198.51.100.7")
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

type waitResult struct {
	res apitypes.CLILoginWait
	err error
}

func (h *harness) waitCLILogin(device string) <-chan waitResult {
	ch := make(chan waitResult, 1)
	go func() {
		res, err := h.c.WaitCLILogin(h.ctx, device)
		ch <- waitResult{res, err}
	}()
	return ch
}

func TestCLILoginApprovedTokenIsReturnedOnce(t *testing.T) {
	h := newHarness(t)
	ada := h.cliUser("ada@example.com", apitypes.ScopeDeploy)
	s := h.startCLILogin()

	l, err := h.c.CLILogin(h.ctx, ada, s.UserCode, "203.0.113.1")
	if err != nil {
		t.Fatal(err)
	}
	if l.Hostname != "laptop[31m" || l.IP != "198.51.100.7" || l.Scope != apitypes.ScopeDeploy || l.Status != "pending" {
		t.Fatalf("lookup = %+v", l)
	}

	// The wait answers on approval, long before its 30s timeout.
	started := time.Now()
	waiting := h.waitCLILogin(s.DeviceCode)
	time.Sleep(20 * time.Millisecond) // let it park
	if err := h.c.DecideCLILogin(h.ctx, ada, s.UserCode, "203.0.113.1", true); err != nil {
		t.Fatal(err)
	}
	r := <-waiting
	if r.err != nil || r.res.Status != "approved" || r.res.User != "ada@example.com" || r.res.Scope != apitypes.ScopeDeploy {
		t.Fatalf("wait = %+v, %v", r.res, r.err)
	}
	if d := time.Since(started); d > 5*time.Second {
		t.Fatalf("wait took %s; approval should wake it", d)
	}

	a, err := h.c.AuthenticateToken(h.ctx, r.res.Token, "")
	if err != nil || a.Scope != apitypes.ScopeDeploy || a.Name != "cli: laptop[31m" || a.Email != "ada@example.com" {
		t.Fatalf("token actor = %+v, %v", a, err)
	}
	// The token acts for ada when she changes her password (dock me
	// password); it gets no session.
	if secret, _, err := h.c.ChangePassword(h.ctx, a, apitypes.ChangePasswordRequest{
		CurrentPassword: "a long enough password", NewPassword: "another long password",
	}, "203.0.113.1"); err != nil || secret != "" {
		t.Fatalf("change password with the CLI token: %q, %v", secret, err)
	}
	tokens, _ := h.c.ListTokens(h.ctx, testSystem)
	last := tokens[len(tokens)-1]
	if last.Origin != "cli_login" || last.CreatedBy != "ada@example.com" || last.ExpiresAt == nil {
		t.Fatalf("token = %+v", last)
	}
	if _, err := h.c.WaitCLILogin(h.ctx, s.DeviceCode); CodeOf(err) != apitypes.CodeExpired {
		t.Fatalf("second wait: %v", err)
	}
	if err := h.c.DecideCLILogin(h.ctx, ada, s.UserCode, "203.0.113.1", true); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("approving a used login: %v", err)
	}

	// The token can revoke itself (dock logout).
	if err := h.c.RevokeOwnToken(h.ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.AuthenticateToken(h.ctx, r.res.Token, ""); CodeOf(err) != apitypes.CodeUnauthorized {
		t.Fatalf("revoked token still works: %v", err)
	}
}

func TestCLILoginDeniedAndPending(t *testing.T) {
	h := newHarness(t)
	h.c.cliLogins.wait = 30 * time.Millisecond
	ada := h.cliUser("ada@example.com", apitypes.ScopeAdmin)
	s := h.startCLILogin()

	if r := <-h.waitCLILogin(s.DeviceCode); r.err != nil || r.res.Status != "pending" || r.res.Token != "" {
		t.Fatalf("undecided wait = %+v, %v", r.res, r.err)
	}
	if err := h.c.DecideCLILogin(h.ctx, ada, s.UserCode, "203.0.113.1", false); err != nil {
		t.Fatal(err)
	}
	if err := h.c.DecideCLILogin(h.ctx, ada, s.UserCode, "203.0.113.1", true); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("approving a denied login: %v", err)
	}
	if r := <-h.waitCLILogin(s.DeviceCode); CodeOf(r.err) != apitypes.CodeDenied {
		t.Fatalf("denied wait = %+v, %v", r.res, r.err)
	}

	// Tokens can't approve: a token could otherwise mint more tokens.
	tok, _ := h.c.CreateToken(h.ctx, testSystem, apitypes.CreateTokenRequest{Name: "ci", Scope: apitypes.ScopeAdmin})
	ta, _ := h.c.AuthenticateToken(h.ctx, tok.Secret, "")
	s = h.startCLILogin()
	if err := h.c.DecideCLILogin(h.ctx, ta, s.UserCode, "203.0.113.1", true); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("token approving: %v", err)
	}
}

func TestCLILoginExpires(t *testing.T) {
	h := newHarness(t)
	ada := h.cliUser("ada@example.com", apitypes.ScopeAdmin)
	s := h.startCLILogin()
	if _, err := h.store.DB.ExecContext(h.ctx, "UPDATE cli_logins SET expires_at = ?", now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := h.c.DecideCLILogin(h.ctx, ada, s.UserCode, "203.0.113.1", true); CodeOf(err) != apitypes.CodeExpired {
		t.Fatalf("approving an expired login: %v", err)
	}
	if _, err := h.c.WaitCLILogin(h.ctx, s.DeviceCode); CodeOf(err) != apitypes.CodeExpired {
		t.Fatalf("waiting on an expired login: %v", err)
	}
	if _, err := h.c.WaitCLILogin(h.ctx, "dockdev_made-up"); CodeOf(err) != apitypes.CodeExpired {
		t.Fatalf("unknown device code: %v", err)
	}
}

func TestCLILoginWrongCodesLockOut(t *testing.T) {
	h := newHarness(t)
	ada := h.cliUser("ada@example.com", apitypes.ScopeAdmin)
	bob := h.cliUser("bob@example.com", apitypes.ScopeAdmin)
	s := h.startCLILogin()
	wrong := "ZZZZ-ZZZZ"
	if s.UserCode == wrong {
		wrong = "XXXX-XXXX"
	}
	for i := range 5 {
		if _, err := h.c.CLILogin(h.ctx, ada, wrong, "203.0.113.1"); CodeOf(err) != apitypes.CodeNotFound {
			t.Fatalf("wrong code %d: %v", i, err)
		}
	}
	// Locked out, even with the right code.
	if _, err := h.c.CLILogin(h.ctx, ada, s.UserCode, "192.0.2.9"); CodeOf(err) != apitypes.CodeRateLimited {
		t.Fatalf("user after 5 wrong codes: %v", err)
	}
	if _, err := h.c.CLILogin(h.ctx, bob, s.UserCode, "203.0.113.1"); CodeOf(err) != apitypes.CodeRateLimited {
		t.Fatalf("same IP after 5 wrong codes: %v", err)
	}
	if _, err := h.c.CLILogin(h.ctx, bob, s.UserCode, "192.0.2.9"); err != nil {
		t.Fatalf("another user and IP: %v", err)
	}
}

func TestUserCodes(t *testing.T) {
	for range 100 {
		code := newUserCode()
		got, ok := parseUserCode(formatUserCode(code))
		if !ok || got != code {
			t.Fatalf("%q round trip: %q %v", code, got, ok)
		}
	}
	for _, s := range []string{"bcdf ghjk", "BCDF-GHJK", "bcdfghjk"} {
		if got, ok := parseUserCode(s); !ok || got != "BCDFGHJK" {
			t.Errorf("parse %q = %q %v", s, got, ok)
		}
	}
	for _, s := range []string{"", "BCDF-GHJ", "BCDF-GHJA", "BCDF-GHJK1"} {
		if _, ok := parseUserCode(s); ok {
			t.Errorf("parse %q accepted", s)
		}
	}
}

// cliToken runs a browser login approved by a and returns the token.
func (h *harness) cliToken(a Actor) string {
	h.t.Helper()
	s := h.startCLILogin()
	if err := h.c.DecideCLILogin(h.ctx, a, s.UserCode, "203.0.113.1", true); err != nil {
		h.t.Fatal(err)
	}
	res, err := h.c.WaitCLILogin(h.ctx, s.DeviceCode)
	if err != nil {
		h.t.Fatal(err)
	}
	return res.Token
}

// A `dock login` token is only as good as its user now: it follows their
// role, and disabling, deleting or a new password ends it. Hand-made
// tokens are separate credentials and stay.
func TestCLILoginTokenFollowsItsUser(t *testing.T) {
	h := newHarness(t)
	ada := h.cliUser("ada@example.com", apitypes.ScopeAdmin)
	authAs := func(secret string) (Actor, error) { return h.c.AuthenticateToken(h.ctx, secret, "") }
	refused := func(what, secret string) {
		t.Helper()
		if _, err := authAs(secret); CodeOf(err) != apitypes.CodeUnauthorized {
			t.Fatalf("%s: token still works: %v", what, err)
		}
	}
	sql := func(q string, args ...any) {
		t.Helper()
		if _, err := h.store.DB.ExecContext(h.ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}

	// Scope is capped at the user's current role, and checked without
	// waiting for a revocation.
	tok := h.cliToken(ada)
	sql("UPDATE org_members SET role = 'read' WHERE user_id = ?", ada.UserID)
	if a, err := authAs(tok); err != nil || a.Scope != apitypes.ScopeRead {
		t.Fatalf("after demotion to read: %+v, %v", a, err)
	}
	sql("UPDATE org_members SET role = 'admin' WHERE user_id = ?", ada.UserID)
	sql("UPDATE users SET disabled_at = ? WHERE id = ?", now(), ada.UserID)
	refused("user disabled behind core's back", tok)
	sql("UPDATE users SET disabled_at = NULL WHERE id = ?", ada.UserID)

	// Disabling revokes: enabling again doesn't bring the token back.
	manual, err := h.c.CreateToken(h.ctx, ada, apitypes.CreateTokenRequest{Name: "ci", Scope: apitypes.ScopeDeploy})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SetUserDisabled(h.ctx, testSystem, ada.UserID, true); err != nil {
		t.Fatal(err)
	}
	refused("disabled", tok)
	if _, err := h.c.SetUserDisabled(h.ctx, testSystem, ada.UserID, false); err != nil {
		t.Fatal(err)
	}
	refused("disabled and enabled again", tok)
	if _, err := authAs(manual.Secret); err != nil {
		t.Fatalf("hand-made token after disable: %v", err)
	}

	// Her own password change keeps the calling token and ends the others.
	mine, other := h.cliToken(ada), h.cliToken(ada)
	caller, err := authAs(mine)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.c.ChangePassword(h.ctx, caller, apitypes.ChangePasswordRequest{
		CurrentPassword: "a long enough password", NewPassword: "another long password",
	}, "203.0.113.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := authAs(mine); err != nil {
		t.Fatalf("calling token after its own password change: %v", err)
	}
	refused("own password changed", other)
	if _, err := h.c.ResetPassword(h.ctx, testSystem, ada.UserID, apitypes.ResetPasswordRequest{}); err != nil {
		t.Fatal(err)
	}
	refused("password reset by an admin", mine)

	// Deleting revokes, even if the email comes back as a new user.
	tok = h.cliToken(ada)
	if err := h.c.DeleteUser(h.ctx, testSystem, ada.UserID); err != nil {
		t.Fatal(err)
	}
	refused("deleted", tok)
	h.cliUser("ada@example.com", apitypes.ScopeAdmin)
	refused("deleted, email reused", tok)
}

// TestTokensCantManageAccounts checks that an operator's token can't create
// or reset accounts, while a signed-in operator and System can.
func TestTokensCantManageAccounts(t *testing.T) {
	h := newHarness(t)
	session := h.cliUser("op@example.com", apitypes.ScopeAdmin)
	session = session.withPlatform(apitypes.PlatformOperator)
	other := h.cliUser("other@example.com", apitypes.ScopeAdmin)
	token := session
	token.Kind = "token"

	req := apitypes.CreateUserRequest{Email: "new@example.com", Name: "x", Password: "a long enough password", Role: apitypes.ScopeAdmin}
	if _, err := h.c.CreateUser(h.ctx, token, req); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("token CreateUser: got %v, want forbidden", err)
	}
	if _, err := h.c.ResetPassword(h.ctx, token, other.UserID, apitypes.ResetPasswordRequest{}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("token ResetPassword: got %v, want forbidden", err)
	}
	if _, err := h.c.CreateUser(h.ctx, session, req); err != nil {
		t.Fatalf("session CreateUser: %v", err)
	}
	if _, err := h.c.ResetPassword(h.ctx, session, other.UserID, apitypes.ResetPasswordRequest{}); err != nil {
		t.Fatalf("session ResetPassword: %v", err)
	}
	if _, err := h.c.ResetPassword(h.ctx, testSystem, other.UserID, apitypes.ResetPasswordRequest{}); err != nil {
		t.Fatalf("system ResetPassword: %v", err)
	}
}
