package core

import (
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// benchChecks adds n uptime checks to svc-00 straight to the store (the
// scheduler doesn't know them) and gives them a state, as the scheduler
// would.
func benchChecks(b *testing.B, bc *benchCore, n int) []store.ListUptimeScheduleRow {
	b.Helper()
	ss, err := bc.c.loadService(bc.ctx, 1, ServiceRef{"big", "production", "svc-00"})
	if err != nil {
		b.Fatal(err)
	}
	out := make([]store.ListUptimeScheduleRow, n)
	for i := range out {
		r, err := bc.c.store.CreateUptimeCheck(bc.ctx, store.CreateUptimeCheckParams{
			ServiceID: &ss.svc.ID, Url: "https://svc-00.example.com/health", ExpectStatus: 200, IntervalS: 30, TimeoutMs: 10000, CreatedAt: now(), CreatedBy: "bench",
		})
		if err != nil {
			b.Fatal(err)
		}
		out[i] = store.ListUptimeScheduleRow{
			ID: r.ID, ServiceID: ss.svc.ID, Url: r.Url, ExpectStatus: r.ExpectStatus, IntervalS: r.IntervalS, TimeoutMs: r.TimeoutMs,
			ServerID: ss.env.ServerID, EnvironmentID: ss.env.ID, ProjectID: ss.project.ID, OrgID: ss.project.OrgID,
			Project: ss.project.Name, Environment: ss.env.Name, Service: ss.svc.Name,
		}
		bc.c.uptime.mu.Lock()
		bc.c.uptime.checks[r.ID] = &checkState{state: apitypes.UptimeUnknown}
		bc.c.uptime.mu.Unlock()
	}
	return out
}

// BenchmarkUptimeRecord is one probe result recorded: the minute row's
// upsert and the check's state, for a check that stays up (no alert, no
// event). Every probe costs this. Most of its time is the disk (SQLite's
// WAL writes and checkpoint fsyncs): about 13 µs with the database on
// tmpfs, 38-54 µs on a shared disk, so it is an io= benchmark, gated on
// allocations only (docs/performance.md).
func BenchmarkUptimeRecord(b *testing.B) {
	bc := newBenchCore(b)
	chk := benchChecks(b, bc, 1)[0]
	res := node.ProbeResult{Up: true, Status: 200, Latency: 42 * time.Millisecond}
	at := time.Now()
	bc.c.recordProbe(bc.ctx, chk, "edge", res, at)
	b.Run("io=sqlite", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			bc.c.recordProbe(bc.ctx, chk, "edge", res, at)
		}
	})
}

// BenchmarkUptimeRollup is the statistics tick's hourly uptime work with
// 50 checks probed every 30 seconds: one hour of minute rows rolled into
// hour rows, and both trims.
func BenchmarkUptimeRollup(b *testing.B) {
	bc := newBenchCore(b)
	checks := benchChecks(b, bc, 50)
	n := time.Now()
	end := n.Unix() - uptimeMinute
	end -= end % uptimeHour
	for _, chk := range checks {
		for m := end - uptimeHour; m < end; m += uptimeMinute {
			for range 2 {
				if err := bc.c.store.AddUptimeProbe(bc.ctx, store.AddUptimeProbeParams{CheckID: chk.ID, At: m, Up: 1, LatencyMsSum: 40}); err != nil {
					b.Fatal(err)
				}
			}
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		if got, err := bc.c.rollUptime(bc.ctx, n, end-uptimeHour); err != nil || got != end {
			b.Fatalf("rolled up to %d, %v", got, err)
		}
	}
}
