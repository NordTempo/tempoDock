package mcp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// TestLockedEnvironmentRefusesTools is the lock's matrix for MCP (ADR
// 0044): every tool that isn't read-only, called in a locked environment,
// fails with environment_locked before it changes anything. Tools are
// taken from tools/list, so a new one is covered without listing it.
func TestLockedEnvironmentRefusesTools(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.core.LockEnvironment(ctx, sys, core.EnvRef{Project: "shop", Environment: "staging"}, apitypes.LockEnvironmentRequest{}); err != nil {
		t.Fatal(err)
	}
	s := f.connect("admin", nil)
	list, err := s.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Cloning reads the environment and creates another one. Data tools
	// are capped at proposing instead (core's TestLockedDataProposes).
	// request_access asks rather than changes. Waking is read scope:
	// anyone who sees an environment wakes it by opening its address.
	// Status page incidents and maintenance change no environment (ADR 0073).
	skip := func(name string) bool {
		return name == "clone_environment" || strings.Contains(name, "data") || name == "request_access" || name == "wake_environment" ||
			strings.HasPrefix(name, "status_incident_") || name == "status_maintenance"
	}
	n := 0
	for _, tool := range list.Tools {
		if tool.Annotations != nil && tool.Annotations.ReadOnlyHint || skip(tool.Name) {
			continue
		}
		n++
		args := toolArgs(tool.InputSchema)
		res := call(t, s, tool.Name, args)
		if !res.isError || res.errorCode(t) != apitypes.CodeEnvironmentLocked {
			t.Errorf("%s %v: %s %s, want environment_locked", tool.Name, args, res.summary, res.json)
		}
	}
	if n < 8 {
		t.Fatalf("only %d mutating tools; the tools/list walk is broken", n)
	}
}

// toolArgs fills a tool's required arguments for shop/staging/web, with
// the smallest values the schema accepts, and a setting for tools that
// change settings (update_service refuses a call that changes nothing).
func toolArgs(schema any) map[string]any {
	b, _ := json.Marshal(schema)
	var s struct {
		Properties map[string]struct {
			Type string `json:"type"`
			Enum []any  `json:"enum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	_ = json.Unmarshal(b, &s)
	args := map[string]any{"project": "shop"}
	for k, v := range map[string]any{"environment": "staging", "service": "web", "set": map[string]string{"A": "1"}, "port": 8080} {
		if _, ok := s.Properties[k]; ok {
			args[k] = v
		}
	}
	for _, k := range s.Required {
		if _, ok := args[k]; ok {
			continue
		}
		p := s.Properties[k]
		switch {
		case len(p.Enum) > 0:
			args[k] = p.Enum[0]
		case p.Type == "integer" || p.Type == "number":
			args[k] = 1
		case p.Type == "boolean":
			args[k] = false
		case p.Type == "object":
			args[k] = map[string]any{}
		case p.Type == "array":
			args[k] = []any{}
		case strings.HasSuffix(k, "_path"): // tools/list leaves out patterns
			args[k] = "/x"
		default:
			args[k] = "x"
		}
	}
	if _, ok := s.Properties["from"]; ok { // promote: into the locked one
		args["from"], args["to"] = "production", "staging"
	}
	return args
}

// TestAccessRequestTools: a refused tool call names the request it filed;
// request_access asks first even where nothing is locked, with a reason;
// once an admin approves, wait_for_request returns the tool's result.
func TestAccessRequestTools(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.core.LockEnvironment(ctx, sys, core.EnvRef{Project: "shop", Environment: "staging"}, apitypes.LockEnvironmentRequest{}); err != nil {
		t.Fatal(err)
	}
	s := f.connect("admin", nil)
	res := call(t, s, "set_variables", map[string]any{"project": "shop", "environment": "staging", "service": "web", "set": map[string]string{"A": "1"}})
	var e struct {
		Error struct {
			Code    apitypes.ErrorCode
			Request int64
			Hint    string
		}
	}
	res.decode(t, &e)
	if e.Error.Code != apitypes.CodeEnvironmentLocked || e.Error.Request == 0 || !strings.Contains(e.Error.Hint, "wait_for_request") {
		t.Fatalf("refused: %s %s", res.summary, res.json)
	}
	// Any tool takes a reason, like the CLI's --reason: the same call with
	// one joins the request it filed and gives it the reason.
	again := call(t, s, "set_variables", map[string]any{"project": "shop", "environment": "staging", "service": "web", "set": map[string]string{"A": "1"}, "reason": "launch prices"})
	var joined struct{ Error struct{ Request int64 } }
	again.decode(t, &joined)
	if r, err := f.core.GetRequest(ctx, sys, e.Error.Request); joined.Error.Request != e.Error.Request || err != nil || r.Reason != "launch prices" {
		t.Fatalf("with reason: %s %s; request %+v %v", again.summary, again.json, r, err)
	}

	// Asking first in blog/production, which isn't locked.
	res = call(t, s, "request_access", map[string]any{
		"tool": "set_variables", "reason": "rotate the key",
		"arguments": map[string]any{"project": "blog", "environment": "production", "service": "api", "set": map[string]string{"KEY": "new"}},
	})
	var asked struct {
		ID     int64
		Status apitypes.AccessRequestStatus
		Reason string
	}
	res.decode(t, &asked)
	if res.isError || asked.ID == 0 || asked.Status != apitypes.RequestPending || asked.Reason != "rotate the key" {
		t.Fatalf("request_access: %s %s", res.summary, res.json)
	}
	admin := core.Actor{Kind: "user", OrgID: 1, Name: "saile@example.com", UserID: 1, Scope: apitypes.ScopeAdmin}
	if _, err := f.core.DecideRequest(ctx, admin, asked.ID, true, apitypes.DecideAccessRequest{}); err != nil {
		t.Fatal(err)
	}
	res = call(t, s, "wait_for_request", map[string]any{"id": asked.ID, "wait_seconds": 5})
	if res.isError || !strings.Contains(res.summary, "ran") || !strings.Contains(res.json, "KEY") {
		t.Fatalf("wait: %s %s", res.summary, res.json)
	}
	if res := call(t, s, "request_access", map[string]any{"tool": "list_projects", "arguments": map[string]any{}, "reason": "x"}); !res.isError {
		t.Fatalf("asking for a read: %s", res.summary)
	}
}
