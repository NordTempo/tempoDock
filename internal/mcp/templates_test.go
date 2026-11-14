package mcp_test

import (
	"strings"
	"testing"
)

// An agent finds a template's inputs, plans it and creates it in its
// token's environment; a secret input is never shown back.
func TestTemplateTools(t *testing.T) {
	f := newFixture(t)
	s := f.connect("shop-agent", nil)
	if res := call(t, s, "list_templates", map[string]any{"name": "grafana"}); res.isError || !strings.Contains(res.summary, "GF_SECURITY_ADMIN_PASSWORD (secret, required)") {
		t.Fatalf("list: %s", res.summary)
	}
	args := map[string]any{"template": "grafana", "inputs": map[string]string{"GF_SECURITY_ADMIN_PASSWORD": "pw_secret_42"}, "plan": true}
	if res := call(t, s, "use_template", args); !res.isError || !strings.Contains(res.summary, "--domain") {
		t.Fatalf("no base domain: %s", res.summary)
	}
	args["domain"] = "grafana.example.com"
	if res := call(t, s, "use_template", args); res.isError || !strings.Contains(res.summary, "Would create grafana") {
		t.Fatalf("plan: %s", res.summary)
	}
	delete(args, "plan")
	res := call(t, s, "use_template", args)
	if res.isError || !strings.Contains(res.summary, "Created grafana in shop/staging") || strings.Contains(res.json, "pw_secret_42") {
		t.Fatalf("use: %s %s", res.summary, res.json)
	}
}
