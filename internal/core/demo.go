package core

import (
	"context"
	"database/sql"
	"errors"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Demo mode (ADR 0100): a daemon on the fake runtime whose sign-in page
// signs every visitor in as one shared user, [demo] user. cmd/dock-demo
// seeds it and creates that user last, so the demo is ready once the
// user exists.

// Demo reports whether this daemon is the public demo.
func (c *Core) Demo() bool { return c.cfg.Demo.Enabled() }

// errDemoStarting is DemoSignIn's answer while the demo is being seeded.
var errDemoStarting = errf(apitypes.CodeServerOffline, "the demo is starting; try again in a few seconds")

// DemoReady reports whether the demo's user exists yet: the demo's
// health check (GET /auth/demo), so traffic moves to a new demo only
// once it is seeded. Once true it stays true without asking the store.
func (c *Core) DemoReady(ctx context.Context) (bool, error) {
	if !c.Demo() {
		return false, notFound("this Dock isn't a demo")
	}
	if c.demoReady.Load() {
		return true, nil
	}
	_, err := c.demoUser(ctx)
	if errors.Is(err, errDemoStarting) {
		return false, nil
	}
	return err == nil, err
}

// DemoSignIn signs a visitor in as the demo's user, without a password.
// The session counts as confirmed until it ends, so nothing asks a
// visitor for a password they don't have (requireRecent).
func (c *Core) DemoSignIn(ctx context.Context, clientIP string) (LoginResult, error) {
	if !c.Demo() {
		return LoginResult{}, notFound("this Dock isn't a demo; sign in with your account")
	}
	u, err := c.demoUser(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	res, err := c.signIn(ctx, u, signInVia{}, clientIP)
	if err != nil || res.Pending {
		return res, err
	}
	if err := c.store.SetSessionFactor(ctx, store.SetSessionFactorParams{VerifiedAt: &res.Expires, ID: res.Actor.SessionID}); err != nil {
		return LoginResult{}, err
	}
	return res, nil
}

// demoUser loads the demo's user, errDemoStarting while it doesn't exist.
func (c *Core) demoUser(ctx context.Context) (store.User, error) {
	u, err := c.store.GetUserByEmail(ctx, c.cfg.Demo.User)
	if errors.Is(err, sql.ErrNoRows) {
		return store.User{}, errDemoStarting
	}
	if err != nil {
		return store.User{}, err
	}
	c.demoReady.Store(true)
	return u, nil
}
