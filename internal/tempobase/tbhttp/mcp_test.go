package tbhttp

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
)

// The MCP endpoint serves tempoBase's tools to tokens only; a token
// limited to one connection sees only it, and the data tools default to
// it.
func TestMCP(t *testing.T) {
	srv, _, code := testServer(t)
	admin := &apiClient{t: t, srv: srv}
	admin.must(http.StatusOK, http.MethodPost, "/setup", tbtypes.SetupRequest{Code: code, Email: "admin@example.com", Name: "Admin", Password: "correct horse battery"}, nil)
	for _, name := range []string{"orders", "billing"} {
		admin.must(http.StatusOK, http.MethodPost, "/connections", tbtypes.CreateConnectionRequest{Name: name, URL: "postgres://app:pw-secret@127.0.0.1:1/" + name + "?sslmode=disable"}, nil)
	}
	var created tbtypes.CreatedToken
	admin.must(http.StatusOK, http.MethodPost, "/tokens", tbtypes.CreateTokenRequest{
		Name: "agent", Connections: []string{"orders"},
		Data: &apitypes.DataTokenAccess{Production: apitypes.LevelNone, NonProduction: apitypes.LevelRead, Sensitive: apitypes.SensitiveMasked},
	}, &created)

	rpc := func(bearer, body string) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+Prefix+"/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	tool := func(name, args string) string {
		t.Helper()
		status, body := rpc(created.Secret, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":`+args+`}}`)
		if status != http.StatusOK {
			t.Fatalf("%s = %d %s", name, status, body)
		}
		return body
	}

	if status, _ := rpc("", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); status != http.StatusUnauthorized {
		t.Fatalf("no token = %d", status)
	}
	_, body := rpc(created.Secret, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var list struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range list.Result.Tools {
		names = append(names, tl.Name)
	}
	if got := strings.Join(names, ","); got != "whoami,list_connections,data_status,data_schema,data_rows,data_query,propose_data_changes" {
		t.Fatalf("tools = %s", got)
	}

	if body := tool("whoami", `{}`); !strings.Contains(body, `token \"agent\" of admin@example.com (role admin), limited to orders`) {
		t.Fatalf("whoami: %s", body)
	}
	if body := tool("list_connections", `{}`); !strings.Contains(body, "1 connections: orders.") || strings.Contains(body, "billing") {
		t.Fatalf("list_connections: %s", body)
	}
	// No connection: the token's only one. Nothing is set up, so the
	// database isn't contacted.
	if body := tool("data_status", `{}`); !strings.Contains(body, "orders: postgres setup_needed, your access read") {
		t.Fatalf("data_status: %s", body)
	}
	if body := tool("data_status", `{"connection":"billing"}`); !strings.Contains(body, `"isError":true`) || !strings.Contains(body, "not found") {
		t.Fatalf("data_status on another connection: %s", body)
	}
}
