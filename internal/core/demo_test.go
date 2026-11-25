package core

import (
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// The public demo (ADR 0100) signs visitors in once its user exists, and
// their sessions never ask for a password they don't have.
func TestDemoSignIn(t *testing.T) {
	h := newHarness(t)
	if _, err := h.c.DemoSignIn(h.ctx, "203.0.113.1"); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("not a demo: %v, want not found", err)
	}

	h.opts.Config.Demo.User = "demo@acme.example"
	h.restart()
	if ready, err := h.c.DemoReady(h.ctx); err != nil || ready {
		t.Fatalf("ready before its user exists: %v %v", ready, err)
	}
	if _, err := h.c.DemoSignIn(h.ctx, "203.0.113.1"); CodeOf(err) != apitypes.CodeServerOffline {
		t.Fatalf("signed in while starting: %v", err)
	}

	if _, err := h.c.CreateUser(h.ctx, testSystem, apitypes.CreateUserRequest{
		Email: "demo@acme.example", Name: "Demo", Password: "a long enough password", Role: apitypes.ScopeAdmin,
	}); err != nil {
		t.Fatal(err)
	}
	if ready, err := h.c.DemoReady(h.ctx); err != nil || !ready {
		t.Fatalf("not ready once its user exists: %v %v", ready, err)
	}
	res, err := h.c.DemoSignIn(h.ctx, "203.0.113.1")
	if err != nil || res.Pending || res.Secret == "" {
		t.Fatalf("demo sign-in: %+v %v", res, err)
	}
	a, err := h.c.AuthenticateSession(h.ctx, res.Secret, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if a.Email != "demo@acme.example" {
		t.Fatalf("signed in as %q", a.Email)
	}
	// Confirmed until the session ends: long past ReauthWindow, a
	// sensitive action still goes through.
	if !a.VerifiedAt.After(time.Now().Add(SessionTTL - time.Minute)) {
		t.Fatalf("verified at %v, want the session's end", a.VerifiedAt)
	}
	if err := a.requireRecent("revealing a secret"); err != nil {
		t.Fatal(err)
	}
}
