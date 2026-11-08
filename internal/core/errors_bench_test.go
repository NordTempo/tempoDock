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

// BenchmarkErrorFollowerLine is what a log follower pays per line to
// find errors when there are none: a lock and errparse's byte scan.
func BenchmarkErrorFollowerLine(b *testing.B) {
	c := &Core{}
	f := &errorFollower{src: newLogSource("shop", "production", "web", 1)}
	lines := []node.LogLine{
		{Text: `10.0.0.1 - - [07/Nov/2026:12:00:00 +0000] "GET /api/cart HTTP/1.1" 200 612 "-" "Mozilla/5.0"`},
		{Text: `{"level":"info","ts":1730980800.5,"logger":"http.log.access","msg":"handled request","status":200,"duration":0.0012}`},
		{Text: `time=2026-11-07T12:00:00Z level=info msg="cache warm" keys=1200 took=31ms`},
	}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		f.line(c, lines[i%len(lines)])
		i++
	}
}

// errorBench is a Core (not started) with 50 services in one
// environment, each with an active release.
func errorBench(b *testing.B) (*Core, []int64) {
	ctx := context.Background()
	dir := b.TempDir()
	st := openTestStore(b, filepath.Join(dir, "dock.db"))
	b.Cleanup(func() { _ = st.Close() })
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
	var ids []int64
	for i := range 50 {
		svc, err := c.CreateService(ctx, testSystem, EnvRef{"shop", "production"}, apitypes.CreateServiceRequest{Name: fmt.Sprintf("s%d", i), Image: "app:1"})
		if err != nil {
			b.Fatal(err)
		}
		rel, err := st.CreateRelease(ctx, store.CreateReleaseParams{ServiceID: svc.ID, Number: 1, Source: "image", Config: []byte{}, CreatedBy: "b"})
		if err != nil {
			b.Fatal(err)
		}
		_ = st.ActivateRelease(ctx, store.ActivateReleaseParams{FinishedAt: ptr(now()), ID: rel.ID})
		ids = append(ids, svc.ID)
	}
	return c, ids
}

// sinkBench adds two errors of each of perService fingerprints to every
// service, as a minute's followers would.
func sinkBench(c *Core, ids []int64, perService int, at time.Time) {
	for _, id := range ids {
		for j := range perService {
			fp := fmt.Sprintf("%016x", j)
			for range 2 {
				c.issues.add(id, fp, pendingError{source: apitypes.ErrorFromLogs, title: "TypeError: x is undefined", sample: "TypeError: x is undefined\n    at f (/app/a.js:1:1)", count: 1, first: at, last: at, release: 1})
			}
		}
	}
}

// BenchmarkErrorsWrite50 is the tick's write of a minute in which 50
// services each logged two errors of two known issues: 100 issues
// counted in one transaction.
func BenchmarkErrorsWrite50(b *testing.B) {
	c, ids := errorBench(b)
	ctx := context.Background()
	at := time.Now()
	sinkBench(c, ids, 2, at)
	c.errorsTick(ctx, at)
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		sinkBench(c, ids, 2, at)
		b.StartTimer()
		c.errorsTick(ctx, at)
	}
}

// BenchmarkListErrors is the errors page of an org with 50 services and
// 200 open issues seen today: issues, trends and facets.
func BenchmarkListErrors(b *testing.B) {
	c, ids := errorBench(b)
	ctx := context.Background()
	sinkBench(c, ids, 4, time.Now())
	c.errorsTick(ctx, time.Now())
	b.ReportAllocs()
	for b.Loop() {
		list, err := c.ListErrors(ctx, testSystem, ErrorFilter{})
		if err != nil || len(list.Issues) != 200 || len(list.Facets) != 50 {
			b.Fatalf("list = %d issues, %d facets, %v", len(list.Issues), len(list.Facets), err)
		}
	}
}
