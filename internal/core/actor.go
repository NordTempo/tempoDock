package core

import (
	"fmt"

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
	// OrgID is the org the actor acts in; 0 for the system (every org) and
	// for a user who hasn't chosen one of several (orgs counts them).
	OrgID   int64
	OrgSlug string
	orgs    int
	Scope   apitypes.Scope
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
		Kind: a.Kind, Name: a.Name, Email: a.Email, Scope: a.Scope, Org: a.OrgSlug, Operator: a.Operator,
		Project: a.ProjectName, Environment: a.EnvironmentName,
	}
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
	if a.OrgID == 0 && a.Kind != "system" {
		return a.noOrg(action)
	}
	if rank(a.Scope) >= rank(need) {
		return nil
	}
	return errf(apitypes.CodeForbidden, "%s has scope %s; %s requires %s", a, a.Scope, action, need)
}

// noOrg explains why an actor without an org can't do action.
func (a Actor) noOrg(action string) error {
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
	return errf(apitypes.CodeForbidden, "%s is not a platform operator; %s requires one", a, action)
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

// checkEnv checks scope and limits for an operation inside an environment.
func (a Actor) checkEnv(need apitypes.Scope, action string, p store.Project, e store.Environment) error {
	if err := a.require(need, action); err != nil {
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
