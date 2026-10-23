package mcp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// BenchmarkToolCall is one stateless MCP tool call through the daemon's
// handler: bearer auth, a throwaway SDK session, the tool and its core
// calls, and the SSE response. whoami needs nothing past auth;
// service_status reads the service and its releases.
func BenchmarkToolCall(b *testing.B) {
	f := newFixture(b)
	ref := core.ServiceRef{Project: "shop", Environment: "staging", Service: "web"}
	if _, err := f.core.DeployImage(context.Background(), sys, ref, apitypes.DeployImageRequest{}); err != nil {
		b.Fatal(err)
	}
	for _, tool := range []string{"whoami", "service_status"} {
		b.Run("tool="+tool, func(b *testing.B) {
			body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":{"service":"web"}}}`
			if tool == "whoami" {
				body = strings.Replace(body, `{"service":"web"}`, `{}`, 1)
			}
			b.ReportAllocs()
			for b.Loop() {
				req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp", strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "application/json, text/event-stream")
				req.Header.Set("Authorization", "Bearer "+f.tokens["shop-agent"])
				w := httptest.NewRecorder()
				f.handler.ServeHTTP(w, req)
				if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"isError":true`) {
					b.Fatalf("%d %s", w.Code, w.Body)
				}
			}
		})
	}
}
