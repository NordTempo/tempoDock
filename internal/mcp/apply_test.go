package mcp_test

import (
	"strings"
	"testing"
)

// An agent plans a project file, then stages it with a secret's value
// that is never shown back; its environment-limited token only reaches
// its own environment.
func TestProjectFileTools(t *testing.T) {
	f := newFixture(t)
	s := f.connect("shop-agent", nil)
	file := "version = 1\n[[environments]]\nname = \"staging\"\n[[environments.services]]\nname = \"web\"\nimage = \"app:2\"\nvariables = [\"KEY\"]\n"

	plan := call(t, s, "plan_project_file", map[string]any{"file": file, "environment": "staging"})
	if plan.isError || !strings.Contains(plan.summary, "1 changes") || !strings.Contains(plan.summary, "KEY") {
		t.Fatalf("plan: %s %s", plan.summary, plan.json)
	}
	res := call(t, s, "apply_project_file", map[string]any{"file": file, "environment": "staging",
		"values": []map[string]string{{"environment": "staging", "service": "web", "key": "KEY", "value": "sk_secret_42"}}})
	if res.isError || !strings.Contains(res.summary, "2 changes now staged") || strings.Contains(res.json, "sk_secret_42") {
		t.Fatalf("apply: %s %s", res.summary, res.json)
	}
	both := file + "[[environments]]\nname = \"production\"\n"
	if res := call(t, s, "apply_project_file", map[string]any{"file": both}); !res.isError || !strings.Contains(res.json, "--environment") {
		t.Fatalf("production with a staging-only token: %s %s", res.summary, res.json)
	}
}
