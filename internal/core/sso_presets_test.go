package core

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core/oidctest"
)

// tenantID is the fake Microsoft tenant's ID; contoso.com is its domain.
const tenantID = "2f4a9c1e-8b3d-4e6f-a5c7-0d1e2f3a4b5c"

// addPreset adds a provider of kind named after it, at w.
func (h *harness) addPreset(kind apitypes.SSOKind, w apitypes.SSOWhere) (apitypes.SSOProvider, error) {
	return h.c.CreateSSOProvider(h.ctx, testSystem, apitypes.CreateSSOProviderRequest{
		Name: string(kind), Kind: kind, SSOWhere: w, ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret,
	})
}

func TestSSONamedProviders(t *testing.T) {
	for _, tc := range []struct {
		kind  apitypes.SSOKind
		path  string
		where func(base string) apitypes.SSOWhere
		label string
	}{
		{apitypes.SSOGitLab, "", func(b string) apitypes.SSOWhere { return apitypes.SSOWhere{Domain: b + "/"} }, "GitLab"},
		{apitypes.SSOOkta, "", func(b string) apitypes.SSOWhere { return apitypes.SSOWhere{Domain: b} }, "Okta"},
		{apitypes.SSOAuth0, "/", func(b string) apitypes.SSOWhere { return apitypes.SSOWhere{Domain: b} }, "Auth0"},
		{apitypes.SSOKeycloak, "/realms/staff", func(b string) apitypes.SSOWhere { return apitypes.SSOWhere{Domain: b, Realm: "staff"} }, "Keycloak"},
		{apitypes.SSOAuthentik, "/application/o/dock/", func(b string) apitypes.SSOWhere { return apitypes.SSOWhere{Domain: b, Realm: "dock"} }, "Authentik"},
		{apitypes.SSOMicrosoft, "/" + tenantID + "/v2.0", func(string) apitypes.SSOWhere { return apitypes.SSOWhere{Tenant: "Contoso.com"} }, "Microsoft"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			h := newHarness(t)
			freshLimits(h)
			idp := oidctest.NewAt(t, tc.path, "/contoso.com/v2.0")
			h.c.sso.microsoft = idp.URL
			p, err := h.addPreset(tc.kind, tc.where(idp.URL))
			if err != nil || p.Issuer != idp.Issuer || p.Label != tc.label {
				t.Fatalf("added %+v, %v; want issuer %s", p, err, idp.Issuer)
			}
			// The fields read back from the issuer build it again.
			if again, err := h.c.sso.presetIssuer(tc.kind, p.SSOWhere); err != nil || again != p.Issuer {
				t.Fatalf("%+v builds %s, %v", p.SSOWhere, again, err)
			}
			h.cliUser(adaEmail, apitypes.ScopeDeploy)
			res, err := h.ssoSignInVia(string(tc.kind), idp, "ada-sub", adaEmail, func(c map[string]any) {
				if tc.kind == apitypes.SSOMicrosoft { // Entra ID's claims: no email_verified, xms_edov when asked for
					delete(c, "email_verified")
					c["xms_edov"] = true
					c["tid"] = tenantID
				}
			})
			if err != nil || res.Secret == "" {
				t.Fatalf("sign-in through %s: %+v, %v", tc.kind, res, err)
			}
		})
	}
}

func TestSSOMicrosoftIsOneTenant(t *testing.T) {
	h := newHarness(t)
	freshLimits(h)
	idp := oidctest.NewAt(t, "/"+tenantID+"/v2.0")
	h.c.sso.microsoft = idp.URL

	// The shared endpoints sign in every tenant's people: refused.
	for _, tenant := range []string{"common", "organizations", "consumers", "", "not a tenant"} {
		_, err := h.addPreset(apitypes.SSOMicrosoft, apitypes.SSOWhere{Tenant: tenant})
		wantCode(t, "tenant "+tenant, err, apitypes.CodeValidation)
	}
	_, err := h.addPreset(apitypes.SSOMicrosoft, apitypes.SSOWhere{Tenant: tenantID, Domain: "contoso.com"})
	wantCode(t, "a domain too", err, apitypes.CodeValidation)
	if _, err := h.addPreset(apitypes.SSOMicrosoft, apitypes.SSOWhere{Tenant: tenantID}); err != nil {
		t.Fatal(err)
	}
	h.cliUser(adaEmail, apitypes.ScopeDeploy)

	// Another tenant's token for Dock's client (as a multi-tenant app
	// gets) fails the exact issuer check.
	_, err = h.ssoSignInVia("microsoft", idp, "ada-sub", adaEmail, func(c map[string]any) {
		c["iss"] = idp.URL + "/00000000-0000-0000-0000-000000000001/v2.0"
		c["xms_edov"] = true
	})
	wantCode(t, "another tenant", err, apitypes.CodeUnauthorized)
	// An email nobody vouched for: Entra ID's email claim alone isn't.
	_, err = h.ssoSignInVia("microsoft", idp, "ada-sub", adaEmail, func(c map[string]any) {
		delete(c, "email_verified")
	})
	wantCode(t, "no xms_edov", err, apitypes.CodeForbidden)
	_, err = h.ssoSignInVia("microsoft", idp, "ada-sub", adaEmail, func(c map[string]any) {
		delete(c, "email_verified")
		c["xms_edov"] = false
	})
	wantCode(t, "xms_edov false", err, apitypes.CodeForbidden)
}

func TestSSONamedProviderFields(t *testing.T) {
	h, _ := ssoHarness(t)
	for what, req := range map[string]apitypes.CreateSSOProviderRequest{
		"okta with a tenant":     {Name: "o", Kind: apitypes.SSOOkta, SSOWhere: apitypes.SSOWhere{Domain: "acme.okta.com", Tenant: "x.com"}},
		"okta without a domain":  {Name: "o", Kind: apitypes.SSOOkta},
		"keycloak without realm": {Name: "k", Kind: apitypes.SSOKeycloak, SSOWhere: apitypes.SSOWhere{Domain: "id.example.com"}},
		"realm with a slash":     {Name: "k", Kind: apitypes.SSOKeycloak, SSOWhere: apitypes.SSOWhere{Domain: "id.example.com", Realm: "a/b"}},
		"plain http":             {Name: "k", Kind: apitypes.SSOGitLab, SSOWhere: apitypes.SSOWhere{Domain: "http://git.example.com"}},
		"github with a domain":   {Name: "g", Kind: apitypes.SSOGitHub, SSOWhere: apitypes.SSOWhere{Domain: "github.example.com"}},
		"gitlab with an issuer":  {Name: "g", Kind: apitypes.SSOGitLab, Issuer: "https://gitlab.com"},
		"unknown kind":           {Name: "d", Kind: "discord"},
	} {
		req.ClientID, req.ClientSecret = "x", "y"
		_, err := h.c.CreateSSOProvider(h.ctx, testSystem, req)
		wantCode(t, what, err, apitypes.CodeValidation)
	}
	empty := ""
	_, err := h.c.UpdateSSOProvider(h.ctx, testSystem, "corp", apitypes.UpdateSSOProviderRequest{Domain: &empty})
	wantCode(t, "oidc with a domain", err, apitypes.CodeValidation)
}
