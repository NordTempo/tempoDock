package core

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/fake"
	"github.com/NordTempo/tempoDock/internal/node/traffic"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store"
)

// BenchmarkTrafficTick50 is the minute tick's traffic step for a server
// whose 50 services all had requests: map hostnames and write 50 minute
// and hour points and 2,500 path counts in one transaction. Core isn't started,
// so nothing else runs.
func BenchmarkTrafficTick50(b *testing.B) {
	ctx := context.Background()
	dir := b.TempDir()
	st := openTestStore(b, filepath.Join(dir, "dock.db"))
	defer func() { _ = st.Close() }()
	box, _ := secrets.New(bytes.Repeat([]byte{7}, secrets.KeySize))
	cfg := config.Default()
	cfg.DataDir = dir
	c, err := New(ctx, Options{Config: cfg, Store: st, Secrets: box, Runtime: fake.New(), Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		b.Fatal(err)
	}
	if _, err := c.CreateProject(ctx, testSystem, apitypes.CreateProjectRequest{Name: "shop"}); err != nil {
		b.Fatal(err)
	}
	env := EnvRef{"shop", "production"}
	hosts := make([]node.HostTraffic, 50)
	verified := now() // routed, so the collector knows them (ADR 0042)
	for i := range hosts {
		svc, err := c.CreateService(ctx, testSystem, env, apitypes.CreateServiceRequest{Name: fmt.Sprintf("s%d", i), Image: "app:1"})
		if err != nil {
			b.Fatal(err)
		}
		host := fmt.Sprintf("s%d.example.com", i)
		if _, err := st.CreateDomain(ctx, store.CreateDomainParams{Hostname: host, ServiceID: svc.ID, VerifiedAt: &verified}); err != nil {
			b.Fatal(err)
		}
		paths := make([]node.PathCount, traffic.DrainPaths)
		for j := range paths {
			paths[j] = node.PathCount{Method: "GET", Path: fmt.Sprintf("/api/things/:id/part%d", j), Requests: uint64(100 - j)}
		}
		hosts[i] = node.HostTraffic{Host: host, Requests: 5000, Status: [4]uint64{4900, 50, 40, 10}, Bytes: 5 << 20, Duration: time.Minute, Paths: paths}
	}
	srv, err := st.GetServer(ctx, c.localID)
	if err != nil {
		b.Fatal(err)
	}
	col := newCollector()
	t := time.Date(2031, 1, 1, 10, 0, 30, 0, time.UTC)
	tick := func() {
		t = t.Add(time.Minute)
		col.samples = []serverSample{{srv: srv, traffic: hosts}}
		c.trafficTick(ctx, col, t, t.Truncate(time.Minute).Unix()-statsMinute)
	}
	tick()
	if len(col.traffic.services) != 50 {
		b.Fatalf("counted %d services", len(col.traffic.services))
	}
	b.ReportAllocs()
	for b.Loop() {
		tick()
	}
}
