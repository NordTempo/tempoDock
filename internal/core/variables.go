package core

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

var keyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validKey(k string) error {
	switch {
	case !keyRe.MatchString(k):
		return invalid("variable name %q must be letters, digits and underscores, not starting with a digit", k)
	case k == "PORT":
		return invalid("PORT is set by the platform; change the service's port setting instead")
	case strings.HasPrefix(k, "DOCK_"):
		return invalid("%s: the DOCK_ prefix is reserved for variables the platform injects", k)
	}
	return nil
}

// ListVariables lists an environment's variables (shared and every
// service's), or only shared plus one service's when service is set. Values
// are included only with reveal, which requires admin.
func (c *Core) ListVariables(ctx context.Context, a Actor, ref EnvRef, service string, reveal bool) ([]apitypes.Variable, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeRead, "listing variables")
	if err != nil {
		return nil, err
	}
	if reveal {
		if err := a.requireReveal(es); err != nil {
			return nil, err
		}
	}
	svcs, err := c.store.ListServices(ctx, es.env.ID)
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	var only int64
	for _, s := range svcs {
		names[s.ID] = s.Name
		if s.Name == service {
			only = s.ID
		}
	}
	if service != "" && only == 0 {
		return nil, notFound("service %q not found in %s", service, es)
	}
	rows, err := c.store.ListVariables(ctx, es.env.ID)
	if err != nil {
		return nil, err
	}
	out := []apitypes.Variable{}
	for _, v := range rows {
		if only != 0 && v.ServiceID != nil && *v.ServiceID != only {
			continue
		}
		item := apitypes.Variable{Key: v.Key, Scope: apitypes.ScopeEnvironment, UpdatedAt: v.UpdatedAt}
		if v.ServiceID != nil {
			item.Scope, item.Service = apitypes.ScopeService, names[*v.ServiceID]
		}
		if v.Pending != "" {
			item.Pending = variableAction(v)
		}
		if reveal {
			if item.Value, err = c.openValue(v.Value, variableAAD(v.EnvironmentID, v.ServiceID, v.Key), v.Key); err != nil {
				return nil, err
			}
		}
		out = append(out, item)
	}
	return out, nil
}

// SetVariables stages setting and unsetting variables at environment or
// service scope, optionally applying the environment's changes right away.
func (c *Core) SetVariables(ctx context.Context, a Actor, ref EnvRef, req apitypes.SetVariablesRequest) (apitypes.ChangeResult, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeDeploy, "setting variables")
	if err != nil {
		return apitypes.ChangeResult{}, err
	}
	if len(req.Set) == 0 && len(req.Unset) == 0 && !req.Apply {
		return apitypes.ChangeResult{}, invalid("nothing to set or unset")
	}
	var serviceID *int64
	target := es.String()
	if req.Service != "" {
		ss, err := c.loadService(ctx, a.OrgID, ServiceRef{ref.Project, ref.Environment, req.Service})
		if err != nil {
			return apitypes.ChangeResult{}, err
		}
		serviceID, target = &ss.svc.ID, ss.String()
	}
	for k := range req.Set {
		if err := validKey(k); err != nil {
			return apitypes.ChangeResult{}, err
		}
	}
	for _, k := range req.Unset {
		if _, set := req.Set[k]; set {
			return apitypes.ChangeResult{}, invalid("%s is both set and unset", k)
		}
	}
	// Someone who may not reveal values here isn't told whether the value
	// they set is the live one (stageSetAs).
	blind := !a.revealsIn(es)
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		for k, v := range req.Set {
			if err := c.orgRefsAllowed(ctx, q, a, es.env.ID, serviceID, k, v); err != nil {
				return err
			}
			if err := c.stageSetAs(ctx, q, es.env.ID, serviceID, k, v, blind); err != nil {
				return err
			}
		}
		for _, k := range req.Unset {
			if err := unsetKnown(ctx, q, es.env.ID, serviceID, k, target); err != nil {
				return err
			}
			if err := stageUnset(ctx, q, es.env.ID, serviceID, k); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return apitypes.ChangeResult{}, err
	}
	if len(req.Set)+len(req.Unset) > 0 {
		c.audit(ctx, a, "variables.set", &es.project, &es.env, target, describeVariableChange(req))
	}
	return c.changeResult(ctx, a, es, req.Apply)
}

// orgRefsAllowed refuses a value that adds a reference to an organization
// variable (${{org.KEY}}) unless the caller edits variables across the
// whole organization. An organization variable is the same secret in
// every project, and a reference hands it to the service that holds the
// reference, whose code whoever deploys there chooses: someone whose
// access ends at one environment or one kind of environment would read
// it from there. References the variable has already (live or staged)
// stay theirs to keep; who may add one can set it for them.
func (c *Core) orgRefsAllowed(ctx context.Context, q *store.Queries, a Actor, envID int64, serviceID *int64, key, value string) error {
	orgRefs := func(v string) (keys []string) {
		for _, m := range refRe.FindAllStringSubmatch(v, -1) {
			if m[1] == orgScope {
				keys = append(keys, m[2])
			}
		}
		return keys
	}
	asked := orgRefs(value)
	if len(asked) == 0 || a.covers(permsOf(PermVariablesWrite), 0, 0, uint8(allKinds)) {
		return nil
	}
	var has []string
	cur, err := q.GetVariable(ctx, store.GetVariableParams{EnvironmentID: envID, ServiceID: serviceID, Key: key})
	switch {
	case err == nil:
		aad := variableAAD(envID, serviceID, key)
		for _, sealed := range [][]byte{cur.Value, cur.PendingValue} {
			if v, err := c.openValue(sealed, aad, key); err == nil && v != nil {
				has = append(has, orgRefs(*v)...)
			}
		}
	case !store.IsNotFound(err):
		return err
	}
	for _, k := range asked {
		if !slices.Contains(has, k) {
			return a.refuse(apitypes.Refusal{
				DecidedBy: apitypes.RefusedGrants, Action: "referencing an organization variable", Need: "variables.write", Place: "the whole organization",
				Reason: fmt.Sprintf("%s=... would reference the organization's variable %s, which is the same secret in every project: adding a reference to one needs the permission to edit variables across the whole organization, which %s holds only in part of it", key, k, a),
				Fix:    "ask someone who edits variables everywhere (an admin) to set this variable",
			})
		}
	}
	return nil
}

// stageSet stages value for a variable, or un-stages the variable when
// value is what is live already.
func (c *Core) stageSet(ctx context.Context, q *store.Queries, envID int64, serviceID *int64, key, value string) error {
	return c.stageSetAs(ctx, q, envID, serviceID, key, value, false)
}

// stageSetAs is stageSet for a caller who may not reveal values (blind):
// their value is staged whatever is live. Un-staging a value equal to
// the live one would tell them, through the count of staged changes,
// whether a guess at a secret was right.
func (c *Core) stageSetAs(ctx context.Context, q *store.Queries, envID int64, serviceID *int64, key, value string, blind bool) error {
	aad := variableAAD(envID, serviceID, key)
	sealed, err := c.box.Seal([]byte(value), aad)
	if err != nil {
		return err
	}
	cur, err := q.GetVariable(ctx, store.GetVariableParams{EnvironmentID: envID, ServiceID: serviceID, Key: key})
	if store.IsNotFound(err) {
		return q.InsertVariable(ctx, store.InsertVariableParams{EnvironmentID: envID, ServiceID: serviceID, Key: key, PendingValue: sealed, UpdatedAt: now()})
	}
	if err != nil {
		return err
	}
	// Setting a variable back to its live value un-stages it.
	if live, err := c.openValue(cur.Value, aad, key); !blind && err == nil && live != nil && *live == value {
		return q.SetVariablePending(ctx, store.SetVariablePendingParams{Pending: "", PendingValue: nil, UpdatedAt: now(), ID: cur.ID})
	}
	return q.SetVariablePending(ctx, store.SetVariablePendingParams{Pending: "set", PendingValue: sealed, UpdatedAt: now(), ID: cur.ID})
}

// unsetKnown refuses unsetting a variable that isn't there (target names
// the scope), instead of staging nothing and saying it did: most often a
// shared variable unset on a service.
func unsetKnown(ctx context.Context, q *store.Queries, envID int64, serviceID *int64, key, target string) error {
	_, err := q.GetVariable(ctx, store.GetVariableParams{EnvironmentID: envID, ServiceID: serviceID, Key: key})
	if !store.IsNotFound(err) {
		return err
	}
	if serviceID != nil {
		if _, err := q.GetVariable(ctx, store.GetVariableParams{EnvironmentID: envID, Key: key}); err == nil {
			return notFound("%s has no variable %s of its own; it is a shared variable: unset it with --shared", target, key)
		}
	}
	return notFound("%s has no variable %s; `dock env list` lists them", target, key)
}

func stageUnset(ctx context.Context, q *store.Queries, envID int64, serviceID *int64, key string) error {
	cur, err := q.GetVariable(ctx, store.GetVariableParams{EnvironmentID: envID, ServiceID: serviceID, Key: key})
	if store.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if cur.Value == nil {
		return q.DeleteVariable(ctx, cur.ID) // only ever staged
	}
	return q.SetVariablePending(ctx, store.SetVariablePendingParams{Pending: "unset", PendingValue: nil, UpdatedAt: now(), ID: cur.ID})
}

func describeVariableChange(req apitypes.SetVariablesRequest) string {
	var parts []string
	if len(req.Set) > 0 {
		keys := make([]string, 0, len(req.Set))
		for k := range req.Set {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		parts = append(parts, "set "+strings.Join(keys, ", "))
	}
	if len(req.Unset) > 0 {
		parts = append(parts, "unset "+strings.Join(req.Unset, ", "))
	}
	return "staged: " + strings.Join(parts, "; ")
}

// refRe matches ${{service.KEY}}, ${{shared.KEY}} and ${{org.KEY}}.
var refRe = regexp.MustCompile(`\$\{\{\s*([a-z0-9-]+)\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// sharedScope is the reference prefix for environment-level variables.
const sharedScope = "shared"

// resolver expands variable references within one environment. vars maps
// a service name (or "shared") to its unexpanded variables; env is the
// environment as project/name, for messages and the commands they give.
type resolver struct {
	env      string
	vars     map[string]map[string]string
	done     map[string]string
	visiting map[string]bool
}

func (r *resolver) value(scope, key string) (string, error) {
	id := scope + "." + key
	if v, ok := r.done[id]; ok {
		return v, nil
	}
	if r.visiting[id] {
		return "", invalid("variable references form a cycle at ${{%s}}", id)
	}
	r.visiting[id] = true
	defer delete(r.visiting, id)

	raw := r.vars[scope][key]
	if !strings.Contains(raw, "${{") {
		// No reference: most values. The regexp would only copy it.
		r.done[id] = raw
		return raw, nil
	}
	var firstErr error
	// broken records the first reference that doesn't resolve, saying what
	// to do next: fix the value or drop the staged change. Built only then:
	// values resolve on every read of staged changes.
	broken := func(m, why string, withFix bool) {
		if firstErr != nil {
			return
		}
		msg := fmt.Sprintf("variable %s of %s references %s, but %s", key, scope, m, why)
		switch {
		case !withFix:
		case scope == sharedScope:
			msg += fmt.Sprintf("; fix it with `dock env set --shared %s=...` or drop it with `dock discard %s --shared`", key, key)
		default:
			msg += fmt.Sprintf("; fix it with `dock env set -s %s %s=...` or drop it with `dock discard %s -s %s`", scope, key, key, scope)
		}
		firstErr = invalid("%s", msg)
	}
	out := refRe.ReplaceAllStringFunc(raw, func(m string) string {
		sub := refRe.FindStringSubmatch(m)
		refScope, refKey := sub[1], sub[2]
		vars, ok := r.vars[refScope]
		if !ok {
			broken(m, fmt.Sprintf("there is no service %q in %s", refScope, r.env), true)
			return ""
		}
		if _, ok := vars[refKey]; !ok && refScope == orgScope {
			broken(m, fmt.Sprintf("the organization has no variable %s; add it in organization settings or with `dock org env set %s=...`", refKey, refKey), false)
			return ""
		}
		if _, ok := vars[refKey]; !ok && refScope == sharedScope {
			// Usually an environment cloned or promoted into without the
			// value: the fix is to set it here, not to change the reference.
			project, env, _ := strings.Cut(r.env, "/")
			broken(m, fmt.Sprintf("%s has no shared variable %s; set it with `dock env set --shared %s=... -p %s -e %s`", r.env, refKey, refKey, project, env), false)
			return ""
		}
		if _, ok := vars[refKey]; !ok {
			broken(m, fmt.Sprintf("%s has no variable %s", refScope, refKey), true)
			return ""
		}
		v, err := r.value(refScope, refKey)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		return v
	})
	if firstErr != nil {
		return "", firstErr
	}
	r.done[id] = out
	return out, nil
}
