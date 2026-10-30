//go:build integration

package docker

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/node"
)

// analyticsTestImage is the store's image as `make image-analytics
// VERSION=dev` builds it (CI builds it before the integration tests).
const analyticsTestImage = "dock-analytics:dev"

// The analytics store's container (ADR 0037) on a real Docker: it runs,
// answers on its loopback port, keeps its data across a recreate for a
// new token, and goes (but not its volume) for a nil spec.
func TestIntegrationAnalyticsStore(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if ok, _ := e.HasImage(ctx, analyticsTestImage); !ok {
		t.Skipf("no %s image; build it with `make image-analytics VERSION=dev`", analyticsTestImage)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	a := &AnalyticsStore{cli: e.cli, name: e.name("analytics"), volume: e.name("analytics-data"), port: port, owner: "/test/" + e.prefix}

	spec := &node.AnalyticsStoreSpec{Image: analyticsTestImage, Token: strings.Repeat("a", 32), MemoryBytes: 512 << 20}
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

	if err := a.Ensure(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cli.ContainerInspect(ctx, a.name, client.ContainerInspectOptions{}); err == nil {
		t.Error("a nil spec left the container")
	}
	if _, err := e.cli.VolumeInspect(ctx, a.volume, client.VolumeInspectOptions{}); err != nil {
		t.Errorf("the volume went with the container: %v", err)
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
