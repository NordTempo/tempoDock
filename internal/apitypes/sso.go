package apitypes

import "time"

// Single sign-on with OpenID Connect (ADR 0058): an org's identity
// providers, the sso-only switch, and signing in through them.

// SSOProvider is an org's OpenID Connect provider. Its client secret is
// never returned.
type SSOProvider struct {
	// Name addresses the provider within the org.
	Name     string `json:"name" example:"google"`
	Issuer   string `json:"issuer" example:"https://accounts.google.com"`
	ClientID string `json:"client_id"`
	// AllowedDomains are the email domains it signs people in for.
	AllowedDomains []string `json:"allowed_domains"`
	// JITRole is the role a person gets on their first sign-in when the
	// org has no invite for them; empty: they need an invite.
	JITRole Scope `json:"jit_role,omitempty" enum:"read,deploy"`
	// RedirectURL is the address to register at the provider.
	RedirectURL string    `json:"redirect_url"`
	CreatedAt   time.Time `json:"created_at"`
	CreatedBy   string    `json:"created_by"`
}

// CreateSSOProviderRequest adds an OpenID Connect provider to the org.
type CreateSSOProviderRequest struct {
	Name     string `json:"name" minLength:"1" maxLength:"32" doc:"Lowercase letters, digits and dashes"`
	Issuer   string `json:"issuer" minLength:"1" doc:"The provider's issuer URL; https, or http on localhost"`
	ClientID string `json:"client_id" minLength:"1"`
	// ClientSecret is write-only.
	ClientSecret   string   `json:"client_secret" minLength:"1"`
	AllowedDomains []string `json:"allowed_domains" minItems:"1" doc:"Email domains it signs people in for"`
	// JITRole: "" (the default) needs an invite for every new member;
	// read or deploy lets anyone with an allowed email domain join with it.
	JITRole Scope `json:"jit_role,omitempty"`
}

// UpdateSSOProviderRequest changes a provider; fields left out stay.
type UpdateSSOProviderRequest struct {
	Issuer         *string  `json:"issuer,omitempty"`
	ClientID       *string  `json:"client_id,omitempty"`
	ClientSecret   *string  `json:"client_secret,omitempty"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	JITRole        *Scope   `json:"jit_role,omitempty" doc:"\"\" turns just-in-time membership off"`
}

// SetSSOOnlyRequest turns an org's requirement of signing in through its
// single sign-on on or off.
type SetSSOOnlyRequest struct {
	SSOOnly bool `json:"sso_only"`
}

// SSOStartRequest starts signing in through single sign-on: with an email
// address, whose domain finds the provider, or an explicit org and
// provider (when several orgs sign that domain in).
type SSOStartRequest struct {
	Email    string `json:"email,omitempty" maxLength:"254"`
	Org      string `json:"org,omitempty" maxLength:"32"`
	Provider string `json:"provider,omitempty" maxLength:"32"`
	// To is the path to land on after signing in; "/" by default.
	To string `json:"to,omitempty" maxLength:"512"`
}

// SSOStart is where signing in goes next: the identity provider's page
// (URL), or, when several orgs sign the email's domain in, a choice of
// them to start again with.
type SSOStart struct {
	URL     string      `json:"url,omitempty"`
	Choices []SSOChoice `json:"choices,omitempty"`
}

// SSOChoice is an org and provider a person can sign in through.
type SSOChoice struct {
	Org      string `json:"org"`
	OrgName  string `json:"org_name"`
	Provider string `json:"provider"`
}
