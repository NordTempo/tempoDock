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
	// The environment defaults to the token's, like every tool's: the
	// file's other environments are left alone.
	both := file + "[[environments]]\nname = \"production\"\n"
	if res := call(t, s, "apply_project_file", map[string]any{"file": both}); res.isError || !strings.Contains(res.summary, "across 1 environments") || strings.Contains(res.json, `"production"`) {
		t.Fatalf("production with a staging-only token: %s %s", res.summary, res.json)
	}
}

// import_compose converts and plans without changing anything; the file it
// returns stages with apply_project_file.
func TestImportComposeTool(t *testing.T) {
	f := newFixture(t)
	s := f.connect("admin", nil)
	compose := "services:\n  db:\n    image: postgres:17\n  api:\n    image: app:3\n    environment:\n      DATABASE_URL: postgres://postgres:x@db:5432/app\n      API_KEY: k-1\n"
	res := call(t, s, "import_compose", map[string]any{"project": "imported", "compose": compose})
	var out struct {
		File    string
		Secrets []struct{ Key string }
	}
	res.decode(t, &out)
	if res.isError || !strings.Contains(out.File, "template = 'postgres'") || len(out.Secrets) != 1 || strings.Contains(res.json, "k-1") {
		t.Fatalf("import_compose: %s %s", res.summary, res.json)
	}
	applied := call(t, s, "apply_project_file", map[string]any{"project": "imported", "file": out.File,
		"values": []map[string]string{{"environment": "production", "service": "api", "key": "API_KEY", "value": "k-1"}}})
	if applied.isError || !strings.Contains(applied.summary, "now staged") {
		t.Fatalf("apply: %s %s", applied.summary, applied.json)
	}
}
