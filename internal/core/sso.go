package core

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Single sign-on with OpenID Connect (ADR 0058), the org's side: its
// identity providers and the sso-only switch. Unlimited admins manage
// them after a recent step-up; client secrets are sealed and never
// returned. Signing in through a provider is sso_signin.go.

// SSOCallbackPath is where identity providers send the browser back, on
// Dock's URL; the API serves it.
const SSOCallbackPath = "/api/" + apitypes.APIVersion + "/auth/sso/callback"

// maxSSODomains bounds a provider's email domains.
const maxSSODomains = 50

// ssoRedirectURL is the redirect URI admins register at their provider.
func (c *Core) ssoRedirectURL() string { return c.dockURL() + SSOCallbackPath }

// ListSSOProviders returns the org's identity providers.
func (c *Core) ListSSOProviders(ctx context.Context, a Actor) ([]apitypes.SSOProvider, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "managing single sign-on"); err != nil {
		return nil, err
	}
	rows, err := c.store.ListOrgSSOProviders(ctx, a.OrgID)
	if err != nil {
		return nil, err
	}
	domains, err := c.store.ListOrgSSODomains(ctx, a.OrgID)
	if err != nil {
		return nil, err
	}
	byProvider := map[int64][]string{}
	for _, d := range domains {
		byProvider[d.ProviderID] = append(byProvider[d.ProviderID], d.Domain)
	}
	out := make([]apitypes.SSOProvider, len(rows))
	for i, p := range rows {
		out[i] = c.toSSOProvider(p, byProvider[p.ID])
	}
	return out, nil
}

// CreateSSOProvider adds an identity provider to the org. Its issuer must
// answer OpenID Connect discovery now, so a typo shows here rather than at
// someone's first sign-in.
func (c *Core) CreateSSOProvider(ctx context.Context, a Actor, req apitypes.CreateSSOProviderRequest) (apitypes.SSOProvider, error) {
	if err := c.requireSSOAdmin(a); err != nil {
		return apitypes.SSOProvider{}, err
	}
	if err := validName("provider", req.Name); err != nil {
		return apitypes.SSOProvider{}, err
	}
	issuer, err := validIssuer(req.Issuer)
	if err != nil {
		return apitypes.SSOProvider{}, err
	}
	domains, err := validDomains(req.AllowedDomains)
	if err != nil {
		return apitypes.SSOProvider{}, err
	}
	if err := validJITRole(req.JITRole); err != nil {
		return apitypes.SSOProvider{}, err
	}
	clientID, secret := strings.TrimSpace(req.ClientID), strings.TrimSpace(req.ClientSecret)
	if clientID == "" || secret == "" {
		return apitypes.SSOProvider{}, invalid("a provider needs the client ID and client secret of the app registered for Dock at it")
	}
	if _, err := c.sso.discover(ctx, c.log, issuer); err != nil {
		return apitypes.SSOProvider{}, err
	}
	sealed, err := c.box.Seal([]byte(secret), ssoSecretAAD(a.OrgID, req.Name))
	if err != nil {
		return apitypes.SSOProvider{}, err
	}
	var p store.SsoProvider
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		p, err = q.CreateSSOProvider(ctx, store.CreateSSOProviderParams{
			OrgID: a.OrgID, Name: req.Name, Issuer: issuer, ClientID: clientID, ClientSecret: sealed,
			JitRole: string(req.JITRole), CreatedAt: now(), CreatedBy: a.Name,
		})
		if err != nil {
			return err
		}
		return addSSODomains(ctx, q, p.ID, domains)
	})
	if store.IsUnique(err) {
		return apitypes.SSOProvider{}, conflict("organization %s has a provider named %s already; pick another name or change that one with `dock org sso set`", a.OrgSlug, req.Name)
	}
	if err != nil {
		return apitypes.SSOProvider{}, err
	}
	c.audit(ctx, a, "org.sso_provider_add", nil, nil, "sso "+p.Name,
		fmt.Sprintf("added single sign-on provider %s (%s) for %s; %s", p.Name, issuer, strings.Join(domains, ", "), jitNote(p.JitRole)))
	return c.toSSOProvider(p, domains), nil
}

// UpdateSSOProvider changes the org's provider name; fields left out of
// req stay.
func (c *Core) UpdateSSOProvider(ctx context.Context, a Actor, name string, req apitypes.UpdateSSOProviderRequest) (apitypes.SSOProvider, error) {
	if err := c.requireSSOAdmin(a); err != nil {
		return apitypes.SSOProvider{}, err
	}
	p, err := c.orgSSOProvider(ctx, a, name)
	if err != nil {
		return apitypes.SSOProvider{}, err
	}
	var changed []string
	params := store.UpdateSSOProviderParams{Issuer: p.Issuer, ClientID: p.ClientID, ClientSecret: p.ClientSecret, JitRole: p.JitRole, ID: p.ID}
	if req.Issuer != nil {
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
		if params.ClientSecret, err = c.box.Seal([]byte(secret), ssoSecretAAD(p.OrgID, p.Name)); err != nil {
			return apitypes.SSOProvider{}, err
		}
		changed = append(changed, "client secret")
	}
	if req.JITRole != nil {
		if err := validJITRole(*req.JITRole); err != nil {
			return apitypes.SSOProvider{}, err
		}
		params.JitRole = string(*req.JITRole)
		changed = append(changed, jitNote(params.JitRole))
	}
	var domains []string
	if req.AllowedDomains != nil {
		if domains, err = validDomains(req.AllowedDomains); err != nil {
			return apitypes.SSOProvider{}, err
		}
		changed = append(changed, "domains "+strings.Join(domains, ", "))
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		if p, err = q.UpdateSSOProvider(ctx, params); err != nil || domains == nil {
			return err
		}
		if err := q.DeleteSSOProviderDomains(ctx, p.ID); err != nil {
			return err
		}
		return addSSODomains(ctx, q, p.ID, domains)
	})
	if err != nil {
		return apitypes.SSOProvider{}, err
	}
	if domains == nil {
		if domains, err = c.store.ListSSOProviderDomains(ctx, p.ID); err != nil {
			return apitypes.SSOProvider{}, err
		}
	}
	if len(changed) > 0 {
		c.audit(ctx, a, "org.sso_provider_update", nil, nil, "sso "+p.Name, fmt.Sprintf("changed single sign-on provider %s: %s", p.Name, strings.Join(changed, "; ")))
	}
	return c.toSSOProvider(p, domains), nil
}

// DeleteSSOProvider removes the org's provider name and the identities
// linked through it. People keep their accounts and memberships. The
// last provider of an sso-only org stays until sso-only is off, so nobody
// is locked out.
func (c *Core) DeleteSSOProvider(ctx context.Context, a Actor, name string) error {
	if err := c.requireSSOAdmin(a); err != nil {
		return err
	}
	p, err := c.orgSSOProvider(ctx, a, name)
	if err != nil {
		return err
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		org, err := q.GetOrg(ctx, a.OrgID)
		if err != nil {
			return err
		}
		n, err := q.CountOrgSSOProviders(ctx, a.OrgID)
		if err != nil {
			return err
		}
		if org.SsoOnly && n <= 1 {
			return conflict("organization %s signs in only through single sign-on, and %s is its last provider; turn that off first with `dock org sso-only off`", org.Slug, p.Name)
		}
		return q.DeleteSSOProvider(ctx, p.ID)
	})
	if err != nil {
		return err
	}
	c.audit(ctx, a, "org.sso_provider_remove", nil, nil, "sso "+p.Name, fmt.Sprintf("removed single sign-on provider %s (%s) and the identities linked through it", p.Name, p.Issuer))
	return nil
}

// SetOrgSSOOnly turns on or off the org's requirement that members sign
// in through its single sign-on. Turning it on needs a provider, and the
// caller's own sign-in to have gone through one of the org's, so they
// don't lock themselves out.
func (c *Core) SetOrgSSOOnly(ctx context.Context, a Actor, req apitypes.SetSSOOnlyRequest) (apitypes.Org, error) {
	if err := c.requireSSOAdmin(a); err != nil {
		return apitypes.Org{}, err
	}
	if req.SSOOnly {
		n, err := c.store.CountOrgSSOProviders(ctx, a.OrgID)
		if err != nil {
			return apitypes.Org{}, err
		}
		if n == 0 {
			return apitypes.Org{}, invalid("add a single sign-on provider first: `dock org sso add`")
		}
		if a.UserID != 0 && a.SSOOrgID != a.OrgID {
			return apitypes.Org{}, invalid("sign in through %s's single sign-on yourself first, so you know it works before it's the only way in", a.OrgSlug)
		}
	}
	o, err := c.store.SetOrgSSOOnly(ctx, store.SetOrgSSOOnlyParams{SsoOnly: req.SSOOnly, ID: a.OrgID})
	if err != nil {
		return apitypes.Org{}, err
	}
	summary := "members sign in with a password or single sign-on again"
	if req.SSOOnly {
		summary = "members act in the organization only after signing in through its single sign-on"
	}
	c.audit(ctx, a, "org.sso_only", nil, nil, o.Slug, summary)
	return toOrg(o, a.Scope), nil
}

// ResetOrgSSOOnly turns the org with slug's sso-only requirement off, for
// when its identity provider is down: `dock admin sso-only <org> off` on
// the server. Operators only.
func (c *Core) ResetOrgSSOOnly(ctx context.Context, a Actor, slug string) error {
	if err := a.requireOperator("turning off an organization's single sign-on requirement"); err != nil {
		return err
	}
	o, err := c.store.SetOrgSSOOnlyBySlug(ctx, store.SetOrgSSOOnlyBySlugParams{SsoOnly: false, Slug: slug})
	if store.IsNotFound(err) {
		return notFound("organization %q not found", slug)
	}
	if err != nil {
		return err
	}
	in := a
	in.OrgID, in.OrgSlug = o.ID, o.Slug
	c.audit(ctx, in, "org.sso_only", nil, nil, o.Slug, "members sign in with a password or single sign-on again (turned off on the server)")
	return nil
}

// requireSSOAdmin checks the actor may change the org's single sign-on:
// an unlimited admin, recently confirmed.
func (c *Core) requireSSOAdmin(a Actor) error {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "managing single sign-on"); err != nil {
		return err
	}
	return a.requireRecent("changing the organization's single sign-on")
}

// orgSSOProvider loads the actor's org's provider name.
func (c *Core) orgSSOProvider(ctx context.Context, a Actor, name string) (store.SsoProvider, error) {
	p, err := c.store.GetOrgSSOProvider(ctx, store.GetOrgSSOProviderParams{OrgID: a.OrgID, Name: name})
	if store.IsNotFound(err) {
		return p, notFound("single sign-on provider %q not found; see `dock org sso list`", name)
	}
	return p, err
}

// addSSODomains records the email domains of provider id.
func addSSODomains(ctx context.Context, q *store.Queries, id int64, domains []string) error {
	for _, d := range domains {
		if err := q.AddSSOProviderDomain(ctx, store.AddSSOProviderDomainParams{Domain: d, ProviderID: id}); err != nil {
			return err
		}
	}
	return nil
}

func (c *Core) toSSOProvider(p store.SsoProvider, domains []string) apitypes.SSOProvider {
	if domains == nil {
		domains = []string{}
	}
	return apitypes.SSOProvider{
		Name: p.Name, Issuer: p.Issuer, ClientID: p.ClientID, AllowedDomains: domains, JITRole: apitypes.Scope(p.JitRole),
		RedirectURL: c.ssoRedirectURL(), CreatedAt: p.CreatedAt, CreatedBy: p.CreatedBy,
	}
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

// validDomains normalizes a provider's email domains: lowercase, sorted,
// each once, at least one.
func validDomains(in []string) ([]string, error) {
	var out []string
	for _, d := range in {
		d = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(d), "@")))
		if d == "" {
			continue
		}
		if _, err := normalizeHostname(d); err != nil || !strings.Contains(d, ".") {
			return nil, invalid("%q is not an email domain like example.com", d)
		}
		if !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return nil, invalid("a provider needs at least one email domain it signs people in for, like example.com")
	}
	if len(out) > maxSSODomains {
		return nil, invalid("a provider takes at most %d email domains", maxSSODomains)
	}
	slices.Sort(out)
	return out, nil
}

// validJITRole checks a provider's just-in-time role: none, read or
// deploy. Admins are made by admins, never by a provider.
func validJITRole(r apitypes.Scope) error {
	switch r {
	case "", apitypes.ScopeRead, apitypes.ScopeDeploy:
		return nil
	}
	return invalid("the just-in-time role must be read or deploy, or empty for invite only; admins are made by an admin")
}

// jitNote says in words who may join through a provider.
func jitNote(role string) string {
	if role == "" {
		return "new members need an invite"
	}
	return "anyone with an allowed email domain joins as " + role
}
