//go:build integration

package docker

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
)

// TestDialPostgres runs SELECT 1 through Dial on a Postgres that publishes
// no port, and checks Dial refuses everything that isn't a service.
func TestDialPostgres(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, 3*time.Minute)
	const pgImage = "postgres:17-alpine"
	if ok, _ := e.HasImage(ctx, pgImage); !ok {
		if _, err := e.Pull(ctx, pgImage, nil); err != nil {
			t.Fatal(err)
		}
	}

	envNet := e.name("net")
	labels := node.Labels{Project: "p1", Environment: "e1", Service: "s1", Release: "r1"}
	if err := e.EnsureNetwork(ctx, envNet, labels); err != nil {
		t.Fatal(err)
	}
	pg := e.start(node.ContainerSpec{
		Name: e.name("pg"), Image: pgImage, Labels: labels, Network: envNet,
		Env: map[string]string{"POSTGRES_PASSWORD": "secret"},
	})
	// Also on Docker's default network: Dial must still pick the
	// environment's.
	if _, err := e.cli.NetworkConnect(ctx, "bridge", client.NetworkConnectOptions{Container: pg.ID}); err != nil {
		t.Fatal(err)
	}
	netRes, err := e.cli.NetworkInspect(ctx, envNet, client.NetworkInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	subnet := netRes.Network.IPAM.Config[0].Subnet

	cfg, err := pgconn.ParseConfig("postgres://postgres:secret@db/postgres?sslmode=disable&connect_timeout=5")
	if err != nil {
		t.Fatal(err)
	}
	var remote net.Addr
	cfg.LookupFunc = func(_ context.Context, host string) ([]string, error) { return []string{host}, nil }
	cfg.DialFunc = func(ctx context.Context, _, _ string) (net.Conn, error) {
		c, err := e.Dial(ctx, node.DialRequest{ContainerID: pg.ID, Port: 5432})
		if err == nil {
			remote = c.RemoteAddr()
		}
		return c, err
	}

	// Postgres takes a few seconds to accept connections.
	var conn *pgconn.PgConn
	for {
		conn, err = pgconn.ConnectConfig(ctx, cfg)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("connect: %v", err)
		}
		time.Sleep(250 * time.Millisecond)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	res, err := conn.Exec(ctx, "SELECT 1").ReadAll()
	if err != nil || len(res) != 1 || len(res[0].Rows) != 1 || string(res[0].Rows[0][0]) != "1" {
		t.Fatalf("SELECT 1 = %+v, %v", res, err)
	}
	if ap, err := netip.ParseAddrPort(remote.String()); err != nil || !subnet.Contains(ap.Addr()) {
		t.Fatalf("dialed %v, want an address in the environment network %s", remote, subnet)
	}

	refused := func(what, id string) {
		t.Helper()
		if c, err := e.Dial(ctx, node.DialRequest{ContainerID: id, Port: 5432}); !errors.Is(err, node.ErrNotFound) {
			if c != nil {
				_ = c.Close()
			}
			t.Errorf("Dial %s = %v, want ErrNotFound", what, err)
		}
	}
	refused("missing container", "does-not-exist")
	for name, l := range map[string]map[string]string{
		"unmanaged": {node.LabelService: "s1"},
		"infra":     {node.LabelManaged: "true", node.LabelService: "s1", node.LabelRole: "test"},
	} {
		created, err := e.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
			Name:   e.name(name),
			Config: &container.Config{Image: shellImage, Cmd: []string{"sleep", "300"}, Labels: l},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
			t.Fatal(err)
		}
		refused(name+" container", created.ID)
	}
	notService := e.start(node.ContainerSpec{
		Name: e.name("not-service"), Image: shellImage, Cmd: "sleep 300", Network: envNet,
		Labels: node.Labels{Project: "p1", Environment: "e1"},
	})
	refused("container without a service label", notService.ID)

	if err := e.Stop(ctx, pg.ID, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Dial(ctx, node.DialRequest{ContainerID: pg.ID, Port: 5432}); err == nil {
		t.Error("Dial of a stopped container succeeded")
	}
}
