package core

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Access request previews (ADR 0044): an admin reviewing a request that
// sets or unsets variables sees what approving it would change now, each
// key's current value against the requested one, not only the raw call.

// requestVariables reads the variable changes in a request's stored
// args, for the two calls that set variables: the API's set-variables
// operation and MCP's set_variables tool. ok is false for any other call.
func requestVariables(via, op string, args json.RawMessage) (req apitypes.SetVariablesRequest, ok bool) {
	switch {
	case via == "api" && op == "set-variables":
		// The Huma input: path parameters, then the body.
		var in struct{ Body apitypes.SetVariablesRequest }
		if json.Unmarshal(args, &in) != nil {
			return req, false
		}
		return in.Body, true
	case via == "mcp" && op == "set_variables":
		// The tool's input; json matches its lower-case keys.
		if json.Unmarshal(args, &req) != nil {
			return req, false
		}
		return req, true
	}
	return req, false
}

// requestPreview diffs a variables request's args against the request's
// environment as it is now (staged values over live ones), with values
// revealed: only admins get it. Keys it wouldn't change are left out.
// Other calls, and a preview that can't be built, get nil; the raw args
// still show. Two queries.
func (c *Core) requestPreview(ctx context.Context, r store.AccessRequest, args json.RawMessage) []apitypes.VariableChange {
	req, ok := requestVariables(r.Via, r.Operation, args)
	if !ok || len(req.Set)+len(req.Unset) == 0 {
		return nil
	}
	changes, err := c.variablesPreview(ctx, r.EnvironmentID, req)
	if err != nil {
		c.log.Error("preview access request", "request", r.ID, "err", err)
		return nil
	}
	return changes
}

// variablesPreview builds the diff of req against environment envID's
// current values, sorted by key.
func (c *Core) variablesPreview(ctx context.Context, envID int64, req apitypes.SetVariablesRequest) ([]apitypes.VariableChange, error) {
	var serviceID *int64
	scope := apitypes.ScopeEnvironment
	if req.Service != "" {
		svcs, err := c.store.ListServices(ctx, envID)
		if err != nil {
			return nil, err
		}
		i := slices.IndexFunc(svcs, func(s store.Service) bool { return s.Name == req.Service })
		if i < 0 {
			return nil, nil // gone since: approving will fail, the raw args show why
		}
		serviceID, scope = &svcs[i].ID, apitypes.ScopeService
	}
	vars, err := c.store.ListVariables(ctx, envID)
	if err != nil {
		return nil, err
	}
	current := map[string]*string{}
	for _, v := range vars {
		if (v.ServiceID == nil) != (serviceID == nil) || (serviceID != nil && *v.ServiceID != *serviceID) {
			continue
		}
		if current[v.Key], err = c.currentValue(v); err != nil {
			return nil, err
		}
	}
	out := []apitypes.VariableChange{}
	add := func(key string, next *string) {
		old := current[key]
		vc := apitypes.VariableChange{Key: key, Scope: scope, Service: req.Service, Old: old, New: next}
		switch {
		case old == nil && next == nil, old != nil && next != nil && *old == *next:
			return
		case old == nil:
			vc.Action = apitypes.ChangeAdd
		case next == nil:
			vc.Action = apitypes.ChangeRemove
		default:
			vc.Action = apitypes.ChangeUpdate
		}
		out = append(out, vc)
	}
	for k, v := range req.Set {
		add(k, &v)
	}
	for _, k := range req.Unset {
		add(k, nil)
	}
	slices.SortFunc(out, func(a, b apitypes.VariableChange) int { return strings.Compare(a.Key, b.Key) })
	return out, nil
}

// currentValue is a variable's value as the environment has it now: the
// staged value when one is staged (nil when an unset is), else the live one.
func (c *Core) currentValue(v store.Variable) (*string, error) {
	aad := variableAAD(v.EnvironmentID, v.ServiceID, v.Key)
	switch v.Pending {
	case "unset":
		return nil, nil
	case "set":
		return c.openValue(v.PendingValue, aad, v.Key)
	}
	return c.openValue(v.Value, aad, v.Key)
}
