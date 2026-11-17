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
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		for k, v := range req.Set {
			if err := c.stageSet(ctx, q, es.env.ID, serviceID, k, v); err != nil {
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

func (c *Core) stageSet(ctx context.Context, q *store.Queries, envID int64, serviceID *int64, key, value string) error {
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
	if live, err := c.openValue(cur.Value, aad, key); err == nil && live != nil && *live == value {
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
// a service name (or "shared") to its unexpanded variables.
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
	var firstErr error
	out := refRe.ReplaceAllStringFunc(raw, func(m string) string {
		sub := refRe.FindStringSubmatch(m)
		refScope, refKey := sub[1], sub[2]
		where := fmt.Sprintf("variable %s of %s references %s", key, scope, m)
		// What to do next: fix the value or drop the staged change.
		fix := fmt.Sprintf("; fix it with `dock env set -s %s %s=...` or drop it with `dock discard %s -s %s`", scope, key, key, scope)
		if scope == sharedScope {
			fix = fmt.Sprintf("; fix it with `dock env set --shared %s=...` or drop it with `dock discard %s --shared`", key, key)
		}
		vars, ok := r.vars[refScope]
		if !ok {
			if firstErr == nil {
				firstErr = invalid("%s, but there is no service %q in %s%s", where, refScope, r.env, fix)
			}
			return ""
		}
		if _, ok := vars[refKey]; !ok && refScope == orgScope {
			if firstErr == nil {
				firstErr = invalid("%s, but the organization has no variable %s; add it in organization settings or with `dock org env set %s=...`", where, refKey, refKey)
			}
			return ""
		}
		if _, ok := vars[refKey]; !ok {
			if firstErr == nil {
				firstErr = invalid("%s, but %s has no variable %s%s", where, refScope, refKey, fix)
			}
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
