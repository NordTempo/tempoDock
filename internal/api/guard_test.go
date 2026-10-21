package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/NordTempo/platform/internal/apitypes"
	"github.com/NordTempo/platform/internal/core"
)

// login creates an admin user and returns its session cookie.
func (f *fixture) login() *http.Cookie {
	f.t.Helper()
	if _, err := f.core.CreateUser(context.Background(), core.System, apitypes.CreateUserRequest{
		Email: "ada@example.com", Name: "Ada", Password: "correct horse battery", Role: apitypes.ScopeAdmin,
	}); err != nil {
		f.t.Fatal(err)
	}
	resp, body := f.do("", "POST", "/api/v1/auth/login", apitypes.LoginRequest{Email: "ada@example.com", Password: "correct horse battery"})
	if resp.StatusCode != 200 || len(resp.Cookies()) != 1 {
		f.t.Fatalf("login: %d %s", resp.StatusCode, body)
	}
	return resp.Cookies()[0]
}

// A page on another origin (or another port of the same host) must not be
// able to use the dashboard's session cookie.
func TestCookieRequestsRefuseCrossSite(t *testing.T) {
	f := newFixture(t)
	cookie := f.login()
	self := strings.TrimPrefix(f.srv.URL, "http://")
	user := `{"email":"eve@example.com","name":"Eve","password":"eve's long password","role":"admin"}`
	cases := []struct {
		name    string
		bearer  bool
		headers map[string]string
		status  int
	}{
		{"foreign origin, no content type", false, map[string]string{"Origin": "https://evil.example"}, 403},
		{"same host other port", false, map[string]string{"Origin": "http://127.0.0.1:1", "Content-Type": "application/json"}, 403},
		{"cross-site fetch metadata", false, map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "http://" + self, "Content-Type": "application/json"}, 403},
		{"foreign referer", false, map[string]string{"Referer": "https://evil.example/x", "Content-Type": "application/json"}, 403},
		{"form body", false, map[string]string{"Origin": "http://" + self, "Content-Type": "text/plain"}, 403},
		{"no content type", false, map[string]string{"Origin": "http://" + self}, 403},
		{"bearer is exempt", true, map[string]string{"Origin": "https://evil.example"}, 201},
		{"same origin JSON", false, map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://" + self, "Content-Type": "application/json"}, 201},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Replace(user, "eve@", strings.Repeat("e", i+1)+"@", 1)
			req, _ := http.NewRequest("POST", f.srv.URL+"/api/v1/users", strings.NewReader(body))
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			if tc.bearer {
				req.Header.Set("Authorization", "Bearer "+f.tokens["admin"])
			} else {
				req.AddCookie(cookie)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Fatalf("status %d, want %d: %s", resp.StatusCode, tc.status, b)
			}
		})
	}

	// Bodyless actions from the UI (archive, logout) need no Content-Type.
	req, _ := http.NewRequest("POST", f.srv.URL+"/api/v1/auth/logout", nil)
	req.Header.Set("Origin", "http://"+self)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
}

func TestVersionReportsUploadLimit(t *testing.T) {
	f := newFixture(t)
	_, body := f.do("", "GET", "/api/v1/version", nil)
	var v apitypes.Version
	if err := json.Unmarshal(body, &v); err != nil || v.MaxUploadBytes != 1<<20 {
		t.Fatalf("version = %s (%v)", body, err)
	}
}

func TestSecurityHeaders(t *testing.T) {
	f := newFixture(t)
	for path, csp := range map[string]string{"/": "script-src 'self'", "/api/v1/health": "default-src 'none'"} {
		resp, err := http.Get(f.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		h := resp.Header
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "DENY" ||
			h.Get("Referrer-Policy") != "same-origin" || !strings.Contains(h.Get("Content-Security-Policy"), csp) {
			t.Errorf("%s headers: %v", path, h)
		}
	}
	resp, body := f.do("admin", "POST", "/api/v1/tokens", apitypes.CreateTokenRequest{Name: "ci", Scope: apitypes.ScopeDeploy})
	if resp.StatusCode != 201 || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("create token: %d %q %s", resp.StatusCode, resp.Header.Get("Cache-Control"), body)
	}
}
