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
	skip := func(name string) bool { return name == "clone_environment" || strings.Contains(name, "data") }
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
// the smallest values the schema accepts.
func toolArgs(schema any) map[string]any {
	b, _ := json.Marshal(schema)
	var s struct {
		Properties map[string]struct {
			Type    string `json:"type"`
			Enum    []any  `json:"enum"`
			Minimum *int   `json:"minimum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	_ = json.Unmarshal(b, &s)
	args := map[string]any{"project": "shop"}
	for k, v := range map[string]any{"environment": "staging", "service": "web", "set": map[string]string{"A": "1"}} {
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
		default:
			args[k] = "x"
		}
	}
	if _, ok := s.Properties["from"]; ok { // promote: into the locked one
		args["from"], args["to"] = "production", "staging"
	}
	return args
}
