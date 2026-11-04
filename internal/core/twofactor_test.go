package core

import (
	"context"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/httpkit"
	"github.com/NordTempo/tempoDock/internal/store"
)

const (
	adaEmail   = "ada@example.com"
	adaPasswd  = "correct horse battery"
	testClient = "192.0.2.1"
)

func TestTOTPCodes(t *testing.T) {
	// RFC 6238 appendix B, SHA-1, cut to six digits.
	key := []byte("12345678901234567890")
	for _, tc := range []struct {
		unix int64
		code string
	}{{59, "287082"}, {1111111109, "081804"}, {1234567890, "005924"}, {2000000000, "279037"}} {
		if got := totpCode(key, tc.unix/totpPeriod); got != tc.code {
			t.Errorf("code at %d = %s, want %s", tc.unix, got, tc.code)
		}
	}

	at := time.Unix(1234567890, 0)
	step := totpStep(at)
	for d, ok := range map[int64]bool{-2: false, -1: true, 0: true, 1: true, 2: false} {
		got, matched := matchTOTP(key, totpCode(key, step+d), at)
		if matched != ok || (ok && got != step+d) {
			t.Errorf("drift %d steps: matched %v at step %d, want %v", d, matched, got, ok)
		}
	}
	if _, ok := matchTOTP(key, "12345", at); ok {
		t.Error("a 5-digit code matched")
	}
}

// freshLimits gives every address its full sign-in budget again: these
// tests sign in more often than one person could in a minute.
func freshLimits(h *harness) { h.c.loginLimit = httpkit.NewLoginLimiter() }

// signedIn is a user's session: its secret and actor.
type signedIn struct {
	secret string
	a      Actor
}

// adaSession creates ada (a deploy member of org 1) and signs her in with
// her password alone.
func adaSession(t *testing.T, h *harness) signedIn {
	t.Helper()
	if _, err := h.c.CreateUser(h.ctx, testSystem, apitypes.CreateUserRequest{Email: adaEmail, Name: "Ada", Password: adaPasswd, Role: apitypes.ScopeAdmin}); err != nil {
		t.Fatal(err)
	}
	return passwordLogin(t, h)
}

func passwordLogin(t *testing.T, h *harness) signedIn {
	t.Helper()
	res, err := h.c.Login(h.ctx, adaEmail, adaPasswd, testClient)
	if err != nil || res.Pending {
		t.Fatalf("password login: %+v %v", res, err)
	}
	return session(t, h, res.Secret)
}

func session(t *testing.T, h *harness, secret string) signedIn {
	t.Helper()
	a, err := h.c.AuthenticateSession(h.ctx, secret, "")
	if err != nil {
		t.Fatal(err)
	}
	return signedIn{secret, a}
}

// addTOTP gives the session's user an authenticator app and returns its
// key and recovery codes.
func addTOTP(t *testing.T, h *harness, s signedIn) ([]byte, []string) {
	t.Helper()
	setup, err := h.c.StartTOTP(h.ctx, s.a)
	if err != nil {
		t.Fatal(err)
	}
	key, err := totpEncoding.DecodeString(setup.Secret)
	if err != nil {
		t.Fatal(err)
	}
	// The setup's code is spent: sign-ins need a later step.
	added, err := h.c.AddTOTP(h.ctx, s.a, apitypes.AddTOTPRequest{Ceremony: setup.Ceremony, Code: totpCode(key, totpStep(now())-1)})
	if err != nil {
		t.Fatal(err)
	}
	return key, added.RecoveryCodes
}

func wantCode(t *testing.T, what string, err error, code apitypes.ErrorCode) {
	t.Helper()
	if CodeOf(err) != code {
		t.Fatalf("%s: got %v (%s), want %s", what, err, CodeOf(err), code)
	}
}

func TestTOTPSecondFactor(t *testing.T) {
	h := newHarness(t)
	first := adaSession(t, h)
	key, codes := addTOTP(t, h, first)
	if len(codes) != recoveryCodeCount {
		t.Fatalf("recovery codes: %v", codes)
	}
	// The first factor ends the user's other sessions; this one now counts
	// as two-factor.
	if s := session(t, h, first.secret); s.a.Factor != factorTOTP {
		t.Fatalf("enrolling session factor = %q", s.a.Factor)
	}

	res, err := h.c.Login(h.ctx, adaEmail, adaPasswd, testClient)
	if err != nil || !res.Pending || len(res.Factors) != 2 || res.Factors[0] != factorTOTP || res.Factors[1] != factorRecovery {
		t.Fatalf("password step: %+v %v", res, err)
	}
	// A pending sign-in authenticates nothing.
	_, err = h.c.AuthenticateSession(h.ctx, res.Secret, "")
	wantCode(t, "pending session", err, apitypes.CodeUnauthorized)

	_, err = h.c.LoginSecondFactor(h.ctx, res.Secret, apitypes.Verification{Code: "000000"}, testClient)
	wantCode(t, "wrong code", err, apitypes.CodeUnauthorized)
	_, err = h.c.LoginSecondFactor(h.ctx, res.Secret, apitypes.Verification{Code: totpCode(key, totpStep(now())-1)}, testClient)
	wantCode(t, "the setup's code again", err, apitypes.CodeUnauthorized)

	code := totpCode(key, totpStep(now()))
	done, err := h.c.LoginSecondFactor(h.ctx, res.Secret, apitypes.Verification{Code: code}, testClient)
	if err != nil {
		t.Fatal(err)
	}
	if s := session(t, h, done.Secret); s.a.Factor != factorTOTP || s.a.OrgID != 1 {
		t.Fatalf("signed in as %+v", s.a)
	}
	// The pending sign-in is gone, and the code works once.
	_, err = h.c.LoginSecondFactor(h.ctx, res.Secret, apitypes.Verification{Code: code}, testClient)
	wantCode(t, "pending sign-in reused", err, apitypes.CodeExpired)
	freshLimits(h)
	again, _ := h.c.Login(h.ctx, adaEmail, adaPasswd, testClient)
	_, err = h.c.LoginSecondFactor(h.ctx, again.Secret, apitypes.Verification{Code: code}, testClient)
	wantCode(t, "code reused", err, apitypes.CodeUnauthorized)
}

func TestRecoveryCodesWorkOnce(t *testing.T) {
	h := newHarness(t)
	_, codes := addTOTP(t, h, adaSession(t, h))

	res, _ := h.c.Login(h.ctx, adaEmail, adaPasswd, testClient)
	done, err := h.c.LoginSecondFactor(h.ctx, res.Secret, apitypes.Verification{RecoveryCode: " " + codes[3] + " "}, testClient)
	if err != nil {
		t.Fatal(err)
	}
	if s := session(t, h, done.Secret); s.a.Factor != factorRecovery {
		t.Fatalf("factor = %q", s.a.Factor)
	}
	res, _ = h.c.Login(h.ctx, adaEmail, adaPasswd, testClient)
	_, err = h.c.LoginSecondFactor(h.ctx, res.Secret, apitypes.Verification{RecoveryCode: codes[3]}, testClient)
	wantCode(t, "used recovery code", err, apitypes.CodeUnauthorized)

	events, err := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Platform: true, Action: "user.recovery_code_used"})
	if err != nil || len(events) != 1 || events[0].Summary != "signed in with a recovery code; 9 left" {
		t.Fatalf("audit: %+v %v", events, err)
	}
}

func TestWrongSecondFactorsEndThePendingSignIn(t *testing.T) {
	h := newHarness(t)
	addTOTP(t, h, adaSession(t, h))
	res, _ := h.c.Login(h.ctx, adaEmail, adaPasswd, testClient)
	freshLimits(h)
	var err error
	for range maxFactorAttempts {
		_, err = h.c.LoginSecondFactor(h.ctx, res.Secret, apitypes.Verification{RecoveryCode: "aaaa-bbbb-cccc-dddd"}, testClient)
	}
	wantCode(t, "last wrong factor", err, apitypes.CodeExpired)
	_, err = h.c.LoginSecondFactor(h.ctx, res.Secret, apitypes.Verification{RecoveryCode: "aaaa-bbbb-cccc-dddd"}, testClient)
	wantCode(t, "after the last", err, apitypes.CodeExpired)
}

// age makes the session's last verification older than ReauthWindow.
func age(t *testing.T, h *harness, s signedIn) signedIn {
	t.Helper()
	if err := h.store.VerifySession(h.ctx, store.VerifySessionParams{VerifiedAt: ptr(now().Add(-ReauthWindow - time.Minute)), ID: s.a.SessionID}); err != nil {
		t.Fatal(err)
	}
	return session(t, h, s.secret)
}

func TestSensitiveActionsNeedARecentStepUp(t *testing.T) {
	h := newHarness(t)
	s := age(t, h, adaSession(t, h))
	create := func(s signedIn) error {
		_, err := h.c.CreateToken(h.ctx, s.a, apitypes.CreateTokenRequest{Name: "ci", Scope: apitypes.ScopeDeploy})
		return err
	}
	wantCode(t, "stale session creates a token", create(s), apitypes.CodeReauthRequired)
	_, err := h.c.ListOrgVariables(h.ctx, s.a, true)
	wantCode(t, "stale session reveals", err, apitypes.CodeReauthRequired)

	// Without a second factor the password confirms.
	freshLimits(h)
	wantCode(t, "wrong password", h.c.Reauth(h.ctx, s.a, apitypes.Verification{Password: "nope nope nope"}, testClient), apitypes.CodeUnauthorized)
	if err := h.c.Reauth(h.ctx, s.a, apitypes.Verification{Password: adaPasswd}, testClient); err != nil {
		t.Fatal(err)
	}
	s = session(t, h, s.secret)
	if err := create(s); err != nil {
		t.Fatal(err)
	}

	// With one, the password alone no longer does; nor a recovery code.
	key, codes := addTOTP(t, h, s)
	s = age(t, h, s)
	freshLimits(h)
	wantCode(t, "password step-up", h.c.Reauth(h.ctx, s.a, apitypes.Verification{Password: adaPasswd}, testClient), apitypes.CodeValidation)
	wantCode(t, "recovery code step-up", h.c.Reauth(h.ctx, s.a, apitypes.Verification{RecoveryCode: codes[0]}, testClient), apitypes.CodeValidation)
	if err := h.c.Reauth(h.ctx, s.a, apitypes.Verification{Code: totpCode(key, totpStep(now()))}, testClient); err != nil {
		t.Fatal(err)
	}
	if err := create(session(t, h, s.secret)); err != nil {
		t.Fatal(err)
	}

	// Tokens are credentials of their own: no step-up.
	tok, err := h.c.CreateToken(h.ctx, testSystem, apitypes.CreateTokenRequest{Name: "admin", Scope: apitypes.ScopeAdmin})
	if err != nil {
		t.Fatal(err)
	}
	ta, err := h.c.AuthenticateToken(h.ctx, tok.Secret, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := create(signedIn{a: ta}); err != nil {
		t.Fatal(err)
	}
}

func TestOrgRequiresTwoFactor(t *testing.T) {
	h := newHarness(t)
	s := adaSession(t, h)
	_, err := h.c.SetOrgRequireTwoFactor(h.ctx, s.a, apitypes.RequireTwoFactorRequest{Require: true})
	wantCode(t, "turning it on without a factor", err, apitypes.CodeValidation)

	addTOTP(t, h, s)
	s = session(t, h, s.secret)
	org, err := h.c.SetOrgRequireTwoFactor(h.ctx, s.a, apitypes.RequireTwoFactorRequest{Require: true})
	if err != nil || !org.RequireTwoFactor {
		t.Fatalf("org %+v %v", org, err)
	}

	// Grace's sign-in used no second factor: she acts in no org, and is
	// told why, until she adds one.
	if _, err := h.c.CreateUser(h.ctx, testSystem, apitypes.CreateUserRequest{Email: "grace@example.com", Name: "Grace", Password: adaPasswd, Role: apitypes.ScopeDeploy}); err != nil {
		t.Fatal(err)
	}
	res, err := h.c.Login(h.ctx, "grace@example.com", adaPasswd, testClient)
	if err != nil {
		t.Fatal(err)
	}
	grace := session(t, h, res.Secret)
	if grace.a.OrgID != 0 || grace.a.Me().TwoFactorRequired == "" {
		t.Fatalf("grace acts as %+v", grace.a)
	}
	_, err = h.c.ListProjects(h.ctx, grace.a, false)
	wantCode(t, "grace lists projects", err, apitypes.CodeTwoFactorRequired)
	if _, err := h.c.AuthenticateSession(h.ctx, res.Secret, org.Slug); err != nil {
		t.Fatal(err) // still authenticated, just not in the org
	}
	addTOTP(t, h, grace)
	if grace = session(t, h, res.Secret); grace.a.OrgID != 1 {
		t.Fatalf("after adding a factor grace acts as %+v", grace.a)
	}

	// Removing the last factor is refused while the org requires one.
	tf, _ := h.c.TwoFactor(h.ctx, grace.a)
	wantCode(t, "removing the last factor", h.c.RemoveFactor(h.ctx, grace.a, tf.Factors[0].ID), apitypes.CodeConflict)
}

func TestOperatorsRequireTwoFactor(t *testing.T) {
	h := newHarness(t)
	if _, err := h.c.CreateUser(h.ctx, testSystem, apitypes.CreateUserRequest{Email: adaEmail, Name: "Ada", Password: adaPasswd, Role: apitypes.ScopeAdmin, Operator: true}); err != nil {
		t.Fatal(err)
	}
	s := passwordLogin(t, h)
	_, err := h.c.SetPlatformSecurity(h.ctx, s.a, apitypes.PlatformSecurity{OperatorsTwoFactor: true})
	wantCode(t, "turning it on without a factor", err, apitypes.CodeValidation)
	if _, err := h.c.SetPlatformSecurity(h.ctx, testSystem, apitypes.PlatformSecurity{OperatorsTwoFactor: true}); err != nil {
		t.Fatal(err)
	}
	s = session(t, h, s.secret)
	_, err = h.c.PlatformSecurity(h.ctx, s.a)
	wantCode(t, "operator without a factor", err, apitypes.CodeTwoFactorRequired)
	if s.a.OrgID != 1 {
		t.Fatal("the org doesn't require it: she still acts in it")
	}
	addTOTP(t, h, s)
	if _, err := h.c.PlatformSecurity(h.ctx, session(t, h, s.secret).a); err != nil {
		t.Fatal(err)
	}
	h.restart() // the policy is kept
	if !h.c.operators2FA.Load() {
		t.Fatal("policy lost on restart")
	}
}

func TestCLILoginCarriesTheSecondFactor(t *testing.T) {
	h := newHarness(t)
	s := adaSession(t, h)
	addTOTP(t, h, s)
	s = age(t, h, s)
	start, err := h.c.StartCLILogin(h.ctx, apitypes.CLILoginStartRequest{ClientName: "dock", Hostname: "laptop"}, testClient)
	if err != nil {
		t.Fatal(err)
	}
	// Approving makes a token: a step-up first.
	wantCode(t, "approving from a stale session", h.c.DecideCLILogin(h.ctx, s.a, start.UserCode, testClient, true), apitypes.CodeReauthRequired)
	s = session(t, h, s.secret)
	if err := h.store.VerifySession(h.ctx, store.VerifySessionParams{VerifiedAt: ptr(now()), ID: s.a.SessionID}); err != nil {
		t.Fatal(err)
	}
	s = session(t, h, s.secret)
	if err := h.c.DecideCLILogin(h.ctx, s.a, start.UserCode, testClient, true); err != nil {
		t.Fatal(err)
	}
	got, err := h.c.WaitCLILogin(h.ctx, start.DeviceCode)
	if err != nil {
		t.Fatal(err)
	}
	ta, err := h.c.AuthenticateToken(h.ctx, got.Token, "")
	if err != nil || ta.Factor != factorLogin {
		t.Fatalf("token actor %+v %v", ta, err)
	}
}

func TestResetTwoFactor(t *testing.T) {
	h := newHarness(t)
	s := adaSession(t, h)
	addTOTP(t, h, s)
	if err := h.c.ResetTwoFactor(h.ctx, s.a, adaEmail); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a non-operator resets: %v", err)
	}
	if err := h.c.ResetTwoFactor(h.ctx, System, adaEmail); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.AuthenticateSession(h.ctx, s.secret, ""); CodeOf(err) != apitypes.CodeUnauthorized {
		t.Fatalf("session survived the reset: %v", err)
	}
	// Back to a password alone.
	passwordLogin(t, h)
	events, _ := h.c.ListAudit(context.Background(), testSystem, AuditFilter{Platform: true, Action: "user.2fa_reset"})
	if len(events) != 1 {
		t.Fatalf("audit %+v", events)
	}
}

// Someone with the password guessing authenticator codes from their
// address is stopped, and the owner still signs in from theirs.
func TestCodeGuesserCantLockOutTheOwner(t *testing.T) {
	h := newHarness(t)
	key, _ := addTOTP(t, h, adaSession(t, h))
	freshLimits(h)
	const guesser = "198.51.100.7"
	var err error
	for range 20 {
		res, lerr := h.c.Login(h.ctx, adaEmail, adaPasswd, guesser)
		if lerr != nil {
			err = lerr
			break
		}
		_, err = h.c.LoginSecondFactor(h.ctx, res.Secret, apitypes.Verification{Code: "000000"}, guesser)
	}
	wantCode(t, "guesser", err, apitypes.CodeRateLimited)
	res, err := h.c.Login(h.ctx, adaEmail, adaPasswd, testClient)
	if err != nil {
		t.Fatalf("owner's password: %v", err)
	}
	if _, err := h.c.LoginSecondFactor(h.ctx, res.Secret, apitypes.Verification{Code: totpCode(key, totpStep(now()))}, testClient); err != nil {
		t.Fatalf("owner's code: %v", err)
	}
}

// Approving an access request or unlocking an environment lets tokens
// change it: a stale session confirms first, and rejecting doesn't.
func TestLockDecisionsNeedAStepUp(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{Port: 3000})
	s := age(t, h, adaSession(t, h))
	_, err := h.c.DecideRequest(h.ctx, s.a, 999, true, apitypes.DecideAccessRequest{})
	wantCode(t, "approving from a stale session", err, apitypes.CodeReauthRequired)
	_, err = h.c.DecideRequest(h.ctx, s.a, 999, false, apitypes.DecideAccessRequest{})
	wantCode(t, "rejecting from a stale session", err, apitypes.CodeNotFound)
	_, err = h.c.UnlockEnvironment(h.ctx, s.a, EnvRef{"shop", "production"}, apitypes.UnlockEnvironmentRequest{})
	wantCode(t, "unlocking from a stale session", err, apitypes.CodeReauthRequired)
}

func TestFactorWait(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	for _, tc := range []struct {
		failures int64
		since    time.Duration
		wait     time.Duration
		locked   bool
	}{
		{9, 0, 0, false},
		{10, 0, time.Second, false},
		{12, time.Second, 3 * time.Second, false},
		{40, 0, maxFactorWait, false},
		{40, maxFactorWait, 0, false},
		{100, 0, 0, true},
	} {
		last := at.Add(-tc.since)
		wait, locked := factorWait(tc.failures, &last, at)
		if wait != tc.wait || locked != tc.locked {
			t.Errorf("%d failures, last %s ago: wait %s locked %v, want %s %v", tc.failures, tc.since, wait, locked, tc.wait, tc.locked)
		}
	}
}

// Codes from every sign-in count against one budget per user: a few in a
// row slow the next try down, many lock codes until a password reset.
func TestCodeGuessingHasAnOverallCap(t *testing.T) {
	h := newHarness(t)
	s := adaSession(t, h)
	key, _ := addTOTP(t, h, s)
	tryCode := func(code string) error {
		t.Helper()
		freshLimits(h) // a new address each time: the per-address limit isn't what's tested
		res, err := h.c.Login(h.ctx, adaEmail, adaPasswd, testClient)
		if err != nil {
			t.Fatal(err)
		}
		_, err = h.c.LoginSecondFactor(h.ctx, res.Secret, apitypes.Verification{Code: code}, testClient)
		return err
	}
	for range factorBackoffAfter {
		wantCode(t, "wrong code", tryCode("000000"), apitypes.CodeUnauthorized)
	}
	wantCode(t, "right code right after ten wrong ones", tryCode(totpCode(key, totpStep(now()))), apitypes.CodeRateLimited)
	for range factorLockAt {
		if _, err := h.store.FailFactor(h.ctx, store.FailFactorParams{Now: ptr(now().Add(-2 * maxFactorWait)), ID: s.a.UserID}); err != nil {
			t.Fatal(err)
		}
	}
	wantCode(t, "right code once locked", tryCode(totpCode(key, totpStep(now()))), apitypes.CodeForbidden)

	// A password reset lifts the lock, and a right code clears the count.
	if _, err := h.c.ResetPassword(h.ctx, System, s.a.UserID, apitypes.ResetPasswordRequest{Password: adaPasswd}); err != nil {
		t.Fatal(err)
	}
	if err := tryCode(totpCode(key, totpStep(now()))); err != nil {
		t.Fatalf("after a password reset: %v", err)
	}
	u, _ := h.store.GetUser(h.ctx, s.a.UserID)
	if u.FactorFailures != 0 {
		t.Fatalf("failures after a right code: %d", u.FactorFailures)
	}
}

// A stolen session cookie gets a few step-up tries, then the session ends.
func TestFailedStepUpsEndTheSession(t *testing.T) {
	h := newHarness(t)
	s := adaSession(t, h)
	addTOTP(t, h, s)
	s = age(t, h, s)
	freshLimits(h)
	var err error
	for range maxFactorAttempts {
		err = h.c.Reauth(h.ctx, s.a, apitypes.Verification{Code: "000000"}, testClient)
	}
	wantCode(t, "last wrong step-up", err, apitypes.CodeUnauthorized)
	if _, err := h.c.AuthenticateSession(h.ctx, s.secret, ""); CodeOf(err) != apitypes.CodeUnauthorized {
		t.Fatalf("session after failed step-ups: %v", err)
	}
}

// Starting an authenticator app setup again replaces the last one, so one
// user can't fill the store everyone's setups share.
func TestOneAppSetupPerUser(t *testing.T) {
	h := newHarness(t)
	s := adaSession(t, h)
	first, err := h.c.StartTOTP(h.ctx, s.a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.StartTOTP(h.ctx, s.a); err != nil {
		t.Fatal(err)
	}
	key, _ := totpEncoding.DecodeString(first.Secret)
	_, err = h.c.AddTOTP(h.ctx, s.a, apitypes.AddTOTPRequest{Ceremony: first.Ceremony, Code: totpCode(key, totpStep(now()))})
	wantCode(t, "the replaced setup", err, apitypes.CodeExpired)
	if n := len(h.c.ceremonies.m); n != 1 {
		t.Fatalf("%d setups held, want 1", n)
	}
}
