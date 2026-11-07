package core

import (
	"fmt"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Actor is who performs an operation: a logged-in user, an API token or the
// system itself. Users and tokens go through the same permission checks.
// An actor acts in one org (ADR 0027): Scope is its role there, and
// everything it reads or changes inside projects is that org's.
type Actor struct {
	Kind    string // "user", "token" or "system"
	Name    string
	Email   string
	UserID  int64
	TokenID int64
	// DisplayName is a signed-in user's own name (Me); empty for tokens.
	DisplayName string
	// OrgID is the org the actor acts in; 0 for the system (every org) and
	// for a user who hasn't chosen one of several (orgs counts them).
	OrgID   int64
	OrgSlug string
	orgs    int
	// suspended: the platform's operator suspended the org (ADR 0062);
	// the actor may read in it, nothing more.
	suspended bool
	Scope     apitypes.Scope
	// Operator may run the platform: servers, backups, user accounts.
	Operator bool
	// ProjectID and EnvironmentID limit a token to one project or one
	// environment. Zero means unlimited.
	ProjectID     int64
	EnvironmentID int64
	// ExpectedProjectID guards a linked client against name reuse during a request.
	ExpectedProjectID int64
	ProjectName       string
	EnvironmentName   string
	// granted is the environment an approved access request lets this one
	// call change despite its lock (ADR 0044); only core sets it, with
	// approval for the audit log.
	granted  int64
	approval *approval
	// checkOnly is CheckRequest's replay: every lock stops it (lockedOut),
	// so the call runs its own checks up to its lock and nothing after.
	checkOnly bool
	// askFirst makes every change in an environment an access request,
	// locked or not (MCP's request_access); changes outside one fail.
	askFirst bool

	// SessionID is the UI session a user signed in with; 0 for tokens and
	// the system. VerifiedAt is when that session last proved who it is,
	// for sensitive actions (requireRecent).
	SessionID  int64
	VerifiedAt time.Time
	// Factor is the second factor the sign-in used ("" for none; see
	// factorTOTP and co.), "login" for a `dock login` token approved
	// from a session that used one (ADR 0057).
	Factor string
	// needs2FAOrg is the org the actor would act in but for its
	// requirement of a second factor; operatorNeeds2FA is set when the
	// platform's requirement took Operator away.
	needs2FAOrg      string
	operatorNeeds2FA bool

	// SSOProviderID is the identity provider the sign-in went through
	// (ADR 0065), SSOProvider its name and SSOLabel its label; zero for a
	// password.
	SSOProviderID int64
	SSOProvider   string
	SSOLabel      string
}

// approval is the access request an actor's call runs for, and who
// approved it.
type approval struct {
	id int64
	by string
}

// AskFirst returns the actor with every change it tries in an
// environment filed as an access request instead of made, locked or not.
func (a Actor) AskFirst() Actor {
	a.askFirst = true
	return a
}

// System is the actor for the daemon's own actions and for trusted local
// callers (tests, admin commands). It is in no org; InOrg gives it one for
// operations that look projects up by name.
var System = Actor{Kind: "system", Name: "system", Scope: apitypes.ScopeAdmin, Operator: true}

// InOrg returns the system actor acting in the org with ID id.
func (a Actor) InOrg(id int64) Actor {
	a.OrgID = id
	return a
}

// platform is the actor outside its org, for auditing platform actions
// (servers, accounts, backup settings) in the platform's log.
func (a Actor) platform() Actor {
	a.OrgID, a.OrgSlug = 0, ""
	return a
}

// Me describes the actor for the API.
func (a Actor) Me() apitypes.Me {
	return apitypes.Me{
		Kind: a.Kind, Name: a.Name, Email: a.Email, DisplayName: a.DisplayName, Scope: a.Scope, Org: a.OrgSlug, Operator: a.Operator,
		Project: a.ProjectName, Environment: a.EnvironmentName,
		TwoFactor: a.Factor, TwoFactorRequired: a.twoFactorRequired(),
		SSO: a.SSOProvider, SSOLabel: a.SSOLabel,
	}
}

// twoFactorRequired says what a second factor the actor didn't use would
// unlock, and how to get it; "" when nothing.
func (a Actor) twoFactorRequired() string {
	const how = "add a passkey or an authenticator app on your account page, or sign in again with yours"
	switch {
	case a.needs2FAOrg != "":
		return fmt.Sprintf("organization %s requires two-factor sign-in; %s", a.needs2FAOrg, how)
	case a.operatorNeeds2FA:
		return "operating the platform requires two-factor sign-in; " + how
	}
	return ""
}

func rank(s apitypes.Scope) int {
	switch s {
	case apitypes.ScopeRead:
		return 1
	case apitypes.ScopeDeploy:
		return 2
	case apitypes.ScopeAdmin:
		return 3
	}
	return 0
}

func validScope(s apitypes.Scope) bool { return rank(s) > 0 }

func (a Actor) String() string {
	if a.Kind == "system" {
		return "system"
	}
	return fmt.Sprintf("%s %q", a.Kind, a.Name)
}

func (a Actor) limited() bool { return a.ProjectID != 0 || a.EnvironmentID != 0 }

// require checks the actor's scope in its org. action completes "…
// requires <scope>", e.g. "archiving a service".
func (a Actor) require(need apitypes.Scope, action string) error {
	if a.askFirst && rank(need) > rank(apitypes.ScopeRead) {
		return invalid("only changes inside one environment can be asked for; %s isn't one", action)
	}
	return a.requireScope(need, action)
}

// requireScope is require without askFirst's refusal, for the checks of
// operations inside an environment, where checkEnv files the request.
func (a Actor) requireScope(need apitypes.Scope, action string) error {
	if a.OrgID == 0 && a.Kind != "system" {
		return a.noOrg(action)
	}
	if a.suspended && rank(need) > rank(apitypes.ScopeRead) {
		return errSuspended(a.OrgSlug, action)
	}
	if rank(a.Scope) >= rank(need) {
		return nil
	}
	return errf(apitypes.CodeForbidden, "%s has scope %s; %s requires %s", a, a.Scope, action, need)
}

// errSuspended is why nothing changes in a suspended org.
func errSuspended(org, action string) error {
	return errf(apitypes.CodeConflict, "organization %s is suspended by the platform's operator: its apps keep running, but %s and every other change wait until it is unsuspended; ask the operator", org, action)
}

// noOrg explains why an actor without an org can't do action.
func (a Actor) noOrg(action string) error {
	if a.needs2FAOrg != "" {
		return errf(apitypes.CodeTwoFactorRequired, "%s", a.twoFactorRequired())
	}
	if a.orgs > 1 {
		return errf(apitypes.CodeValidation, "%s belongs to %d organizations; %s needs one: pass --org (or ?org=) with its slug, see `dock org list`", a, a.orgs, action)
	}
	return errf(apitypes.CodeForbidden, "%s doesn't belong to any organization; %s needs one: ask an organization's admin to add you", a, action)
}

// requireOperator checks the actor may run the platform: servers, backups,
// user accounts. Operator tokens are always unlimited admin tokens.
func (a Actor) requireOperator(action string) error {
	if a.Operator {
		return nil
	}
	if a.operatorNeeds2FA {
		return errf(apitypes.CodeTwoFactorRequired, "%s requires two-factor sign-in: %s", action, a.twoFactorRequired())
	}
	return errf(apitypes.CodeForbidden, "%s is not a platform operator; %s requires one", a, action)
}

// requireAccountManager guards creating, resetting, enabling, disabling and
// deleting user accounts: an operator, but never a token. A token that could
// mint or reset an admin account could sign in as it and get around
// environment locks and access-request approvals, which only bind tokens.
func (a Actor) requireAccountManager(action string) error {
	if err := a.requireOperator(action); err != nil {
		return err
	}
	if a.Kind == "token" {
		return errf(apitypes.CodeForbidden, "%s is for a person, not %s; ask a person to do it from a signed-in session in the web UI", action, a)
	}
	return nil
}

// requireUnlimited is for operations outside any one project, like managing
// tokens and users.
func (a Actor) requireUnlimited(need apitypes.Scope, action string) error {
	if err := a.require(need, action); err != nil {
		return err
	}
	if a.limited() {
		return errf(apitypes.CodeForbidden, "%s is limited to %s; %s requires an unlimited %s token", a, a.limitName(), action, need)
	}
	return nil
}

// readsServerStats reports whether the actor may read server-wide
// statistics and platform events: operators, as requireOperator checks.
func (a Actor) readsServerStats() bool { return a.Operator }

func (a Actor) limitName() string {
	if a.EnvironmentID != 0 {
		return fmt.Sprintf("environment %s/%s", a.ProjectName, a.EnvironmentName)
	}
	return fmt.Sprintf("project %s", a.ProjectName)
}

// canSee reports whether the actor may read things in the org, project
// and environment (envID 0: project-level things). Things in no org are
// the platform's, for operators.
func (a Actor) canSee(orgID, projectID, envID int64) bool {
	if orgID == 0 {
		return a.Operator
	}
	if a.Kind != "system" && a.OrgID != orgID {
		return false
	}
	if a.ProjectID != 0 && a.ProjectID != projectID {
		return false
	}
	if a.EnvironmentID != 0 && a.EnvironmentID != envID {
		return false
	}
	return true
}

// inOrg checks the project is in the actor's org. Lookups by name only
// find the actor's org's projects; this guards lookups by ID.
func (a Actor) inOrg(p store.Project) error {
	if a.Kind == "system" || p.OrgID == a.OrgID {
		if a.ExpectedProjectID != 0 && a.ExpectedProjectID != p.ID {
			return conflict("project name changed; retry or link this directory again with `dock link`")
		}
		return nil
	}
	return notFound("project %q not found", p.Name)
}

// checkProject checks scope and limits for a project-level operation.
// Environment-limited actors may read the project but not change it.
func (a Actor) checkProject(need apitypes.Scope, action string, p store.Project) error {
	if err := a.require(need, action); err != nil {
		return err
	}
	if err := a.inOrg(p); err != nil {
		return err
	}
	if a.ProjectID != 0 && a.ProjectID != p.ID {
		return errf(apitypes.CodeForbidden, "%s is limited to project %s; it cannot access project %s", a, a.ProjectName, p.Name)
	}
	if a.EnvironmentID != 0 && rank(need) > rank(apitypes.ScopeRead) {
		return errf(apitypes.CodeForbidden, "%s is limited to environment %s/%s; %s needs access to the whole project", a, a.ProjectName, a.EnvironmentName, action)
	}
	return nil
}

// checkEnv checks scope, limits and the environment's lock (ADR 0044) for
// an operation inside an environment. Every operation that changes
// something in an environment passes here (envFor and serviceFor call
// it): a token that needs more than read in a locked environment gets a
// LockedError, which its caller turns into an access request.
func (a Actor) checkEnv(need apitypes.Scope, action string, p store.Project, e store.Environment) error {
	if err := a.checkEnvAccess(need, action, p, e); err != nil {
		return err
	}
	if rank(need) > rank(apitypes.ScopeRead) && a.lockedOut(e) {
		return a.lockedErr(p, e, action)
	}
	return nil
}

// checkEnvAccess is checkEnv without the lock: scope and limits only.
// Only operations that can't loosen a lock use it (locking itself).
func (a Actor) checkEnvAccess(need apitypes.Scope, action string, p store.Project, e store.Environment) error {
	if err := a.requireScope(need, action); err != nil {
		return err
	}
	if err := a.inOrg(p); err != nil {
		return err
	}
	if a.ProjectID != 0 && a.ProjectID != p.ID {
		return errf(apitypes.CodeForbidden, "%s is limited to project %s; it cannot access project %s", a, a.ProjectName, p.Name)
	}
	if a.EnvironmentID != 0 && a.EnvironmentID != e.ID {
		return errf(apitypes.CodeForbidden, "%s is limited to environment %s/%s; it cannot access %s/%s", a, a.ProjectName, a.EnvironmentName, p.Name, e.Name)
	}
	return nil
}
