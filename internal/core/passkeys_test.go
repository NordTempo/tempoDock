package core

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// devOrigin is a browser on localhost, which passkeys accept.
const devOrigin = "http://localhost:7070"

// addPasskey registers auth for the session's user.
func addPasskey(t *testing.T, h *harness, s signedIn, auth *authenticator) apitypes.FactorAdded {
	t.Helper()
	ch, err := h.c.PasskeyRegistration(h.ctx, s.a, devOrigin)
	if err != nil {
		t.Fatal(err)
	}
	added, err := h.c.AddPasskey(h.ctx, s.a, apitypes.AddPasskeyRequest{Name: "Laptop", Passkey: auth.register(ch)})
	if err != nil {
		t.Fatal(err)
	}
	return added
}

func TestPasskeySecondFactor(t *testing.T) {
	h := newHarness(t)
	auth := newAuthenticator(t, devOrigin, "localhost")
	added := addPasskey(t, h, adaSession(t, h), auth)
	if added.Factor.Kind != factorPasskey || len(added.RecoveryCodes) != recoveryCodeCount {
		t.Fatalf("added %+v", added)
	}

	// pending starts a password sign-in and its passkey challenge.
	pending := func() (string, apitypes.PasskeyChallenge) {
		t.Helper()
		freshLimits(h)
		res, err := h.c.Login(h.ctx, adaEmail, adaPasswd, testClient)
		if err != nil || !res.Pending || res.Factors[0] != factorPasskey {
			t.Fatalf("password step: %+v %v", res, err)
		}
		ch, err := h.c.PasskeyOptions(h.ctx, apitypes.PasskeyOptionsRequest{Purpose: ceremonySecond}, res.Secret, devOrigin, testClient)
		if err != nil {
			t.Fatal(err)
		}
		return res.Secret, ch
	}
	verify := func(secret string, res apitypes.PasskeyResponse) (LoginResult, error) {
		return h.c.LoginSecondFactor(h.ctx, secret, apitypes.Verification{Passkey: &res}, testClient)
	}

	secret, ch := pending()
	answer := auth.assert(ch)
	done, err := verify(secret, answer)
	if err != nil {
		t.Fatal(err)
	}
	if s := session(t, h, done.Secret); s.a.Factor != factorPasskey {
		t.Fatalf("factor = %q", s.a.Factor)
	}

	// Replaying the answer: its ceremony is spent, and on a new sign-in
	// its challenge is not the new one.
	secret, ch = pending()
	_, err = verify(secret, answer)
	wantCode(t, "replayed ceremony", err, apitypes.CodeExpired)
	replay := answer
	replay.Ceremony = ch.Ceremony
	_, err = verify(secret, replay)
	wantCode(t, "replayed answer", err, apitypes.CodeUnauthorized)

	// A challenge is bound to the sign-in that asked for it.
	_, ch = pending()
	other, _ := pending()
	_, err = verify(other, auth.assert(ch))
	wantCode(t, "another sign-in's challenge", err, apitypes.CodeExpired)

	// A page on another origin, an authenticator scoped to another RP ID.
	for name, bend := range map[string]func(*authenticator){
		"wrong origin": func(a *authenticator) { a.origin = "https://evil.example" },
		"wrong RP ID":  func(a *authenticator) { a.rpID = "evil.example" },
	} {
		bent := *auth
		bend(&bent)
		secret, ch = pending()
		_, err = verify(secret, bent.assert(ch))
		wantCode(t, name, err, apitypes.CodeUnauthorized)
	}

	// A sign count going backwards means a cloned authenticator.
	clone := *auth
	clone.count = 0
	secret, ch = pending()
	_, err = verify(secret, clone.assert(ch))
	wantCode(t, "counter regression", err, apitypes.CodeUnauthorized)
	events, _ := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Platform: true, Action: "user.passkey_cloned"})
	if len(events) != 1 {
		t.Fatalf("clone audit: %+v", events)
	}
	// The real one still works.
	secret, ch = pending()
	if _, err := verify(secret, auth.assert(ch)); err != nil {
		t.Fatal(err)
	}
}

func TestPasskeyOnlyOnDocksOwnHost(t *testing.T) {
	h := newHarness(t)
	s := adaSession(t, h)
	refused := []string{"https://evil.example", "http://192.168.10.50:7070", "http://127.0.0.1:7070", "http://[::1]:7070", "", "null", "http://localhost:7070/path"}
	for _, origin := range refused {
		_, err := h.c.PasskeyRegistration(h.ctx, s.a, origin)
		wantCode(t, "registration from "+origin, err, apitypes.CodeValidation)
	}
	for _, origin := range []string{devOrigin, "http://dock.localhost:5173"} {
		if _, err := h.c.PasskeyRegistration(h.ctx, s.a, origin); err != nil {
			t.Fatalf("registration from %s: %v", origin, err)
		}
	}

	// The public address only as Dock serves it: same scheme and port.
	host := "dock.example.com"
	h.c.address.Store(&host)
	pub := h.c.publicURL()
	if _, err := h.c.PasskeyRegistration(h.ctx, s.a, pub); err != nil {
		t.Fatalf("on the public address %s: %v", pub, err)
	}
	for _, origin := range []string{strings.Replace(pub, "https://", "http://", 1), "https://" + host + ":8443", "https://sub." + host} {
		_, err := h.c.PasskeyRegistration(h.ctx, s.a, origin)
		wantCode(t, "registration from "+origin, err, apitypes.CodeValidation)
	}

	// Never an IP, even as the public address.
	ip := "203.0.113.10"
	h.c.address.Store(&ip)
	_, err := h.c.PasskeyRegistration(h.ctx, s.a, h.c.publicURL())
	wantCode(t, "registration on an IP address", err, apitypes.CodeValidation)
}

func TestPasskeySignInAlone(t *testing.T) {
	h := newHarness(t)
	auth := newAuthenticator(t, devOrigin, "localhost")
	s := adaSession(t, h)
	addPasskey(t, h, s, auth)
	start := func() apitypes.PasskeyChallenge {
		t.Helper()
		ch, err := h.c.PasskeyOptions(h.ctx, apitypes.PasskeyOptionsRequest{Purpose: ceremonyLogin}, "", devOrigin, testClient)
		if err != nil {
			t.Fatal(err)
		}
		return ch
	}

	answer := auth.assert(start())
	res, err := h.c.PasskeyLogin(h.ctx, answer, testClient)
	if err != nil {
		t.Fatal(err)
	}
	if s := session(t, h, res.Secret); s.a.Email != adaEmail || s.a.Factor != factorPasskey {
		t.Fatalf("signed in as %+v", s.a)
	}
	_, err = h.c.PasskeyLogin(h.ctx, answer, testClient)
	wantCode(t, "replay", err, apitypes.CodeExpired)

	// A user handle naming someone else is refused.
	other := *auth
	other.handle = userHandle(s.a.UserID + 1)
	_, err = h.c.PasskeyLogin(h.ctx, other.assert(start()), testClient)
	wantCode(t, "someone else's handle", err, apitypes.CodeUnauthorized)

	// Without user verification it is one factor, not a sign-in.
	noUV := *auth
	noUV.noUV = true
	_, err = h.c.PasskeyLogin(h.ctx, noUV.assert(start()), testClient)
	wantCode(t, "no user verification", err, apitypes.CodeUnauthorized)
}

func TestPasskeyStepUp(t *testing.T) {
	h := newHarness(t)
	auth := newAuthenticator(t, devOrigin, "localhost")
	s := adaSession(t, h)
	addPasskey(t, h, s, auth)
	s = age(t, h, s)

	ch, err := h.c.ReauthOptions(h.ctx, s.a, devOrigin)
	if err != nil {
		t.Fatal(err)
	}
	answer := auth.assert(ch)
	if err := h.c.Reauth(h.ctx, s.a, apitypes.Verification{Passkey: &answer}, testClient); err != nil {
		t.Fatal(err)
	}
	if s := session(t, h, s.secret); s.a.requireRecent("this") != nil {
		t.Fatal("the step-up didn't cover the session")
	}

	// Another session can't answer this session's challenge.
	login, err := h.c.PasskeyOptions(h.ctx, apitypes.PasskeyOptionsRequest{Purpose: ceremonyLogin}, "", devOrigin, testClient)
	if err != nil {
		t.Fatal(err)
	}
	res, err := h.c.PasskeyLogin(h.ctx, auth.assert(login), testClient)
	if err != nil {
		t.Fatal(err)
	}
	ch, _ = h.c.ReauthOptions(h.ctx, s.a, devOrigin)
	answer = auth.assert(ch)
	err = h.c.Reauth(h.ctx, session(t, h, res.Secret).a, apitypes.Verification{Passkey: &answer}, testClient)
	wantCode(t, "another session's challenge", err, apitypes.CodeExpired)
}

// Two sign-ins answered with the same passkey at once: only the first to
// store its sign count passes.
func TestPasskeyConcurrentUse(t *testing.T) {
	h := newHarness(t)
	auth := newAuthenticator(t, devOrigin, "localhost")
	s := adaSession(t, h)
	addPasskey(t, h, s, auth)
	u, err := h.store.GetUser(h.ctx, s.a.UserID)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := h.c.passkeyUser(h.ctx, u)
	if err != nil {
		t.Fatal(err)
	}

	ch, _ := h.c.PasskeyOptions(h.ctx, apitypes.PasskeyOptionsRequest{Purpose: ceremonyLogin}, "", devOrigin, testClient)
	if _, err := h.c.PasskeyLogin(h.ctx, auth.assert(ch), testClient); err != nil {
		t.Fatal(err)
	}
	cred := stale.creds[0]
	cred.Authenticator.SignCount = 99
	wantCode(t, "the slower sign-in", h.c.usePasskey(h.ctx, stale, &cred), apitypes.CodeUnauthorized)
}
