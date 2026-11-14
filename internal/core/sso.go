package core

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Single sign-on, the platform's side (ADR 0058, 0065): operators manage
// the identity providers and the sign-in switches (passwords, invite
// only) after a recent step-up; client secrets are sealed and never
// returned. Signing in through a provider is sso_signin.go.

// SSOCallbackPath is where identity providers send the browser back, on
// Dock's URL; the API serves it. One for every provider: the flow in
// flight names its provider.
const SSOCallbackPath = "/api/" + apitypes.APIVersion + "/auth/sso/callback"

// googleIssuer is the issuer of the google kind.
const googleIssuer = "https://accounts.google.com"

// maxSSOLabel bounds a provider's label, in characters.
const maxSSOLabel = 40

// ssoRedirectURL is the redirect URI operators register at each provider.
func (c *Core) ssoRedirectURL() string { return c.dockURL() + SSOCallbackPath }

// ListSSOProviders returns the platform's identity providers and the
// redirect URL to register at them.
func (c *Core) ListSSOProviders(ctx context.Context, a Actor) (apitypes.SSOProviders, error) {
	if err := a.requirePlatform(PlatformSettings, "managing single sign-on"); err != nil {
		return apitypes.SSOProviders{}, err
	}
	rows, err := c.store.ListSSOProvidersLinked(ctx)
	if err != nil {
		return apitypes.SSOProviders{}, err
	}
	out := apitypes.SSOProviders{Providers: make([]apitypes.SSOProvider, len(rows)), RedirectURL: c.ssoRedirectURL()}
	for i, r := range rows {
		out.Providers[i] = toSSOProvider(r.SsoProvider, r.Linked)
	}
	return out, nil
}

// CreateSSOProvider adds an identity provider. An OpenID Connect issuer
// (google's too) must answer discovery now, so a typo shows here rather
// than at someone's first sign-in.
func (c *Core) CreateSSOProvider(ctx context.Context, a Actor, req apitypes.CreateSSOProviderRequest) (apitypes.SSOProvider, error) {
	if err := requireSSOOperator(a); err != nil {
		return apitypes.SSOProvider{}, err
	}
	if err := validName("provider", req.Name); err != nil {
		return apitypes.SSOProvider{}, err
	}
	var issuer, label string
	switch req.Kind {
	case apitypes.SSOGoogle:
		issuer, label = googleIssuer, "Google"
	case apitypes.SSOGitHub:
		label = "GitHub"
	case apitypes.SSOOIDC:
		var err error
		if issuer, err = validIssuer(req.Issuer); err != nil {
			return apitypes.SSOProvider{}, err
		}
	default:
		return apitypes.SSOProvider{}, invalid("kind must be google, github or oidc (any other OpenID Connect provider, by its issuer)")
	}
	if req.Issuer != "" && req.Kind != apitypes.SSOOIDC {
		return apitypes.SSOProvider{}, invalid("only an oidc provider takes an issuer; %s's is built in", label)
	}
	if req.Label != "" || req.Kind == apitypes.SSOOIDC {
		var err error
		if label, err = validLabel(req.Label); err != nil {
			return apitypes.SSOProvider{}, err
		}
	}
	clientID, secret := strings.TrimSpace(req.ClientID), strings.TrimSpace(req.ClientSecret)
	if clientID == "" || secret == "" {
		return apitypes.SSOProvider{}, invalid("a provider needs the client ID and client secret of the app registered for Dock at it")
	}
	if issuer != "" {
		if _, err := c.sso.discover(ctx, c.log, issuer); err != nil {
			return apitypes.SSOProvider{}, err
		}
	}
	sealed, err := c.box.Seal([]byte(secret), ssoSecretAAD(req.Name))
	if err != nil {
		return apitypes.SSOProvider{}, err
	}
	p, err := c.store.CreateSSOProvider(ctx, store.CreateSSOProviderParams{
		Name: req.Name, Kind: string(req.Kind), Label: label, Issuer: issuer, ClientID: clientID, ClientSecret: sealed,
		CreatedAt: now(), CreatedBy: a.Name,
	})
	if store.IsUnique(err) {
		return apitypes.SSOProvider{}, conflict("there is a provider named %s already; pick another name or change that one with `dock sso set`", req.Name)
	}
	if err != nil {
		return apitypes.SSOProvider{}, err
	}
	c.audit(ctx, a.platform(), "platform.sso_provider_add", nil, nil, "sso "+p.Name,
		fmt.Sprintf("added identity provider %s (%s%s): Continue with %s", p.Name, p.Kind, issuerNote(p.Issuer), p.Label))
	return toSSOProvider(p, 0), nil
}

// UpdateSSOProvider changes provider name; fields left out of req stay.
func (c *Core) UpdateSSOProvider(ctx context.Context, a Actor, name string, req apitypes.UpdateSSOProviderRequest) (apitypes.SSOProvider, error) {
	if err := requireSSOOperator(a); err != nil {
		return apitypes.SSOProvider{}, err
	}
	p, err := c.ssoProviderNamed(ctx, name)
	if err != nil {
		return apitypes.SSOProvider{}, err
	}
	var changed []string
	params := store.UpdateSSOProviderParams{Label: p.Label, Issuer: p.Issuer, ClientID: p.ClientID, ClientSecret: p.ClientSecret, ID: p.ID}
	if req.Label != nil {
		if params.Label, err = validLabel(*req.Label); err != nil {
			return apitypes.SSOProvider{}, err
		}
		changed = append(changed, "label "+params.Label)
	}
	if req.Issuer != nil {
		if p.Kind != string(apitypes.SSOOIDC) {
			return apitypes.SSOProvider{}, invalid("only an oidc provider takes an issuer; %s's is built in", p.Label)
		}
		if params.Issuer, err = validIssuer(*req.Issuer); err != nil {
			return apitypes.SSOProvider{}, err
		}
		if params.Issuer != p.Issuer {
			if _, err := c.sso.discover(ctx, c.log, params.Issuer); err != nil {
				return apitypes.SSOProvider{}, err
			}
			changed = append(changed, "issuer "+params.Issuer)
		}
	}
	if req.ClientID != nil {
		if params.ClientID = strings.TrimSpace(*req.ClientID); params.ClientID == "" {
			return apitypes.SSOProvider{}, invalid("the client ID can't be empty")
		}
		changed = append(changed, "client ID")
	}
	if req.ClientSecret != nil {
		secret := strings.TrimSpace(*req.ClientSecret)
		if secret == "" {
			return apitypes.SSOProvider{}, invalid("the client secret can't be empty")
		}
		if params.ClientSecret, err = c.box.Seal([]byte(secret), ssoSecretAAD(p.Name)); err != nil {
			return apitypes.SSOProvider{}, err
		}
		changed = append(changed, "client secret")
	}
	if p, err = c.store.UpdateSSOProvider(ctx, params); err != nil {
		return apitypes.SSOProvider{}, err
	}
	if len(changed) > 0 {
		c.audit(ctx, a.platform(), "platform.sso_provider_update", nil, nil, "sso "+p.Name, fmt.Sprintf("changed identity provider %s: %s", p.Name, strings.Join(changed, "; ")))
	}
	return toSSOProvider(p, 0), nil
}

// DeleteSSOProvider removes provider name, the identities linked through
// it and the sessions that signed in through it. People keep their
// accounts and memberships. The last provider stays while password
// sign-in is off, so nobody is locked out.
func (c *Core) DeleteSSOProvider(ctx context.Context, a Actor, name string) error {
	if err := requireSSOOperator(a); err != nil {
		return err
	}
	p, err := c.ssoProviderNamed(ctx, name)
	if err != nil {
		return err
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		set, err := signInSettings(ctx, q)
		if err != nil {
			return err
		}
		n, err := q.CountSSOProviders(ctx)
		if err != nil {
			return err
		}
		if !set.PasswordSignIn && n <= 1 {
			return conflict("password sign-in is off and %s is the last identity provider; turn passwords on first with `dock sso password-sign-in on`", p.Name)
		}
		return q.DeleteSSOProvider(ctx, p.ID)
	})
	if err != nil {
		return err
	}
	c.audit(ctx, a.platform(), "platform.sso_provider_remove", nil, nil, "sso "+p.Name,
		fmt.Sprintf("removed identity provider %s (%s%s), the identities linked through it and its sessions", p.Name, p.Kind, issuerNote(p.Issuer)))
	return nil
}

// SignInSettings returns the platform's sign-in switches.
func (c *Core) SignInSettings(ctx context.Context, a Actor) (apitypes.SignInSettings, error) {
	if err := a.requirePlatform(PlatformSettings, "managing sign-in"); err != nil {
		return apitypes.SignInSettings{}, err
	}
	return signInSettings(ctx, c.store.Queries)
}

// SetSignInSettings changes the platform's sign-in switches. Turning
// password sign-in off needs a provider, and a caller who signed in
// through one, so they know it works before it's the only way in.
func (c *Core) SetSignInSettings(ctx context.Context, a Actor, req apitypes.SignInSettingsRequest) (apitypes.SignInSettings, error) {
	if err := requireSSOOperator(a); err != nil {
		return apitypes.SignInSettings{}, err
	}
	var before, set apitypes.SignInSettings
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		if before, err = signInSettings(ctx, q); err != nil {
			return err
		}
		set = before
		if req.InviteOnly != nil {
			set.InviteOnly = *req.InviteOnly
		}
		if req.PasswordSignIn != nil {
			set.PasswordSignIn = *req.PasswordSignIn
		}
		if before.PasswordSignIn && !set.PasswordSignIn {
			n, err := q.CountSSOProviders(ctx)
			if err != nil {
				return err
			}
			if n == 0 {
				return invalid("add an identity provider first: `dock sso add`")
			}
			if a.UserID != 0 && a.SSOProviderID == 0 {
				return invalid("sign in through an identity provider yourself first, so you know it works before it's the only way in")
			}
		}
		return q.SetSignInSettings(ctx, store.SetSignInSettingsParams{PasswordSignIn: set.PasswordSignIn, InviteOnly: set.InviteOnly, UpdatedBy: a.Name, UpdatedAt: now()})
	})
	if err != nil {
		return apitypes.SignInSettings{}, err
	}
	if set.PasswordSignIn != before.PasswordSignIn {
		summary := "turned password sign-in off: people sign in through an identity provider or a passkey"
		if set.PasswordSignIn {
			summary = "turned password sign-in on"
		}
		c.audit(ctx, a.platform(), "platform.password_sign_in", nil, nil, "password sign-in", summary)
	}
	if set.InviteOnly != before.InviteOnly {
		summary := "made sign-up invite only: an identity provider creates accounts only for emails with an open invite"
		if !set.InviteOnly {
			summary = "opened sign-up: an identity provider creates an account for anyone it signs in, in no organization"
		}
		c.audit(ctx, a.platform(), "platform.invite_only", nil, nil, "sign-up", summary)
	}
	return set, nil
}

// ResetPasswordSignIn turns password sign-in back on, for when every
// identity provider is down or misconfigured: `dock admin password-sign-in
// on` on the server. With an email, that account also gets a new
// generated password, returned once (someone who only ever signed in
// through a provider has none they know). Operators only.
func (c *Core) ResetPasswordSignIn(ctx context.Context, a Actor, email string) (password string, err error) {
	if err := a.requirePlatform(PlatformSettings, "turning password sign-in on"); err != nil {
		return "", err
	}
	var u store.User
	if email != "" {
		if u, err = c.store.GetUserByEmail(ctx, email); store.IsNotFound(err) {
			return "", notFound("no account with email %s", email)
		} else if err != nil {
			return "", err
		}
	}
	set, err := signInSettings(ctx, c.store.Queries)
	if err != nil {
		return "", err
	}
	if err := c.store.SetSignInSettings(ctx, store.SetSignInSettingsParams{PasswordSignIn: true, InviteOnly: set.InviteOnly, UpdatedBy: a.Name, UpdatedAt: now()}); err != nil {
		return "", err
	}
	c.audit(ctx, a.platform(), "platform.password_sign_in", nil, nil, "password sign-in", "turned password sign-in on (on the server)")
	if email == "" {
		return "", nil
	}
	res, err := c.ResetPassword(ctx, a, u.ID, apitypes.ResetPasswordRequest{})
	return res.Password, err
}

// SignInMethods is what the sign-in page offers anyone: a button per
// provider and whether passwords sign in.
func (c *Core) SignInMethods(ctx context.Context) (apitypes.SignInMethods, error) {
	set, err := signInSettings(ctx, c.store.Queries)
	if err != nil {
		return apitypes.SignInMethods{}, err
	}
	rows, err := c.store.ListSSOProviders(ctx)
	if err != nil {
		return apitypes.SignInMethods{}, err
	}
	out := apitypes.SignInMethods{Password: set.PasswordSignIn, Providers: make([]apitypes.SignInButton, len(rows))}
	if out.Password {
		if out.PasswordReset, err = c.canEmailLinks(ctx); err != nil {
			return apitypes.SignInMethods{}, err
		}
	}
	for i, p := range rows {
		out.Providers[i] = apitypes.SignInButton{Name: p.Name, Kind: apitypes.SSOKind(p.Kind), Label: p.Label}
	}
	return out, nil
}

// signInSettings reads the platform's sign-in switches: both on until an
// operator first sets them.
func signInSettings(ctx context.Context, q *store.Queries) (apitypes.SignInSettings, error) {
	row, err := q.GetSignInSettings(ctx)
	if store.IsNotFound(err) {
		return apitypes.SignInSettings{PasswordSignIn: true, InviteOnly: true}, nil
	}
	return apitypes.SignInSettings{PasswordSignIn: row.PasswordSignIn, InviteOnly: row.InviteOnly}, err
}

// requireSSOOperator checks the actor may change the platform's sign-in:
// an operator, recently confirmed.
func requireSSOOperator(a Actor) error {
	if err := a.requirePlatform(PlatformSettings, "managing single sign-on"); err != nil {
		return err
	}
	return a.requireRecent("changing how people sign in")
}

// ssoProviderNamed loads provider name.
func (c *Core) ssoProviderNamed(ctx context.Context, name string) (store.SsoProvider, error) {
	p, err := c.store.GetSSOProviderByName(ctx, name)
	if store.IsNotFound(err) {
		return p, notFound("identity provider %q not found; see `dock sso list`", name)
	}
	return p, err
}

func toSSOProvider(p store.SsoProvider, linked int64) apitypes.SSOProvider {
	return apitypes.SSOProvider{
		Name: p.Name, Kind: apitypes.SSOKind(p.Kind), Label: p.Label, Issuer: p.Issuer, ClientID: p.ClientID,
		Linked: linked, CreatedAt: p.CreatedAt, CreatedBy: p.CreatedBy,
	}
}

// issuerNote is ", <issuer>" for the audit log; "" for none.
func issuerNote(issuer string) string {
	if issuer == "" {
		return ""
	}
	return ", " + issuer
}

// validLabel checks the name a provider's button shows.
func validLabel(s string) (string, error) {
	s = strings.TrimSpace(s)
	if n := utf8.RuneCountInString(s); n == 0 || n > maxSSOLabel {
		return "", invalid("a provider needs a label of 1 to %d characters, the button's \"Continue with <label>\"", maxSSOLabel)
	}
	return clean(s, maxSSOLabel, s), nil
}

// validIssuer checks an issuer URL: https, or http on this machine for
// trying a provider out. OpenID Connect compares issuers exactly, so it
// is kept as given, without a trailing slash added or removed.
func validIssuer(s string) (string, error) {
	s = strings.TrimSpace(s)
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", invalid("issuer %q is not a URL like https://accounts.example.com", s)
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && loopbackHost(u.Hostname()):
	default:
		return "", invalid("issuer %q must use https (http only on localhost, for trying a provider out)", s)
	}
	return s, nil
}

// loopbackHost reports whether host is this machine.
func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
