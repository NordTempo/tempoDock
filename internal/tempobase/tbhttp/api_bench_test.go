package tbhttp

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/NordTempo/tempoDock/internal/tempobase"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
)

// BenchmarkListConnections is GET /connections for an editor with 50
// connections and 20 people seeded (ADR 0024 section 5): session lookup,
// the connections, the rules and the policy decision for each.
func BenchmarkListConnections(b *testing.B) {
	srv, app, code := testServer(b)
	admin := &apiClient{t: b, srv: srv}
	admin.must(http.StatusOK, http.MethodPost, "/setup", tbtypes.SetupRequest{Code: code, Email: "admin@example.com", Name: "Admin", Password: "correct horse battery"}, nil)
	for i := range 50 {
		admin.must(http.StatusOK, http.MethodPost, "/connections", tbtypes.CreateConnectionRequest{
			Name: fmt.Sprintf("db-%02d", i), URL: fmt.Sprintf("postgres://app:pw@db%d.example.com/app", i), Production: i%3 == 0,
		}, nil)
	}
	for i := range 20 {
		if _, _, err := app.CreateUser(b.Context(), tempobase.System, fmt.Sprintf("user%d@example.com", i), "User", tbtypes.RoleEditor, "correct horse battery"); err != nil {
			b.Fatal(err)
		}
	}
	editor := &apiClient{t: b, srv: srv}
	editor.must(http.StatusOK, http.MethodPost, "/auth/login", map[string]string{"email": "user0@example.com", "password": "correct horse battery"}, nil)
	var conns []tbtypes.Connection
	editor.must(http.StatusOK, http.MethodGet, "/connections", nil, &conns)
	if len(conns) != 50 {
		b.Fatalf("editor sees %d connections", len(conns))
	}
	b.ReportAllocs()
	for b.Loop() {
		editor.must(http.StatusOK, http.MethodGet, "/connections", nil, nil)
	}
}
