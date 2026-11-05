package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
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

// ssoStart starts a sign-in for email and returns the provider's request
// and the binding cookie.
func (f *fixture) ssoStart(email string) (oidctest.Request, *http.Cookie) {
	f.t.Helper()
	resp, body := f.do("", "POST", "/api/v1/auth/sso/start", apitypes.SSOStartRequest{Email: email, To: "/test/shop"})
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
		Name: "corp", Issuer: idp.Issuer, ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret, AllowedDomains: []string{"example.com"},
	}); err != nil {
		t.Fatal(err)
	}

	req, binding := f.ssoStart("ada@example.com")
	code := idp.Authorize(req, idp.Claims(req, "ada-sub", "ada@example.com"))
	r := f.ssoCallback(req.State, code, binding)
	session := r.cookies[sessionCookie]
	if r.status != http.StatusSeeOther || r.location != "/test/shop" || session == nil || session.SameSite != http.SameSiteStrictMode {
		t.Fatalf("callback: %+v", r)
	}
	if c := r.cookies[ssoCookie]; c == nil || c.MaxAge >= 0 {
		t.Fatalf("binding cookie not cleared: %+v", c)
	}
	if _, body := f.as(session, "GET", "/api/v1/me", nil); !strings.Contains(string(body), `"sso":"test"`) {
		t.Fatalf("me: %s", body)
	}

	// A used state, or a browser without the binding cookie, goes back to
	// the sign-in page with the reason.
	for name, try := range map[string]func() redirect{
		"replayed": func() redirect { return f.ssoCallback(req.State, code, binding) },
		"another browser": func() redirect {
			req, _ := f.ssoStart("ada@example.com")
			return f.ssoCallback(req.State, idp.Authorize(req, idp.Claims(req, "ada-sub", "ada@example.com")), nil)
		},
	} {
		r := try()
		loc, _ := url.Parse(r.location)
		if r.status != http.StatusSeeOther || loc.Path != "/login" || loc.Query().Get("error") == "" {
			t.Errorf("%s: %+v", name, r)
		}
	}
}
