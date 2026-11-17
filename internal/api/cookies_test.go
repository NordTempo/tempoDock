package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// https sends a request as Caddy forwards an HTTPS one, with cookies.
func (f *fixture) https(method, path string, body any, cookies ...*http.Cookie) result {
	f.t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest(method, f.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("X-Forwarded-Proto", "https")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return result{StatusCode: resp.StatusCode, Header: resp.Header}
}

// Over HTTPS the session cookie is __Host-dock_session, which an app on
// a sibling hostname can't set. The bare name still signs in a session
// from before the prefix, never a newer one tossed under it.
func TestSessionCookieOverHTTPS(t *testing.T) {
	f := newFixture(t)
	if _, err := f.core.CreateUser(context.Background(), sys, apitypes.CreateUserRequest{
		Email: "ada@example.com", Name: "Ada", Password: "correct horse battery", Role: apitypes.ScopeAdmin,
	}); err != nil {
		t.Fatal(err)
	}
	login := f.https("POST", "/api/v1/auth/login", apitypes.LoginRequest{Email: "ada@example.com", Password: "correct horse battery"})
	var session *http.Cookie
	clearsBare := false
	for _, c := range login.Cookies() {
		switch c.Name {
		case hostPrefix + sessionCookie:
			session = c
		case sessionCookie:
			clearsBare = c.MaxAge < 0
		}
	}
	if login.StatusCode != 200 || session == nil || !session.Secure || session.Path != "/" || session.Domain != "" || !clearsBare {
		t.Fatalf("login over HTTPS: %d, cookies %v", login.StatusCode, login.Cookies())
	}
	if r := f.https("GET", "/api/v1/me", nil, session); r.StatusCode != 200 {
		t.Fatalf("prefixed session: %d", r.StatusCode)
	}
	bare := &http.Cookie{Name: sessionCookie, Value: session.Value}
	if r := f.https("GET", "/api/v1/me", nil, bare); r.StatusCode != 401 {
		t.Fatalf("a new session under the bare name: %d", r.StatusCode)
	}
	if _, err := f.store.DB.Exec(`UPDATE sessions SET legacy_cookie = 1`); err != nil {
		t.Fatal(err)
	}
	if r := f.https("GET", "/api/v1/me", nil, bare); r.StatusCode != 200 {
		t.Fatalf("a session from before the prefix: %d", r.StatusCode)
	}
	logout := f.https("POST", "/api/v1/auth/logout", nil, session)
	if r := f.https("GET", "/api/v1/me", nil, session); logout.StatusCode != 200 || r.StatusCode != 401 {
		t.Fatalf("logout %d, then %d", logout.StatusCode, r.StatusCode)
	}
}
