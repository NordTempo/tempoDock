package core

import (
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// cliUser creates a user and returns them as a signed-in actor.
func (h *harness) cliUser(email string, role apitypes.Scope) Actor {
	h.t.Helper()
	u, err := h.c.CreateUser(h.ctx, System, apitypes.CreateUserRequest{Email: email, Name: "x", Password: "a long enough password", Role: role})
	if err != nil {
		h.t.Fatal(err)
	}
	su, err := h.store.GetUser(h.ctx, u.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	return userActor(su)
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

	a, err := h.c.AuthenticateToken(h.ctx, r.res.Token)
	if err != nil || a.Scope != apitypes.ScopeDeploy || a.Name != "cli: laptop[31m" {
		t.Fatalf("token actor = %+v, %v", a, err)
	}
	tokens, _ := h.c.ListTokens(h.ctx, System)
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
	if _, err := h.c.AuthenticateToken(h.ctx, r.res.Token); CodeOf(err) != apitypes.CodeUnauthorized {
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
	tok, _ := h.c.CreateToken(h.ctx, System, apitypes.CreateTokenRequest{Name: "ci", Scope: apitypes.ScopeAdmin})
	ta, _ := h.c.AuthenticateToken(h.ctx, tok.Secret)
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
