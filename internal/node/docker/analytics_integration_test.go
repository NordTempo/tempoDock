//go:build integration

package docker

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/update"
)

// analyticsTestBinary is the store as `make analytics-binary` builds it
// (CI builds it before the integration tests).
var analyticsTestBinary = filepath.Join("..", "..", "..", "bin", "dock-analytics-linux-"+runtime.GOARCH)

// testAnalyticsStore is a store of its own for the test, on a free port.
func testAnalyticsStore(t *testing.T, e *testEnv, name string) *AnalyticsStore {
	t.Helper()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	a := &AnalyticsStore{cli: e.cli, name: e.name(name), volume: e.name(name + "-data"), port: port, owner: "/test/" + e.prefix}
	t.Cleanup(func() { _ = a.Remove(context.Background()) })
	return a
}

// The analytics store's container (ADR 0037) on a real Docker: the
// release's binary in the base image as a nonroot user, answering on its
// loopback port, keeping its data across a recreate for a new token,
// moving its database to another store (which restarts on it), and going
// with its data on Remove.
func TestIntegrationAnalyticsStore(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	bin, err := filepath.Abs(analyticsTestBinary)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := update.HashFile(bin)
	if err != nil {
		t.Skipf("no store binary (%v); build it with `make analytics-binary`", err)
	}
	a := testAnalyticsStore(t, e, "analytics")
	spec := node.AnalyticsStoreSpec{Binary: bin, SHA256: sum, Token: strings.Repeat("a", 32), MemoryBytes: 512 << 20}
	if err := a.Ensure(ctx, spec); err != nil {
		t.Fatal(err)
	}
	c := storeClient(a, spec.Token)
	waitStore(t, c)
	if _, err := c.Write(ctx, "server:1", 1, []analytics.Event{{Time: time.Now().UnixMilli(), Site: 1, Kind: analytics.KindPageview, Path: "/", Visitor: 7}}); err != nil {
		t.Fatal(err)
	}
	first := containerID(t, e, a.name)
	if err := a.Ensure(ctx, spec); err != nil || containerID(t, e, a.name) != first {
		t.Errorf("the same spec recreated the store: %v", err)
	}

	// A new token: a new container, the same data.
	spec.Token = strings.Repeat("b", 32)
	if err := a.Ensure(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if containerID(t, e, a.name) == first {
		t.Error("a new token kept the old container")
	}
	c = storeClient(a, spec.Token)
	waitStore(t, c)
	if pos, err := c.Cursor(ctx, "server:1"); err != nil || pos != 1 {
		t.Errorf("after the recreate the cursor is %d, %v; want the data kept", pos, err)
	}

	// A move: another store takes this one's database and restarts on it.
	b := testAnalyticsStore(t, e, "analytics-b")
	if err := b.Ensure(ctx, spec); err != nil {
		t.Fatal(err)
	}
	to := storeClient(b, spec.Token)
	waitStore(t, to)
	snap, err := c.Snapshot(ctx)
	if err != nil || snap.Events != 1 {
		t.Fatalf("snapshot = %+v, %v; want 1 event", snap, err)
	}
	got, err := to.Restore(ctx, snap, snap.Size)
	_ = snap.Close()
	if err != nil || got.Bytes != snap.Size {
		t.Fatalf("restore = %+v, %v; want %d bytes", got, err, snap.Size)
	}
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(200 * time.Millisecond) {
		if h, err := to.Health(ctx); err == nil && h.Events == 1 {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("the restored store didn't come back with the event: %+v, %v", h, err)
		}
	}

	if err := a.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cli.ContainerInspect(ctx, a.name, client.ContainerInspectOptions{}); err == nil {
		t.Error("Remove left the container")
	}
	if _, err := e.cli.VolumeInspect(ctx, a.volume, client.VolumeInspectOptions{}); err == nil {
		t.Error("Remove left the volume")
	}
}

// storeClient reaches the store a runs.
func storeClient(a *AnalyticsStore, token string) *analytics.Client {
	return &analytics.Client{BaseURL: "http://analytics", Token: token, HTTP: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return a.Dial(ctx) },
	}}}
}

// waitStore waits for the store to answer, as it opens its database.
func waitStore(t *testing.T, c *analytics.Client) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if _, err := c.Cursor(context.Background(), "x"); err == nil {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("the store didn't answer: %v", err)
		}
	}
}

func containerID(t *testing.T, e *testEnv, name string) string {
	t.Helper()
	res, err := e.cli.ContainerInspect(context.Background(), name, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return res.Container.ID
}
