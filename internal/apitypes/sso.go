package apitypes

import "time"

// Single sign-on (ADR 0058, 0065): the platform's identity providers, its
// sign-in switches, and signing in through a provider.

// SSOKind is what a provider is. github is GitHub's OAuth, reading the
// account's primary verified email from its API; every other kind speaks
// OpenID Connect. oidc takes any issuer; the named ones (ADR 0081) know
// theirs: google's is fixed, microsoft's comes from a tenant, keycloak's
// and authentik's from a domain and a realm, the rest from a domain.
type SSOKind string

const (
	SSOGoogle    SSOKind = "google"
	SSOGitHub    SSOKind = "github"
	SSOOIDC      SSOKind = "oidc"
	SSOMicrosoft SSOKind = "microsoft"
	SSOGitLab    SSOKind = "gitlab"
	SSOOkta      SSOKind = "okta"
	SSOAuth0     SSOKind = "auth0"
	SSOKeycloak  SSOKind = "keycloak"
	SSOAuthentik SSOKind = "authentik"
)

// SSOKindNames names every kind: the default label of its button, except
// oidc's, whose providers name their own.
var SSOKindNames = map[SSOKind]string{
	SSOGoogle: "Google", SSOGitHub: "GitHub", SSOOIDC: "OpenID Connect",
	SSOMicrosoft: "Microsoft", SSOGitLab: "GitLab", SSOOkta: "Okta",
	SSOAuth0: "Auth0", SSOKeycloak: "Keycloak", SSOAuthentik: "Authentik",
}

// SSOWhere is where a named provider is, instead of an issuer: which
// fields a kind takes is in the doc of each.
type SSOWhere struct {
	// Tenant is microsoft's directory: its ID or one of its domains. Not
	// common, organizations or consumers: Dock signs in one tenant's
	// people (ADR 0081).
	Tenant string `json:"tenant,omitempty" example:"contoso.onmicrosoft.com" doc:"microsoft only: the tenant ID or a domain of it"`
	// Domain is a hosted provider's domain (okta, auth0) or where a
	// self-hosted one runs (keycloak, authentik, gitlab; gitlab.com when
	// left out), with or without https://.
	Domain string `json:"domain,omitempty" example:"acme.okta.com" doc:"okta, auth0: the domain; keycloak, authentik, gitlab: the base URL (gitlab.com by default)"`
	// Realm is keycloak's realm or authentik's application slug.
	Realm string `json:"realm,omitempty" example:"staff" doc:"keycloak: the realm; authentik: the application's slug"`
}

// SSOProvider is one of the platform's identity providers. Its client
// secret is never returned.
type SSOProvider struct {
	// Name addresses the provider in the API and CLI.
	Name string  `json:"name" example:"google"`
	Kind SSOKind `json:"kind" enum:"google,github,oidc,microsoft,gitlab,okta,auth0,keycloak,authentik"`
	// Label is what the sign-in page's button names: "Continue with <label>".
	Label string `json:"label" example:"Google"`
	// Issuer is the OpenID Connect issuer; empty for github.
	Issuer string `json:"issuer,omitempty" example:"https://accounts.google.com"`
	// SSOWhere is what a named kind's issuer was built from.
	SSOWhere
	ClientID string `json:"client_id"`
	// Linked is how many people are linked through the provider.
	Linked    int64     `json:"linked"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by"`
}

// SSOProviders is the platform's providers, with the one redirect URL to
// register at each of them.
type SSOProviders struct {
	Providers   []SSOProvider `json:"providers"`
	RedirectURL string        `json:"redirect_url" example:"https://dock.example.com/api/v1/auth/sso/callback"`
}

// CreateSSOProviderRequest adds an identity provider to the platform.
type CreateSSOProviderRequest struct {
	Name string  `json:"name" minLength:"1" maxLength:"32" doc:"Lowercase letters, digits and dashes"`
	Kind SSOKind `json:"kind" enum:"google,github,oidc,microsoft,gitlab,okta,auth0,keycloak,authentik"`
	// Label defaults to the kind's name (Google, Microsoft, ...); a
	// custom provider needs one.
	Label  string `json:"label,omitempty" maxLength:"40"`
	Issuer string `json:"issuer,omitempty" doc:"oidc only: the issuer URL; https, or http on localhost"`
	SSOWhere
	// ClientID and ClientSecret are the app registered for Dock at the
	// provider; the secret is write-only.
	ClientID     string `json:"client_id" minLength:"1"`
	ClientSecret string `json:"client_secret" minLength:"1"`
}

// UpdateSSOProviderRequest changes a provider; fields left out stay.
type UpdateSSOProviderRequest struct {
	Label        *string `json:"label,omitempty" maxLength:"40"`
	Issuer       *string `json:"issuer,omitempty" doc:"oidc only"`
	Tenant       *string `json:"tenant,omitempty" doc:"microsoft only"`
	Domain       *string `json:"domain,omitempty" doc:"okta, auth0, keycloak, authentik, gitlab"`
	Realm        *string `json:"realm,omitempty" doc:"keycloak, authentik"`
	ClientID     *string `json:"client_id,omitempty"`
	ClientSecret *string `json:"client_secret,omitempty"`
}

// SignInSettings is the platform's sign-in switches.
type SignInSettings struct {
	// PasswordSignIn: the sign-in page takes email and password. Off
	// only while a provider exists; `dock admin password-sign-in on`
	// turns it back on from the server.
	PasswordSignIn bool `json:"password_sign_in"`
	// InviteOnly: a provider creates an account only for an email with an
	// open invite; off, for anyone it signs in.
	InviteOnly bool `json:"invite_only"`
}

// SignInSettingsRequest changes the platform's sign-in switches; fields
// left out stay.
type SignInSettingsRequest struct {
	PasswordSignIn *bool `json:"password_sign_in,omitempty"`
	InviteOnly     *bool `json:"invite_only,omitempty"`
}

// SignInMethods is what the sign-in page offers, for anyone: a button per
// provider, and whether it takes passwords.
type SignInMethods struct {
	Password bool `json:"password"`
	// PasswordReset: the sign-in page offers "Forgot password?", which
	// emails a reset link (ADR 0079): passwords sign in, email is set up
	// and Dock has a public address.
	PasswordReset bool           `json:"password_reset"`
	Providers     []SignInButton `json:"providers"`
}

// SignInButton is a provider on the sign-in page.
type SignInButton struct {
	Name  string  `json:"name"`
	Kind  SSOKind `json:"kind" enum:"google,github,oidc,microsoft,gitlab,okta,auth0,keycloak,authentik"`
	Label string  `json:"label"`
}

// SSOStartRequest starts signing in through the provider named.
type SSOStartRequest struct {
	Provider string `json:"provider" minLength:"1" maxLength:"32"`
	// To is the path to land on after signing in; "/" by default.
	To string `json:"to,omitempty" maxLength:"512"`
	// Confirm is a step-up ("Confirm it's you"): the provider is asked to
	// have the person sign in again rather than reuse its own session.
	Confirm bool `json:"confirm,omitempty"`
}

// SSOStart is the identity provider's page to send the browser to.
type SSOStart struct {
	URL string `json:"url"`
}
