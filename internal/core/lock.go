package core

import (
	"context"
	"fmt"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Locked environments (ADR 0044). A locked environment stops tokens (API
// tokens, `dock login` tokens, MCP) from changing anything in it; humans
// in a UI session and the daemon itself are unaffected. checkEnv applies
// the lock, so every environment operation gets it without code of its
// own. An environment can be opened for a while: until open_until the
// lock doesn't hold, and after it holds again with no timer involved.

// lockHolds reports whether e's lock applies at t.
func lockHolds(e store.Environment, t time.Time) bool {
	return e.LockedAt != nil && (e.OpenUntil == nil || !t.Before(*e.OpenUntil))
}

// envLock is e's lock as the API shows it; nil when unlocked.
func envLock(e store.Environment) *apitypes.EnvironmentLock {
	if e.LockedAt == nil {
		return nil
	}
	return &apitypes.EnvironmentLock{LockedAt: *e.LockedAt, LockedBy: e.LockedBy, Reason: e.LockReason, OpenUntil: e.OpenUntil}
}

// lockedOut reports whether e's lock stops the actor: it is a token, the
// lock holds, and no approved request lets this call through.
func (a Actor) lockedOut(e store.Environment) bool {
	return a.askFirst || (a.Kind == "token" && a.granted != e.ID && lockHolds(e, now()))
}

// checkLock is checkEnv's lock alone, for the few changes whose scope
// check isn't checkEnv's: operator actions on one service, which load it
// with read scope.
func (a Actor) checkLock(es envScope, action string) error {
	if a.lockedOut(es.env) {
		return a.lockedErr(es.project, es.env, action)
	}
	return nil
}

// LockedError is the error for a token's change in a locked environment.
// It is a core.Error with code environment_locked (Unwrap); the API and
// MCP layers file an access request for the call and set Request.
type LockedError struct {
	err       *Error
	OrgID     int64
	ProjectID int64
	EnvID     int64
	// Project and Environment name the locked environment; Action is what
	// was refused, as checkEnv was told ("setting variables").
	Project, Environment, Action string
	// Request is the access request filed for the call, 0 before one is.
	Request int64
	// askedFirst: the environment isn't locked; the caller asked first.
	askedFirst bool
}

func (e *LockedError) Error() string { return e.err.Msg }

// Unwrap makes the error a core.Error for CodeOf and errors.As.
func (e *LockedError) Unwrap() error { return e.err }

// AccessRequest returns the request filed for the call (0 for none); the
// MCP endpoint reads it through this method.
func (e *LockedError) AccessRequest() int64 { return e.Request }

// lockedErr is the error checkEnv returns when the lock stops a token.
func (a Actor) lockedErr(p store.Project, e store.Environment, action string) *LockedError {
	le := &LockedError{OrgID: p.OrgID, ProjectID: p.ID, EnvID: e.ID, Project: p.Name, Environment: e.Name, Action: action,
		askedFirst: a.askFirst && (a.Kind != "token" || !lockHolds(e, now()))}
	le.err = &Error{Code: apitypes.CodeEnvironmentLocked, Msg: le.message()}
	return le
}

// message says what was refused and what to do next.
func (e *LockedError) message() string {
	follow := fmt.Sprintf("An admin approves or rejects it in the UI (Requests); approved, it runs as you. Follow it with `dock request wait %d`", e.Request)
	switch {
	case e.Request != 0 && e.askedFirst:
		return fmt.Sprintf("%s in %s/%s waits for an admin's approval as access request #%d. %s", e.Action, e.Project, e.Environment, e.Request, follow)
	case e.Request != 0:
		return fmt.Sprintf("environment %s/%s is locked: %s waits for an admin's approval as access request #%d. %s", e.Project, e.Environment, e.Action, e.Request, follow)
	}
	return fmt.Sprintf("environment %s/%s is locked: tokens can't change anything in it (%s); ask an admin to unlock it in the UI, or to open it for a while",
		e.Project, e.Environment, e.Action)
}

// withRequest names the access request filed for the call.
func (e *LockedError) withRequest(id int64) *LockedError {
	out := *e
	out.Request = id
	out.err = &Error{Code: apitypes.CodeEnvironmentLocked, Msg: out.message()}
	return &out
}

// checkProjectLocks is checkEnv's lock for project-wide changes that
// change every environment (archiving a project): any locked environment
// stops a token. One query, and only for tokens.
func (c *Core) checkProjectLocks(ctx context.Context, a Actor, p store.Project, action string) error {
	if a.Kind != "token" {
		return nil
	}
	envs, err := c.store.ListEnvironments(ctx, p.ID)
	if err != nil {
		return err
	}
	for _, e := range envs {
		if a.lockedOut(e) {
			return a.lockedErr(p, e, action)
		}
	}
	return nil
}

// LockEnvironment locks an environment, or keeps it locked with a new
// reason and closes it if it was opened for a while. Admins only; tokens
// may lock, since locking only takes power away.
func (c *Core) LockEnvironment(ctx context.Context, a Actor, ref EnvRef, req apitypes.LockEnvironmentRequest) (apitypes.Environment, error) {
	if err := a.require(apitypes.ScopeAdmin, "locking an environment"); err != nil {
		return apitypes.Environment{}, err
	}
	es, err := c.loadEnv(ctx, a.OrgID, ref)
	if err != nil {
		return apitypes.Environment{}, err
	}
	if err := a.checkEnvAccess(apitypes.ScopeAdmin, "locking an environment", es.project, es.env); err != nil {
		return apitypes.Environment{}, err
	}
	at := now()
	if es.env.LockedAt != nil {
		at = *es.env.LockedAt
	}
	return c.setLock(ctx, a, es, store.SetEnvironmentLockParams{LockedAt: &at, LockedBy: a.Name, LockReason: req.Reason, ID: es.env.ID},
		"environment.lock", "locked: tokens can't change anything in it"+reasonSuffix(req.Reason))
}

// UnlockEnvironment unlocks an environment, or with Minutes opens it for
// that long. Admins only, and the lock applies: a token asking to unlock
// gets an access request like any other change.
func (c *Core) UnlockEnvironment(ctx context.Context, a Actor, ref EnvRef, req apitypes.UnlockEnvironmentRequest) (apitypes.Environment, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeAdmin, "unlocking an environment")
	if err != nil {
		return apitypes.Environment{}, err
	}
	// Unlocking lets tokens change it: a step-up (ADR 0049).
	if err := a.requireRecent("unlocking an environment"); err != nil {
		return apitypes.Environment{}, err
	}
	if req.Minutes < 0 || req.Minutes > 7*24*60 {
		return apitypes.Environment{}, invalid("minutes must be between 0 (unlock) and %d (a week)", 7*24*60)
	}
	if es.env.LockedAt == nil {
		return c.toEnv(ctx, es)
	}
	if req.Minutes == 0 {
		return c.setLock(ctx, a, es, store.SetEnvironmentLockParams{ID: es.env.ID}, "environment.unlock", "unlocked")
	}
	until := now().Add(time.Duration(req.Minutes) * time.Minute)
	return c.setLock(ctx, a, es, store.SetEnvironmentLockParams{
		LockedAt: es.env.LockedAt, LockedBy: es.env.LockedBy, LockReason: es.env.LockReason, OpenUntil: &until, ID: es.env.ID,
	}, "environment.unlock", fmt.Sprintf("opened for %s: tokens may change it until %s, then it locks again", time.Duration(req.Minutes)*time.Minute, until.Format("15:04 MST")))
}

// setLock writes an environment's lock, audits it and tells open pages.
func (c *Core) setLock(ctx context.Context, a Actor, es envScope, params store.SetEnvironmentLockParams, action, summary string) (apitypes.Environment, error) {
	e, err := c.store.SetEnvironmentLock(ctx, params)
	if err != nil {
		return apitypes.Environment{}, err
	}
	es.env = e
	c.audit(ctx, a, action, &es.project, &es.env, es.String(), summary)
	c.bus.publish(busEvent{orgID: es.project.OrgID, projectID: es.project.ID, envID: e.ID, ev: apitypes.Event{
		Type: apitypes.EventEnvironmentLock, Project: es.project.Name, Environment: e.Name, Lock: envLock(e),
	}})
	return c.toEnv(ctx, es)
}

// toEnv is an environment as the API shows it, with its server's name.
func (c *Core) toEnv(ctx context.Context, es envScope) (apitypes.Environment, error) {
	srv, err := c.store.GetServer(ctx, es.env.ServerID)
	if err != nil {
		return apitypes.Environment{}, err
	}
	return toEnvironment(es.project, es.env, srv.Name), nil
}

func reasonSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return " (" + reason + ")"
}

// GetOrgLockSettings returns the org's settings for locked environments.
func (c *Core) GetOrgLockSettings(ctx context.Context, a Actor) (apitypes.OrgLockSettings, error) {
	if err := a.require(apitypes.ScopeRead, "reading the organization's lock settings"); err != nil {
		return apitypes.OrgLockSettings{}, err
	}
	o, err := c.store.GetOrg(ctx, a.OrgID)
	return apitypes.OrgLockSettings{LockProduction: o.LockProduction != 0, RequestTTLHours: int(requestTTL(o) / time.Hour)}, err
}

// SetOrgLockSettings changes the org's settings for locked environments.
// Unlimited admins. It only decides how new production environments
// start; existing ones keep their lock.
func (c *Core) SetOrgLockSettings(ctx context.Context, a Actor, req apitypes.OrgLockSettings) (apitypes.OrgLockSettings, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "changing the organization's lock settings"); err != nil {
		return apitypes.OrgLockSettings{}, err
	}
	if req.RequestTTLHours < 0 || req.RequestTTLHours > 720 {
		return apitypes.OrgLockSettings{}, invalid("request_ttl_hours must be between 1 and 720 (30 days)")
	}
	var on int64
	if req.LockProduction {
		on = 1
	}
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.SetOrgLockProduction(ctx, store.SetOrgLockProductionParams{LockProduction: on, ID: a.OrgID}); err != nil {
			return err
		}
		if req.RequestTTLHours == 0 {
			return nil
		}
		return q.SetOrgRequestTTL(ctx, store.SetOrgRequestTTLParams{RequestTtlHours: int64(req.RequestTTLHours), ID: a.OrgID})
	})
	if err != nil {
		return apitypes.OrgLockSettings{}, err
	}
	state := "no longer start locked"
	if req.LockProduction {
		state = "start locked"
	}
	out, err := c.GetOrgLockSettings(ctx, a)
	c.audit(ctx, a, "org.lock_settings", nil, nil, a.OrgSlug, fmt.Sprintf("new production environments %s; access requests wait %d hours", state, out.RequestTTLHours))
	return out, err
}
