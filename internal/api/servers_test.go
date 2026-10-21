package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// Server and user administration needs an admin token that isn't limited
// to a project; service statistics only need read on the service.
func TestAdminEndpointsNeedUnlimitedAdmin(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tok, err := f.core.CreateToken(ctx, core.System, apitypes.CreateTokenRequest{Name: "shop-admin", Scope: apitypes.ScopeAdmin, Project: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	f.tokens["shop-admin"] = tok.Secret
	servers, err := f.core.ListServers(ctx, core.System)
	if err != nil {
		t.Fatal(err)
	}
	server := "/api/v1/servers/" + servers[0].Name
	bob, err := f.core.CreateUser(ctx, core.System, apitypes.CreateUserRequest{Email: "bob@example.com", Name: "Bob", Password: "bob's long password", Role: apitypes.ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	user := fmt.Sprintf("/api/v1/users/%d", bob.ID)

	type call struct {
		method, path string
		body         any
		ok           int // status for an unlimited admin
	}
	admin := []call{
		{"GET", server, nil, 200},
		{"PATCH", server, apitypes.UpdateServerRequest{DisabledForPlacement: new(bool)}, 200},
		{"GET", server + "/stats?range=24h", nil, 200},
		{"GET", server + "/stats/live", nil, 0},
		{"PUT", "/api/v1/projects/blog/placements/non_production", apitypes.PinPlacementRequest{Server: servers[0].Name}, 200},
		{"GET", "/api/v1/users", nil, 200},
		{"POST", "/api/v1/users", apitypes.CreateUserRequest{Email: "eve@example.com", Name: "Eve", Password: "eve's long password", Role: apitypes.ScopeRead}, 201},
		{"POST", user + "/reset-password", nil, 200},
		{"POST", user + "/disable", nil, 200},
		{"POST", user + "/enable", nil, 200},
		{"DELETE", user, nil, 204},
	}
	for _, c := range admin {
		for token, detail := range map[string]string{"deploy": "requires admin", "shop-admin": "unlimited admin token"} {
			resp, body := f.do(token, c.method, c.path, c.body)
			var p apitypes.Error
			_ = json.Unmarshal(body, &p)
			if resp.StatusCode != 403 || !strings.Contains(p.Detail, detail) {
				t.Errorf("%s %s as %s: %d %s", c.method, c.path, token, resp.StatusCode, body)
			}
		}
	}
	// In order: delete comes last.
	for _, c := range admin {
		if c.ok == 0 {
			continue // a live stream; see TestServerStatsLive
		}
		if resp, body := f.do("admin", c.method, c.path, c.body); resp.StatusCode != c.ok {
			t.Errorf("%s %s as admin: %d %s", c.method, c.path, resp.StatusCode, body)
		}
	}

	// Listing servers and reading a service's statistics only need read.
	for _, path := range []string{"/api/v1/servers", shopProd + "/services/web/stats?range=7d", "/api/v1/projects/shop/placements"} {
		if resp, body := f.do("read", "GET", path, nil); resp.StatusCode != 200 {
			t.Errorf("GET %s as read: %d %s", path, resp.StatusCode, body)
		}
	}
	if resp, _ := f.do("shop-deploy", "GET", "/api/v1/projects/blog/environments/production/services/api/stats", nil); resp.StatusCode != 403 {
		t.Errorf("another project's service stats: %d", resp.StatusCode)
	}
}

func TestServerStatsLive(t *testing.T) {
	f := newFixture(t)
	servers, _ := f.core.ListServers(context.Background(), core.System)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", f.srv.URL+"/api/v1/servers/"+servers[0].Name+"/stats/live", nil)
	req.Header.Set("Authorization", "Bearer "+f.tokens["admin"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	if !sc.Scan() || sc.Text() != "event: ready" {
		t.Fatalf("first line %q", sc.Text())
	}
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "data: "); ok && v != "{}" {
			var u apitypes.ServerUsage
			if err := json.Unmarshal([]byte(v), &u); err != nil || u.MemoryTotalBytes == 0 {
				t.Fatalf("sample %s: %v", v, err)
			}
			return
		}
	}
	t.Fatal("no sample")
}

func TestResetPasswordAndUserCounts(t *testing.T) {
	f := newFixture(t)
	admin := f.login()
	bob, err := f.core.CreateUser(context.Background(), core.System, apitypes.CreateUserRequest{
		Email: "bob@example.com", Name: "Bob", Password: "bob's long password", Role: apitypes.ScopeAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, bobSession := f.loginAs("bob@example.com", "bob's long password")
	if resp, body := f.as(bobSession, "POST", "/api/v1/tokens", apitypes.CreateTokenRequest{Name: "bobs", Scope: apitypes.ScopeRead}); resp.StatusCode != 201 {
		t.Fatalf("bob's token: %d %s", resp.StatusCode, body)
	}
	counts := func() apitypes.User {
		_, body := f.as(admin, "GET", "/api/v1/users", nil)
		var users []apitypes.User
		_ = json.Unmarshal(body, &users)
		for _, u := range users {
			if u.ID == bob.ID {
				return u
			}
		}
		t.Fatal("bob not listed")
		return apitypes.User{}
	}
	if u := counts(); u.Sessions != 1 || u.Tokens != 1 {
		t.Fatalf("bob = %+v, want 1 session and 1 token", u)
	}

	path := fmt.Sprintf("/api/v1/users/%d/reset-password", bob.ID)
	resp, body := f.as(admin, "POST", path, nil)
	var res apitypes.ResetPasswordResult
	_ = json.Unmarshal(body, &res)
	if resp.StatusCode != 200 || len(res.Password) < 12 || res.User.Email != "bob@example.com" {
		t.Fatalf("reset: %d %s", resp.StatusCode, body)
	}
	if resp, _ := f.as(bobSession, "GET", "/api/v1/me", nil); resp.StatusCode != 401 {
		t.Fatalf("session survived the reset: %d", resp.StatusCode)
	}
	if resp, _ := f.loginAs("bob@example.com", "bob's long password"); resp.StatusCode != 401 {
		t.Fatal("old password still works")
	}
	if resp, _ := f.loginAs("bob@example.com", res.Password); resp.StatusCode != 200 {
		t.Fatal("generated password doesn't work")
	}
	if u := counts(); u.Sessions != 1 || u.Tokens != 1 {
		t.Fatalf("bob = %+v; tokens stay, the new login is the one session", u)
	}

	// An admin-chosen password, which must be long enough; and not your own.
	if resp, body := f.as(admin, "POST", path, apitypes.ResetPasswordRequest{Password: "short"}); resp.StatusCode != 422 {
		t.Fatalf("short password: %d %s", resp.StatusCode, body)
	}
	resp, body = f.as(admin, "POST", path, apitypes.ResetPasswordRequest{Password: "a brand new password"})
	res = apitypes.ResetPasswordResult{}
	_ = json.Unmarshal(body, &res)
	if resp.StatusCode != 200 || res.Password != "" {
		t.Fatalf("set password: %d %s", resp.StatusCode, body)
	}
	_, body = f.as(admin, "GET", "/api/v1/me", nil)
	var me apitypes.Me
	_ = json.Unmarshal(body, &me)
	_, body = f.as(admin, "GET", "/api/v1/users", nil)
	var users []apitypes.User
	_ = json.Unmarshal(body, &users)
	for _, u := range users {
		if u.Email == me.Email {
			if resp, _ := f.as(admin, "POST", fmt.Sprintf("/api/v1/users/%d/reset-password", u.ID), nil); resp.StatusCode != 400 {
				t.Fatalf("resetting your own password: %d", resp.StatusCode)
			}
		}
	}
}
