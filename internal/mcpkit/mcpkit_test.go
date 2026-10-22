package mcpkit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
)

// connOps is a tempoBase-shaped host: the actor is a name, a database is
// a connection name. Only DataInfo is implemented.
type connOps struct {
	datapanel.Ops[string, string]
	gotActor, gotRef string
}

func (o *connOps) DataInfo(_ context.Context, a, ref string) (apitypes.DataInfo, error) {
	o.gotActor, o.gotRef = a, ref
	return apitypes.DataInfo{Engine: apitypes.EnginePostgres, Status: apitypes.DataReady}, nil
}

type connInput struct {
	Connection string `json:"connection" doc:"Connection name."`
}

func newConnServer(ops *connOps) *Server[string] {
	return New(Options[string]{
		Name: "test", Realm: "test", NoToken: "send a token",
		Authenticate: func(_ context.Context, token string) (string, error) {
			if token != "good" {
				return "", datapanel.Errorf(apitypes.CodeUnauthorized, "bad token")
			}
			return "agent", nil
		},
		Tools: func(s *Server[string]) {
			DataTools(s, datapanel.Ops[string, string](ops), func(_ *Call[string], in connInput) (string, error) { return in.Connection, nil },
				func(ref string) string { return ref })
		},
	})
}

func rpc(t *testing.T, h http.Handler, token, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

// TestDataToolsLocator: the host's locator is merged into every data
// tool's schema, validated and passed to the operations with the actor.
func TestDataToolsLocator(t *testing.T) {
	ops := &connOps{}
	s := newConnServer(ops)
	if code, _ := rpc(t, s, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	_, body := rpc(t, s, "good", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var list struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Properties map[string]any `json:"properties"`
					Required   []string       `json:"required"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list.Result.Tools) != 5 {
		t.Fatalf("tools/list: %v %s", err, body)
	}
	for _, tool := range list.Result.Tools {
		if _, ok := tool.InputSchema.Properties["connection"]; !ok || !strings.Contains(strings.Join(tool.InputSchema.Required, ","), "connection") {
			t.Errorf("%s lacks the locator: %+v", tool.Name, tool.InputSchema)
		}
	}
	_, body = rpc(t, s, "good", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"data_status","arguments":{"connection":"orders"}}}`)
	if ops.gotActor != "agent" || ops.gotRef != "orders" || !strings.Contains(body, "orders: postgres ready") {
		t.Fatalf("data_status: %s/%s %s", ops.gotActor, ops.gotRef, body)
	}
	_, body = rpc(t, s, "good", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"data_status","arguments":{"connection":"orders","service":"db"}}}`)
	if !strings.Contains(body, `"isError":true`) || !strings.Contains(body, "invalid arguments") {
		t.Fatalf("unknown property accepted: %s", body)
	}
}
