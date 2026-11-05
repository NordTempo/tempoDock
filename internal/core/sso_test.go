package core

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core/oidctest"
	"github.com/NordTempo/tempoDock/internal/store"
)

// ssoHarness is the test org with a fake identity provider, corp, for
// example.com, invite only.
func ssoHarness(t *testing.T) (*harness, *oidctest.Provider) {
	t.Helper()
	h := newHarness(t)
	freshLimits(h)
	idp := oidctest.New(t)
	h.addSSOProvider(idp, "corp", "")
	return h, idp
}

func (h *harness) addSSOProvider(idp *oidctest.Provider, name string, jit apitypes.Scope) apitypes.SSOProvider {
	h.t.Helper()
	p, err := h.c.CreateSSOProvider(h.ctx, testSystem, apitypes.CreateSSOProviderRequest{
		Name: name, Issuer: idp.Issuer, ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret,
		AllowedDomains: []string{"Example.com"}, JITRole: jit,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return p
}

// ssoBegin starts a single sign-on for email and returns what the
// provider was asked and the callback the browser will bring back, still
// without a code.
func (h *harness) ssoBegin(email string) (oidctest.Request, SSOCallback) {
	h.t.Helper()
	start, err := h.c.StartSSO(h.ctx, apitypes.SSOStartRequest{Email: email, To: "/test/shop"}, testClient)
	if err != nil || start.URL == "" || start.Binding == "" {
		h.t.Fatalf("start: %+v, %v", start, err)
	}
	req := oidctest.Read(h.t, start.URL)
	if req.RedirectURI != h.c.ssoRedirectURL() {
		h.t.Fatalf("redirect URI %s", req.RedirectURI)
	}
	return req, SSOCallback{State: req.State, Binding: start.Binding}
}

// ssoSignIn signs subject in through idp as email; edit changes the ID
// token's claims first.
func (h *harness) ssoSignIn(idp *oidctest.Provider, subject, email string, edit func(map[string]any)) (SSOResult, error) {
	h.t.Helper()
	req, cb := h.ssoBegin(email)
	claims := idp.Claims(req, subject, email)
	if edit != nil {
		edit(claims)
	}
	cb.Code = idp.Authorize(req, claims)
	return h.c.FinishSSO(h.ctx, cb, testClient)
}

// ssoSession signs in through idp and returns the session.
func (h *harness) ssoSession(idp *oidctest.Provider, subject, email string) signedIn {
	h.t.Helper()
	res, err := h.ssoSignIn(idp, subject, email, nil)
	if err != nil || res.Pending || res.To != "/test/shop" {
		h.t.Fatalf("sso sign-in: %+v, %v", res, err)
	}
	return session(h.t, h, res.Secret)
}

func (h *harness) audited(action string) int {
	h.t.Helper()
	var n int
	if err := h.store.DB.QueryRowContext(h.ctx, `SELECT count(*) FROM audit_events WHERE action = ?`, action).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func TestSSOMemberSignsIn(t *testing.T) {
	h, idp := ssoHarness(t)
	h.cliUser(adaEmail, apitypes.ScopeDeploy)

	s := h.ssoSession(idp, "ada-sub", adaEmail)
	if s.a.SSOOrgID != 1 || s.a.Factor != factorSSO || s.a.OrgSlug != "test" || s.a.Scope != apitypes.ScopeDeploy || s.a.Me().SSO != "test" {
		t.Fatalf("actor = %+v", s.a)
	}
	// The identity is linked: the provider's subject finds her again,
	// whatever email it sends now.
	again := h.ssoSession(idp, "ada-sub", "ada.lovelace@example.com")
	if again.a.UserID != s.a.UserID {
		t.Fatalf("identity mapped to user %d, want %d", again.a.UserID, s.a.UserID)
	}
	if h.audited("user.sso_link") != 1 || h.audited("user.login") != 2 {
		t.Fatal("link and sign-ins not audited")
	}
	// An org's provider vouches only there: no account-wide second factor
	// changes, and an SSO session confirms by signing in again.
	if _, err := h.c.StartTOTP(h.ctx, s.a); CodeOf(err) != apitypes.CodeSSORequired {
		t.Fatalf("SSO session started an authenticator app: %v", err)
	}
	wantCode(t, "reauth", h.c.Reauth(h.ctx, s.a, apitypes.Verification{Password: "x"}, testClient), apitypes.CodeSSORequired)
}

func TestSSOAcceptsInvite(t *testing.T) {
	h, idp := ssoHarness(t)
	if _, err := h.c.CreateInvite(h.ctx, testSystem, apitypes.CreateInviteRequest{Email: "Bob@Example.com", Role: apitypes.ScopeRead}); err != nil {
		t.Fatal(err)
	}
	s := h.ssoSession(idp, "bob-sub", "bob@example.com")
	if s.a.Scope != apitypes.ScopeRead || s.a.OrgID != 1 {
		t.Fatalf("actor = %+v", s.a)
	}
	invites, err := h.c.ListInvites(h.ctx, testSystem)
	if err != nil || len(invites) != 0 {
		t.Fatalf("invite still open: %+v, %v", invites, err)
	}
}

func TestSSOJustInTime(t *testing.T) {
	h := newHarness(t)
	freshLimits(h)
	idp := oidctest.New(t)
	h.addSSOProvider(idp, "corp", apitypes.ScopeDeploy)

	s := h.ssoSession(idp, "carol-sub", "carol@example.com")
	if s.a.Scope != apitypes.ScopeDeploy || s.a.Email != "carol@example.com" {
		t.Fatalf("actor = %+v", s.a)
	}
	if h.audited("user.create") != 1 || h.audited("org.join") != 1 {
		t.Fatal("just-in-time member not audited")
	}
	// Admins are made by admins.
	_, err := h.c.CreateSSOProvider(h.ctx, testSystem, apitypes.CreateSSOProviderRequest{
		Name: "other", Issuer: idp.Issuer, ClientID: "x", ClientSecret: "y", AllowedDomains: []string{"example.org"}, JITRole: apitypes.ScopeAdmin,
	})
	wantCode(t, "admin just-in-time role", err, apitypes.CodeValidation)
}

func TestSSORefusals(t *testing.T) {
	h, idp := ssoHarness(t)
	h.cliUser(adaEmail, apitypes.ScopeDeploy)

	for _, tc := range []struct {
		name  string
		email string
		edit  func(map[string]any)
		code  apitypes.ErrorCode
	}{
		{"nonce mismatch", adaEmail, func(c map[string]any) { c["nonce"] = "another" }, apitypes.CodeUnauthorized},
		{"wrong audience", adaEmail, func(c map[string]any) { c["aud"] = "another-client" }, apitypes.CodeUnauthorized},
		{"wrong issuer", adaEmail, func(c map[string]any) { c["iss"] = "https://evil.example" }, apitypes.CodeUnauthorized},
		{"expired", adaEmail, func(c map[string]any) {
			c["iat"], c["exp"] = time.Now().Add(-2*time.Hour).Unix(), time.Now().Add(-time.Hour).Unix()
		}, apitypes.CodeUnauthorized},
		{"unverified email", adaEmail, func(c map[string]any) { c["email_verified"] = false }, apitypes.CodeForbidden},
		{"untrusted domain", adaEmail, func(c map[string]any) { c["email"] = "ada@elsewhere.org" }, apitypes.CodeForbidden},
		{"no invite", "nobody@example.com", nil, apitypes.CodeForbidden},
	} {
		res, err := h.ssoSignIn(idp, "sub-"+tc.name, tc.email, tc.edit)
		if CodeOf(err) != tc.code || res.Secret != "" {
			t.Errorf("%s: %+v, %v (%s), want %s", tc.name, res, err, CodeOf(err), tc.code)
		}
	}
	if _, err := h.store.GetUserByEmail(h.ctx, "nobody@example.com"); !store.IsNotFound(err) {
		t.Fatalf("an uninvited person got an account: %v", err)
	}
	if n := h.audited("org.sso_refused"); n != 7 {
		t.Fatalf("%d refusals audited, want 7", n)
	}

	// The browser that comes back must be the one that started, with the
	// state Dock gave it, once.
	req, cb := h.ssoBegin(adaEmail)
	cb.Code = idp.Authorize(req, idp.Claims(req, "ada-sub", adaEmail))
	stolen := cb
	stolen.Binding = ""
	_, err := h.c.FinishSSO(h.ctx, stolen, testClient)
	wantCode(t, "no browser cookie", err, apitypes.CodeUnauthorized)
	_, err = h.c.FinishSSO(h.ctx, cb, testClient)
	wantCode(t, "state used up", err, apitypes.CodeExpired)

	req, cb = h.ssoBegin(adaEmail)
	cb.Code = idp.Authorize(req, idp.Claims(req, "ada-sub", adaEmail))
	cb.State = "made-up"
	_, err = h.c.FinishSSO(h.ctx, cb, testClient)
	wantCode(t, "unknown state", err, apitypes.CodeExpired)
}

func TestSSOWithSecondFactor(t *testing.T) {
	h, idp := ssoHarness(t)
	key, _ := addTOTP(t, h, adaSession(t, h))

	res, err := h.ssoSignIn(idp, "ada-sub", adaEmail, nil)
	if err != nil || !res.Pending || res.Actor.UserID != 0 {
		t.Fatalf("sso with an authenticator app: %+v, %v", res, err)
	}
	done, err := h.c.LoginSecondFactor(h.ctx, res.Secret, apitypes.Verification{Code: totpCode(key, totpStep(now()))}, testClient)
	if err != nil {
		t.Fatal(err)
	}
	s := session(t, h, done.Secret)
	if s.a.SSOOrgID != 1 || s.a.Factor != factorTOTP {
		t.Fatalf("finished session = %+v", s.a)
	}
}

func TestSSOOnly(t *testing.T) {
	h, idp := ssoHarness(t)
	password := adaSession(t, h) // an admin of the test org
	other, err := h.c.CreateOrg(h.ctx, password.a, apitypes.CreateOrgRequest{Slug: "other", Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = h.c.SetOrgSSOOnly(h.ctx, password.a, apitypes.SetSSOOnlyRequest{SSOOnly: true})
	wantCode(t, "sso-only from a password session", err, apitypes.CodeValidation)

	sso := h.ssoSession(idp, "ada-sub", adaEmail)
	if o, err := h.c.SetOrgSSOOnly(h.ctx, sso.a, apitypes.SetSSOOnlyRequest{SSOOnly: true}); err != nil || !o.SSOOnly {
		t.Fatalf("sso-only on: %+v, %v", o, err)
	}
	// Her password session no longer acts in the org; the SSO one does,
	// but only there.
	if a, err := h.c.AuthenticateSession(h.ctx, password.secret, "test"); err != nil || a.OrgID != 0 || a.Me().SSORequired == "" {
		t.Fatalf("password session in an sso-only org: %+v, %v", a, err)
	} else if _, err := h.c.GetOrg(h.ctx, a); CodeOf(err) != apitypes.CodeSSORequired {
		t.Fatalf("password session read the org: %v", err)
	}
	if a, err := h.c.AuthenticateSession(h.ctx, sso.secret, "test"); err != nil || a.OrgID != 1 {
		t.Fatalf("sso session: %+v, %v", a, err)
	}
	if a, err := h.c.AuthenticateSession(h.ctx, sso.secret, other.Slug); err != nil || a.OrgID != 0 || a.Me().SSORequired == "" {
		t.Fatalf("sso session acted in another org: %+v, %v", a, err)
	}
	if a, err := h.c.AuthenticateSession(h.ctx, password.secret, other.Slug); err != nil || a.OrgSlug != "other" {
		t.Fatalf("password session in the other org: %+v, %v", a, err)
	}

	// Its last provider stays while it's on; the server turns it off.
	wantCode(t, "remove last provider", h.c.DeleteSSOProvider(h.ctx, testSystem, "corp"), apitypes.CodeConflict)
	if err := h.c.ResetOrgSSOOnly(h.ctx, System, "test"); err != nil {
		t.Fatal(err)
	}
	if a, err := h.c.AuthenticateSession(h.ctx, password.secret, "test"); err != nil || a.OrgID != 1 {
		t.Fatalf("password session after the reset: %+v, %v", a, err)
	}
}

func TestSSOClientSecretSealed(t *testing.T) {
	h, _ := ssoHarness(t)
	sealed := h.blob(`SELECT client_secret FROM sso_providers WHERE name = 'corp'`)
	if len(sealed) == 0 || bytes.Contains(sealed, []byte(oidctest.ClientSecret)) {
		t.Fatalf("client secret stored as %q", sealed)
	}
	ps, err := h.c.ListSSOProviders(h.ctx, testSystem)
	if err != nil || len(ps) != 1 || ps[0].AllowedDomains[0] != "example.com" || !strings.HasSuffix(ps[0].RedirectURL, SSOCallbackPath) {
		t.Fatalf("providers = %+v, %v", ps, err)
	}
}
