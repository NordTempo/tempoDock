package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Named identity providers (ADR 0081): OpenID Connect providers Dock knows
// by kind, so an operator gives a tenant, a domain or a realm instead of
// an issuer, and the button gets the kind's label and mark. The issuer is
// built here and stored as any other; ssoWhere reads the fields back from
// it, so nothing but the issuer is kept.

// microsoftDotCom is where Microsoft Entra ID's tenants are.
const microsoftDotCom = "https://login.microsoftonline.com"

// gitlabDotCom is the gitlab kind's issuer without a domain.
const gitlabDotCom = "https://gitlab.com"

// microsoftConsumers is the tenant of personal Microsoft accounts.
const microsoftConsumers = "9188040d-6c67-4c5b-b112-36a304b66dad"

// namedKind reports whether kind builds its issuer from an SSOWhere.
func namedKind(kind apitypes.SSOKind) bool {
	_, ok := apitypes.SSOKindNames[kind]
	return ok && kind != apitypes.SSOGitHub && kind != apitypes.SSOOIDC && kind != apitypes.SSOGoogle
}

// presetIssuer builds a named kind's issuer from w, checking w has what
// the kind takes and nothing else. A microsoft tenant given by a domain
// still needs resolveIssuer.
func (s ssoState) presetIssuer(kind apitypes.SSOKind, w apitypes.SSOWhere) (string, error) {
	label := apitypes.SSOKindNames[kind]
	takes := func(tenant, domain, realm bool) error {
		if !tenant && w.Tenant != "" || !domain && w.Domain != "" || !realm && w.Realm != "" {
			return invalid("%s takes %s", label, whereWords(kind))
		}
		return nil
	}
	switch kind {
	case apitypes.SSOMicrosoft:
		if err := takes(true, false, false); err != nil {
			return "", err
		}
		tenant, err := validTenant(w.Tenant)
		if err != nil {
			return "", err
		}
		return s.microsoft + "/" + tenant + "/v2.0", nil
	case apitypes.SSOGitLab:
		if err := takes(false, true, false); err != nil {
			return "", err
		}
		if strings.TrimSpace(w.Domain) == "" {
			return gitlabDotCom, nil
		}
		return validBaseURL(label, w.Domain)
	case apitypes.SSOOkta, apitypes.SSOAuth0:
		if err := takes(false, true, false); err != nil {
			return "", err
		}
		base, err := validBaseURL(label, w.Domain)
		if kind == apitypes.SSOAuth0 { // Auth0's issuer ends in a slash
			base += "/"
		}
		return base, err
	case apitypes.SSOKeycloak, apitypes.SSOAuthentik:
		if err := takes(false, true, true); err != nil {
			return "", err
		}
		base, err := validBaseURL(label, w.Domain)
		if err != nil {
			return "", err
		}
		realm := strings.TrimSpace(w.Realm)
		if realm == "" || strings.ContainsAny(realm, "/?#%\\ \t") {
			what := "its application's slug (the realm)"
			if kind == apitypes.SSOKeycloak {
				what = "its realm"
			}
			return "", invalid("%s needs %s, without slashes or spaces", label, what)
		}
		if kind == apitypes.SSOKeycloak {
			return base + "/realms/" + realm, nil
		}
		return base + "/application/o/" + realm + "/", nil
	}
	return "", fmt.Errorf("presetIssuer: %s is not a named kind", kind)
}

// whereWords says what a named kind takes, for errors.
func whereWords(kind apitypes.SSOKind) string {
	switch kind {
	case apitypes.SSOMicrosoft:
		return "a tenant (its ID or a domain of it), not a domain or realm"
	case apitypes.SSOGitLab:
		return "only a domain (its base URL; gitlab.com when left out)"
	case apitypes.SSOKeycloak:
		return "a domain (its base URL) and a realm"
	case apitypes.SSOAuthentik:
		return "a domain (its base URL) and a realm (the application's slug)"
	}
	return "only a domain"
}

// ssoWhere reads back what a named kind's issuer was built from; nothing
// for other kinds.
func (s ssoState) ssoWhere(kind apitypes.SSOKind, issuer string) apitypes.SSOWhere {
	switch kind {
	case apitypes.SSOMicrosoft:
		t, _ := strings.CutPrefix(issuer, s.microsoft+"/")
		return apitypes.SSOWhere{Tenant: strings.TrimSuffix(t, "/v2.0")}
	case apitypes.SSOGitLab, apitypes.SSOOkta, apitypes.SSOAuth0:
		return apitypes.SSOWhere{Domain: strings.TrimSuffix(issuer, "/")}
	case apitypes.SSOKeycloak:
		base, realm, _ := strings.Cut(issuer, "/realms/")
		return apitypes.SSOWhere{Domain: base, Realm: realm}
	case apitypes.SSOAuthentik:
		base, slug, _ := strings.Cut(issuer, "/application/o/")
		return apitypes.SSOWhere{Domain: base, Realm: strings.TrimSuffix(slug, "/")}
	}
	return apitypes.SSOWhere{}
}

// resolveIssuer turns a microsoft issuer of a tenant's domain into its
// tenant ID's, the one its tokens carry: discovery under the domain
// names it. Other issuers are kept.
func (s ssoState) resolveIssuer(ctx context.Context, log *slog.Logger, kind apitypes.SSOKind, issuer string) (string, error) {
	tenant := s.ssoWhere(kind, issuer).Tenant
	if kind != apitypes.SSOMicrosoft || isUUID(tenant) {
		return issuer, nil
	}
	var doc struct {
		Issuer string `json:"issuer"`
	}
	err := s.getJSON(ctx, issuer+"/.well-known/openid-configuration", &doc)
	id := s.ssoWhere(kind, doc.Issuer).Tenant
	if err != nil || doc.Issuer != s.microsoft+"/"+id+"/v2.0" || !isUUID(id) {
		log.Warn("sso: microsoft tenant", "tenant", tenant, "issuer", doc.Issuer, "err", err)
		return "", invalid("Microsoft doesn't know a tenant %s; give its tenant ID (Entra ID, Overview) or one of its verified domains", tenant)
	}
	if id == microsoftConsumers {
		return "", invalid("%s is personal Microsoft accounts' tenant; Dock signs in one organization's tenant", tenant)
	}
	return doc.Issuer, nil
}

// getJSON reads url into v.
func (s ssoState) getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// validTenant checks a Microsoft tenant: its ID or a domain, never one of
// the shared endpoints that sign in anyone's tenant (ADR 0081).
func validTenant(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch {
	case s == "common" || s == "organizations" || s == "consumers" || s == microsoftConsumers:
		return "", invalid("%s signs in people of any tenant, and Dock signs in one tenant's: give your tenant's ID or a domain of it (Entra ID, Overview)", s)
	case s == "":
		return "", invalid("Microsoft needs your tenant: its ID or a domain of it (Entra ID, Overview)")
	case isUUID(s):
		return s, nil
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || strings.Trim(label, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
			return "", invalid("tenant %q is not a tenant ID or a domain like contoso.onmicrosoft.com", s)
		}
	}
	if !strings.Contains(s, ".") {
		return "", invalid("tenant %q is not a tenant ID or a domain like contoso.onmicrosoft.com", s)
	}
	return s, nil
}

// isUUID reports whether s is a lowercase UUID, as Microsoft's tenant IDs.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdef", r) {
				return false
			}
		}
	}
	return true
}

// validBaseURL checks where a named provider is: a domain or a URL
// (https, or http on localhost), without a trailing slash.
func validBaseURL(label, s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", invalid("%s needs its domain or base URL", label)
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	base, err := validIssuer(strings.TrimRight(s, "/"))
	if err != nil {
		return "", invalid("%s's domain %q is not a domain or a URL like https://id.example.com (http only on localhost)", label, s)
	}
	return base, nil
}
