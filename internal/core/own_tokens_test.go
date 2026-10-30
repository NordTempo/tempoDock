package core

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// A user lists and revokes the tokens they created, whatever their role
// now; others' tokens stay out of reach without admin.
func TestOwnTokens(t *testing.T) {
	h := newHarness(t)
	ada := h.cliUser("ada@example.com", apitypes.ScopeAdmin)
	bob := h.cliUser("bob@example.com", apitypes.ScopeRead)
	ci, err := h.c.CreateToken(h.ctx, ada, apitypes.CreateTokenRequest{Name: "ci", Scope: apitypes.ScopeDeploy})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.CreateToken(h.ctx, testSystem, apitypes.CreateTokenRequest{Name: "system", Scope: apitypes.ScopeRead}); err != nil {
		t.Fatal(err)
	}

	mine, err := h.c.ListOwnTokens(h.ctx, ada)
	if err != nil || len(mine) != 1 || mine[0].ID != ci.Token.ID {
		t.Fatalf("ada's tokens = %+v, %v; want only ci", mine, err)
	}
	if theirs, err := h.c.ListOwnTokens(h.ctx, bob); err != nil || len(theirs) != 0 {
		t.Fatalf("bob's tokens = %+v, %v; want none", theirs, err)
	}
	if err := h.c.RevokeToken(h.ctx, bob, ci.Token.ID); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("bob revoking ada's token: %v, want forbidden", err)
	}

	demoted := ada
	demoted.Scope = apitypes.ScopeRead
	if err := h.c.RevokeToken(h.ctx, demoted, ci.Token.ID); err != nil {
		t.Fatalf("ada revoking her own token as a reader: %v", err)
	}
	if _, err := h.c.AuthenticateToken(h.ctx, ci.Secret, ""); CodeOf(err) != apitypes.CodeUnauthorized {
		t.Fatalf("revoked token still works: %v", err)
	}
}
