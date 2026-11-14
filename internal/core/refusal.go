package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Refusals that explain themselves (ADR 0080). The checks in actor.go
// return them: a forbidden error whose message says why and what would
// allow it, with the same as data (apitypes.Refusal) for the CLI, MCP
// and the web UI. Explain adds who can change it.

// refuse returns the forbidden error for r, filling in who was refused
// and what they have. Its message is r's reason and fix.
func (a Actor) refuse(r apitypes.Refusal) error {
	r.Actor = a.String()
	if a.Kind != "system" {
		r.Has = a.Scope
	}
	if a.limited() {
		r.Limit = a.limitPlace()
	}
	msg := r.Reason
	if r.Fix != "" {
		msg += "; " + r.Fix
	}
	return &Error{Code: apitypes.CodeForbidden, Msg: msg, Refusal: &r}
}

// actsForPerson reports whether the actor is a person: a signed-in user,
// or a `dock login` token, which acts for its user.
func (a Actor) actsForPerson() bool {
	return a.Kind == "user" || (a.Kind == "token" && a.UserID != 0)
}

// orgName is the org the actor acts in, for refusals' fixes.
func (a Actor) orgName() string {
	if a.OrgSlug == "" {
		return "the organization"
	}
	return a.OrgSlug
}

// refuseScope is the refusal when the actor's scope is below need. A
// person's role can change; a manual token's scope can't, so its fix is
// a new token.
func (a Actor) refuseScope(need apitypes.Scope, action string) error {
	r := apitypes.Refusal{Action: action, Need: string(need)}
	if a.actsForPerson() {
		r.DecidedBy = apitypes.RefusedRole
		r.Reason = fmt.Sprintf("%s has role %s in %s; %s requires %s", a, a.Scope, a.orgName(), action, need)
		r.Fix = fmt.Sprintf("an admin of %s can give you role %s in Settings › Members (`dock org role %s %s`)", a.orgName(), need, a.who(), need)
		return a.refuse(r)
	}
	r.DecidedBy = apitypes.RefusedScope
	r.Reason = fmt.Sprintf("%s has scope %s; %s requires %s", a, a.Scope, action, need)
	r.Fix = fmt.Sprintf("a token's scope doesn't change: an admin of %s creates one with scope %s in Settings › Tokens (`dock token create <name> --scope %s`)", a.orgName(), need, need)
	return a.refuse(r)
}

// who names the person the actor acts for, for a command's argument.
func (a Actor) who() string {
	if a.Email != "" {
		return a.Email
	}
	return "<email>"
}

// refuseLimit is the refusal when a token's project or environment limit
// keeps it from place (empty for the whole org). reason is the check's
// own words.
func (a Actor) refuseLimit(need apitypes.Scope, action, place, reason string) error {
	fix := fmt.Sprintf("an admin of %s creates an unlimited token in Settings › Tokens (`dock token create <name> --scope %s`)", a.orgName(), need)
	if project, env, ok := strings.Cut(place, "/"); place != "" {
		cmd := fmt.Sprintf("dock token create <name> --scope %s --project %s", need, project)
		if ok {
			cmd += " --environment " + env
		}
		fix = fmt.Sprintf("an admin of %s creates a token for it in Settings › Tokens (`%s`)", a.orgName(), cmd)
	}
	return a.refuse(apitypes.Refusal{
		DecidedBy: apitypes.RefusedLimit, Action: action, Need: string(need), Place: place,
		Reason: reason, Fix: fix,
	})
}

// limitPlace is a limited token's place as "project" or
// "project/environment".
func (a Actor) limitPlace() string {
	if a.EnvironmentID != 0 {
		return a.ProjectName + "/" + a.EnvironmentName
	}
	return a.ProjectName
}

// refuseOperator is the refusal when a platform action meets an actor
// that isn't an operator. An org's token is the usual surprise (a
// context holding an org admin token runs `dock update`), so it says
// which credential the platform needs.
func (a Actor) refuseOperator(action string) error {
	r := apitypes.Refusal{DecidedBy: apitypes.RefusedOperator, Action: action, Need: "operator"}
	switch {
	case a.Kind == "token" && a.UserID == 0:
		r.Reason = fmt.Sprintf("%s is a token of organization %s, not a platform operator token; %s runs the platform, which org tokens never do", a, a.orgName(), action)
		r.Fix = "platform commands need an operator's own `dock login` or an operator token; `dock whoami` shows what this context holds"
	default:
		r.Reason = fmt.Sprintf("%s is not a platform operator; %s requires one", a, action)
		r.Fix = "an operator makes others operators in the console (Console › Users); `dock whoami` shows whether this login is one"
	}
	return a.refuse(r)
}

// Explain completes a refusal in err with who can change it: the org's
// admins, by name, for callers acting for a person (a person may ask
// them; a manual token's refusal goes to whoever runs it, who knows the
// org). Other errors come back as they are. It costs one query, only for
// refusals.
func (c *Core) Explain(ctx context.Context, a Actor, err error) error {
	ce, ok := errors.AsType[*Error](err)
	if !ok || ce.Refusal == nil || len(ce.Refusal.Ask) > 0 || !a.actsForPerson() || a.OrgID == 0 {
		return err
	}
	switch ce.Refusal.DecidedBy {
	case apitypes.RefusedRole, apitypes.RefusedScope, apitypes.RefusedLimit:
	default:
		return err
	}
	admins, qerr := c.store.ListAdminNames(ctx, a.OrgID)
	if qerr != nil || len(admins) == 0 {
		return err
	}
	r := *ce.Refusal
	r.Ask = make([]string, len(admins))
	for i, ad := range admins {
		r.Ask[i] = ad.Email
		if ad.Name != "" {
			r.Ask[i] = ad.Name
		}
	}
	out := *ce
	out.Refusal = &r
	return &out
}
