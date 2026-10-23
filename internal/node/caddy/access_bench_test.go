package caddy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/access"
)

// BenchmarkAccessHandler is the access check Caddy makes before every
// request to a login-protected service (ADR 0030): a signed-in visitor.
func BenchmarkAccessHandler(b *testing.B) {
	key := access.NewKey()
	h := AccessHandler()
	session := access.Sign(key, access.Session, "app.example.com", "ada@example.com", time.Now().Add(time.Hour))
	r := checkRequest(key, "GET", "/api/orders?page=2", http.Header{"Cookie": {accessCookie + "=" + session}})
	b.ReportAllocs()
	for b.Loop() {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent {
			b.Fatal(w.Code)
		}
	}
}
