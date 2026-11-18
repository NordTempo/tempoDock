package caddy

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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

	// A page load without a session goes to Dock, keeping the path, with
	// a nonce left in the browser and its hash sent along.
	w := serve(checkRequest(key, "GET", "/orders?id=7", nav))
	left := w.Result().Cookies()
	if w.Code != http.StatusFound || len(left) != 1 {
		t.Fatalf("page load: %d %v", w.Code, left)
	}
	nonce := left[0]
	if nonce.Name != access.SecureNonceCookie || !access.ValidNonce(nonce.Value) || !nonce.HttpOnly || !nonce.Secure || nonce.Path != "/" || nonce.Domain != "" || nonce.SameSite != http.SameSiteLaxMode {
		t.Fatalf("nonce cookie = %+v", nonce)
	}
	if got, want := w.Header().Get("Location"), "https://dock.example.com/access?host=app.example.com&n="+access.NonceHash(nonce.Value)+"&to=%2Forders%3Fid%3D7"; got != want {
		t.Fatalf("page load goes to %q, want %q", got, want)
	}
	browser := http.Header{"Sec-Fetch-Mode": {"navigate"}, "Cookie": {nonce.Name + "=" + nonce.Value}}
	// A second tab keeps the nonce the first one got.
	if again := serve(checkRequest(key, "GET", "/", browser)).Result().Cookies(); len(again) != 1 || again[0].Value != nonce.Value {
		t.Fatalf("second page load: %v", again)
	}
	// Each grant differs (by its expiry), as each is traded once.
	grants := 0
	bound := func() string {
		grants++
		return access.SignBound(key, "app.example.com", access.NonceHash(nonce.Value), "ada@example.com", time.Now().Add(access.GrantTTL-time.Duration(grants)*time.Second))
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
	grant := bound()
	callback := access.CallbackPath + "?" + url.Values{"grant": {grant}, "to": {"/orders?id=7"}}.Encode()
	// Not without the nonce the grant was made for, or with another
	// browser's: neither spends the grant.
	other := http.Header{"Sec-Fetch-Mode": {"navigate"}, "Cookie": {access.SecureNonceCookie + "=" + access.NewNonce()}}
	for name, h := range map[string]http.Header{"no nonce": nav, "another browser's nonce": other} {
		if w := serve(checkRequest(key, "GET", callback, h)); w.Code != http.StatusForbidden || len(w.Result().Cookies()) != 0 {
			t.Fatalf("%s: %d %v, want 403 and no cookie", name, w.Code, w.Result().Cookies())
		}
	}
	w = serve(checkRequest(key, "GET", callback, browser))
	cookies := w.Result().Cookies()
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/orders?id=7" || len(cookies) != 2 {
		t.Fatalf("callback: %d %q %v", w.Code, w.Header().Get("Location"), cookies)
	}
	// The nonce is dropped with the sign-in, and the grant works once.
	if c := cookies[1]; c.Name != access.SecureNonceCookie || c.MaxAge >= 0 {
		t.Errorf("nonce after sign-in = %+v", c)
	}
	if w := serve(checkRequest(key, "GET", callback, browser)); w.Code != http.StatusForbidden || len(w.Result().Cookies()) != 0 {
		t.Fatalf("second trade: %d %v, want 403 and no cookie", w.Code, w.Result().Cookies())
	}
	// The browser that is signed in by now (a second tab) just goes on.
	if w := serve(checkRequest(key, "GET", callback, http.Header{"Cookie": {cookies[0].Name + "=" + cookies[0].Value}})); w.Code != http.StatusFound || w.Header().Get("Location") != "/orders?id=7" || len(w.Result().Cookies()) != 0 {
		t.Fatalf("signed-in browser at the callback: %d %q", w.Code, w.Header().Get("Location"))
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
	// so is one that names no browser, and the callback never redirects
	// off the hostname.
	session := access.Sign(key, access.Session, "app.example.com", "ada@example.com", time.Now().Add(time.Hour))
	for name, grant := range map[string]string{
		"session as grant": session,
		"other key":        access.SignBound(access.NewKey(), "app.example.com", access.NonceHash(nonce.Value), "eve", time.Now().Add(time.Minute)),
		"unbound grant":    access.Sign(key, access.Grant, "app.example.com", "ada@example.com", time.Now().Add(access.GrantTTL)),
	} {
		if w := serve(checkRequest(key, "GET", access.CallbackPath+"?grant="+url.QueryEscape(grant), browser)); w.Code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, w.Code)
		}
	}
	for _, to := range []string{"https://evil.example", "//evil.example/", "/\\evil.example", "/\t/evil.example", "/\n/evil.example", ""} {
		cb := access.CallbackPath + "?" + url.Values{"grant": {bound()}, "to": {to}}.Encode()
		if w := serve(checkRequest(key, "GET", cb, browser)); w.Header().Get("Location") != "/" {
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

// An uptime probe's session (access.ProbeCookie) passes the check on its
// own hostname for a minute, naming the probe, and nowhere else.
func TestAccessHandlerLetsAProbeThrough(t *testing.T) {
	key := access.NewKey()
	serve := func(r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		AccessHandler().ServeHTTP(w, r)
		return w
	}
	cookie := func(host string, at time.Time) http.Header {
		return http.Header{"Cookie": {access.ProbeCookie(key, host, at)}, "User-Agent": {"dock-uptime/1.0"}}
	}
	w := serve(checkRequest(key, "GET", "/", cookie("app.example.com", time.Now())))
	if w.Code != http.StatusNoContent || w.Header().Get(access.UserHeader) != access.ProbeUser {
		t.Fatalf("probe: %d %q", w.Code, w.Header().Get(access.UserHeader))
	}
	for name, h := range map[string]http.Header{
		"another hostname's": cookie("other.example.com", time.Now()),
		"an expired":         cookie("app.example.com", time.Now().Add(-access.ProbeTTL)),
	} {
		if w := serve(checkRequest(key, "GET", "/", h)); w.Code != http.StatusUnauthorized {
			t.Errorf("%s session: %d, want 401", name, w.Code)
		}
	}
}

// On a password-protected route the check passes Dock's uptime probe and
// nobody else: a visitor's session under the same key, or a grant at the
// callback, gets a 401 and so meets the password.
func TestAccessHandlerPassesOnlyTheProbeOnAPasswordRoute(t *testing.T) {
	key := access.NewKey()
	serve := func(uri string, h http.Header) *httptest.ResponseRecorder {
		r := checkRequest(key, "GET", uri, h)
		r.Header.Set(accessProbeHeader, "1")
		w := httptest.NewRecorder()
		AccessHandler().ServeHTTP(w, r)
		return w
	}
	now := time.Now()
	w := serve("/", http.Header{"Cookie": {access.ProbeCookie(key, "app.example.com", now)}})
	if w.Code != http.StatusNoContent || w.Header().Get(access.UserHeader) != access.ProbeUser {
		t.Fatalf("probe: %d %q", w.Code, w.Header().Get(access.UserHeader))
	}
	visitor := accessCookie + "=" + access.Sign(key, access.Session, "app.example.com", "ada@example.com", now.Add(access.SessionTTL))
	nonce := access.NewNonce()
	grant := access.SignBound(key, "app.example.com", access.NonceHash(nonce), access.ProbeUser, now.Add(access.GrantTTL))
	for name, w := range map[string]*httptest.ResponseRecorder{
		"a visitor's session":          serve("/", http.Header{"Cookie": {visitor}}),
		"another hostname's probe":     serve("/", http.Header{"Cookie": {access.ProbeCookie(key, "other.example.com", now)}}),
		"another key's probe":          serve("/", http.Header{"Cookie": {access.ProbeCookie(access.NewKey(), "app.example.com", now)}}),
		"a grant at the callback":      serve(access.CallbackPath+"?grant="+grant, http.Header{"Sec-Fetch-Mode": {"navigate"}, "Cookie": {access.SecureNonceCookie + "=" + nonce}}),
		"a page load without a cookie": serve("/", http.Header{"Sec-Fetch-Mode": {"navigate"}}),
	} {
		if w.Code != http.StatusUnauthorized || len(w.Result().Cookies()) != 0 {
			t.Errorf("%s: %d, want 401 and no cookie", name, w.Code)
		}
	}
}

// The set of traded grants drops expired ones as it grows and refuses new
// ones at its cap, so it never holds more than maxSpentGrants.
func TestSpentGrantsStayBounded(t *testing.T) {
	var s spentGrants
	token := func(i int) string { return "grant-" + strconv.Itoa(i) }
	// Grants that expire at 100, traded at 50: all kept, each works once.
	for i := range maxSpentGrants {
		if got := s.spend(token(i), 100, 50); got != grantFresh {
			t.Fatalf("grant %d: %d", i, got)
		}
	}
	if got := s.spend(token(0), 100, 50); got != grantUsed {
		t.Fatalf("second trade: %d", got)
	}
	if got := s.spend("one more", 100, 50); got != grantsFull || len(s.until) != maxSpentGrants {
		t.Fatalf("at the cap: %d, %d kept", got, len(s.until))
	}
	// Once they have expired, a new one sweeps them out.
	if got := s.spend("later", 200, 100); got != grantFresh || len(s.until) != 1 {
		t.Fatalf("after expiry: %d, %d kept", got, len(s.until))
	}
}
