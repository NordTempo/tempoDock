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
}
