package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// setup isolates the user config and the DOCK_* environment.
func setup(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, k := range []string{clientconfig.EnvURL, clientconfig.EnvToken, clientconfig.EnvContext} {
		t.Setenv(k, "")
	}
}

// exec runs a command line non-interactively and returns exit code, stdout
// and stderr.
func exec(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(context.Background(), args, &output.IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	return code, out.String(), errOut.String()
}

func TestContextLifecycle(t *testing.T) {
	setup(t)

	if code, _, stderr := exec(t, "context", "add", "prod", "--server", "https://prod.example.com/", "--token", "t1"); code != 0 {
		t.Fatalf("add prod: %d %s", code, stderr)
	}
	if code, _, stderr := exec(t, "context", "add", "home", "--server", "http://home:7070", "--token", "t2"); code != 0 {
		t.Fatalf("add home: %d %s", code, stderr)
	}

	_, stdout, _ := exec(t, "context", "list", "--json")
	var list []contextView
	if err := json.Unmarshal([]byte(stdout), &list); err != nil {
		t.Fatalf("list --json is not JSON: %q", stdout)
	}
	want := []contextView{
		{Name: "home", Server: "http://home:7070", HasToken: true},
		{Name: "prod", Server: "https://prod.example.com", Current: true, HasToken: true}, // first added is current
	}
	if len(list) != 2 || list[0] != want[0] || list[1] != want[1] {
		t.Fatalf("list = %+v", list)
	}
	if strings.Contains(stdout, "t1") {
		t.Fatal("tokens must never be printed")
	}

	exec(t, "context", "use", "home")
	if _, stdout, _ := exec(t, "context", "current", "--json"); !strings.Contains(stdout, `"name": "home"`) {
		t.Errorf("current after use = %s", stdout)
	}

	if code, stdout, _ := exec(t, "logout", "--json"); code != 0 || !strings.Contains(stdout, `"hadToken": true`) {
		t.Errorf("logout = %d %s", code, stdout)
	}
	if code, _, _ := exec(t, "logout"); code != 0 {
		t.Error("logging out twice should succeed")
	}

	exec(t, "context", "remove", "home")
	if code, _, stderr := exec(t, "context", "current"); code != int(output.ExitAuth) || !strings.Contains(stderr, "dock login") {
		t.Errorf("no current context: %d %q", code, stderr)
	}
}

func TestExitCodes(t *testing.T) {
	setup(t)
	tests := []struct {
		args   []string
		exit   output.ExitCode
		stderr string
	}{
		{[]string{"context", "use", "nope"}, output.ExitNotFound, "dock context list"},
		{[]string{"context", "add", "x", "--server", "https://x"}, output.ExitUsage, "--token"},
		{[]string{"context", "add", "x", "--server", "x.example.com", "--token", "t"}, output.ExitUsage, "https://"},
		{[]string{"context", "add"}, output.ExitUsage, "--help"},
		{[]string{"context", "list", "--bogus"}, output.ExitUsage, "unknown flag"},
		{[]string{"nope"}, output.ExitUsage, "unknown command"},
		{[]string{"unlink"}, output.ExitNotFound, "not linked"},
	}
	for _, tt := range tests {
		code, _, stderr := exec(t, tt.args...)
		if code != int(tt.exit) || !strings.Contains(stderr, tt.stderr) {
			t.Errorf("%v: exit %d, stderr %q; want exit %d mentioning %q", tt.args, code, stderr, tt.exit, tt.stderr)
		}
	}

	// --json errors are a JSON object on stderr.
	_, _, stderr := exec(t, "context", "use", "nope", "--json")
	var e struct{ Error struct{ Code string } }
	if err := json.Unmarshal([]byte(stderr), &e); err != nil || e.Error.Code != "not_found" {
		t.Errorf("JSON error = %q", stderr)
	}
}

func TestUnlink(t *testing.T) {
	setup(t)
	project := t.TempDir()
	path, _ := clientconfig.DefaultPath()
	cfg, _ := clientconfig.Load(path)
	link := clientconfig.Link{Context: "prod", Project: clientconfig.Ref{ID: "p1", Name: "shop"}, Environment: clientconfig.Ref{ID: "e1", Name: "production"}}
	if err := cfg.SetLink(project, link); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}

	t.Chdir(project)
	code, stdout, stderr := exec(t, "unlink")
	if code != 0 || !strings.Contains(stdout, "shop / production") {
		t.Fatalf("unlink = %d %q %q", code, stdout, stderr)
	}
	if code, _, _ := exec(t, "unlink"); code != int(output.ExitNotFound) {
		t.Errorf("second unlink = %d, want not found", code)
	}
}
