package core

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// A guesser hammering one account from their address is refused, but the
// account's owner still signs in from theirs.
func TestLoginGuesserCantLockOutTheOwner(t *testing.T) {
	h := newHarness(t)
	h.user("ada@example.com", apitypes.ScopeAdmin)
	var err error
	for range 50 {
		_, err = h.c.Login(h.ctx, "ada@example.com", "wrong guess", "198.51.100.7")
	}
	if CodeOf(err) != apitypes.CodeRateLimited {
		t.Fatalf("guesser: %v", err)
	}
	if _, err := h.c.Login(h.ctx, "ADA@example.com", "password-123456", "192.0.2.1"); err != nil {
		t.Fatalf("owner from their own address: %v", err)
	}
}
