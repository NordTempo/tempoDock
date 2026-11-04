package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
	"github.com/NordTempo/tempoDock/internal/node"
)

// TestJob: `dock job set` creates a job with its next time, `run` waits
// for a run and passes its exit code on, `history` lists the runs, and
// `remove` takes the job away.
func TestJob(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	exec(t, "project", "create", "shop")
	at := []string{"-p", "shop", "-e", "production", "-s", "web"}
	ok[apitypes.Release](t, append([]string{"deploy", "--image", "web:1"}, at...)...)

	// --json goes before --: ok would put it after, into the command.
	code, stdout, stderr := exec(t, append(append([]string{"job", "set", "nightly", "--schedule", "0 3 * * *", "--timezone", "Europe/Oslo", "--timeout", "10m", "--json"}, at...), "--", "rake", "cleanup:all")...)
	var j apitypes.Job
	if err := json.Unmarshal([]byte(stdout), &j); err != nil || code != 0 || j.Name != "nightly" || j.Command != "rake cleanup:all" || j.Timezone != "Europe/Oslo" || j.TimeoutSeconds != 600 || !j.Enabled || j.NextAt == nil || j.Source != apitypes.JobFromAPI {
		t.Fatalf("job set: exit %d %+v\n%s", code, j, stderr)
	}
	if code, _, stderr := exec(t, append([]string{"job", "set", "nightly", "--schedule", "@daily", "rake"}, at...)...); code != int(output.ExitUsage) || !strings.Contains(stderr, "after --") {
		t.Fatalf("job set without --: exit %d, stderr %q", code, stderr)
	}
	if _, stdout, _ := exec(t, append([]string{"job", "list"}, at...)...); !strings.Contains(stdout, "nightly") || !strings.Contains(stdout, "Europe/Oslo") || !strings.Contains(stdout, " in ") {
		t.Fatalf("job list:\n%s", stdout)
	}

	// Run now waits for the run, prints its output and exits with its code.
	srv.rt.OnJob(func(node.JobSpec) (node.JobResult, error) {
		return node.JobResult{ExitCode: 3, Output: "cleaned 0 rows\n"}, nil
	})
	if code, stdout, _ := exec(t, append([]string{"job", "run", "nightly"}, at...)...); code != 3 || stdout != "cleaned 0 rows\n" {
		t.Fatalf("job run: exit %d, stdout %q", code, stdout)
	}
	srv.rt.OnJob(nil)
	code, stdout, stderr = exec(t, append([]string{"job", "run", "nightly", "--json"}, at...)...)
	var r apitypes.CommandRun
	if err := json.Unmarshal([]byte(stdout), &r); err != nil || code != 0 || r.Status != apitypes.RunSucceeded || r.Trigger != apitypes.TriggerManual {
		t.Fatalf("job run --json: exit %d %+v\n%s", code, r, stderr)
	}

	runs := ok[[]apitypes.CommandRun](t, append([]string{"job", "history", "nightly"}, at...)...)
	if len(runs) != 2 || runs[0].ID != r.ID || runs[1].ExitCode == nil || *runs[1].ExitCode != 3 {
		t.Fatalf("job history = %+v", runs)
	}

	ok[map[string]string](t, append([]string{"job", "remove", "nightly"}, at...)...)
	if jobs := ok[[]apitypes.Job](t, append([]string{"job", "list"}, at...)...); len(jobs) != 0 {
		t.Fatalf("jobs after remove = %+v", jobs)
	}
}
