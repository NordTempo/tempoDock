//go:build integration

package docker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// hangServer is an HTTP server in busybox nc that answers 200 until GET
// /hang, then keeps accepting connections but never answers them, like an
// app that hung. A start clears the trigger.
const hangServer = `rm -f /tmp/hang
cat > /tmp/h.sh <<'EOF'
#!/bin/sh
read -r method path proto
if [ "$path" = /hang ]; then touch /tmp/hang
elif [ -f /tmp/hang ]; then exec sleep 600
fi
printf 'HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok'
EOF
chmod +x /tmp/h.sh
exec nc -lk -p 8080 -e /tmp/h.sh`

// Docker runs dock-probe as the container's healthcheck: health_status
// events report healthy, then unhealthy once the app hangs, with the
// probe's reason in Inspect; Restart brings it back, and leaves a stopped
// container alone.
func TestLivenessCheck(t *testing.T) {
	e := newEnv(t)
	e.Probe = buildProbe(t)
	ctx := ctxTimeout(t, 2*time.Minute)

	evCtx, stopEvents := context.WithCancel(ctx)
	defer stopEvents()
	events, err := e.Events(evCtx)
	if err != nil {
		t.Fatal(err)
	}
	c := e.start(node.ContainerSpec{
		Name: e.name("live"), Image: shellImage, Cmd: hangServer, Port: 8080,
		Labels: node.Labels{Project: "p", Environment: "e", Service: "s", Release: "1"},
		Health: &node.LivenessCheck{Port: 8080, Path: "/health", Interval: time.Second, Timeout: time.Second, Retries: 2},
	})
	// health waits for c's next health_status event.
	health := func(want string) {
		t.Helper()
		for {
			select {
			case ev := <-events:
				if ev.ContainerID != c.ID || ev.Action != "health_status" {
					continue
				}
				if ev.Health != want {
					t.Fatalf("health event = %+v, want %s", ev, want)
				}
				return
			case <-ctx.Done():
				t.Fatalf("no %s event", want)
			}
		}
	}
	health(node.HealthHealthy)

	hc := http.Client{Timeout: 10 * time.Second}
	resp, err := hc.Get(fmt.Sprintf("http://127.0.0.1:%d/hang", c.HostPort))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	health(node.HealthUnhealthy)
	got, err := e.Inspect(ctx, c.ID)
	if err != nil || got.Health != node.HealthUnhealthy || !strings.Contains(got.HealthOutput, "GET /health: timed out") {
		t.Fatalf("inspect unhealthy = %+v, %v", got, err)
	}

	if err := e.Restart(ctx, c.ID, time.Second); err != nil {
		t.Fatal(err)
	}
	if got, err := e.Inspect(ctx, c.ID); err != nil || got.ID != c.ID || got.State != node.StateRunning ||
		got.Health != node.HealthStarting && got.Health != node.HealthHealthy {
		t.Fatalf("inspect after restart = %+v, %v", got, err)
	}
	health(node.HealthHealthy)

	if err := e.Stop(ctx, c.ID, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := e.Restart(ctx, c.ID, time.Second); !errors.Is(err, node.ErrNotRunning) {
		t.Fatalf("restart a stopped container = %v, want ErrNotRunning", err)
	}
	if got, err := e.Inspect(ctx, c.ID); err != nil || got.State != node.StateExited {
		t.Fatalf("inspect after restarting a stopped container = %+v, %v", got, err)
	}
}

// buildProbe builds cmd/dock-probe for this machine (static, like `make
// probe`) and returns its path.
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
