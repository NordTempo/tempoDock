//go:build integration

package core

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// hangingApp is an HTTP server (busybox nc) that answers 200 until it is
// asked for /hang; from then on it accepts connections but never answers
// them, while its process stays alive: an app that hung. A start clears
// the trigger, so a restart brings it back. It exits on SIGTERM, so a
// restart doesn't wait out the stop timeout.
const hangingApp = `rm -f /tmp/hang
cat > /tmp/h.sh <<'EOF'
#!/bin/sh
read -r method path proto
if [ "$path" = /hang ]; then touch /tmp/hang
elif [ -f /tmp/hang ]; then exec sleep 600
fi
printf 'HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok'
EOF
chmod +x /tmp/h.sh
trap 'kill $!; exit 0' TERM
nc -lk -p 8080 -e /tmp/h.sh &
wait`

// A live app that hangs while its process stays alive, on real Docker
// with dock-probe: it is marked unhealthy and alerted on, restarted at
// once, and recovers (resolving the alert). The backoff after that is
// TestUnhealthyServiceIsRestartedWithBackoff's.
func TestIntegrationUnhealthyAppIsRestarted(t *testing.T) {
	c := dockerHarnessWithProbe(t, buildProbe(t))
	ctx := context.Background()
	project := fmt.Sprintf("hc%d", time.Now().UnixNano()%1_000_000)
	if _, err := c.CreateProject(ctx, testSystem, apitypes.CreateProjectRequest{Name: project}); err != nil {
		t.Fatal(err)
	}
	ref := ServiceRef{project, "production", "web"}
	if _, err := c.CreateService(ctx, testSystem, ref.Env(), apitypes.CreateServiceRequest{
		Name: ref.Service, Port: 8080, HealthCheckPath: "/health", StartCommand: hangingApp, HealthCheckTimeoutSeconds: 30,
		HealthCheckIntervalSeconds: 1, HealthCheckProbeTimeoutSeconds: 1, HealthCheckRetries: 2,
	}); err != nil {
		t.Fatal(err)
	}
	rel, err := c.DeployImage(ctx, testSystem, ref, apitypes.DeployImageRequest{Image: "alpine:latest"})
	if err != nil {
		t.Fatal(err)
	}
	if r := waitRelease(t, c, ref, rel.Number); r.Status != apitypes.ReleaseActive {
		t.Fatalf("release %s: %s", r.Status, r.Error)
	}
	ss, err := c.serviceFor(ctx, testSystem, ref, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}
	active, err := c.store.GetActiveRelease(ctx, ss.svc.ID)
	if err != nil {
		t.Fatal(err)
	}
	key := healthAlertKey(ss.svc.ID)
	status := func() apitypes.ServiceStatus { return c.getStatus(ss.svc.ID) }
	alert := func() (string, bool) {
		c.alerts.mu.Lock()
		defer c.alerts.mu.Unlock()
		a, ok := c.alerts.open[key]
		return a.Severity + " " + a.Message, ok
	}
	until := func(what string, d time.Duration, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(d)
		for !cond() {
			if time.Now().After(deadline) {
				_, open := alert()
				t.Fatalf("timed out waiting: %s (status %+v, alert open %v)", what, status(), open)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	hang := func() {
		t.Helper()
		hc := http.Client{Timeout: 10 * time.Second}
		resp, err := hc.Get(fmt.Sprintf("http://127.0.0.1:%d/hang", active.HostPort))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	until("healthy", 30*time.Second, func() bool { return status().Health == apitypes.HealthHealthy })

	// Hangs: unhealthy, alerted on with the probe's reason, restarted at once.
	hang()
	until("alert", 30*time.Second, func() bool {
		msg, ok := alert()
		return ok && strings.Contains(msg, "GET /health: timed out")
	})
	until("first restart", 60*time.Second, func() bool { return status().HealthRestarts == 1 })
	until("recovered", 60*time.Second, func() bool { return status().Health == apitypes.HealthHealthy })
	until("alert resolved", 10*time.Second, func() bool { _, ok := alert(); return !ok })
	if s := status(); s.State != apitypes.ServiceRunning || s.RestartCount != 0 {
		t.Fatalf("status after recovering = %+v", s)
	}
}

// buildProbe builds cmd/dock-probe for this machine, static like `make
// probe`, and returns its path.
func buildProbe(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dock-probe")
	cmd := exec.Command("go", "build", "-trimpath", "-o", path, "github.com/NordTempo/tempoDock/cmd/dock-probe")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build dock-probe: %v\n%s", err, out)
	}
	return path
}
