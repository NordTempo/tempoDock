//go:build integration

package core

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// On real Docker the pre-deploy command runs in the new image with the
// release's variables and reaches another service over the environment's
// private network. A failing one is TestPreDeployFailureKeepsOldRelease's
// (real exit codes: TestIntegrationRunCommand).
func TestIntegrationPreDeploy(t *testing.T) {
	c := dockerHarness(t)
	ctx := context.Background()
	project := fmt.Sprintf("it-%d", time.Now().UnixNano()%1_000_000)
	if _, err := c.CreateProject(ctx, testSystem, apitypes.CreateProjectRequest{Name: project}); err != nil {
		t.Fatal(err)
	}
	// db stands in for a database: it answers HTTP on the private network.
	db := ServiceRef{project, "production", "db"}
	if _, err := c.CreateService(ctx, testSystem, db.Env(), apitypes.CreateServiceRequest{
		Name: "db", StartCommand: `trap 'exit 0' TERM; while true; do printf 'HTTP/1.0 200 OK\r\n\r\nschema-ok\n' | nc -l -p 8080; done`, HealthCheckTimeoutSeconds: 20,
	}); err != nil {
		t.Fatal(err)
	}
	web := ServiceRef{project, "production", "web"}
	if _, err := c.CreateService(ctx, testSystem, web.Env(), apitypes.CreateServiceRequest{
		Name: "web", StartCommand: "trap 'exit 0' TERM; while true; do sleep 1; done", HealthCheckTimeoutSeconds: 20,
		PreDeployCommand: `wget -q -O- http://db:8080/ && echo "migrated for $DOCK_SERVICE"`,
	}); err != nil {
		t.Fatal(err)
	}
	deploy := func(ref ServiceRef) apitypes.Release {
		t.Helper()
		rel, err := c.DeployImage(ctx, testSystem, ref, apitypes.DeployImageRequest{Image: "alpine:latest"})
		if err != nil {
			t.Fatal(err)
		}
		return waitRelease(t, c, ref, rel.Number)
	}
	buildLog := func(number int64) string {
		t.Helper()
		rc, err := c.BuildLog(ctx, testSystem, web, number)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rc.Close() }()
		b, _ := io.ReadAll(rc)
		return string(b)
	}
	if r := deploy(db); r.Status != apitypes.ReleaseActive {
		t.Fatalf("db: %s %s", r.Status, r.Error)
	}
	first := deploy(web)
	if first.Status != apitypes.ReleaseActive {
		t.Fatalf("web release 1: %s %s\n%s", first.Status, first.Error, buildLog(first.Number))
	}
	if log := buildLog(first.Number); !strings.Contains(log, "schema-ok\nmigrated for web\n") {
		t.Fatalf("build log of release 1:\n%s", log)
	}
}
