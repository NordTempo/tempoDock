package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/core/oidctest"
)

// redirect is a redirect response: its status, where to and the cookies
// it sets by name.
type redirect struct {
	status   int
	location string
	cookies  map[string]*http.Cookie
}

// ssoCallback plays the browser coming back from the provider: a GET of
// the callback with the binding cookie (nil for none), redirects not
// followed.
func (f *fixture) ssoCallback(state, code string, binding *http.Cookie) redirect {
	f.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+core.SSOCallbackPath+"?"+url.Values{"state": {state}, "code": {code}}.Encode(), nil)
	if binding != nil {
		req.AddCookie(binding)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	_ = resp.Body.Close()
	r := redirect{status: resp.StatusCode, location: resp.Header.Get("Location"), cookies: map[string]*http.Cookie{}}
	for _, c := range resp.Cookies() {
		r.cookies[c.Name] = c
	}
	return r
}

// ssoStart starts a sign-in through corp and returns the provider's
// request and the binding cookie.
func (f *fixture) ssoStart() (oidctest.Request, *http.Cookie) {
	f.t.Helper()
	resp, body := f.do("", "POST", "/api/v1/auth/sso/start", apitypes.SSOStartRequest{Provider: "corp", To: "/test/shop"})
	var start apitypes.SSOStart
	cs := resp.Cookies()
	if err := json.Unmarshal(body, &start); err != nil || resp.StatusCode != 200 || len(cs) != 1 {
		f.t.Fatalf("start: %d %s", resp.StatusCode, body)
	}
	if c := cs[0]; c.Name != ssoCookie || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/api/v1/auth/sso" {
		f.t.Fatalf("binding cookie %+v", c)
	}
	return oidctest.Read(f.t, start.URL), cs[0]
}

func TestSSOCallbackRedirects(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	idp := oidctest.New(t)
	if _, err := f.core.CreateUser(ctx, sys, apitypes.CreateUserRequest{Email: "ada@example.com", Name: "Ada", Password: "a long enough password", Role: apitypes.ScopeDeploy}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.core.CreateSSOProvider(ctx, sys, apitypes.CreateSSOProviderRequest{
		Name: "corp", Kind: apitypes.SSOOIDC, Label: "Corp ID", Issuer: idp.Issuer, ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret,
	}); err != nil {
		t.Fatal(err)
	}
	// The sign-in page's buttons, for anyone.
	if _, body := f.do("", "GET", "/api/v1/auth/methods", nil); !strings.Contains(string(body), `"providers":[{"name":"corp","kind":"oidc","label":"Corp ID"}]`) {
		t.Fatalf("methods: %s", body)
	}

	req, binding := f.ssoStart()
	code := idp.Authorize(req, idp.Claims(req, "ada-sub", "ada@example.com"))
	r := f.ssoCallback(req.State, code, binding)
	session := r.cookies[sessionCookie]
	if r.status != http.StatusSeeOther || r.location != "/test/shop" || session == nil || session.SameSite != http.SameSiteStrictMode {
		t.Fatalf("callback: %+v", r)
	}
	if c := r.cookies[ssoCookie]; c == nil || c.MaxAge >= 0 {
		t.Fatalf("binding cookie not cleared: %+v", c)
	}
	if _, body := f.as(session, "GET", "/api/v1/me", nil); !strings.Contains(string(body), `"sso":"corp","sso_label":"Corp ID"`) {
		t.Fatalf("me: %s", body)
	}

	// A used state, a browser without the binding cookie, or a provider
	// that says no goes back to the sign-in page with the reason as a
	// word the page has a sentence for and the provider's name: never
	// with words of the server's, or the provider's, in the address.
	for want, try := range map[string]func() redirect{
		"/login?error=expired": func() redirect { return f.ssoCallback(req.State, code, binding) },
		"/login?error=other_browser&provider=corp": func() redirect {
			req, _ := f.ssoStart()
			return f.ssoCallback(req.State, idp.Authorize(req, idp.Claims(req, "ada-sub", "ada@example.com")), nil)
		},
		"/login?error=not_accepted&provider=corp": func() redirect {
			req, binding := f.ssoStart()
			other, _ := f.ssoStart()
			return f.ssoCallback(req.State, idp.Authorize(other, idp.Claims(other, "ada-sub", "ada@example.com")), binding)
		},
	} {
		if r := try(); r.status != http.StatusSeeOther || r.location != want {
			t.Errorf("want %s: %+v", want, r)
		}
	}

	// Someone with no account and no invite on an invite-only Dock lands
	// on the page that says so, signed in to nothing.
	req, binding = f.ssoStart()
	r = f.ssoCallback(req.State, idp.Authorize(req, idp.Claims(req, "eve-sub", "eve@example.com")), binding)
	if r.status != http.StatusSeeOther || r.location != "/invite-only?email=eve%40example.com&provider=corp" || r.cookies[sessionCookie] != nil {
		t.Fatalf("invite only: %+v", r)
	}
}

// The sign-in page has a sentence of its own for every reason a failed
// single sign-on sends the browser back with.
func TestSSOReasonsHaveWords(t *testing.T) {
	const page = "../../web/src/features/sso/failures.ts"
	src, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, reason := range core.SSOReasons {
		if !regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(reason) + `: `).Match(src) {
			t.Errorf("%s has no sentence for %q: add one, in the words of core's error", page, reason)
		}
	}
}
