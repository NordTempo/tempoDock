package core

import (
	"cmp"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Signing in through one of the platform's identity providers (ADR 0058,
// 0065): the OAuth 2.0 authorization code flow with PKCE (S256) and
// state; OpenID Connect providers (google, oidc) add a nonce and an ID
// token, GitHub's OAuth (github) has none, so Dock reads the account and
// its primary verified email from GitHub's API instead.
//
//  1. StartSSO returns the authorization URL of the provider named. The
//     flow waits in memory (ceremonies, SSOTTL), bound to the browser by
//     a random value the API keeps in a cookie.
//  2. The provider sends the browser back to SSOCallbackPath. FinishSSO
//     takes the flow out by its state (once), checks the browser's value,
//     exchanges the code with the PKCE verifier and learns who the person
//     is: for OpenID Connect the verified ID token (signature, issuer,
//     audience, expiry, nonce), for GitHub its API. Either way it needs a
//     verified email.
//  3. ssoUser maps the person to a Dock account (see there). The session
//     then starts as after a password, with factor sso and the provider
//     recorded, and a user with an authenticator app still gives a code.

const (
	// ssoTimeout bounds each request to an identity provider.
	ssoTimeout = 10 * time.Second
	// ssoMaxResponse bounds what Dock reads of a provider's answer.
	ssoMaxResponse = 1 << 20
)

// ssoState is single sign-on's in-memory side: the flows waiting for a
// provider's answer, the client that talks to providers, and where GitHub
// and Microsoft are (tests point them at fakes).
type ssoState struct {
	flows     *ceremonies
	client    *http.Client
	github    githubEndpoints
	microsoft string
}

func newSSOState() ssoState {
	return ssoState{
		flows:     newCeremonies(),
		client:    &http.Client{Timeout: ssoTimeout, Transport: limitedTransport{http.DefaultTransport}},
		github:    githubDotCom,
		microsoft: microsoftDotCom,
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
	// kind and issuer are the provider's when the flow started; a change
	// meanwhile ends it.
	kind   string
	issuer string
	// provider is an OpenID Connect issuer's discovery, reused for the
	// code exchange and the ID token's keys; nil for GitHub.
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

// githubEndpoints is where Dock finds GitHub: its OAuth pages and its API.
type githubEndpoints struct {
	AuthURL, TokenURL, API string
}

var githubDotCom = githubEndpoints{
	AuthURL: "https://github.com/login/oauth/authorize", TokenURL: "https://github.com/login/oauth/access_token", API: "https://api.github.com",
}

// SSOStart is where StartSSO sends the browser, with Binding for the
// browser's cookie.
type SSOStart struct {
	apitypes.SSOStart
	Binding string
}

// StartSSO starts signing in through the provider req names.
// Unauthenticated, so it is rate limited per client IP.
func (c *Core) StartSSO(ctx context.Context, req apitypes.SSOStartRequest, clientIP string) (SSOStart, error) {
	if !c.loginLimit.AllowIP(clientIP, now()) {
		return SSOStart{}, errf(apitypes.CodeRateLimited, "too many sign-ins from %s; wait a minute and try again", clientIP)
	}
	to, err := landingPath(req.To)
	if err != nil {
		return SSOStart{}, err
	}
	p, err := c.store.GetSSOProviderByName(ctx, req.Provider)
	if store.IsNotFound(err) {
		return SSOStart{}, notFound("this Dock has no identity provider %q; reload the sign-in page", req.Provider)
	}
	if err != nil {
		return SSOStart{}, err
	}
	var prov *oidc.Provider
	if p.Kind != string(apitypes.SSOGitHub) {
		if prov, err = c.sso.discover(ctx, c.log, p.Issuer); err != nil {
			return SSOStart{}, errf(apitypes.CodeServerOffline, "%s isn't answering; try again in a minute, or sign in another way", p.Label)
		}
	}
	nonce, _ := newSecret("")
	binding, bindingHash := newSecret("")
	verifier := oauth2.GenerateVerifier()
	state, err := c.sso.flows.put(&ceremony{purpose: ceremonySSO, client: clientIP, sso: &ssoFlow{
		providerID: p.ID, kind: p.Kind, issuer: p.Issuer, provider: prov, nonce: nonce, verifier: verifier, binding: bindingHash, to: to,
	}})
	if err != nil {
		return SSOStart{}, err
	}
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}
	if prov != nil {
		opts = append(opts, oidc.Nonce(nonce))
	}
	authURL := c.oauthConfig(p, prov, "").AuthCodeURL(state, opts...)
	return SSOStart{SSOStart: apitypes.SSOStart{URL: authURL}, Binding: binding}, nil
}

// oauthConfig is the OAuth 2.0 client Dock is at provider p; prov is its
// discovery, nil for GitHub.
func (c *Core) oauthConfig(p store.SsoProvider, prov *oidc.Provider, secret string) *oauth2.Config {
	cfg := &oauth2.Config{ClientID: p.ClientID, ClientSecret: secret, RedirectURL: c.ssoRedirectURL()}
	if prov == nil {
		gh := c.sso.github
		cfg.Endpoint = oauth2.Endpoint{AuthURL: gh.AuthURL, TokenURL: gh.TokenURL, AuthStyle: oauth2.AuthStyleInParams}
		cfg.Scopes = []string{"read:user", "user:email"}
		return cfg
	}
	cfg.Endpoint = prov.Endpoint()
	cfg.Scopes = []string{oidc.ScopeOpenID, "email", "profile"}
	return cfg
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
// factor and the path to land on, or, on an invite-only Dock, Blocked:
// the person has no account and no invite.
type SSOResult struct {
	LoginResult
	To      string
	Blocked *SSOBlocked
}

// SSOBlocked is a person a provider signed in whom an invite-only Dock
// has no account for: the provider's label and their email.
type SSOBlocked struct {
	Provider, Email string
}

// errInviteOnly is ssoUser's refusal of a person with no account and no
// invite on an invite-only Dock.
var errInviteOnly = errf(apitypes.CodeForbidden, "this Dock is invite only, and you have no account and no open invite; ask an organization's admin for an invite")

// FinishSSO finishes a single sign-on the provider sent the browser back
// from. Every refusal after the flow is found is audited; error messages
// are for the person and never carry what the provider answered.
func (c *Core) FinishSSO(ctx context.Context, cb SSOCallback, clientIP string) (SSOResult, error) {
	cer, err := c.sso.flows.take(cb.State, ceremonySSO, 0, 0)
	if err != nil {
		c.log.Warn("sso: unknown, used or expired state", "ip", clientIP)
		return SSOResult{}, errf(apitypes.CodeExpired, "this sign-in expired or was used already; start again")
	}
	f := cer.sso
	p, err := c.store.GetSSOProvider(ctx, f.providerID)
	if store.IsNotFound(err) || err == nil && (p.Issuer != f.issuer || p.Kind != f.kind) {
		return SSOResult{}, errf(apitypes.CodeExpired, "the identity provider changed during the sign-in; start again")
	}
	if err != nil {
		return SSOResult{}, err
	}
	email := ""
	res, err := c.finishSSO(ctx, p, f, cb, clientIP, &email)
	if errors.Is(err, errInviteOnly) {
		c.audit(ctx, Actor{Kind: "user", Name: email}, "user.sso_refused", nil, nil, "sso "+p.Name,
			fmt.Sprintf("refused a sign-in through %s from %s: no account and no open invite, and sign-up is invite only", p.Name, clientIP))
		return SSOResult{Blocked: &SSOBlocked{Provider: p.Label, Email: email}}, nil
	}
	if err != nil {
		reason := err.Error()
		if CodeOf(err) == apitypes.CodeInternal {
			reason = "internal error"
		}
		c.audit(ctx, Actor{Kind: "user", Name: cmp.Or(email, "unknown")}, "user.sso_refused", nil, nil, "sso "+p.Name,
			fmt.Sprintf("refused a sign-in through %s from %s: %s", p.Name, clientIP, reason))
		return SSOResult{}, err
	}
	return SSOResult{LoginResult: res, To: c.ssoLanding(ctx, f.to)}, nil
}

// ssoLanding is where a finished sign-in lands: where it started, unless
// that is an invite link the sign-in just used up (a new account accepts
// its invites), which would only say it doesn't work; then the start.
func (c *Core) ssoLanding(ctx context.Context, to string) string {
	code, ok := strings.CutPrefix(to, "/invite/")
	if !ok {
		return to
	}
	if _, err := c.openInvite(ctx, code); err != nil {
		return "/"
	}
	return to
}

// errSSOAnswer is the refusal for an answer from provider label that
// doesn't check out; the details go to the log.
func errSSOAnswer(label string) error {
	return errf(apitypes.CodeUnauthorized, "%s's answer didn't check out; try again, or ask an operator to check the %s provider in the console", label, label)
}

// ssoPerson is who a provider says signed in.
type ssoPerson struct {
	subject, email, name string
}

// finishSSO checks the provider's answer for flow f and signs the person
// in; email is set once the provider names them.
func (c *Core) finishSSO(ctx context.Context, p store.SsoProvider, f *ssoFlow, cb SSOCallback, clientIP string, email *string) (LoginResult, error) {
	log := c.log.With("provider", p.Name, "ip", clientIP)
	if cb.Binding == "" || subtle.ConstantTimeCompare([]byte(hashSecret(cb.Binding)), []byte(f.binding)) != 1 {
		return LoginResult{}, errf(apitypes.CodeUnauthorized, "this sign-in was started in another browser, or its cookie was lost; start again here")
	}
	switch {
	case cb.Error == "access_denied":
		return LoginResult{}, errf(apitypes.CodeDenied, "the sign-in was cancelled at %s", p.Label)
	case cb.Error != "":
		log.Warn("sso: provider refused", "error", cb.Error)
		return LoginResult{}, errf(apitypes.CodeDenied, "%s refused the sign-in; ask an operator to check the %s provider in the console", p.Label, p.Label)
	case cb.Code == "":
		return LoginResult{}, invalid("%s sent no sign-in code; start again", p.Label)
	}
	secret, err := c.box.Open(p.ClientSecret, ssoSecretAAD(p.Name))
	if err != nil {
		return LoginResult{}, fmt.Errorf("open client secret of %s: %w", p.Name, err)
	}
	hctx := oidc.ClientContext(ctx, c.sso.client)
	tok, err := c.oauthConfig(p, f.provider, string(secret)).Exchange(hctx, cb.Code, oauth2.VerifierOption(f.verifier))
	if err != nil {
		log.Warn("sso: code exchange failed", "err", err)
		return LoginResult{}, errf(apitypes.CodeUnauthorized, "%s didn't accept the sign-in; start again, or ask an operator to check the %s provider's client ID and secret", p.Label, p.Label)
	}
	var who ssoPerson
	if f.provider == nil {
		who, err = c.githubPerson(hctx, log, p, tok)
	} else {
		who, err = oidcPerson(hctx, log, p, f, tok)
	}
	*email = who.email
	if err != nil {
		return LoginResult{}, err
	}
	u, err := c.ssoUser(ctx, p, who)
	if err != nil {
		return LoginResult{}, err
	}
	via := signInVia{factor: factorSSO, ssoProviderID: p.ID, ssoProvider: p.Name, ssoLabel: p.Label}
	n, err := c.store.CountUserFactors(ctx, u.ID)
	if err != nil {
		return LoginResult{}, err
	}
	if n > 0 {
		return c.startSecondFactor(ctx, u, via)
	}
	return c.signIn(ctx, u, via, clientIP)
}

// oidcPerson reads who an OpenID Connect provider's ID token names: it
// must be signed by the issuer for Dock's client, fresh, carry the flow's
// nonce, and name a verified email.
func oidcPerson(ctx context.Context, log *slog.Logger, p store.SsoProvider, f *ssoFlow, tok *oauth2.Token) (ssoPerson, error) {
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		log.Warn("sso: no id_token in the token response")
		return ssoPerson{}, errSSOAnswer(p.Label)
	}
	idt, err := f.provider.Verifier(&oidc.Config{ClientID: p.ClientID, Now: now}).Verify(ctx, raw)
	if err != nil {
		log.Warn("sso: ID token rejected", "err", err)
		return ssoPerson{}, errSSOAnswer(p.Label)
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(f.nonce)) != 1 {
		log.Warn("sso: ID token nonce mismatch")
		return ssoPerson{}, errSSOAnswer(p.Label)
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified any    `json:"email_verified"`
		// EmailDomainVerified is Microsoft's xms_edov, an optional claim:
		// the email's domain is verified by the tenant that owns it.
		EmailDomainVerified any    `json:"xms_edov"`
		Name                string `json:"name"`
		PreferredUsername   string `json:"preferred_username"`
	}
	if err := idt.Claims(&claims); err != nil {
		log.Warn("sso: ID token claims", "err", err)
		return ssoPerson{}, errSSOAnswer(p.Label)
	}
	who := ssoPerson{subject: idt.Subject, email: strings.TrimSpace(claims.Email)}
	if who.email == "" {
		return who, errf(apitypes.CodeForbidden, "%s sent no email address; ask an operator to let Dock read it (the email scope)", p.Label)
	}
	switch {
	case p.Kind == string(apitypes.SSOMicrosoft):
		// Entra ID sends no email_verified, and a tenant's admins may set
		// any address; only xms_edov vouches for it (ADR 0081).
		if !emailVerified(claims.EmailDomainVerified) && !emailVerified(claims.EmailVerified) {
			return who, errf(apitypes.CodeForbidden, "Microsoft didn't say %s is verified; ask an operator to add the optional claims email and xms_edov to the ID token of Dock's app registration (Token configuration)", who.email)
		}
	case !emailVerified(claims.EmailVerified):
		return who, errf(apitypes.CodeForbidden, "%s hasn't verified %s; verify it there and try again", p.Label, who.email)
	}
	who.name = cmp.Or(strings.TrimSpace(claims.Name), strings.TrimSpace(claims.PreferredUsername))
	return who, nil
}

// githubPerson reads who signed in at GitHub from its API: the account
// (its numeric id is the subject; logins can be renamed) and its primary
// email, which must be verified.
func (c *Core) githubPerson(ctx context.Context, log *slog.Logger, p store.SsoProvider, tok *oauth2.Token) (ssoPerson, error) {
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := c.githubGet(ctx, tok, "/user", &user); err != nil || user.ID == 0 {
		log.Warn("sso: github user", "err", err)
		return ssoPerson{}, errSSOAnswer(p.Label)
	}
	if err := c.githubGet(ctx, tok, "/user/emails", &emails); err != nil {
		log.Warn("sso: github emails", "err", err)
		return ssoPerson{}, errSSOAnswer(p.Label)
	}
	who := ssoPerson{subject: strconv.FormatInt(user.ID, 10), name: cmp.Or(strings.TrimSpace(user.Name), user.Login)}
	for _, e := range emails {
		if e.Primary {
			who.email = strings.TrimSpace(e.Email)
			if !e.Verified {
				return who, errf(apitypes.CodeForbidden, "GitHub hasn't verified %s, your primary email; verify it at GitHub and try again", who.email)
			}
		}
	}
	if who.email == "" {
		return who, errf(apitypes.CodeForbidden, "GitHub sent no primary email address; ask an operator to check the %s provider asks for the user:email scope", p.Label)
	}
	return who, nil
}

// githubGet reads path of GitHub's API with the person's token into v.
func (c *Core) githubGet(ctx context.Context, tok *oauth2.Token, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.sso.github.API+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	resp, err := c.sso.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// ssoUser maps the person provider p vouched for to a Dock account:
//
//  1. by their identity (provider, subject), linked on an earlier sign-in;
//  2. else by their verified email to an existing account, linked now
//     (taken over first if its email was never proven: takeOver);
//  3. else a new account, in no org, when they have open invites (all
//     accepted now) or the platform isn't invite only.
//
// Otherwise errInviteOnly. Accounts are refused while disabled.
func (c *Core) ssoUser(ctx context.Context, p store.SsoProvider, who ssoPerson) (store.User, error) {
	var u store.User
	id, err := c.store.GetSSOIdentity(ctx, store.GetSSOIdentityParams{ProviderID: p.ID, Subject: who.subject})
	linked := err == nil
	switch {
	case linked:
		u, err = c.store.GetUser(ctx, id.UserID)
	case store.IsNotFound(err):
		u, err = c.store.GetUserByEmail(ctx, who.email)
		if store.IsNotFound(err) {
			return c.ssoNewUser(ctx, p, who)
		}
	}
	if err != nil {
		return store.User{}, err
	}
	if u.DisabledAt != nil {
		return store.User{}, errf(apitypes.CodeForbidden, "your Dock account is disabled; ask an operator")
	}
	if linked {
		return u, nil
	}
	if !u.EmailProven {
		return c.takeOver(ctx, p, who, u)
	}
	err = c.store.CreateSSOIdentity(ctx, store.CreateSSOIdentityParams{ProviderID: p.ID, Subject: who.subject, UserID: u.ID, Email: who.email, CreatedAt: now()})
	if err != nil && !store.IsUnique(err) { // a concurrent sign-in linked it
		return store.User{}, err
	}
	c.audit(ctx, userActor(u), "user.sso_link", nil, nil, u.Email, fmt.Sprintf("linked %s to their %s identity (%s)", u.Email, p.Label, p.Name))
	return u, nil
}

// takeOver gives an account whose email was never proven (an invite
// sign-up's, whose password whoever held the invite code chose) to the
// person p just vouched for as its email's owner, and links their
// identity. Whoever set it up loses every way in: sessions and tokens
// end, the password becomes an unknown random one and the second factors
// and recovery codes go. The account keeps its orgs and roles, which
// were the invite's for this email. Audited, with what was removed.
func (c *Core) takeOver(ctx context.Context, p store.SsoProvider, who ssoPerson, u store.User) (store.User, error) {
	hash, err := c.hashPassword(ctx, generatePassword())
	if err != nil {
		return store.User{}, err
	}
	var factors, logins, tokens int64
	took := false
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		// Proven once: a concurrent sign-in that got here first took it.
		n, err := q.ProveUserEmail(ctx, u.ID)
		if err != nil || n == 0 {
			return err
		}
		took = true
		if err := q.SetUserPassword(ctx, store.SetUserPasswordParams{PasswordHash: hash, ID: u.ID}); err != nil {
			return err
		}
		if factors, err = q.DeleteUserFactors(ctx, u.ID); err != nil {
			return err
		}
		if err := q.DeleteRecoveryCodes(ctx, u.ID); err != nil {
			return err
		}
		if err := q.DeleteUnusedPasswordResets(ctx, u.ID); err != nil {
			return err
		}
		if logins, err = endUserAccess(ctx, q, u.ID, 0); err != nil {
			return err
		}
		if tokens, err = q.RevokeUserTokens(ctx, store.RevokeUserTokensParams{Now: ptr(now()), UserID: &u.ID}); err != nil {
			return err
		}
		err = q.CreateSSOIdentity(ctx, store.CreateSSOIdentityParams{ProviderID: p.ID, Subject: who.subject, UserID: u.ID, Email: who.email, CreatedAt: now()})
		if store.IsUnique(err) {
			return nil // a concurrent sign-in linked it
		}
		return err
	})
	if err != nil {
		return store.User{}, err
	}
	if !took {
		return c.ssoUser(ctx, p, who)
	}
	u.EmailProven = true
	c.endOrphanedRequests(ctx)
	c.audit(ctx, userActor(u), "user.sso_takeover", nil, nil, u.Email, fmt.Sprintf(
		"gave %s to the owner of its email, who signed in through %s (%s): the account was made from an invite and its email never proven, so its sessions ended, its password was replaced, %d second factors and its recovery codes were removed and %d tokens revoked%s",
		u.Email, p.Label, p.Name, factors, tokens, revokedNote(logins)))
	return u, nil
}

// ssoNewUser creates the account of a person p vouched for, with an
// unusable random password (they sign in through a provider; an operator
// can give them a password), and accepts their open invites. On an
// invite-only platform, someone without an invite gets errInviteOnly.
func (c *Core) ssoNewUser(ctx context.Context, p store.SsoProvider, who ssoPerson) (store.User, error) {
	invites, err := c.store.ListOpenInvitesByEmail(ctx, store.ListOpenInvitesByEmailParams{Email: who.email, Now: now()})
	if err != nil {
		return store.User{}, err
	}
	if len(invites) == 0 {
		set, err := signInSettings(ctx, c.store.Queries)
		if err != nil {
			return store.User{}, err
		}
		if set.InviteOnly {
			return store.User{}, errInviteOnly
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(generatePassword()), c.passwordCost)
	if err != nil {
		return store.User{}, err
	}
	name := clean(who.name, maxDisplayName, strings.Split(who.email, "@")[0])
	var u store.User
	var joined []store.ListOpenInvitesByEmailRow
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		if u, err = q.CreateUser(ctx, store.CreateUserParams{Email: who.email, Name: name, PasswordHash: string(hash), EmailProven: true}); err != nil {
			return err
		}
		if err := q.CreateSSOIdentity(ctx, store.CreateSSOIdentityParams{ProviderID: p.ID, Subject: who.subject, UserID: u.ID, Email: who.email, CreatedAt: now()}); err != nil {
			return err
		}
		seen := map[int64]bool{}
		for _, r := range invites {
			if seen[r.Org.ID] { // two invites to one org: the first counts
				continue
			}
			seen[r.Org.ID] = true
			if err := joinTx(ctx, q, r.Org, &r.OrgInvite, u.ID, r.OrgInvite.Role); err != nil {
				return err
			}
			joined = append(joined, r)
		}
		return nil
	})
	if store.IsUnique(err) {
		return store.User{}, conflict("another sign-in created your account meanwhile; start again")
	}
	if err != nil {
		return store.User{}, err
	}
	c.audit(ctx, userActor(u), "user.create", nil, nil, u.Email, fmt.Sprintf("created user %s on their first sign-in with %s (%s)", u.Email, p.Label, p.Name))
	for _, r := range joined {
		in := userActor(u).member(r.Org, r.OrgInvite.Role, apitypes.ScopeAdmin)
		c.audit(ctx, in, "org.join", nil, nil, u.Email, fmt.Sprintf("%s joined as %s, accepting their invite on their first sign-in with %s", u.Email, r.OrgInvite.Role, p.Label))
	}
	return u, nil
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

// emailVerified reads the email_verified claim (or Microsoft's xms_edov):
// a boolean, or the string "true" some providers send.
func emailVerified(v any) bool {
	switch v := v.(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}
