package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// The CLI starts and waits without credentials; only a signed-in user,
// from the dashboard's own origin, can approve.
func TestCLILoginFlow(t *testing.T) {
	f := newFixture(t)
	cookie := f.login()

	resp, body := f.do("", "POST", "/api/v1/auth/cli", apitypes.CLILoginStartRequest{ClientName: "dock CLI", Hostname: "laptop", OS: "linux/amd64"})
	var start apitypes.CLILoginStart
	_ = json.Unmarshal(body, &start)
	if resp.StatusCode != 201 || start.DeviceCode == "" || start.VerificationURL != f.srv.URL+"/cli-login?code="+start.UserCode {
		t.Fatalf("start: %d %s", resp.StatusCode, body)
	}

	type waited struct {
		status int
		body   []byte
	}
	done := make(chan waited, 1)
	go func() {
		resp, body := f.do("", "POST", "/api/v1/auth/cli/wait", apitypes.CLILoginWaitRequest{DeviceCode: start.DeviceCode})
		done <- waited{resp.StatusCode, body}
	}()

	decision := apitypes.CLILoginDecision{UserCode: start.UserCode}
	if resp, body := f.do("", "POST", "/api/v1/auth/cli/approve", decision); resp.StatusCode != 401 {
		t.Fatalf("approve without credentials: %d %s", resp.StatusCode, body)
	}
	if resp, body := f.do("admin", "POST", "/api/v1/auth/cli/approve", decision); resp.StatusCode != 403 {
		t.Fatalf("approve with a token: %d %s", resp.StatusCode, body)
	}
	b, _ := json.Marshal(decision)
	req, _ := http.NewRequest("POST", f.srv.URL+"/api/v1/auth/cli/approve", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.AddCookie(cookie)
	cross, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, cross.Body)
	_ = cross.Body.Close()
	if cross.StatusCode != 403 {
		t.Fatalf("cross-site approve: %d", cross.StatusCode)
	}

	resp, body = f.as(cookie, "GET", "/api/v1/auth/cli/requests/"+start.UserCode, nil)
	var l apitypes.CLILogin
	_ = json.Unmarshal(body, &l)
	if resp.StatusCode != 200 || l.Hostname != "laptop" || l.Scope != apitypes.ScopeAdmin || l.TokenName != "cli: laptop" {
		t.Fatalf("lookup: %d %s", resp.StatusCode, body)
	}
	if resp, body := f.as(cookie, "POST", "/api/v1/auth/cli/approve", decision); resp.StatusCode != 204 {
		t.Fatalf("approve: %d %s", resp.StatusCode, body)
	}

	w := <-done
	var res apitypes.CLILoginWait
	_ = json.Unmarshal(w.body, &res)
	if w.status != 200 || res.Status != "approved" || res.User != "ada@example.com" || res.Scope != apitypes.ScopeAdmin {
		t.Fatalf("wait: %d %s", w.status, w.body)
	}
	resp, body = f.do("", "POST", "/api/v1/auth/cli/wait", apitypes.CLILoginWaitRequest{DeviceCode: start.DeviceCode})
	var p apitypes.Error
	_ = json.Unmarshal(body, &p)
	if resp.StatusCode != 410 || p.Code != apitypes.CodeExpired {
		t.Fatalf("second wait: %d %s", resp.StatusCode, body)
	}

	// The new token works, and revokes itself on `dock logout`.
	f.tokens["cli"] = res.Token
	if resp, body := f.do("cli", "GET", "/api/v1/me", nil); resp.StatusCode != 200 {
		t.Fatalf("me with the new token: %d %s", resp.StatusCode, body)
	}
	if resp, body := f.as(cookie, "DELETE", "/api/v1/me/token", nil); resp.StatusCode != 400 {
		t.Fatalf("session revoking its token: %d %s", resp.StatusCode, body)
	}
	if resp, body := f.do("cli", "DELETE", "/api/v1/me/token", nil); resp.StatusCode != 204 {
		t.Fatalf("revoke own token: %d %s", resp.StatusCode, body)
	}
	if resp, _ := f.do("cli", "GET", "/api/v1/me", nil); resp.StatusCode != 401 {
		t.Fatalf("revoked token: %d", resp.StatusCode)
	}
}
