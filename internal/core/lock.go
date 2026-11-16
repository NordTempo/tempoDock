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
	return &apitypes.EnvironmentLock{LockedAt: *e.LockedAt, LockedBy: e.LockedBy, Reason: e.LockReason, OpenUntil: e.OpenUntil, People: e.LockPeople}
}

// lockedOut reports whether e's lock stops the actor: it is a token (or,
// in an environment locked for people too, a person who doesn't approve
// there), the lock holds, and no approved request lets this call through. A
// check-only replay (CheckRequest) is always stopped, so it never runs.
func (a Actor) lockedOut(e store.Environment) bool {
	switch {
	case a.checkOnly || a.askFirst:
		return true
	case a.granted == e.ID || !lockHolds(e, now()):
		return false
	case a.Kind == "token":
		return true
	}
	// A lock for people too stops a person, but one who may approve here.
	return e.LockPeople && a.Kind == "user" && !a.approves(e.ProjectID, e.ID, envKind(e))
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
	// was refused, as checkEnv was told ("setting variables"). Service is
	// the service the call changes, when it loaded one (serviceFor).
	Project, Environment, Action, Service string
	// Request is the access request filed for the call, 0 before one is.
	Request int64
	// askedFirst: the environment isn't locked; the caller asked first.
	askedFirst bool
	// envKind is the environment's kind (envKind), for who sees its request.
	envKind uint8
	// askOwner is the person a personal agent asks first (their agent
	// setting): they decide the request, not the approvers. owner names
	// them.
	askOwner *int64
	owner    string
	// person: a person's change in an environment locked for people too.
	person bool
	// rule: the org's agent rules ask an approver first here.
	rule bool
}

func (e *LockedError) Error() string { return e.err.Msg }

// Unwrap makes the error a core.Error for CodeOf and errors.As.
func (e *LockedError) Unwrap() error { return e.err }

// AccessRequest returns the request filed for the call (0 for none); the
// MCP endpoint reads it through this method.
func (e *LockedError) AccessRequest() int64 { return e.Request }

// ruleAskErr is checkEnv's error when the org's agent rules ask an
// approver first: a LockedError the approvers decide.
func (a Actor) ruleAskErr(p store.Project, e store.Environment, action string) *LockedError {
	le := &LockedError{OrgID: p.OrgID, ProjectID: p.ID, EnvID: e.ID, Project: p.Name, Environment: e.Name, Action: action, rule: true}
	le.err = &Error{Code: apitypes.CodeEnvironmentLocked, Msg: le.message()}
	return le
}

// askOwnerErr is checkEnv's error when a personal agent's change waits
// for its person (asksOwner): a LockedError their approval lets through.
func (a Actor) askOwnerErr(p store.Project, e store.Environment, action string) *LockedError {
	le := &LockedError{OrgID: p.OrgID, ProjectID: p.ID, EnvID: e.ID, Project: p.Name, Environment: e.Name, Action: action,
		envKind: envKind(e), askOwner: &a.UserID, owner: a.Email}
	le.err = &Error{Code: apitypes.CodeEnvironmentLocked, Msg: le.message()}
	return le
}

// lockedErr is the error checkEnv returns when the lock stops a token.
func (a Actor) lockedErr(p store.Project, e store.Environment, action string) *LockedError {
	le := &LockedError{OrgID: p.OrgID, ProjectID: p.ID, EnvID: e.ID, Project: p.Name, Environment: e.Name, Action: action, envKind: envKind(e),
		askedFirst: a.askFirst && (a.Kind != "token" || !lockHolds(e, now())), person: a.Kind == "user" && !a.askFirst && !a.checkOnly}
	le.err = &Error{Code: apitypes.CodeEnvironmentLocked, Msg: le.message()}
	return le
}

// message says what was refused and what to do next.
func (e *LockedError) message() string {
	follow := fmt.Sprintf("An admin approves or rejects it in the UI (the Inbox); approved, it runs as you. Follow it with `dock request wait %d`", e.Request)
	switch {
	case e.person && e.Request != 0:
		return fmt.Sprintf("environment %s/%s is protected for people too: %s waits for an approver as access request #%d. They approve or reject it in the Inbox; approved, it runs as you", e.Project, e.Environment, e.Action, e.Request)
	case e.person:
		return fmt.Sprintf("environment %s/%s is protected for people too: %s needs an approver; ask one to approve it or to open the environment for a while", e.Project, e.Environment, e.Action)
	case e.rule && e.Request != 0:
		return fmt.Sprintf("%s in %s/%s waits for an approver as access request #%d: the organization's agent rules ask first there. %s", e.Action, e.Project, e.Environment, e.Request, follow)
	case e.rule:
		return fmt.Sprintf("%s in %s/%s needs an approver first: the organization's agent rules ask there; give a reason and wait for the request", e.Action, e.Project, e.Environment)
	case e.askOwner != nil && e.Request != 0:
		return fmt.Sprintf("%s in %s/%s waits for %s's approval as access request #%d: they asked their agents to ask first. They approve or reject it in the UI (the Inbox); approved, it runs as you. Follow it with `dock request wait %d`", e.Action, e.Project, e.Environment, e.owner, e.Request, e.Request)
	case e.askOwner != nil:
		return fmt.Sprintf("%s in %s/%s needs %s's approval first: they asked their agents to ask; ask with a reason and wait for them", e.Action, e.Project, e.Environment, e.owner)
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
// stops a token, and one locked for people too a person who doesn't
// approve there. One query, and only for tokens and people.
func (c *Core) checkProjectLocks(ctx context.Context, a Actor, p store.Project, action string) error {
	if a.Kind != "token" && a.Kind != "user" {
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
	if es.env.LockedAt != nil && es.env.LockPeople && !req.People {
		// Letting people change it again loosens it: an approver's call.
		if err := c.loosens(a, es, "letting people change a protected environment"); err != nil {
			return apitypes.Environment{}, err
		}
	}
	summary := "locked: tokens can't change anything in it"
	if req.People {
		summary = "locked for people too: only its approvers change it; everyone else's changes wait for them"
	}
	return c.setLock(ctx, a, es, store.SetEnvironmentLockParams{LockedAt: &at, LockedBy: a.Name, LockReason: req.Reason, LockPeople: req.People, ID: es.env.ID},
		"environment.lock", summary+reasonSuffix(req.Reason))
}

// UnlockEnvironment unlocks an environment, or with Minutes opens it for
// that long, and expires its pending access requests. Loosening it is
// for whoever may approve there (loosens); a token asking to unlock gets
// an access request like any other change.
func (c *Core) UnlockEnvironment(ctx context.Context, a Actor, ref EnvRef, req apitypes.UnlockEnvironmentRequest) (apitypes.Environment, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeRead, "unlocking an environment")
	if err != nil {
		return apitypes.Environment{}, err
	}
	if err := c.loosens(a, es, "unlocking an environment"); err != nil {
		return apitypes.Environment{}, err
	}
	if req.Minutes < 0 || req.Minutes > 7*24*60 {
		return apitypes.Environment{}, invalid("minutes must be between 0 (unlock) and %d (a week)", 7*24*60)
	}
	if es.env.LockedAt == nil {
		return c.toEnv(ctx, a, es)
	}
	var out apitypes.Environment
	if req.Minutes == 0 {
		out, err = c.setLock(ctx, a, es, store.SetEnvironmentLockParams{ID: es.env.ID}, "environment.unlock", "unlocked")
	} else {
		until := now().Add(time.Duration(req.Minutes) * time.Minute)
		out, err = c.setLock(ctx, a, es, store.SetEnvironmentLockParams{
			LockedAt: es.env.LockedAt, LockedBy: es.env.LockedBy, LockReason: es.env.LockReason, OpenUntil: &until, LockPeople: es.env.LockPeople, ID: es.env.ID,
		}, "environment.unlock", fmt.Sprintf("opened for %s: tokens may change it until %s, then it locks again", time.Duration(req.Minutes)*time.Minute, until.Format("15:04 MST")))
	}
	if err == nil {
		// Tokens may make their calls themselves now.
		c.endUnlockedRequests(ctx, es.env.ID)
	}
	return out, err
}

// loosens checks the actor may loosen es's lock (ADR 0080: loosening a
// protection needs a current approver): the lock applies to a token,
// which gets an access request; a person needs approve there, and a
// recent sign-in (ADR 0057).
func (c *Core) loosens(a Actor, es envScope, action string) error {
	if a.Kind == "token" {
		if err := a.checkLock(es, action); err != nil {
			return err
		}
	}
	if !a.approves(es.project.ID, es.env.ID, envKind(es.env)) {
		return a.refuse(apitypes.Refusal{
			DecidedBy: apitypes.RefusedGrants, Action: action, Need: "approve", Place: es.String(),
			Reason: fmt.Sprintf("%s can't do %s: it loosens the lock, which takes someone who may approve in %s", a, action, es),
			Fix:    "an approver there does it (the Approvers group, or Full access)",
		})
	}
	return a.requireRecent(action)
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
	return c.toEnv(ctx, a, es)
}

// toEnv is an environment as the API shows it to a (toEnvironment).
func (c *Core) toEnv(ctx context.Context, a Actor, es envScope) (apitypes.Environment, error) {
	srv, err := c.store.GetServer(ctx, es.env.ServerID)
	if err != nil {
		return apitypes.Environment{}, err
	}
	return c.toEnvironment(a, es.project, es.env, srv), nil
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
