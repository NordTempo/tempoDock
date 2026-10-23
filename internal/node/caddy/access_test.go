package caddy

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/access"
)

// checkRequest is the request Caddy sends the access check for a visit to
// uri on app.example.com (loginAccess).
func checkRequest(key []byte, method, uri string, header http.Header) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/internal/caddy/access", nil)
	r.Host = "app.example.com"
	for k, v := range header {
		r.Header[k] = v
	}
	r.Header.Set(accessKeyHeader, base64.RawURLEncoding.EncodeToString(key))
	r.Header.Set(accessLoginHeader, "https://dock.example.com/access")
	r.Header.Set(forwardedMethod, method)
	r.Header.Set(forwardedURI, uri)
	r.Header.Set("X-Forwarded-Proto", "https")
	return r
}

func TestAccessHandler(t *testing.T) {
	key := access.NewKey()
	h := AccessHandler()
	serve := func(r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	nav := http.Header{"Sec-Fetch-Mode": {"navigate"}}

	// A page load without a session goes to Dock, keeping the path.
	w := serve(checkRequest(key, "GET", "/orders?id=7", nav))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "https://dock.example.com/access?host=app.example.com&to=%2Forders%3Fid%3D7" {
		t.Fatalf("page load: %d %q", w.Code, w.Header().Get("Location"))
	}
	// A script's request or a POST can't follow that: 401.
	for _, r := range []*http.Request{
		checkRequest(key, "GET", "/api", http.Header{"Sec-Fetch-Mode": {"cors"}, "Accept": {"text/html"}}),
		checkRequest(key, "POST", "/form", nav),
		checkRequest(key, "GET", "/api", nil),
	} {
		if w := serve(r); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: %d, want 401", r.Header.Get(forwardedMethod), r.Header.Get(forwardedURI), w.Code)
		}
	}

	// The callback trades a grant for a session cookie and goes on.
	grant := access.Sign(key, access.Grant, "app.example.com", "ada@example.com", time.Now().Add(access.GrantTTL))
	callback := access.CallbackPath + "?" + url.Values{"grant": {grant}, "to": {"/orders?id=7"}}.Encode()
	w = serve(checkRequest(key, "GET", callback, nav))
	cookies := w.Result().Cookies()
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/orders?id=7" || len(cookies) != 1 {
		t.Fatalf("callback: %d %q %v", w.Code, w.Header().Get("Location"), cookies)
	}
	if c := cookies[0]; c.Name != accessCookie || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie = %+v", c)
	}

	// With the cookie every request passes, naming the visitor.
	w = serve(checkRequest(key, "POST", "/api", http.Header{"Cookie": {cookies[0].String()}}))
	if w.Code != http.StatusNoContent || w.Header().Get(access.UserHeader) != "ada@example.com" {
		t.Fatalf("signed in: %d %q", w.Code, w.Header().Get(access.UserHeader))
	}
	// Not after the key changes (access turned off and on again).
	if w := serve(checkRequest(access.NewKey(), "GET", "/api", http.Header{"Cookie": {cookies[0].String()}})); w.Code != http.StatusUnauthorized {
		t.Errorf("cookie under another key: %d", w.Code)
	}

	// A session cookie is no grant, a grant from another key is refused,
	// and the callback never redirects off the hostname.
	session := access.Sign(key, access.Session, "app.example.com", "ada@example.com", time.Now().Add(time.Hour))
	for name, grant := range map[string]string{
		"session as grant": session,
		"other key":        access.Sign(access.NewKey(), access.Grant, "app.example.com", "eve", time.Now().Add(time.Minute)),
	} {
		if w := serve(checkRequest(key, "GET", access.CallbackPath+"?grant="+url.QueryEscape(grant), nav)); w.Code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, w.Code)
		}
	}
	for _, to := range []string{"https://evil.example", "//evil.example/", "/\\evil.example", ""} {
		cb := access.CallbackPath + "?" + url.Values{"grant": {grant}, "to": {to}}.Encode()
		if w := serve(checkRequest(key, "GET", cb, nav)); w.Header().Get("Location") != "/" {
			t.Errorf("to %q: redirected to %q", to, w.Header().Get("Location"))
		}
	}

	// Without the route's key the request didn't come from Caddy.
	r := checkRequest(key, "GET", "/", nav)
	r.Header.Del(accessKeyHeader)
	if w := serve(r); w.Code != http.StatusBadRequest {
		t.Errorf("no key: %d", w.Code)
	}
}
