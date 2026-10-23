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
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store"
)

// BenchmarkStatsTick50 is one minute tick on a server running 50 services:
// list targets, sample (fake runtime, so this is the control plane's own
// cost; see internal/node/stats for the file reads), compute rates and
// write 51 points in one transaction. Core isn't started, so nothing else
// runs.
func BenchmarkStatsTick50(b *testing.B) {
	ctx := context.Background()
	dir := b.TempDir()
	st := openTestStore(b, filepath.Join(dir, "dock.db"))
	defer func() { _ = st.Close() }()
	box, _ := secrets.New(bytes.Repeat([]byte{7}, secrets.KeySize))
	cfg := config.Default()
	cfg.DataDir = dir
	rt := fake.New()
	c, err := New(ctx, Options{Config: cfg, Store: st, Secrets: box, Runtime: rt, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		b.Fatal(err)
	}
	if _, err := c.CreateProject(ctx, testSystem, apitypes.CreateProjectRequest{Name: "shop"}); err != nil {
		b.Fatal(err)
	}
	env := EnvRef{"shop", "production"}
	var ids []string
	for i := range 50 {
		svc, err := c.CreateService(ctx, testSystem, env, apitypes.CreateServiceRequest{Name: fmt.Sprintf("s%d", i), Image: "app:1"})
		if err != nil {
			b.Fatal(err)
		}
		ct, _ := rt.Start(ctx, node.ContainerSpec{Name: fmt.Sprintf("c%d", i)})
		rel, err := st.CreateRelease(ctx, store.CreateReleaseParams{ServiceID: svc.ID, Number: 1, Source: "image", Config: []byte{}, CreatedBy: "b"})
		if err != nil {
			b.Fatal(err)
		}
		_ = st.SetReleaseContainer(ctx, store.SetReleaseContainerParams{ContainerID: ct.ID, ID: rel.ID})
		_ = st.ActivateRelease(ctx, store.ActivateReleaseParams{FinishedAt: ptr(now()), ID: rel.ID})
		ids = append(ids, ct.ID)
	}
	col := newCollector()
	t := time.Date(2031, 1, 1, 10, 0, 30, 0, time.UTC) // off the 10-minute boundaries
	n := uint64(0)
	step := func() {
		n++
		t = t.Add(time.Minute)
		for _, id := range ids {
			rt.SetContainerStats(node.ContainerStats{ID: id, Time: t, CPUUsec: n * 1e6, Memory: 64 << 20})
		}
	}
	step()
	c.statsTick(ctx, col, t)
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		step()
		b.StartTimer()
		c.statsTick(ctx, col, t)
	}
	if len(col.services) != 50 {
		b.Fatalf("stored %d service points", len(col.services))
	}
}
