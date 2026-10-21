package core

import (
	"fmt"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Actor is who performs an operation: a logged-in user, an API token or the
// system itself. Users and tokens go through the same permission checks.
type Actor struct {
	Kind    string // "user", "token" or "system"
	Name    string
	Email   string
	UserID  int64
	TokenID int64
	Scope   apitypes.Scope
	// ProjectID and EnvironmentID limit a token to one project or one
	// environment. Zero means unlimited.
	ProjectID       int64
	EnvironmentID   int64
	ProjectName     string
	EnvironmentName string
}

// System is the actor for the daemon's own actions and for trusted local
// callers (tests, admin commands).
var System = Actor{Kind: "system", Name: "system", Scope: apitypes.ScopeAdmin}

// Me describes the actor for the API.
func (a Actor) Me() apitypes.Me {
	return apitypes.Me{
		Kind: a.Kind, Name: a.Name, Email: a.Email, Scope: a.Scope,
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

// require checks the actor's scope. action completes "… requires <scope>",
// e.g. "archiving a service".
func (a Actor) require(need apitypes.Scope, action string) error {
	if rank(a.Scope) >= rank(need) {
		return nil
	}
	return errf(apitypes.CodeForbidden, "%s has scope %s; %s requires %s", a, a.Scope, action, need)
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

func (a Actor) limitName() string {
	if a.EnvironmentID != 0 {
		return fmt.Sprintf("environment %s/%s", a.ProjectName, a.EnvironmentName)
	}
	return fmt.Sprintf("project %s", a.ProjectName)
}

// canSee reports whether the actor may read things in the project and
// environment (envID 0: project-level things).
func (a Actor) canSee(projectID, envID int64) bool {
	if a.ProjectID != 0 && a.ProjectID != projectID {
		return false
	}
	if a.EnvironmentID != 0 && a.EnvironmentID != envID {
		return false
	}
	return true
}

// checkProject checks scope and limits for a project-level operation.
// Environment-limited actors may read the project but not change it.
func (a Actor) checkProject(need apitypes.Scope, action string, p store.Project) error {
	if err := a.require(need, action); err != nil {
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
	if a.ProjectID != 0 && a.ProjectID != p.ID {
		return errf(apitypes.CodeForbidden, "%s is limited to project %s; it cannot access project %s", a, a.ProjectName, p.Name)
	}
	if a.EnvironmentID != 0 && a.EnvironmentID != e.ID {
		return errf(apitypes.CodeForbidden, "%s is limited to environment %s/%s; it cannot access %s/%s", a, a.ProjectName, a.EnvironmentName, p.Name, e.Name)
	}
	return nil
}
