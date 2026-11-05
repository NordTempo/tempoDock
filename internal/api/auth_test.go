package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// as sends a request authenticated by a session cookie, like the UI.
func (f *fixture) as(cookie *http.Cookie, method, path string, body any) (result, []byte) {
	f.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, f.srv.URL+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return result{StatusCode: resp.StatusCode, Header: resp.Header}, b
}

func (f *fixture) loginAs(email, password string) (result, *http.Cookie) {
	f.t.Helper()
	resp, _ := f.do("", "POST", "/api/v1/auth/login", apitypes.LoginRequest{Email: email, Password: password})
	return resp, resp.session()
}

func TestLoginIsRateLimitedAndAudited(t *testing.T) {
	f := newFixture(t)
	f.login() // one attempt of the burst of 5
	for range 4 {
		if resp, _ := f.loginAs("ada@example.com", "guess number one"); resp.StatusCode != 401 {
			t.Fatalf("wrong password: %d", resp.StatusCode)
		}
	}
	resp, body := f.do("", "POST", "/api/v1/auth/login", apitypes.LoginRequest{Email: "ada@example.com", Password: "correct horse battery"})
	var p apitypes.Error
	_ = json.Unmarshal(body, &p)
	if resp.StatusCode != 429 || p.Code != apitypes.CodeRateLimited {
		t.Fatalf("sixth attempt: %d %s", resp.StatusCode, body)
	}

	// Accounts are the platform's: their events are in the platform's log.
	_, body = f.do("operator", "GET", "/api/v1/audit?platform=true&action=user.login_failed", nil)
	var audit []apitypes.AuditEvent
	_ = json.Unmarshal(body, &audit)
	if len(audit) != 4 || audit[0].Actor != "ada@example.com" || strings.Contains(string(body), "guess") {
		t.Fatalf("failed logins audit: %s", body)
	}
}

func TestDisableAndDeleteUser(t *testing.T) {
	f := newFixture(t)
	admin := f.login()
	bob, err := f.core.CreateUser(context.Background(), sys, apitypes.CreateUserRequest{
		Email: "bob@example.com", Name: "Bob", Password: "bob's long password", Role: apitypes.ScopeDeploy,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, bobSession := f.loginAs("bob@example.com", "bob's long password")
	userPath := fmt.Sprintf("/api/v1/users/%d", bob.ID)

	resp, body := f.as(admin, "POST", userPath+"/disable", nil)
	var u apitypes.User
	_ = json.Unmarshal(body, &u)
	if resp.StatusCode != 200 || u.DisabledAt == nil {
		t.Fatalf("disable: %d %s", resp.StatusCode, body)
	}
	if resp, _ := f.as(bobSession, "GET", "/api/v1/me", nil); resp.StatusCode != 401 {
		t.Fatalf("disabled user's session still works: %d", resp.StatusCode)
	}
	if resp, _ := f.loginAs("bob@example.com", "bob's long password"); resp.StatusCode != 401 {
		t.Fatalf("disabled user logged in: %d", resp.StatusCode)
	}
	if resp, _ := f.as(admin, "POST", userPath+"/enable", nil); resp.StatusCode != 200 {
		t.Fatalf("enable: %d", resp.StatusCode)
	}
	if resp, _ := f.loginAs("bob@example.com", "bob's long password"); resp.StatusCode != 200 {
		t.Fatalf("enabled user can't log in: %d", resp.StatusCode)
	}

	var users []apitypes.User
	_, body = f.as(admin, "GET", "/api/v1/users", nil)
	_ = json.Unmarshal(body, &users)
	if resp, body := f.as(admin, "DELETE", fmt.Sprintf("/api/v1/users/%d", users[0].ID), nil); resp.StatusCode != 400 {
		t.Fatalf("deleting yourself: %d %s", resp.StatusCode, body)
	}
	if resp, _ := f.as(admin, "DELETE", userPath, nil); resp.StatusCode != 204 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if resp, _ := f.loginAs("bob@example.com", "bob's long password"); resp.StatusCode != 401 {
		t.Fatalf("deleted user logged in: %d", resp.StatusCode)
	}
}

func TestChangePasswordRotatesSessions(t *testing.T) {
	f := newFixture(t)
	first := f.login()
	_, second := f.loginAs("ada@example.com", "correct horse battery")

	req := apitypes.ChangePasswordRequest{CurrentPassword: "wrong one, surely", NewPassword: "a brand new passphrase"}
	if resp, body := f.as(first, "POST", "/api/v1/me/password", req); resp.StatusCode != 400 {
		t.Fatalf("wrong current password: %d %s", resp.StatusCode, body)
	}
	req.CurrentPassword = "correct horse battery"
	resp, body := f.as(first, "POST", "/api/v1/me/password", req)
	if resp.StatusCode != 204 || len(resp.Cookies()) != 1 {
		t.Fatalf("change password: %d %s", resp.StatusCode, body)
	}
	fresh := resp.Cookies()[0]
	for name, c := range map[string]*http.Cookie{"first": first, "second": second} {
		if resp, _ := f.as(c, "GET", "/api/v1/me", nil); resp.StatusCode != 401 {
			t.Errorf("%s session survived the password change: %d", name, resp.StatusCode)
		}
	}
	if resp, _ := f.as(fresh, "GET", "/api/v1/me", nil); resp.StatusCode != 200 {
		t.Fatalf("fresh session: %d", resp.StatusCode)
	}
	if resp, _ := f.do("admin", "POST", "/api/v1/me/password", req); resp.StatusCode != 400 {
		t.Fatalf("token changing a password: %d", resp.StatusCode)
	}
}

// The Dock-Org header names the org like the `org` parameter does (the web
// UI sends it), and a token is still refused another org.
func TestOrgHeader(t *testing.T) {
	f := newFixture(t)
	for org, want := range map[string]int{"test": 200, "acme": 403} {
		req, _ := http.NewRequest("GET", f.srv.URL+"/api/v1/projects", nil)
		req.Header.Set("Authorization", "Bearer "+f.tokens["read"])
		req.Header.Set("Dock-Org", org)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("Dock-Org: %s: %d, want %d", org, resp.StatusCode, want)
		}
	}
}
