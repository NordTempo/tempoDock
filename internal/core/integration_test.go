//go:build integration

package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/docker"
	"github.com/NordTempo/tempoDock/internal/secrets"
)

// Core against a real Docker Engine. Routing is a no-op (no Caddy needed)
// and builds are unsupported; image deploys only.

type noProxy struct{}

func (noProxy) ApplyRoutes(context.Context, []node.Route) error { return nil }
func (noProxy) CheckDomain(context.Context, string) (node.DomainCheck, error) {
	return node.DomainCheck{}, nil
}

// noStats keeps the minute statistics tick from sampling (and from calling
// a nil Stats) during these tests.
type noStats struct{}

func (noStats) HostStats(context.Context) (node.HostStats, error) {
	return node.HostStats{}, errors.New("no stats in this test")
}

func (noStats) ContainerStats(context.Context, []string) ([]node.ContainerStats, error) {
	return nil, nil
}

func (noStats) DrainTraffic(context.Context) ([]node.HostTraffic, error) { return nil, nil }

type noBuilder struct{}

func (noBuilder) Build(context.Context, node.BuildRequest) (string, error) {
	return "", errors.New("no builds in this test")
}

func (noBuilder) PruneBuildCache(context.Context, int64) (node.PruneResult, error) {
	return node.PruneResult{}, nil
}

// dockerHarness starts Core on the local Docker Engine with a temp data
// dir. DB IDs start at a random high number, so container labels and names
// (dock_p<id>_...) can't match those of another daemon on the same Docker
// host, and host ports come from a range of their own.
func dockerHarness(t *testing.T) *Core {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st := openTestStore(t, filepath.Join(dir, "dock.db"))
	box, err := secrets.New(bytes.Repeat([]byte{7}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := docker.New(netip.MustParsePrefix("10.213.0.0/16"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.DataDir = dir
	base := freePortRange(t)
	cfg.HostPorts = config.PortRange{Min: base, Max: base + 99}
	c, err := New(ctx, Options{Config: cfg, Store: st, Secrets: box, Runtime: node.Local{Containers: rt, Builder: noBuilder{}, Proxy: noProxy{}, Stats: noStats{}, Traffic: noStats{}}, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	id := 1_000_000 + rand.Int64N(1_000_000)*10
	for _, q := range []string{
		fmt.Sprintf(`INSERT INTO projects (id, org_id, name) VALUES (%d, 1, 'seed')`, id),
		fmt.Sprintf(`INSERT INTO environments (id, project_id, name, server_id) VALUES (%d, %d, 'seed', %d)`, id, id, c.local.ID),
		fmt.Sprintf(`INSERT INTO services (id, environment_id, name) VALUES (%d, %d, 'seed')`, id, id),
		fmt.Sprintf(`INSERT INTO releases (id, service_id, number, status, source, config, created_by) VALUES (%d, %d, 1, 'failed', 'image', x'00', 'seed')`, id, id),
	} {
		if _, err := st.DB.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Archive everything (stops and removes containers), then stop.
		ps, _ := c.ListProjects(ctx, testSystem, false)
		for _, p := range ps {
			_, _ = c.ArchiveProject(ctx, testSystem, p.Name)
		}
		deadline := time.Now().Add(time.Minute)
		for time.Now().Before(deadline) {
			ctrs, _ := rt.List(ctx)
			mine := 0
			for _, ct := range ctrs {
				if r, _ := c.store.GetRelease(ctx, parseID(ct.Labels.Release)); r.ID != 0 {
					mine++
				}
			}
			if mine == 0 {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		c.Close()
		if cli, err := client.New(client.FromEnv); err == nil {
			for _, p := range ps {
				envs, _ := c.ListEnvironments(ctx, testSystem, p.Name)
				for _, e := range envs {
					es, _ := c.loadEnv(ctx, 1, EnvRef{p.Name, e.Name})
					_, _ = cli.NetworkRemove(ctx, networkName(es), client.NetworkRemoveOptions{})
				}
			}
			_ = cli.Close()
		}
		_ = st.Close()
		_ = rt.Close()
	})
	return c
}

func parseID(s string) int64 {
	var n int64
	_, _ = fmt.Sscan(s, &n)
	return n
}

func waitRelease(t *testing.T, c *Core, ref ServiceRef, number int64) apitypes.Release {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		rel, err := c.GetRelease(context.Background(), testSystem, ref, number)
		if err != nil {
			t.Fatal(err)
		}
		if rel.Status.Done() {
			return rel
		}
		if time.Now().After(deadline) {
			t.Fatalf("release %d of %s stuck in %s", number, ref, rel.Status)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Right after a cutover the old container still holds its host port while
// it drains and stops. Deploys on the same server in that window must not
// be handed that port ("port is already allocated").
func TestIntegrationDeploysDuringDrain(t *testing.T) {
	c := dockerHarness(t)
	ctx := context.Background()
	project := fmt.Sprintf("it-%d", time.Now().UnixNano()%1_000_000)
	if _, err := c.CreateProject(ctx, testSystem, apitypes.CreateProjectRequest{Name: project}); err != nil {
		t.Fatal(err)
	}
	env := EnvRef{project, "production"}
	a, b := ServiceRef{project, "production", "a"}, ServiceRef{project, "production", "b"}
	for _, ref := range []ServiceRef{a, b} {
		if _, err := c.CreateService(ctx, testSystem, env, apitypes.CreateServiceRequest{Name: ref.Service, Port: 80}); err != nil {
			t.Fatal(err)
		}
	}
	const image = "traefik/whoami:latest"
	deploy := func(ref ServiceRef) int64 {
		t.Helper()
		rel, err := c.DeployImage(ctx, testSystem, ref, apitypes.DeployImageRequest{Image: image})
		if err != nil {
			t.Fatal(err)
		}
		return rel.Number
	}
	if r := waitRelease(t, c, a, deploy(a)); r.Status != apitypes.ReleaseActive {
		t.Fatalf("a release 1: %s %s", r.Status, r.Error)
	}

	// A restart is a cutover: release 1 drains (12s) and then stops.
	restart, err := c.Restart(ctx, testSystem, a)
	if err != nil {
		t.Fatal(err)
	}
	if r := waitRelease(t, c, a, restart.Number); r.Status != apitypes.ReleaseActive {
		t.Fatalf("a release 2: %s %s", r.Status, r.Error)
	}
	// Release 1 is still draining with its port; B must get another.
	if r := waitRelease(t, c, b, deploy(b)); r.Status != apitypes.ReleaseActive {
		t.Fatalf("b release 1 during a's drain: %s %s", r.Status, r.Error)
	}
	// And another restart of A while release 1 may still be stopping.
	again, err := c.Restart(ctx, testSystem, a)
	if err != nil {
		t.Fatal(err)
	}
	if r := waitRelease(t, c, a, again.Number); r.Status != apitypes.ReleaseActive {
		t.Fatalf("a release 3: %s %s", r.Status, r.Error)
	}
}

// Docker's die and destroy events for containers the daemon stops itself
// (stateful recreate, archive) are not drift.
func TestIntegrationOwnRemovalsAreNotDrift(t *testing.T) {
	c := dockerHarness(t)
	ctx := context.Background()
	project := fmt.Sprintf("it-%d", time.Now().UnixNano()%1_000_000)
	if _, err := c.CreateProject(ctx, testSystem, apitypes.CreateProjectRequest{Name: project}); err != nil {
		t.Fatal(err)
	}
	db := ServiceRef{project, "production", "db"}
	if _, err := c.CreateService(ctx, testSystem, db.Env(), apitypes.CreateServiceRequest{Name: "db", StartCommand: "trap 'exit 0' TERM; while true; do sleep 1; done", HealthCheckTimeoutSeconds: 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddVolume(ctx, testSystem, db, apitypes.AddVolumeRequest{Name: "data", MountPath: "/data"}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		rel, err := c.DeployImage(ctx, testSystem, db, apitypes.DeployImageRequest{Image: "alpine:latest"})
		if err != nil {
			t.Fatal(err)
		}
		if r := waitRelease(t, c, db, rel.Number); r.Status != apitypes.ReleaseActive {
			t.Fatalf("release %d: %s %s", r.Number, r.Status, r.Error)
		}
	}
	if svc, _ := c.GetService(ctx, testSystem, db); svc.Status.State != apitypes.ServiceRunning {
		t.Fatalf("status after recreate = %+v", svc.Status)
	}
	if _, err := c.ArchiveService(ctx, testSystem, db); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Minute)
	for {
		c.statusMu.Lock()
		n := len(c.removing)
		c.statusMu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("removed containers' events never arrived")
		}
		time.Sleep(100 * time.Millisecond)
	}
	evs, err := c.ListAudit(ctx, testSystem, AuditFilter{Project: project, Action: "reconcile.drift"})
	if err != nil || len(evs) > 0 {
		t.Fatalf("drift: %+v %v", evs, err)
	}
	// The volume is kept.
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()
	ss, _ := c.loadService(ctx, 1, db)
	vols, _ := c.store.ListVolumes(ctx, ss.svc.ID)
	if _, err := cli.VolumeInspect(ctx, vols[0].DockerName, client.VolumeInspectOptions{}); err != nil {
		t.Fatalf("volume gone: %v", err)
	}
	// Test cleanup: the platform never removes volumes.
	for start := time.Now(); ; time.Sleep(200 * time.Millisecond) {
		_, err := cli.VolumeRemove(ctx, vols[0].DockerName, client.VolumeRemoveOptions{})
		if err == nil {
			break
		}
		if time.Since(start) > 30*time.Second {
			t.Errorf("remove test volume: %v", err)
			break
		}
	}
}

// freePortRange picks 100 host ports below Linux's ephemeral range (32768+),
// where outgoing connections can't take them, and checks the first ones are
// free right now.
func freePortRange(t *testing.T) int {
	t.Helper()
	for range 50 {
		base := 21000 + rand.IntN(110)*100
		free := true
		for p := base; p < base+20 && free; p++ {
			l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
			if err != nil {
				free = false
				continue
			}
			_ = l.Close()
		}
		if free {
			return base
		}
	}
	t.Fatal("no free host port range")
	return 0
}
