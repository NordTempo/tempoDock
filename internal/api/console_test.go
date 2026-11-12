package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// The platform console doesn't depend on the org the operator is in (ADR
// 0062): every request its pages make answers the same whichever org the
// session acts in. Org information comes only from the Organizations
// page's own requests, which name the org in their path.
func TestConsoleIgnoresTheCurrentOrg(t *testing.T) {
	f := newFixture(t)
	cookie := f.login() // ada, an operator and an admin of org "test"
	call := func(method, path, org string, body any) []byte {
		t.Helper()
		var r *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		} else {
			r = bytes.NewReader(nil)
		}
		req, _ := http.NewRequest(method, f.srv.URL+path, r)
		req.AddCookie(cookie)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", f.srv.URL)
		if org != "" {
			req.Header.Set("Dock-Org", org)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out bytes.Buffer
		_, _ = out.ReadFrom(resp.Body)
		if resp.StatusCode/100 != 2 {
			t.Fatalf("%s %s as %q: %d %s", method, path, org, resp.StatusCode, out.Bytes())
		}
		return out.Bytes()
	}
	// A second org with its own project, so anything read through the
	// current org would differ.
	call("POST", "/api/v1/orgs", "", apitypes.CreateOrgRequest{Slug: "acme", Name: "Acme"})
	call("POST", "/api/v1/projects", "test", apitypes.CreateProjectRequest{Name: "store"})
	call("POST", "/api/v1/projects", "acme", apitypes.CreateProjectRequest{Name: "blog"})

	var servers []apitypes.Server
	if err := json.Unmarshal(call("GET", "/api/v1/servers", "test", nil), &servers); err != nil || len(servers) == 0 {
		t.Fatalf("servers = %v, %v", servers, err)
	}
	paths := []string{
		"/api/v1/console/orgs",
		"/api/v1/console/orgs/acme/members",
		"/api/v1/console/orgs/acme/invites",
		"/api/v1/console/users",
		"/api/v1/console/data-policy",
		"/api/v1/alerts?platform=true",
		"/api/v1/alerts?platform=true&all=true",
		"/api/v1/alerts/channels?platform=true",
		"/api/v1/audit?platform=true",
		"/api/v1/servers",
		"/api/v1/servers/" + servers[0].Name,
		"/api/v1/registry",
		"/api/v1/backup",
		"/api/v1/storage",
		"/api/v1/cloudflare?platform=true",
		"/api/v1/github",
		"/api/v1/analytics",
		"/api/v1/analytics/defaults",
		"/api/v1/update",
		"/api/v1/address",
		"/api/v1/platform/security",
	}
	clearOfStatsTick(t, call, servers[0].Name)
	for _, p := range paths {
		a, b := call("GET", p, "test", nil), call("GET", p, "acme", nil)
		if !bytes.Equal(a, b) {
			t.Errorf("GET %s differs by the current org:\n test: %s\n acme: %s", p, a, b)
		}
	}
}

// clearOfStatsTick returns when the next minute's statistics tick
// (internal/core/stats.go) is at least 10 s away, so a few seconds of
// requests all see the same samples: a tick stores new usage for every
// server and org, and would make two requests either side of it differ.
// When the tick is closer, it waits until server's usage shows it.
func clearOfStatsTick(t *testing.T, call func(method, path, org string, body any) []byte, server string) {
	t.Helper()
	next := time.Now().Truncate(time.Minute).Add(time.Minute)
	if time.Until(next) >= 10*time.Second {
		return
	}
	for deadline := next.Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var got struct {
			Server apitypes.Server `json:"server"`
		}
		if err := json.Unmarshal(call("GET", "/api/v1/servers/"+server, "test", nil), &got); err != nil {
			t.Fatal(err)
		}
		if u := got.Server.Usage; u != nil && !u.Time.Before(next) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no statistics tick at %s", next.Format(time.TimeOnly))
		}
	}
}
