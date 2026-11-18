package core

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// releaseID is the ID of a service's release by number.
func releaseID(t *testing.T, h *harness, serviceID, number int64) int64 {
	t.Helper()
	r, err := h.store.GetReleaseByNumber(h.ctx, store.GetReleaseByNumberParams{ServiceID: serviceID, Number: number})
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// finishedAt sets when a release finished, as if it had been that long.
func finishedAt(t *testing.T, h *harness, id int64, at time.Time) {
	t.Helper()
	if _, err := h.store.DB.ExecContext(h.ctx, "UPDATE releases SET finished_at = ? WHERE id = ?", at, id); err != nil {
		t.Fatal(err)
	}
}

// A release's token sends while the release is its service's and for an
// hour after it was replaced: spans sent with it later never reach the
// trace store, and the live release's do.
func TestReplacedReleaseStopsSendingSpans(t *testing.T) {
	h := newHarness(t)
	ref := ServiceRef{"shop", "production", "web"}
	old := tracedService(t, h, "web")
	if _, err := h.c.EnableTraces(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	h.deploy(ref, "nginx:2")
	live := old
	live.Release = releaseID(t, h, old.Service, 2)
	span := func(from analytics.Span, id uint64) analytics.Span {
		from.Trace, from.ID, from.Start, from.Duration, from.Name = analytics.TraceID{15: byte(id)}, analytics.SpanID(id), now().UnixMicro(), 1000, "GET /"
		return from
	}
	stored := func() []analytics.SpanID {
		var ids []analytics.SpanID
		if ts := h.rt.TraceStore(); ts != nil {
			for _, sp := range ts.Spans() {
				ids = append(ids, sp.ID)
			}
		}
		slices.Sort(ids)
		return ids
	}

	// Just replaced: the old release's last spans are still taken.
	spool(t, h, span(live, 1), span(old, 2))
	eventually(t, "both releases' spans in the trace store", func() bool { return slices.Equal(stored(), []analytics.SpanID{1, 2}) })

	// Replaced two hours ago: its token sends nothing more.
	finishedAt(t, h, old.Release, now().Add(-3*time.Hour))
	finishedAt(t, h, live.Release, now().Add(-2*time.Hour))
	spool(t, h, span(old, 3), span(live, 4))
	eventually(t, "the live release's span in the trace store", func() bool { return slices.Contains(stored(), 4) })
	if got := stored(); !slices.Equal(got, []analytics.SpanID{1, 2, 4}) {
		t.Errorf("stored spans %v, want 1, 2 and 4: span 3 came with a release replaced two hours before", got)
	}
}

// When each kind of release ends for its token: a replaced one when its
// replacement went live, a failed one when it failed, the active one
// (here a rollback, a release of its own) never; and with no time to go
// by, nothing is dropped.
func TestBoundLinesByReleaseAge(t *testing.T) {
	h := newHarness(t)
	ref := ServiceRef{"shop", "production", "web"}
	base := tracedService(t, h, "web") // release 1
	h.deploy(ref, "nginx:2")           // 2 replaces 1
	h.rt.FailStart(errors.New("no such image"))
	if r := h.deploy(ref, "nginx:3"); r.Status != apitypes.ReleaseFailed { // 3 fails
		t.Fatalf("release 3 is %s, want failed", r.Status)
	}
	h.rt.FailStart(nil)
	back, err := h.c.Rollback(h.ctx, testSystem, ref, 1) // 4 replaces 2
	if err != nil {
		t.Fatal(err)
	}
	if r := h.wait(ref, back.Number); r.Number != 4 || r.Status != apitypes.ReleaseActive {
		t.Fatalf("the rollback is release %d, %s", r.Number, r.Status)
	}
	// 1 went live 10 h ago, 2 replaced it 6 h ago, 3 failed 4 h ago and
	// 4 replaced 2 2 h 30 min ago.
	t0 := now()
	ids := map[int64]int64{} // release ID to number
	var lines []byte
	for number, ago := range map[int64]time.Duration{1: 10 * time.Hour, 2: 6 * time.Hour, 3: 4 * time.Hour, 4: 150 * time.Minute} {
		id := releaseID(t, h, base.Service, number)
		ids[id] = number
		finishedAt(t, h, id, t0.Add(-ago))
		sp := base
		sp.Release, sp.ID = id, analytics.SpanID(number)
		b, err := json.Marshal(sp)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(append(lines, b...), '\n')
	}
	for _, tc := range []struct {
		name string
		asOf time.Time
		want []int64 // the releases whose lines are kept
	}{
		{"no time to go by", time.Time{}, []int64{1, 2, 3, 4}},
		{"half an hour after 1 was replaced", t0.Add(-330 * time.Minute), []int64{1, 2, 3, 4}},
		{"an hour and a half after 1 was replaced", t0.Add(-270 * time.Minute), []int64{2, 3, 4}},
		{"over an hour after 3 failed", t0.Add(-165 * time.Minute), []int64{2, 4}},
		{"over an hour after 2 was replaced", t0, []int64{4}},
		{"days later", t0.Add(100 * time.Hour), []int64{4}},
	} {
		out, dropped, ended, err := h.c.boundLines(h.ctx, h.c.localID, lines, tc.asOf)
		if err != nil {
			t.Fatal(err)
		}
		var kept []int64
		for line := range bytes.Lines(out) {
			var sp analytics.Span
			if err := json.Unmarshal(bytes.TrimSpace(line), &sp); err != nil {
				t.Fatal(err)
			}
			kept = append(kept, ids[sp.Release])
		}
		slices.Sort(kept)
		if !slices.Equal(kept, tc.want) || dropped != 0 || ended != 4-len(tc.want) {
			t.Errorf("%s: kept releases %v (dropped %d, ended %d), want %v", tc.name, kept, dropped, ended, tc.want)
		}
	}
}

// A spool's clock is now while its forwarder keeps up, and when it last
// did while it works through a backlog: after an outage, or a daemon
// start, old lines aren't judged by the time they are forwarded at.
func TestSpoolClock(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	at := func(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }
	var k spoolClock
	if got := k.at(at(0)); !got.IsZero() {
		t.Errorf("before it ever kept up: %v, want no time", got)
	}
	k.forwarded(at(1), true) // a full batch of the backlog a start finds
	if got := k.at(at(2)); !got.IsZero() {
		t.Errorf("still in the first backlog: %v, want no time", got)
	}
	k.forwarded(at(2), false) // the backlog's end
	if got := k.at(at(3)); got != at(3) {
		t.Errorf("keeping up: %v, want now", got)
	}
	k.forwarded(at(3), true) // a burst: more may wait behind it
	if got := k.at(at(4)); got != at(3) {
		t.Errorf("after a full batch: %v, want when it last kept up", got)
	}
	k.forwarded(at(4), false)
	k.stopped(at(5)) // the trace store goes away
	k.stopped(at(60))
	k.forwarded(at(180), true) // back: three hours of backlog
	if got := k.at(at(181)); got != at(5) {
		t.Errorf("in the backlog of an outage: %v, want when the outage began", got)
	}
	k.forwarded(at(181), false)
	if got := k.at(at(182)); got != at(182) {
		t.Errorf("caught up after the outage: %v, want now", got)
	}
}
