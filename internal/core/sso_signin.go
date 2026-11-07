package core

import (
	"cmp"
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Signing in through an org's single sign-on (ADR 0058): OpenID
// Connect's authorization code flow with PKCE (S256), state and nonce.
//
//  1. StartSSO finds the provider by the email's domain (or the org and
//     provider named) and returns its authorization URL. The flow waits in
//     memory (ceremonies, SSOTTL), bound to the browser by a random value
//     the API keeps in a cookie.
//  2. The provider sends the browser back to SSOCallbackPath. FinishSSO
//     takes the flow out by its state (once), checks the browser's value,
//     exchanges the code with the PKCE verifier, verifies the ID token
//     (signature, issuer, audience, expiry, nonce) and requires a verified
//     email in one of the provider's domains.
//  3. ssoUser maps the person to a Dock user (see there). The session
//     then starts as after a password, with factor sso and the org
//     recorded: it acts only in that org, and a user with an authenticator
//     app still gives a code.

const (
	// ssoTimeout bounds each request to an identity provider.
	ssoTimeout = 10 * time.Second
	// ssoMaxResponse bounds what Dock reads of a provider's answer.
	ssoMaxResponse = 1 << 20
)

// ssoState is single sign-on's in-memory side: the flows waiting for a
// provider's answer, and the client that talks to providers.
type ssoState struct {
	flows  *ceremonies
	client *http.Client
}

func newSSOState() ssoState {
	return ssoState{
		flows:  newCeremonies(),
		client: &http.Client{Timeout: ssoTimeout, Transport: limitedTransport{http.DefaultTransport}},
	}
}

// limitedTransport cuts responses off at ssoMaxResponse: the OIDC
// libraries read whole bodies.
type limitedTransport struct{ next http.RoundTripper }

// RoundTrip sends r and limits the response's body.
func (t limitedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(r)
	if err == nil {
		resp.Body = limitedBody{io.LimitReader(resp.Body, ssoMaxResponse), resp.Body}
	}
	return resp, err
}

type limitedBody struct {
	io.Reader
	io.Closer
}

// ssoFlow is a single sign-on waiting for the provider's answer.
type ssoFlow struct {
	providerID int64
	issuer     string
	// provider is the issuer's discovery, reused for the code exchange
	// and the ID token's keys.
	provider *oidc.Provider
	nonce    string
	verifier string
	// binding is the hash of the value in the browser's cookie.
	binding string
	to      string
}

// discover reads issuer's OpenID Connect discovery document. A failure is
// logged in full; callers tell people only that it failed, never what the
// issuer answered.
func (s ssoState) discover(ctx context.Context, log *slog.Logger, issuer string) (*oidc.Provider, error) {
	p, err := oidc.NewProvider(oidc.ClientContext(ctx, s.client), issuer)
	if err != nil {
		log.Warn("sso: discovery failed", "issuer", issuer, "err", err)
		return nil, invalid("couldn't read the OpenID Connect discovery document of %s; check the issuer URL (Dock reads %s/.well-known/openid-configuration and its issuer must match exactly) and that Dock can reach it",
			issuer, strings.TrimSuffix(issuer, "/"))
	}
	return p, nil
}

// ssoProvider is a provider with its org.
type ssoProvider struct {
	store.SsoProvider
	org store.Org
}

// SSOStart is where StartSSO sends the browser: the provider's page, with
// Binding for the browser's cookie, or a choice of orgs.
type SSOStart struct {
	apitypes.SSOStart
	Binding string
}

// StartSSO starts signing in through single sign-on. Unauthenticated, so
// it is rate limited per client IP.
func (c *Core) StartSSO(ctx context.Context, req apitypes.SSOStartRequest, clientIP string) (SSOStart, error) {
	if !c.loginLimit.AllowIP(clientIP, now()) {
		return SSOStart{}, errf(apitypes.CodeRateLimited, "too many sign-ins from %s; wait a minute and try again", clientIP)
	}
	to, err := landingPath(req.To)
	if err != nil {
		return SSOStart{}, err
	}
	p, choices, err := c.ssoProviderFor(ctx, req)
	if err != nil || choices != nil {
		return SSOStart{SSOStart: apitypes.SSOStart{Choices: choices}}, err
	}
	prov, err := c.sso.discover(ctx, c.log, p.Issuer)
	if err != nil {
		return SSOStart{}, errf(apitypes.CodeServerOffline, "the identity provider of %s isn't answering; try again in a minute, or sign in with your password", p.org.Name)
	}
	nonce, _ := newSecret("")
	binding, bindingHash := newSecret("")
	verifier := oauth2.GenerateVerifier()
	state, err := c.sso.flows.put(&ceremony{purpose: ceremonySSO, sso: &ssoFlow{
		providerID: p.ID, issuer: p.Issuer, provider: prov, nonce: nonce, verifier: verifier, binding: bindingHash, to: to,
	}})
	if err != nil {
		return SSOStart{}, err
	}
	authURL := c.oauthConfig(p, prov, "").AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
	return SSOStart{SSOStart: apitypes.SSOStart{URL: authURL}, Binding: binding}, nil
}

// ssoProviderFor finds the provider a sign-in starts with: the org and
// provider named, or the one whose domains include the email's. Several
// orgs' providers for that domain come back as choices.
func (c *Core) ssoProviderFor(ctx context.Context, req apitypes.SSOStartRequest) (ssoProvider, []apitypes.SSOChoice, error) {
	if req.Org != "" || req.Provider != "" {
		if req.Org == "" || req.Provider == "" {
			return ssoProvider{}, nil, invalid("name both the organization and its provider, or give your email address")
		}
		row, err := c.store.GetSSOProviderByOrg(ctx, store.GetSSOProviderByOrgParams{Slug: req.Org, Name: req.Provider})
		if store.IsNotFound(err) {
			// An org's old slug still names it (ADR 0064).
			if to, ok, merr := c.movedSlug(ctx, req.Org); merr != nil {
				return ssoProvider{}, nil, merr
			} else if ok {
				row, err = c.store.GetSSOProviderByOrg(ctx, store.GetSSOProviderByOrgParams{Slug: to, Name: req.Provider})
			}
		}
		if store.IsNotFound(err) {
			return ssoProvider{}, nil, notFound("organization %s has no single sign-on provider %s", req.Org, req.Provider)
		}
		return ssoProvider{row.SsoProvider, row.Org}, nil, err
	}
	domain := emailDomain(req.Email)
	if domain == "" {
		return ssoProvider{}, nil, invalid("enter your email address to sign in with single sign-on")
	}
	rows, err := c.store.FindSSOProviders(ctx, domain)
	if err != nil {
		return ssoProvider{}, nil, err
	}
	switch len(rows) {
	case 0:
		return ssoProvider{}, nil, notFound("single sign-on isn't set up for %s; sign in with your password", domain)
	case 1:
		return ssoProvider{rows[0].SsoProvider, rows[0].Org}, nil, nil
	}
	choices := make([]apitypes.SSOChoice, len(rows))
	for i, r := range rows {
		choices[i] = apitypes.SSOChoice{Org: r.Org.Slug, OrgName: r.Org.Name, Provider: r.SsoProvider.Name}
	}
	slices.SortFunc(choices, func(x, y apitypes.SSOChoice) int {
		return cmp.Or(cmp.Compare(x.Org, y.Org), cmp.Compare(x.Provider, y.Provider))
	})
	return ssoProvider{}, choices, nil
}

// oauthConfig is the OAuth 2.0 client Dock is at provider p.
func (c *Core) oauthConfig(p ssoProvider, prov *oidc.Provider, secret string) *oauth2.Config {
	return &oauth2.Config{
		ClientID: p.ClientID, ClientSecret: secret, Endpoint: prov.Endpoint(), RedirectURL: c.ssoRedirectURL(),
		Scopes: []string{oidc.ScopeOpenID, "email", "profile"},
	}
}

// SSOCallback is what a provider sent the browser back with, and the
// value of the browser's binding cookie.
type SSOCallback struct {
	State, Code string
	// Error is the provider's error code (access_denied when the person
	// cancelled).
	Error   string
	Binding string
}

// SSOResult is a finished single sign-on: a session or a pending second
// factor, and the path to land on.
type SSOResult struct {
	LoginResult
	To string
}

// FinishSSO finishes a single sign-on the provider sent the browser back
// from. Every refusal after the flow is found is audited in the
// provider's org; error messages are for the person and never carry what
// the provider answered.
func (c *Core) FinishSSO(ctx context.Context, cb SSOCallback, clientIP string) (SSOResult, error) {
	cer, err := c.sso.flows.take(cb.State, ceremonySSO, 0, 0)
	if err != nil {
		c.log.Warn("sso: unknown, used or expired state", "ip", clientIP)
		return SSOResult{}, errf(apitypes.CodeExpired, "this sign-in expired or was used already; start again")
	}
	f := cer.sso
	row, err := c.store.GetSSOProviderWithOrg(ctx, f.providerID)
	if store.IsNotFound(err) || err == nil && row.SsoProvider.Issuer != f.issuer {
		return SSOResult{}, errf(apitypes.CodeExpired, "the organization's single sign-on changed during the sign-in; start again")
	}
	if err != nil {
		return SSOResult{}, err
	}
	p := ssoProvider{row.SsoProvider, row.Org}
	email := ""
	res, err := c.finishSSO(ctx, p, f, cb, clientIP, &email)
	if err != nil {
		reason := err.Error()
		if CodeOf(err) == apitypes.CodeInternal {
			reason = "internal error"
		}
		who := Actor{Kind: "user", Name: cmp.Or(email, "unknown"), OrgID: p.OrgID}
		c.audit(ctx, who, "org.sso_refused", nil, nil, "sso "+p.Name, fmt.Sprintf("refused a single sign-on from %s: %s", clientIP, reason))
		return SSOResult{}, err
	}
	return SSOResult{LoginResult: res, To: f.to}, nil
}

// errSSOAnswer is the refusal for an answer from the provider that
// doesn't check out; the details go to the log.
func errSSOAnswer(org string) error {
	return errf(apitypes.CodeUnauthorized, "the identity provider's answer didn't check out; try again, or ask an admin of %s to check the single sign-on settings", org)
}

// finishSSO checks the provider's answer for flow f and signs the person
// in; email is set once the ID token names them.
func (c *Core) finishSSO(ctx context.Context, p ssoProvider, f *ssoFlow, cb SSOCallback, clientIP string, email *string) (LoginResult, error) {
	log := c.log.With("org", p.org.Slug, "provider", p.Name, "ip", clientIP)
	if cb.Binding == "" || subtle.ConstantTimeCompare([]byte(hashSecret(cb.Binding)), []byte(f.binding)) != 1 {
		return LoginResult{}, errf(apitypes.CodeUnauthorized, "this sign-in was started in another browser, or its cookie was lost; start again here")
	}
	switch {
	case cb.Error == "access_denied":
		return LoginResult{}, errf(apitypes.CodeDenied, "the sign-in was cancelled at the identity provider")
	case cb.Error != "":
		log.Warn("sso: provider refused", "error", cb.Error)
		return LoginResult{}, errf(apitypes.CodeDenied, "the identity provider refused the sign-in; ask an admin of %s to check the single sign-on settings", p.org.Name)
	case cb.Code == "":
		return LoginResult{}, invalid("the identity provider sent no sign-in code; start again")
	}
	secret, err := c.box.Open(p.ClientSecret, ssoSecretAAD(p.OrgID, p.Name))
	if err != nil {
		return LoginResult{}, fmt.Errorf("open client secret of %s/%s: %w", p.org.Slug, p.Name, err)
	}
	hctx := oidc.ClientContext(ctx, c.sso.client)
	tok, err := c.oauthConfig(p, f.provider, string(secret)).Exchange(hctx, cb.Code, oauth2.VerifierOption(f.verifier))
	if err != nil {
		log.Warn("sso: code exchange failed", "err", err)
		return LoginResult{}, errf(apitypes.CodeUnauthorized, "the identity provider didn't accept the sign-in; start again, or ask an admin of %s to check the provider's client ID and secret", p.org.Name)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		log.Warn("sso: no id_token in the token response")
		return LoginResult{}, errSSOAnswer(p.org.Name)
	}
	idt, err := f.provider.Verifier(&oidc.Config{ClientID: p.ClientID, Now: now}).Verify(hctx, raw)
	if err != nil {
		log.Warn("sso: ID token rejected", "err", err)
		return LoginResult{}, errSSOAnswer(p.org.Name)
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(f.nonce)) != 1 {
		log.Warn("sso: ID token nonce mismatch")
		return LoginResult{}, errSSOAnswer(p.org.Name)
	}
	var claims struct {
		Email             string `json:"email"`
		EmailVerified     any    `json:"email_verified"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
	}
	if err := idt.Claims(&claims); err != nil {
		log.Warn("sso: ID token claims", "err", err)
		return LoginResult{}, errSSOAnswer(p.org.Name)
	}
	*email = strings.TrimSpace(claims.Email)
	if *email == "" {
		return LoginResult{}, errf(apitypes.CodeForbidden, "the identity provider sent no email address; ask an admin of %s to let Dock read it (the email scope)", p.org.Name)
	}
	if !emailVerified(claims.EmailVerified) {
		return LoginResult{}, errf(apitypes.CodeForbidden, "your identity provider hasn't verified %s; verify it there and try again", *email)
	}
	domains, err := c.store.ListSSOProviderDomains(ctx, p.ID)
	if err != nil {
		return LoginResult{}, err
	}
	if !slices.Contains(domains, emailDomain(*email)) {
		return LoginResult{}, errf(apitypes.CodeForbidden, "%s isn't an address %s signs in with single sign-on; sign in with your password", *email, p.org.Name)
	}
	name := cmp.Or(strings.TrimSpace(claims.Name), strings.TrimSpace(claims.PreferredUsername), strings.Split(*email, "@")[0])
	u, err := c.ssoUser(ctx, p, idt.Subject, *email, clean(name, 100, *email))
	if err != nil {
		return LoginResult{}, err
	}
	via := signInVia{factor: factorSSO, ssoOrgID: p.OrgID, ssoOrg: p.org.Slug, provider: p.Name}
	n, err := c.store.CountUserFactors(ctx, u.ID)
	if err != nil {
		return LoginResult{}, err
	}
	if n > 0 {
		return c.startSecondFactor(ctx, u, via)
	}
	return c.signIn(ctx, u, via, clientIP)
}

// ssoUser maps the person a provider vouched for to a Dock user:
//
//  1. by their identity (provider, subject), linked on an earlier sign-in;
//  2. else by their verified email to an existing user;
//  3. else a new user, when the org invited that email or the provider
//     has a just-in-time role.
//
// The user must then be a member of the provider's org: one already, one
// with an open invite (accepted now), or one the provider's
// just-in-time role lets in. Anyone else is refused and told to ask for an
// invite. A disabled user is refused.
func (c *Core) ssoUser(ctx context.Context, p ssoProvider, subject, email, name string) (store.User, error) {
	var u store.User
	id, err := c.store.GetSSOIdentity(ctx, store.GetSSOIdentityParams{ProviderID: p.ID, Subject: subject})
	linked := err == nil
	switch {
	case linked:
		u, err = c.store.GetUser(ctx, id.UserID)
	case store.IsNotFound(err):
		u, err = c.store.GetUserByEmail(ctx, email)
		if store.IsNotFound(err) {
			return c.ssoNewUser(ctx, p, subject, email, name)
		}
	}
	if err != nil {
		return store.User{}, err
	}
	if u.DisabledAt != nil {
		return store.User{}, errf(apitypes.CodeForbidden, "your Dock account is disabled; ask an operator")
	}
	if err := c.ssoMembership(ctx, p, u); err != nil {
		return store.User{}, err
	}
	if linked {
		return u, nil
	}
	err = c.store.CreateSSOIdentity(ctx, store.CreateSSOIdentityParams{ProviderID: p.ID, Subject: subject, UserID: u.ID, Email: email, CreatedAt: now()})
	if err != nil && !store.IsUnique(err) { // a concurrent sign-in linked it
		return store.User{}, err
	}
	c.audit(ctx, userActor(u), "user.sso_link", nil, nil, u.Email, fmt.Sprintf("linked %s to their identity at %s's single sign-on (%s)", u.Email, p.org.Slug, p.Name))
	return u, nil
}

// ssoMembership makes sure u is a member of p's org: already, through an
// open invite of their email (accepted now) or through the provider's
// just-in-time role. Otherwise they are refused.
func (c *Core) ssoMembership(ctx context.Context, p ssoProvider, u store.User) error {
	_, err := c.store.GetMemberRole(ctx, store.GetMemberRoleParams{OrgID: p.OrgID, UserID: u.ID})
	if !store.IsNotFound(err) {
		return err
	}
	inv, role, err := c.ssoJoinRole(ctx, p, u.Email)
	if err != nil {
		return err
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error { return joinTx(ctx, q, p.org, inv, u.ID, role) })
	if store.IsUnique(err) { // joined meanwhile
		return nil
	}
	if err != nil {
		return err
	}
	c.auditSSOJoin(ctx, p, u, role, inv)
	return nil
}

// ssoNewUser creates the user a provider vouched for, when the org
// invited their email or the provider has a just-in-time role, with an
// unusable random password: they sign in through single sign-on (an
// operator can give them a password).
func (c *Core) ssoNewUser(ctx context.Context, p ssoProvider, subject, email, name string) (store.User, error) {
	inv, role, err := c.ssoJoinRole(ctx, p, email)
	if err != nil {
		return store.User{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(generatePassword()), bcrypt.DefaultCost)
	if err != nil {
		return store.User{}, err
	}
	var u store.User
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		if u, err = q.CreateUser(ctx, store.CreateUserParams{Email: email, Name: name, PasswordHash: string(hash)}); err != nil {
			return err
		}
		if err := joinTx(ctx, q, p.org, inv, u.ID, role); err != nil {
			return err
		}
		return q.CreateSSOIdentity(ctx, store.CreateSSOIdentityParams{ProviderID: p.ID, Subject: subject, UserID: u.ID, Email: email, CreatedAt: now()})
	})
	if store.IsUnique(err) {
		return store.User{}, conflict("another sign-in created your account meanwhile; start again")
	}
	if err != nil {
		return store.User{}, err
	}
	c.audit(ctx, userActor(u), "user.create", nil, nil, u.Email, fmt.Sprintf("created user %s on their first sign-in through %s's single sign-on (%s)", u.Email, p.org.Slug, p.Name))
	c.auditSSOJoin(ctx, p, u, role, inv)
	return u, nil
}

// ssoJoinRole is how someone who isn't a member joins p's org on signing
// in: their open invite (inv, with its role), or the provider's
// just-in-time role (inv nil). Without either they are refused.
func (c *Core) ssoJoinRole(ctx context.Context, p ssoProvider, email string) (*store.OrgInvite, string, error) {
	inv, err := c.store.GetOpenOrgInviteByEmail(ctx, store.GetOpenOrgInviteByEmailParams{OrgID: p.OrgID, Email: email})
	open := err == nil && now().Before(inv.ExpiresAt)
	switch {
	case open:
		return &inv, inv.Role, nil
	case err != nil && !store.IsNotFound(err):
		return nil, "", err
	case p.JitRole != "":
		return nil, p.JitRole, nil
	case err == nil:
		return nil, "", errf(apitypes.CodeForbidden, "your invite to %s expired; ask an admin of %s for a new one", p.org.Name, p.org.Name)
	}
	return nil, "", errf(apitypes.CodeForbidden, "you aren't a member of %s; ask an admin of %s for an invite", p.org.Name, p.org.Name)
}

// auditSSOJoin records u joining p's org on signing in.
func (c *Core) auditSSOJoin(ctx context.Context, p ssoProvider, u store.User, role string, inv *store.OrgInvite) {
	how := "just in time"
	if inv != nil {
		how = "accepting their invite"
	}
	in := userActor(u).member(p.org, role, apitypes.ScopeAdmin)
	c.audit(ctx, in, "org.join", nil, nil, u.Email, fmt.Sprintf("%s joined as %s through single sign-on (%s), %s", u.Email, role, p.Name, how))
}

// landingPath checks where to send the browser after signing in: a path
// on Dock, never another site.
func landingPath(to string) (string, error) {
	if to == "" {
		return "/", nil
	}
	u, err := url.Parse(to)
	if err != nil || !strings.HasPrefix(to, "/") || strings.HasPrefix(to, "//") || strings.ContainsAny(to, "\\\r\n\t") || u.Scheme != "" || u.Host != "" {
		return "", invalid("to must be a path on Dock, like /acme/shop")
	}
	return to, nil
}

// emailDomain is an email address's domain in lowercase; "" when it has
// none.
func emailDomain(email string) string {
	i := strings.LastIndexByte(email, '@')
	if i <= 0 || i == len(email)-1 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(email[i+1:]))
}

// emailVerified reads the email_verified claim: a boolean, or the string
// "true" some providers send.
func emailVerified(v any) bool {
	switch v := v.(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}
