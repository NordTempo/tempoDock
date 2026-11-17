package core

import (
	"bytes"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core/oidctest"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store"
)

// ssoHarness is the test org with a fake OpenID Connect provider, corp,
// on an invite-only platform (the default).
func ssoHarness(t *testing.T) (*harness, *oidctest.Provider) {
	t.Helper()
	h := newHarness(t)
	freshLimits(h)
	idp := oidctest.New(t)
	if _, err := h.c.CreateSSOProvider(h.ctx, testSystem, apitypes.CreateSSOProviderRequest{
		Name: "corp", Kind: apitypes.SSOOIDC, Label: "Corp ID", Issuer: idp.Issuer, ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret,
	}); err != nil {
		t.Fatal(err)
	}
	return h, idp
}

// ssoBegin starts a single sign-on through provider and returns the
// authorization URL and the callback the browser will bring back, still
// without a code.
func (h *harness) ssoBegin(provider string) (string, SSOCallback) {
	h.t.Helper()
	start, err := h.c.StartSSO(h.ctx, apitypes.SSOStartRequest{Provider: provider, To: "/test/shop"}, testClient)
	if err != nil || start.URL == "" || start.Binding == "" {
		h.t.Fatalf("start: %+v, %v", start, err)
	}
	return start.URL, SSOCallback{Binding: start.Binding}
}

// ssoSignIn signs subject in through corp as email; edit changes the ID
// token's claims first.
func (h *harness) ssoSignIn(idp *oidctest.Provider, subject, email string, edit func(map[string]any)) (SSOResult, error) {
	h.t.Helper()
	return h.ssoSignInVia("corp", idp, subject, email, edit)
}

// ssoSignInVia is ssoSignIn through the provider named.
func (h *harness) ssoSignInVia(provider string, idp *oidctest.Provider, subject, email string, edit func(map[string]any)) (SSOResult, error) {
	h.t.Helper()
	authURL, cb := h.ssoBegin(provider)
	req := oidctest.Read(h.t, authURL)
	if req.RedirectURI != h.c.ssoRedirectURL() {
		h.t.Fatalf("redirect URI %s", req.RedirectURI)
	}
	claims := idp.Claims(req, subject, email)
	if edit != nil {
		edit(claims)
	}
	cb.State, cb.Code = req.State, idp.Authorize(req, claims)
	return h.c.FinishSSO(h.ctx, cb, testClient)
}

// ssoSession signs in through corp and returns the session.
func (h *harness) ssoSession(idp *oidctest.Provider, subject, email string) signedIn {
	h.t.Helper()
	res, err := h.ssoSignIn(idp, subject, email, nil)
	if err != nil || res.Pending || res.Blocked != nil || res.To != "/test/shop" {
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

func TestSSOSignsInAnExistingAccount(t *testing.T) {
	h, idp := ssoHarness(t)
	ada := h.cliUser(adaEmail, apitypes.ScopeDeploy)
	if _, err := h.c.SetOperator(h.ctx, testSystem, ada.UserID, true); err != nil {
		t.Fatal(err)
	}

	// Her verified email finds her account the first time; it is a full
	// session: her org, operating the platform, her own second factors.
	s := h.ssoSession(idp, "ada-sub", adaEmail)
	if s.a.UserID != ada.UserID || s.a.Factor != factorSSO || s.a.OrgSlug != "test" || !s.a.Operator || s.a.Me().SSOLabel != "Corp ID" {
		t.Fatalf("actor = %+v", s.a)
	}
	if _, err := h.c.StartTOTP(h.ctx, s.a); err != nil {
		t.Fatalf("SSO session can't add an authenticator app: %v", err)
	}
	// Linked: the subject finds her again, whatever email it sends now.
	again := h.ssoSession(idp, "ada-sub", "ada.lovelace@example.com")
	if again.a.UserID != ada.UserID {
		t.Fatalf("identity mapped to user %d, want %d", again.a.UserID, ada.UserID)
	}
	if h.audited("user.sso_link") != 1 || h.audited("user.login") != 2 {
		t.Fatal("link and sign-ins not audited")
	}
	// An operator made her account, so its email counts as proven: linking
	// leaves her password working.
	if _, err := h.c.Login(h.ctx, adaEmail, "a long enough password", testClient); err != nil {
		t.Fatalf("password after linking: %v", err)
	}
	// A step-up without a Dock second factor signs in through it again.
	wantCode(t, "reauth", h.c.Reauth(h.ctx, s.a, apitypes.Verification{Password: "x"}, testClient), apitypes.CodeSSORequired)
	// Removing the provider ends the sessions that came through it.
	if err := h.c.DeleteSSOProvider(h.ctx, testSystem, "corp"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.AuthenticateSession(h.ctx, s.secret, "", false); CodeOf(err) != apitypes.CodeUnauthorized {
		t.Fatalf("session outlived its provider: %v", err)
	}
}

// An account made from an invite never proved its email: whoever held
// the invite code chose its password. The email's owner signing in
// through a provider takes it over, and that person's ways in end.
func TestSSOTakesOverAnUnprovenAccount(t *testing.T) {
	h, idp := ssoHarness(t)
	admin := h.in(h.member("ann@x", map[string]apitypes.Scope{"test": apitypes.ScopeAdmin}), "test")
	inv, err := h.c.CreateInvite(h.ctx, admin, apitypes.CreateInviteRequest{Email: "vic@example.com", Role: apitypes.ScopeAdmin})
	if err != nil {
		t.Fatal(err)
	}
	// The inviter opens the link themselves and digs in.
	res, err := h.c.SignUpWithInvite(h.ctx, inv.Code, apitypes.InviteSignUpRequest{Name: "Vic", Password: "the inviter's password"}, testClient)
	if err != nil {
		t.Fatal(err)
	}
	squat := session(t, h, res.Secret)
	addTOTP(t, h, squat)
	squat = session(t, h, squat.secret)
	tok, err := h.c.CreateToken(h.ctx, h.in(squat.a, "test"), apitypes.CreateTokenRequest{Name: "kept", Scope: apitypes.ScopeDeploy})
	if err != nil {
		t.Fatal(err)
	}

	// Vic signs in through corp: no second factor to give, Vic's account.
	s := h.ssoSession(idp, "vic-sub", "vic@example.com")
	if s.a.UserID != squat.a.UserID || s.a.OrgSlug != "test" || s.a.Scope != apitypes.ScopeAdmin {
		t.Fatalf("actor = %+v", s.a)
	}
	if _, err := h.c.AuthenticateSession(h.ctx, squat.secret, "", false); CodeOf(err) != apitypes.CodeUnauthorized {
		t.Fatalf("the inviter's session: %v", err)
	}
	if _, err := h.c.AuthenticateToken(h.ctx, tok.Secret, ""); err == nil {
		t.Fatal("the inviter's token still works")
	}
	if _, err := h.c.Login(h.ctx, "vic@example.com", "the inviter's password", testClient); err == nil {
		t.Fatal("the inviter's password still works")
	}
	if n, err := h.store.CountUserFactors(h.ctx, s.a.UserID); err != nil || n != 0 {
		t.Fatalf("second factors left: %d, %v", n, err)
	}
	if h.audited("user.sso_takeover") != 1 || h.audited("user.sso_link") != 0 {
		t.Fatalf("audited takeover %d, link %d", h.audited("user.sso_takeover"), h.audited("user.sso_link"))
	}
	// Proven now: the next sign-in finds the linked identity.
	if again := h.ssoSession(idp, "vic-sub", "vic@example.com"); again.a.UserID != s.a.UserID || h.audited("user.sso_takeover") != 1 {
		t.Fatalf("second sign-in: %+v", again.a)
	}
}

func TestSSONewAccounts(t *testing.T) {
	h, idp := ssoHarness(t)

	// Invite only: nobody invited, no account, and the page says why.
	res, err := h.ssoSignIn(idp, "eve-sub", "eve@example.com", nil)
	if err != nil || res.Blocked == nil || res.Blocked.Provider != "Corp ID" || res.Blocked.Email != "eve@example.com" || res.Secret != "" {
		t.Fatalf("uninvited on an invite-only Dock: %+v, %v", res, err)
	}
	if _, err := h.store.GetUserByEmail(h.ctx, "eve@example.com"); !store.IsNotFound(err) {
		t.Fatalf("an uninvited person got an account: %v", err)
	}

	// An invite makes the account and is accepted with its role; started
	// from the invite link, it lands on the start, the link used up.
	inv, err := h.c.CreateInvite(h.ctx, testSystem, apitypes.CreateInviteRequest{Email: "Bob@Example.com", Role: apitypes.ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	start, err := h.c.StartSSO(h.ctx, apitypes.SSOStartRequest{Provider: "corp", To: "/invite/" + inv.Code}, testClient)
	if err != nil {
		t.Fatal(err)
	}
	req := oidctest.Read(t, start.URL)
	res, err = h.c.FinishSSO(h.ctx, SSOCallback{State: req.State, Code: idp.Authorize(req, idp.Claims(req, "bob-sub", "bob@example.com")), Binding: start.Binding}, testClient)
	if err != nil || res.To != "/" {
		t.Fatalf("invited sign-in from the link: %+v, %v", res, err)
	}
	s := session(t, h, res.Secret)
	if s.a.Scope != apitypes.ScopeRead || s.a.OrgID != 1 || s.a.DisplayName != "Test Person" {
		t.Fatalf("invited actor = %+v", s.a)
	}
	if invites, err := h.c.ListInvites(h.ctx, testSystem); err != nil || len(invites) != 0 {
		t.Fatalf("invite still open: %+v, %v", invites, err)
	}

	// Open sign-up: an account in no org.
	off := false
	if _, err := h.c.SetSignInSettings(h.ctx, testSystem, apitypes.SignInSettingsRequest{InviteOnly: &off}); err != nil {
		t.Fatal(err)
	}
	carol := h.ssoSession(idp, "carol-sub", "carol@example.org")
	if carol.a.Email != "carol@example.org" || carol.a.OrgID != 0 || carol.a.Operator {
		t.Fatalf("open sign-up actor = %+v", carol.a)
	}
	if orgs, err := h.c.ListOrgs(h.ctx, carol.a); err != nil || len(orgs) != 0 {
		t.Fatalf("a new account joined %+v, %v", orgs, err)
	}
	if h.audited("user.create") != 2 || h.audited("org.join") != 1 || h.audited("user.sso_refused") != 1 {
		t.Fatal("new accounts not audited")
	}
}

func TestSSORefusals(t *testing.T) {
	h, idp := ssoHarness(t)
	h.cliUser(adaEmail, apitypes.ScopeDeploy)

	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		code apitypes.ErrorCode
	}{
		{"nonce mismatch", func(c map[string]any) { c["nonce"] = "another" }, apitypes.CodeUnauthorized},
		{"wrong audience", func(c map[string]any) { c["aud"] = "another-client" }, apitypes.CodeUnauthorized},
		{"wrong issuer", func(c map[string]any) { c["iss"] = "https://evil.example" }, apitypes.CodeUnauthorized},
		{"expired", func(c map[string]any) {
			c["iat"], c["exp"] = time.Now().Add(-2*time.Hour).Unix(), time.Now().Add(-time.Hour).Unix()
		}, apitypes.CodeUnauthorized},
		{"unverified email", func(c map[string]any) { c["email_verified"] = false }, apitypes.CodeForbidden},
		{"no email", func(c map[string]any) { delete(c, "email") }, apitypes.CodeForbidden},
	} {
		res, err := h.ssoSignIn(idp, "sub-"+tc.name, adaEmail, tc.edit)
		if CodeOf(err) != tc.code || res.Secret != "" {
			t.Errorf("%s: %+v, %v (%s), want %s", tc.name, res, err, CodeOf(err), tc.code)
		}
	}
	if n := h.audited("user.sso_refused"); n != 6 {
		t.Fatalf("%d refusals audited, want 6", n)
	}

	// The browser that comes back must be the one that started, with the
	// state Dock gave it, once.
	authURL, cb := h.ssoBegin("corp")
	req := oidctest.Read(t, authURL)
	cb.State, cb.Code = req.State, idp.Authorize(req, idp.Claims(req, "ada-sub", adaEmail))
	stolen := cb
	stolen.Binding = ""
	_, err := h.c.FinishSSO(h.ctx, stolen, testClient)
	wantCode(t, "no browser cookie", err, apitypes.CodeUnauthorized)
	_, err = h.c.FinishSSO(h.ctx, cb, testClient)
	wantCode(t, "state used up", err, apitypes.CodeExpired)

	_, err = h.c.FinishSSO(h.ctx, SSOCallback{State: "made-up", Code: "x", Binding: "y"}, testClient)
	wantCode(t, "unknown state", err, apitypes.CodeExpired)
	_, err = h.c.StartSSO(h.ctx, apitypes.SSOStartRequest{Provider: "nope"}, testClient)
	wantCode(t, "unknown provider", err, apitypes.CodeNotFound)
}

func TestSSOGitHub(t *testing.T) {
	h := newHarness(t)
	freshLimits(h)
	gh := oidctest.NewGitHub(t)
	h.c.sso.github = githubEndpoints{AuthURL: gh.AuthURL, TokenURL: gh.TokenURL, API: gh.API}
	p, err := h.c.CreateSSOProvider(h.ctx, testSystem, apitypes.CreateSSOProviderRequest{
		Name: "github", Kind: apitypes.SSOGitHub, ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret,
	})
	if err != nil || p.Label != "GitHub" || p.Issuer != "" {
		t.Fatalf("add github: %+v, %v", p, err)
	}
	ada := h.cliUser(adaEmail, apitypes.ScopeDeploy)
	signIn := func(user oidctest.GitHubUser) (SSOResult, error) {
		authURL, cb := h.ssoBegin("github")
		req := oidctest.ReadGitHub(t, authURL)
		cb.State, cb.Code = req.State, gh.Authorize(req, user)
		return h.c.FinishSSO(h.ctx, cb, testClient)
	}

	// The primary email counts, and only once GitHub verified it.
	_, err = signIn(oidctest.GitHubUser{ID: 42, Login: "ada", Emails: []oidctest.GitHubEmail{
		{Email: "other@example.org", Verified: true}, {Email: adaEmail, Primary: true},
	}})
	wantCode(t, "unverified primary", err, apitypes.CodeForbidden)
	res, err := signIn(oidctest.GitHubUser{ID: 42, Login: "ada", Emails: []oidctest.GitHubEmail{{Email: adaEmail, Primary: true, Verified: true}}})
	if err != nil || res.Secret == "" {
		t.Fatalf("github sign-in: %+v, %v", res, err)
	}
	if s := session(t, h, res.Secret); s.a.UserID != ada.UserID || s.a.SSOProvider != "github" {
		t.Fatalf("actor = %+v", s.a)
	}
	// Its numeric id is the subject: a renamed login finds her.
	if res, err := signIn(oidctest.GitHubUser{ID: 42, Login: "ada-renamed", Emails: []oidctest.GitHubEmail{{Email: "new@example.org", Primary: true, Verified: true}}}); err != nil || session(t, h, res.Secret).a.UserID != ada.UserID {
		t.Fatalf("renamed github account: %+v, %v", res, err)
	}
}

func TestPasswordSignInOff(t *testing.T) {
	h, idp := ssoHarness(t)
	password := adaSession(t, h)
	if _, err := h.c.SetOperator(h.ctx, testSystem, password.a.UserID, true); err != nil {
		t.Fatal(err)
	}
	password = passwordLogin(t, h)
	off, on := false, true

	// Only from a sign-in through a provider, so it is known to work.
	_, err := h.c.SetSignInSettings(h.ctx, password.a, apitypes.SignInSettingsRequest{PasswordSignIn: &off})
	wantCode(t, "off from a password session", err, apitypes.CodeValidation)
	sso := h.ssoSession(idp, "ada-sub", adaEmail)
	if set, err := h.c.SetSignInSettings(h.ctx, sso.a, apitypes.SignInSettingsRequest{PasswordSignIn: &off}); err != nil || set.PasswordSignIn || !set.InviteOnly {
		t.Fatalf("password sign-in off: %+v, %v", set, err)
	}
	if m, err := h.c.SignInMethods(h.ctx); err != nil || m.Password || len(m.Providers) != 1 || m.Providers[0].Label != "Corp ID" {
		t.Fatalf("methods = %+v, %v", m, err)
	}
	_, err = h.c.Login(h.ctx, adaEmail, adaPasswd, testClient)
	wantCode(t, "password login", err, apitypes.CodeForbidden)
	inv, err := h.c.CreateInvite(h.ctx, testSystem, apitypes.CreateInviteRequest{Email: "bob@example.com", Role: apitypes.ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.c.SignUpWithInvite(h.ctx, inv.Code, apitypes.InviteSignUpRequest{Name: "Bob", Password: "a long enough password"}, testClient)
	wantCode(t, "password sign-up", err, apitypes.CodeForbidden)
	// Its last provider stays while passwords are off.
	wantCode(t, "remove last provider", h.c.DeleteSSOProvider(h.ctx, testSystem, "corp"), apitypes.CodeConflict)

	// The server turns passwords on again, and gives an account a new one.
	pw, err := h.c.ResetPasswordSignIn(h.ctx, System, adaEmail)
	if err != nil || len(pw) < 12 {
		t.Fatalf("reset: %q, %v", pw, err)
	}
	if _, err := h.c.Login(h.ctx, adaEmail, pw, testClient); err != nil {
		t.Fatalf("login after the reset: %v", err)
	}
	if set, err := h.c.SetSignInSettings(h.ctx, testSystem, apitypes.SignInSettingsRequest{PasswordSignIn: &on}); err != nil || !set.PasswordSignIn {
		t.Fatalf("settings = %+v, %v", set, err)
	}
}

func TestSSOProvidersAreTheOperators(t *testing.T) {
	h, _ := ssoHarness(t)
	admin := h.cliUser(adaEmail, apitypes.ScopeAdmin) // an org admin, not an operator
	if _, err := h.c.ListSSOProviders(h.ctx, admin); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("an org admin listed providers: %v", err)
	}
	_, err := h.c.CreateSSOProvider(h.ctx, testSystem, apitypes.CreateSSOProviderRequest{
		Name: "g", Kind: apitypes.SSOGoogle, Issuer: "https://evil.example", ClientID: "x", ClientSecret: "y",
	})
	wantCode(t, "google with an issuer", err, apitypes.CodeValidation)
	_, err = h.c.CreateSSOProvider(h.ctx, testSystem, apitypes.CreateSSOProviderRequest{
		Name: "corp", Kind: apitypes.SSOGitHub, ClientID: "x", ClientSecret: "y",
	})
	wantCode(t, "name taken", err, apitypes.CodeConflict)

	sealed := h.blob(`SELECT client_secret FROM sso_providers WHERE name = 'corp'`)
	if len(sealed) == 0 || bytes.Contains(sealed, []byte(oidctest.ClientSecret)) {
		t.Fatalf("client secret stored as %q", sealed)
	}
	ps, err := h.c.ListSSOProviders(h.ctx, testSystem)
	if err != nil || len(ps.Providers) != 1 || ps.Providers[0].Kind != apitypes.SSOOIDC || !strings.HasSuffix(ps.RedirectURL, SSOCallbackPath) {
		t.Fatalf("providers = %+v, %v", ps, err)
	}
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
	if s := session(t, h, done.Secret); s.a.SSOProvider != "corp" || s.a.Factor != factorTOTP {
		t.Fatalf("finished session = %+v", s.a)
	}
}

// A session counts as confirmed when the provider last had the person
// sign in, so signing in again on the provider's old session is no
// step-up; a step-up asks the provider to sign them in again.
func TestSSOStepUpNeedsAFreshSignIn(t *testing.T) {
	h, idp := ssoHarness(t)
	h.cliUser(adaEmail, apitypes.ScopeDeploy)
	signedInAt := func(at time.Time) func(map[string]any) {
		return func(c map[string]any) { c["auth_time"] = at.Unix() }
	}

	res, err := h.ssoSignIn(idp, "ada-sub", adaEmail, signedInAt(now().Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	stale := session(t, h, res.Secret)
	wantCode(t, "step-up on an hour-old provider sign-in", stale.a.requireRecent("revealing a secret"), apitypes.CodeReauthRequired)

	start, err := h.c.StartSSO(h.ctx, apitypes.SSOStartRequest{Provider: "corp", To: "/test/shop", Confirm: true}, testClient)
	if err != nil {
		t.Fatal(err)
	}
	if u, _ := url.Parse(start.URL); u.Query().Get("prompt") != "login" {
		t.Fatalf("a step-up doesn't ask the provider to sign in again: %s", start.URL)
	}
	req := oidctest.Read(t, start.URL)
	claims := idp.Claims(req, "ada-sub", adaEmail)
	signedInAt(now())(claims)
	res, err = h.c.FinishSSO(h.ctx, SSOCallback{State: req.State, Code: idp.Authorize(req, claims), Binding: start.Binding}, testClient)
	if err != nil {
		t.Fatal(err)
	}
	if err := session(t, h, res.Secret).a.requireRecent("revealing a secret"); err != nil {
		t.Fatalf("a fresh sign-in at the provider doesn't confirm: %v", err)
	}
}

// A client secret an org's provider sealed (bound to its org and old
// name) opens as the platform provider's after the next start.
func TestSSOSecretResealed(t *testing.T) {
	h, _ := ssoHarness(t)
	old := secrets.Column{Table: "sso_providers", Name: "client_secret", Key: []string{"org_id", "name"}}
	sealed, err := h.c.box.Seal([]byte("old secret"), old.AAD("1", "corp"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.DB.ExecContext(h.ctx, `UPDATE sso_providers SET client_secret = ? WHERE name = 'corp'`, sealed); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.DB.ExecContext(h.ctx, `INSERT INTO sso_reseal (provider_id, org_id, name) SELECT id, 1, 'corp' FROM sso_providers`); err != nil {
		t.Fatal(err)
	}
	if err := h.c.resealSSOSecrets(h.ctx); err != nil {
		t.Fatal(err)
	}
	p, err := h.store.GetSSOProviderByName(h.ctx, "corp")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := h.c.box.Open(p.ClientSecret, ssoSecretAAD("corp")); err != nil || string(got) != "old secret" {
		t.Fatalf("resealed secret = %q, %v", got, err)
	}
	if rows, err := h.store.ListSSOReseal(h.ctx); err != nil || len(rows) != 0 {
		t.Fatalf("still to reseal: %+v, %v", rows, err)
	}
}
