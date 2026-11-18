//go:build integration

package build

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A source's own mise config runs nothing on the build host when Dock
// plans it. Each case is a config that would run a command if mise
// evaluated it (templates with exec, hooks, sourced scripts, tasks, tool
// options), written so the command leaves a marker file; the real mise
// binary, placed by ensureMise, reads them through the planner. The
// control shows the planner does read the source's mise.toml.
func TestHostileMiseConfigRunsNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	if err := ensureMise(ctx, os.Stderr); err != nil {
		t.Fatal(err)
	}
	marks := t.TempDir()
	// touch is a shell command that leaves the marker called name.
	touch := func(name string) string { return "touch " + filepath.Join(marks, name) }
	// exec is a template that runs touch and evaluates to value.
	exec := func(name, value string) string {
		return "{{ exec(command='" + touch(name) + "; echo " + value + "') }}"
	}
	script := "#!/bin/sh\n" + touch("script") + "\n"

	cases := map[string]map[string]string{
		"tools template":    {"mise.toml": "[tools]\nnode = \"" + exec("tools", "20") + "\"\n"},
		"env template":      {"mise.toml": "[env]\nX = \"" + exec("env", "1") + "\"\n[tools]\nnode = \"20\"\n"},
		"env source":        {"mise.toml": "[env]\n_.source = \"./evil.sh\"\n[tools]\nnode = \"20\"\n", "evil.sh": script},
		"env file and path": {"mise.toml": "[env]\n_.file = \"" + exec("env_file", ".env") + "\"\n_.path = [\"" + exec("env_path", "bin") + "\"]\n[tools]\nnode = \"20\"\n"},
		"hooks":             {"mise.toml": "[hooks]\nenter = \"" + touch("hook_enter") + "\"\ncd = \"" + touch("hook_cd") + "\"\npreinstall = \"" + touch("hook_preinstall") + "\"\n[tools]\nnode = \"20\"\n"},
		"tasks": {"mise.toml": "[tasks.default]\nrun = \"" + touch("task") + "\"\n[tasks.build]\nrun = \"" + touch("task_build") + "\"\n[tools]\nnode = \"20\"\n",
			"mise-tasks/build": script, ".mise/tasks/install": script},
		"vars":           {"mise.toml": "[vars]\nv = \"" + exec("vars", "20") + "\"\n[tools]\nnode = \"{{ vars.v }}\"\n"},
		"settings off":   {"mise.toml": "[settings]\nsafe = false\nparanoid = false\n[tools]\nnode = \"" + exec("settings", "20") + "\"\n"},
		"dot mise":       {".mise/config.toml": "[tools]\nnode = \"" + exec("dotmise", "20") + "\"\n", ".config/mise/config.toml": "[tools]\npython = \"" + exec("dotconfig", "3.12") + "\"\n"},
		"dot mise.toml":  {".mise.toml": "[hooks]\nenter = \"" + touch("dot_hook") + "\"\n[env]\n_.source = \"./evil.sh\"\nX = \"" + exec("dot_env", "1") + "\"\n[tools]\nnode = \"20\"\n", "evil.sh": script},
		"local config":   {"mise.local.toml": "[tools]\nnode = \"" + exec("local", "20") + "\"\n"},
		"tool versions":  {".tool-versions": "node " + exec("toolversions", "20") + "\n"},
		"idiomatic file": {".node-version": exec("idiomatic", "20") + "\n"},
		"plugin":         {"mise.toml": "[plugins]\nevil = \"https://127.0.0.1:1/evil.git\"\n[tools]\nevil = \"1\"\n"},
		"tool options":   {"mise.toml": "[tools]\nnode = { version = \"20\", postinstall = \"" + touch("postinstall") + "\" }\n\"exec:evil\" = \"1\"\n"},
		"aliases":        {"mise.toml": "[alias]\nnode = \"" + exec("alias", "node") + "\"\n[tool_alias]\nx = \"" + exec("tool_alias", "node") + "\"\n[tools]\nnode = \"20\"\n"},
	}
	// plan writes files into a new source directory and plans it.
	plan := func(t *testing.T, files map[string]string) planResult {
		t.Helper()
		src := filepath.Join(t.TempDir(), "app")
		files["package.json"], files["server.js"] = packageJSON, serverJS
		for name, body := range files {
			path := filepath.Join(src, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		res, err := runPlan(ctx, planInput{Dir: src})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	t.Run("control", func(t *testing.T) {
		res := plan(t, map[string]string{"mise.toml": "[tools]\nnode = \"20.11.0\"\n"})
		if !res.Success || !strings.Contains(string(res.Plan), "20.11.0") {
			t.Fatalf("the planner did not take node 20.11.0 from mise.toml, so the cases below prove nothing: %+v\n%s", res, res.Plan)
		}
	})
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			res := plan(t, files)
			left, err := os.ReadDir(marks)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range left {
				t.Errorf("planning ran a command from the source's mise config: marker %s (plan error %q)", m.Name(), res.Error)
				_ = os.Remove(filepath.Join(marks, m.Name()))
			}
		})
	}
}
