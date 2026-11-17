package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
	"github.com/NordTempo/tempoDock/internal/node"
)

// Promote follows the releases it starts like deploy does, lists what it
// skipped, and exits 6 when a promoted release fails.
func TestPromote(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeDeploy])
	exec(t, "project", "create", "shop")
	if code, _, stderr := exec(t, "environment", "create", "staging", "-p", "shop"); code != 0 {
		t.Fatalf("create staging: %s", stderr)
	}
	for _, args := range [][]string{
		{"deploy", "-p", "shop", "-e", "staging", "-s", "web", "--image", "web:1"},
		{"deploy", "-p", "shop", "-e", "staging", "-s", "worker", "--image", "worker:1"},
		{"service", "create", "web", "-p", "shop", "-e", "production", "--image", "web:0"},
	} {
		if code, _, stderr := exec(t, args...); code != 0 {
			t.Fatalf("%v: %s", args, stderr)
		}
	}

	res := ok[apitypes.PromoteResult](t, "promote", "staging", "production", "-p", "shop")
	if len(res.Promoted) != 1 || res.Promoted[0].Service != "web" || res.Promoted[0].Release.Status != apitypes.ReleaseActive ||
		res.Promoted[0].Release.ImageRef != "web:1" || res.Promoted[0].Release.Source != apitypes.SourcePromote {
		t.Fatalf("promoted = %+v", res.Promoted)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Service != "worker" || res.Skipped[0].Reason != apitypes.SkipMissingInTarget {
		t.Fatalf("skipped = %+v", res.Skipped)
	}

	srv.rt.FailHealthCheck(func(node.ContainerSpec) error { return errors.New("connection refused") })
	code, stdout, stderr := exec(t, "promote", "staging", "production", "-p", "shop", "-s", "web")
	if code != int(output.ExitDeployFailed) || !strings.Contains(stderr, "health check") {
		t.Fatalf("failed promote: exit %d\n%s%s", code, stdout, stderr)
	}

	_, stdout, _ = exec(t, "promote", "staging", "production", "-p", "shop", "-s", "web", "--detach", "--json")
	var detached apitypes.PromoteResult
	if err := json.Unmarshal([]byte(stdout), &detached); err != nil || len(detached.Promoted) != 1 || detached.Promoted[0].Release.Status.Done() {
		t.Fatalf("--detach: %v %s", err, stdout)
	}

	// Nothing promoted: exit 4, with the skipped services still listed.
	code, _, stderr = exec(t, "promote", "staging", "production", "-p", "shop", "-s", "worker")
	if code != int(output.ExitNotFound) || !strings.Contains(stderr, "worker skipped") || !strings.Contains(stderr, "nothing was promoted") {
		t.Fatalf("all skipped: exit %d\n%s", code, stderr)
	}
	code, stdout, _ = exec(t, "promote", "staging", "production", "-p", "shop", "-s", "worker", "--json")
	var skipped apitypes.PromoteResult
	if err := json.Unmarshal([]byte(stdout), &skipped); code != int(output.ExitNotFound) || err != nil || len(skipped.Skipped) != 1 {
		t.Fatalf("all skipped --json: exit %d, %v %s", code, err, stdout)
	}
}

// A promotion that stopped at a service it couldn't start is a deploy
// failure (exit 6), even though the services before it went live.
func TestPromoteStoppedIsADeployFailure(t *testing.T) {
	err := promoteStopped([]apitypes.SkippedService{
		{Service: "web", Reason: apitypes.SkipFailed, Message: "web couldn't start in qa: variable API_SECRET of web references ${{api.SECRET_KEY}}"},
		{Service: "worker", Reason: apitypes.SkipFailed, Message: "not promoted: web failed first"},
	})
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Exit != output.ExitDeployFailed || !strings.Contains(oe.Message, "web wasn't promoted: web couldn't start") {
		t.Fatalf("err = %v", err)
	}
	if err := promoteStopped([]apitypes.SkippedService{{Service: "db", Reason: apitypes.SkipMissingInTarget}}); err != nil {
		t.Errorf("a service missing in the target: %v", err)
	}
}

// `dock rollback` without a number skips releases that run what is live:
// after a restart, the release before it is the one restarted.
func TestRollbackSkipsCopiesOfTheLiveRelease(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeDeploy])
	exec(t, "project", "create", "shop")
	for _, args := range [][]string{
		{"deploy", "-p", "shop", "-e", "production", "-s", "web", "--image", "web:1"},
		{"deploy", "-p", "shop", "-e", "production", "-s", "web", "--image", "web:2"},
		{"restart", "-p", "shop", "-e", "production", "-s", "web"},
		{"restart", "-p", "shop", "-e", "production", "-s", "web"},
	} {
		if code, _, stderr := exec(t, args...); code != 0 {
			t.Fatalf("%v: %s", args, stderr)
		}
	}
	back := ok[apitypes.Release](t, "rollback", "-p", "shop", "-e", "production", "-s", "web")
	if back.FromRelease != 1 || back.ImageRef != "web:1" {
		t.Fatalf("rollback copied release %d (%s), want 1", back.FromRelease, back.ImageRef)
	}
}
