package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
	"github.com/NordTempo/tempoDock/internal/node"
)

func TestShellJoin(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"rake a && rake b"}, "rake a && rake b"},
		{[]string{"psql", "-c", "select 1"}, "psql -c 'select 1'"},
		{[]string{"env", "a=b", "/x/y", "user@host:5432"}, "env a=b /x/y user@host:5432"},
		{[]string{"echo", "it's", "", "$HOME"}, `echo 'it'\''s' '' '$HOME'`},
	}
	for _, tt := range tests {
		if got := shellJoin(tt.args); got != tt.want {
			t.Errorf("shellJoin(%q) = %q, want %q", tt.args, got, tt.want)
		}
	}
}

// TestRun: `dock run` streams stdin and output, passes the command's exit
// code on, prints only the run on stdout with --json, and `dock runs`
// lists what ran.
func TestRun(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	exec(t, "project", "create", "shop")
	at := []string{"-p", "shop", "-e", "production"}
	ok[apitypes.Release](t, append([]string{"deploy", "-s", "web", "--image", "web:1"}, at...)...)

	// The fake runtime echoes a streamed run's stdin, like cat.
	code, stdout, stderr := execStdin(t, "hello", append(append([]string{"run", "-i", "-s", "web"}, at...), "--", "cat")...)
	if code != 0 || stdout != "hello" {
		t.Fatalf("run -i cat: exit %d, stdout %q\n%s", code, stdout, stderr)
	}

	// --json: the command's output goes to stderr, the run to stdout.
	code, stdout, stderr = execStdin(t, "hi", append(append([]string{"run", "web", "-i", "--json"}, at...), "--", "cat")...)
	var r apitypes.CommandRun
	if err := json.Unmarshal([]byte(stdout), &r); err != nil || code != 0 || stderr != "hi" ||
		r.Status != apitypes.RunSucceeded || r.ExitCode == nil || *r.ExitCode != 0 || r.Command != "cat" {
		t.Fatalf("run --json: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}

	// A non-zero exit is dock's, with nothing printed.
	srv.rt.OnJob(func(node.JobSpec) (node.JobResult, error) { return node.JobResult{ExitCode: 3}, nil })
	if code, _, stderr := exec(t, append(append([]string{"run", "web"}, at...), "--", "sh", "-c", "exit 3")...); code != 3 || stderr != "" {
		t.Fatalf("run exit 3: exit %d, stderr %q", code, stderr)
	}
	// A command that couldn't run is an error saying so.
	srv.rt.OnJob(func(node.JobSpec) (node.JobResult, error) { return node.JobResult{}, errors.New("no such image") })
	if code, _, stderr := exec(t, append(append([]string{"run", "web"}, at...), "--", "true")...); code != int(output.ExitError) || !strings.Contains(stderr, "no such image") {
		t.Fatalf("run that can't start: exit %d, stderr %q", code, stderr)
	}
	srv.rt.OnJob(nil)

	if code, _, stderr := exec(t, append([]string{"run", "web", "true"}, at...)...); code != int(output.ExitUsage) || !strings.Contains(stderr, "after --") {
		t.Fatalf("run without --: exit %d, stderr %q", code, stderr)
	}

	runs := ok[[]apitypes.CommandRun](t, append([]string{"runs", "web"}, at...)...)
	if len(runs) != 4 || runs[0].Command != "true" || runs[1].Command != "sh -c 'exit 3'" || runs[1].ExitCode == nil || *runs[1].ExitCode != 3 {
		t.Fatalf("runs = %+v", runs)
	}
	if _, stdout, _ := exec(t, append([]string{"runs", "show", itoa(runs[3].ID), "-s", "web"}, at...)...); !strings.Contains(stdout, "succeeded") || !strings.HasSuffix(stdout, "\nhello\n") {
		t.Fatalf("runs show: %s", stdout)
	}
}

// TestRunDetach: --detach starts a background run and prints it at once;
// it takes no input, and it is what a locked environment's hint suggests.
func TestRunDetach(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	exec(t, "project", "create", "shop")
	at := []string{"-p", "shop", "-e", "production", "-s", "web"}
	ok[apitypes.Release](t, append([]string{"deploy", "--image", "web:1"}, at...)...)

	// --json goes before --: after it, it would be part of the command.
	code, stdout, stderr := exec(t, append(append([]string{"run", "--detach", "--timeout", "90s", "--json"}, at...), "--", "rake", "db:seed")...)
	var r apitypes.CommandRun
	if err := json.Unmarshal([]byte(stdout), &r); err != nil || code != 0 || r.ID == 0 || r.Command != "rake db:seed" || r.TimeoutSeconds != 90 || r.Trigger != apitypes.TriggerCommand {
		t.Fatalf("run --detach: exit %d %+v\n%s", code, r, stderr)
	}
	if code, _, _ := exec(t, append(append([]string{"run", "-d", "-i"}, at...), "--", "cat")...); code != int(output.ExitUsage) {
		t.Fatalf("run -d -i: exit %d, want usage", code)
	}

	// In a locked environment a streamed run is refused and files an
	// access request, which runs it in the background once approved.
	ok[apitypes.Environment](t, "environment", "lock", "production", "-p", "shop")
	if code, _, stderr := exec(t, append(append([]string{"run"}, at...), "--", "true")...); code != int(output.ExitConflict) || !strings.Contains(stderr, "dock request wait") || !strings.Contains(stderr, "runs in the background") {
		t.Fatalf("locked run: exit %d, stderr %q", code, stderr)
	}
}

// A run that couldn't start because the image has no shell says so,
// instead of pointing at the release and the server.
func TestRunExitWithoutAShell(t *testing.T) {
	err := runExit(apitypes.CommandRun{ID: 4, Status: apitypes.RunFailed,
		Error: `it could not run: exec: "/bin/sh": stat /bin/sh: no such file or directory`})
	var oe *output.Error
	if !errors.As(err, &oe) || !strings.Contains(oe.Hint, "no /bin/sh") {
		t.Fatalf("err = %#v", err)
	}
}

// Right after a daemon restart a service on a server that hasn't
// reconnected has no state yet: unknown, not "none (last known)".
func TestServiceStateBeforeItsServerReconnects(t *testing.T) {
	never := apitypes.Service{Status: apitypes.ServiceStatus{State: apitypes.ServiceNone, ServerOffline: true, ServerStatus: apitypes.ServerOffline}}
	if got := serviceState(never); got != "unknown" {
		t.Errorf("never reported: %q", got)
	}
	at := time.Now()
	seen := apitypes.Service{Status: apitypes.ServiceStatus{State: apitypes.ServiceRunning, ServerOffline: true, UpdatedAt: &at}}
	if got := serviceState(seen); got != "running (last known)" {
		t.Errorf("reported before: %q", got)
	}
}
