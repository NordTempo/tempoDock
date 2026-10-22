package tempobase

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
)

// BenchmarkAuthenticateSession is what every UI request pays before its
// handler: one indexed query joining the session to its user.
func BenchmarkAuthenticateSession(b *testing.B) {
	h := newHarness(b)
	h.user("viewer@example.com", tbtypes.RoleViewer)
	secret, _, _, err := h.Login(b.Context(), "viewer@example.com", testPassword, "10.0.0.1")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := h.AuthenticateSession(b.Context(), secret); err != nil {
			b.Fatal(err)
		}
	}
}
