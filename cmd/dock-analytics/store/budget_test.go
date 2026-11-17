package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// budgetNow is the tests' "now": two days after day0, at noon.
var budgetNow = day0.Add(2*24*time.Hour + 12*time.Hour)

// arriving is n page views for site, received at when.
func arriving(site int64, when time.Time, n int) []analytics.Event {
	out := make([]analytics.Event, n)
	for i := range out {
		out[i] = at(uint64(i+1), 0, "/")
		out[i].Site, out[i].Time = site, when.UnixMilli()
	}
	return out
}

// recording is a replay chunk for site received at when, of about size
// bytes that don't compress.
func recording(site int64, segment uint64, when time.Time, size int) analytics.ReplayChunk {
	noise := make([]byte, size/2)
	_, _ = rand.Read(noise)
	ch := chunk(1, segment, 0, when.Sub(day0), `[{"timestamp":1,"data":"`+hex.EncodeToString(noise)+`"}]`)
	ch.Site = site
	return ch
}

// events counts site's rows.
func events(t *testing.T, s *Store, site int64) (n int) {
	t.Helper()
	if err := s.db.QueryRow("SELECT count(*) FROM events WHERE site_id = $1", uint32(site)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A site keeps its budget of events a day and no more, however many
// arrive: a day of batches at many times the budget leaves exactly the
// budget, the site is named once, another site is not held back, a
// restart remembers what the day already took, and the next day starts
// over.
func TestSiteDayEventBudget(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "a.duckdb")
	open := func(now time.Time) *Store {
		s, err := Open(ctx, path, Options{MemoryLimit: 128 << 20})
		if err != nil {
			t.Fatal(err)
		}
		s.now = func() time.Time { return now }
		return s
	}
	lim := analytics.StoreLimits{SiteDayEvents: 5000, SiteDayReplayBytes: -1, MaxBytes: -1}
	day := utcDay(budgetNow)

	s := open(day)
	var written, rejected int
	var named []int64
	pos := uint64(0)
	for hour := range 24 {
		now := day.Add(time.Duration(hour) * time.Hour)
		s.now = func() time.Time { return now }
		pos++
		res, err := s.WriteLimited(ctx, "server-1", pos, arriving(1, now, 1000), nil, lim)
		if err != nil {
			t.Fatal(err)
		}
		written, rejected, named = written+res.Written, rejected+res.Rejected, append(named, res.OverBudget...)
	}
	if written != 5000 || rejected != 19000 || events(t, s, 1) != 5000 {
		t.Fatalf("a day at 24,000 events with a budget of 5,000: %d written, %d rejected, %d kept", written, rejected, events(t, s, 1))
	}
	if !slices.Equal(named, []int64{1}) {
		t.Fatalf("the site should be named over its budget once: %v", named)
	}
	if res, err := s.WriteLimited(ctx, "server-1", 100, arriving(2, day.Add(23*time.Hour), 10), nil, lim); err != nil || res.Written != 10 || res.Rejected != 0 {
		t.Fatalf("another site the same day: %+v, %v", res, err)
	}
	// A batch sent again counts for nothing.
	if res, err := s.WriteLimited(ctx, "server-1", 100, arriving(2, day.Add(23*time.Hour), 10), nil, lim); err != nil || res.Written != 0 || res.Rejected != 0 {
		t.Fatalf("the batch again: %+v, %v", res, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s = open(day.Add(23*time.Hour + 30*time.Minute))
	if res, err := s.WriteLimited(ctx, "server-1", 101, arriving(1, day.Add(23*time.Hour), 10), nil, lim); err != nil || res.Written != 0 || res.Rejected != 10 {
		t.Fatalf("after a restart the same day: %+v, %v", res, err)
	}
	next := day.Add(24*time.Hour + time.Minute)
	s.now = func() time.Time { return next }
	if res, err := s.WriteLimited(ctx, "server-1", 102, arriving(1, next, 10), nil, lim); err != nil || res.Written != 10 {
		t.Fatalf("the next day: %+v, %v", res, err)
	}
	_ = s.Close()
}

// A site keeps its budget of replay bytes a day: chunks past it are
// dropped and counted, and nothing of them reaches the disk.
func TestSiteDayReplayBudget(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	s.now = func() time.Time { return budgetNow }
	lim := analytics.StoreLimits{SiteDayEvents: -1, SiteDayReplayBytes: 100 << 10, MaxBytes: -1}
	var written, rejected int
	for i := range 20 {
		res, err := s.WriteReplayLimited(ctx, "replay:server:1", uint64(i+1), []analytics.ReplayChunk{recording(1, uint64(i+1), budgetNow, 30<<10)}, nil, lim)
		if err != nil {
			t.Fatal(err)
		}
		written, rejected = written+res.Written, rejected+res.Rejected
	}
	// What is on disk decides: the file passes the budget by less than
	// the one chunk that took it there, and the rest are dropped.
	fi, err := os.Stat(filepath.Join(s.replayDir(), replayFile(1, utcDay(budgetNow))))
	if err != nil || written == 0 || rejected == 0 || written+rejected != 20 || fi.Size() > lim.SiteDayReplayBytes+40<<10 {
		t.Fatalf("20 chunks of about 30 KB with a budget of %d: %d written, %d rejected, the day's file %d bytes, %v", lim.SiteDayReplayBytes, written, rejected, fi.Size(), err)
	}
	if res, err := s.WriteReplayLimited(ctx, "replay:server:1", 21, []analytics.ReplayChunk{recording(2, 99, budgetNow, 30<<10)}, nil, lim); err != nil || res.Written != 1 {
		t.Fatalf("another site: %+v, %v", res, err)
	}
}

// Over its size the store drops the oldest days of replays first, one at
// a time and only as many as it takes; then old raw events; and never
// anything of today, whatever the size asked for.
func TestStoreKeepsWithinItsSize(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	s.now = func() time.Time { return budgetNow }
	today := utcDay(budgetNow)
	days := []time.Time{day0.Add(time.Hour), day0.Add(25 * time.Hour), budgetNow}
	for i, when := range days {
		if _, _, err := s.WriteReplay(ctx, "replay:server:1", uint64(i+1), []analytics.ReplayChunk{recording(1, uint64(i+1), when, 400<<10), recording(2, uint64(i+10), when, 400<<10)}, nil); err != nil {
			t.Fatal(err)
		}
		write(t, s, "server-1", uint64(i+1), arriving(1, when, 50)...)
	}
	replayDays := func() (out []string) {
		rows, err := s.db.Query("SELECT DISTINCT strftime(day, '%Y-%m-%d') FROM replay_chunks ORDER BY 1")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var d string
			_ = rows.Scan(&d)
			out = append(out, d)
		}
		return out
	}
	used, err := s.usedBytes(ctx)
	if err != nil || used < 3*800<<10 {
		t.Fatalf("used: %d, %v", used, err)
	}

	// A little under what is used: the oldest day of replays is enough.
	if err := s.keepWithin(ctx, used-100<<10, today); err != nil {
		t.Fatal(err)
	}
	if got := replayDays(); !slices.Equal(got, []string{"2026-03-03", "2026-03-04"}) {
		t.Fatalf("replay days after a small cut: %v", got)
	}
	for _, site := range []string{"1", "2"} {
		if _, err := os.Stat(filepath.Join(s.replayDir(), site, "2026-03-02")); !os.IsNotExist(err) {
			t.Fatalf("site %s's oldest day is still on disk: %v", site, err)
		}
	}
	if n := events(t, s, 1); n != 150 {
		t.Fatalf("events went before the replays did: %d left", n)
	}

	// Next to nothing: every day but today goes, replays and then raw
	// events once they are rolled up; today stays.
	if err := s.BuildRollups(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.keepWithin(ctx, 1, today); err != nil {
		t.Fatal(err)
	}
	if got := replayDays(); !slices.Equal(got, []string{"2026-03-04"}) {
		t.Fatalf("replay days with no room: %v", got)
	}
	var todays, older int
	if err := s.db.QueryRow("SELECT count(*) FILTER (ts >= $1), count(*) FILTER (ts < $1) FROM events", today).Scan(&todays, &older); err != nil {
		t.Fatal(err)
	}
	if todays != 50 || older >= 100 {
		t.Fatalf("events with no room: %d of today (want 50), %d older (the oldest day's 50 should be gone)", todays, older)
	}
}
