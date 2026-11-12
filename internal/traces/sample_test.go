package traces

import (
	"encoding/json/v2"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// sampled runs spans through a sampler and returns what it kept.
type sampled struct {
	sm   sampler
	l    sampleLimits
	now  time.Time
	kept []analytics.Span
}

func newSampled(budget int) *sampled {
	return &sampled{l: sampleLimits{budget: budget, rescues: 10, ringBytes: 2 * ringChunk, slow: time.Second}, now: time.Unix(1_800_000_000, 0)}
}

// send samples one span of service 1.
func (s *sampled) send(t *testing.T, sp analytics.Span, exception bool) {
	t.Helper()
	sp.Service = cmp(sp.Service, 1)
	line, err := json.Marshal(&sp)
	if err != nil {
		t.Fatal(err)
	}
	err = s.sm.span(&sp, line, signals{exception: exception}, s.l, s.now, func(b []byte) error {
		var got analytics.Span
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("%v: %s", err, b)
		}
		s.kept = append(s.kept, got)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func cmp(v, def int64) int64 {
	if v == 0 {
		return def
	}
	return v
}

// trace is a trace ID with n as its random bits.
func trace(n uint64) analytics.TraceID { return analytics.TraceIDOf(1, n) }

// A service under its budget keeps every trace; one over it keeps about
// the budget the next minute, the same spans of a trace each time.
func TestSampleBudget(t *testing.T) {
	s := newSampled(100)
	for i := range 400 {
		s.send(t, analytics.Span{Trace: trace(uint64(i) << 40), ID: 1, Entry: true}, false)
	}
	if len(s.kept) != 100 {
		t.Fatalf("first minute kept %d of 400, want the budget, 100", len(s.kept))
	}
	s.now, s.kept = s.now.Add(time.Minute), nil
	for i := range 400 {
		id := trace(uint64(i) * (1 << randomBits / 400))
		s.send(t, analytics.Span{Trace: id, ID: 1, Entry: true}, false)
		s.send(t, analytics.Span{Trace: id, ID: 2, Service: 2}, false) // another service follows the decision
	}
	if n := len(s.kept); n < 180 || n > 220 || n%2 != 0 {
		t.Errorf("next minute kept %d spans, want about 2 x 100 in whole traces", n)
	}
}

// Dropped spans wait in the ring: a failing span, or a slow entry span
// that ends after its children, brings its trace's spans back, and later
// ones follow.
func TestSampleRescue(t *testing.T) {
	s := newSampled(1)
	s.send(t, analytics.Span{Trace: trace(1), ID: 1, Entry: true}, false) // the minute's one sampled trace
	slow, failed := trace(2), trace(3)
	s.send(t, analytics.Span{Trace: slow, ID: 2, Parent: 1, Duration: 400_000}, false)
	s.send(t, analytics.Span{Trace: failed, ID: 2, Parent: 1}, false)
	s.send(t, analytics.Span{Trace: trace(4), ID: 1, Entry: true, Duration: 999_000}, false)
	if len(s.kept) != 1 {
		t.Fatalf("kept %d spans before a rescue, want 1", len(s.kept))
	}
	s.send(t, analytics.Span{Trace: slow, ID: 1, Entry: true, Duration: 1_200_000}, false)
	s.send(t, analytics.Span{Trace: failed, ID: 3, Parent: 1}, true) // an exception
	s.send(t, analytics.Span{Trace: failed, ID: 1, Entry: true, HTTPStatus: 200}, false)
	want := []struct {
		trace analytics.TraceID
		id    analytics.SpanID
		kept  analytics.Kept
	}{
		{trace(1), 1, analytics.KeptSampled},
		{slow, 2, analytics.KeptSlow}, {slow, 1, analytics.KeptSlow},
		{failed, 2, analytics.KeptError}, {failed, 3, analytics.KeptError}, {failed, 1, analytics.KeptError},
	}
	if len(s.kept) != len(want) {
		t.Fatalf("kept %d spans, want %d: %+v", len(s.kept), len(want), s.kept)
	}
	for i, w := range want {
		if g := s.kept[i]; g.Trace != w.trace || g.ID != w.id || g.Kept != w.kept {
			t.Errorf("span %d = %v/%v kept %v, want %v/%v kept %v", i, g.Trace, g.ID, g.Kept, w.trace, w.id, w.kept)
		}
	}
}

// Probe traces are never sampled and a slow one isn't kept, but an error
// still rescues one. An SDK that didn't take the probe's trace context
// still records its user agent: the trace's entry span isn't kept.
func TestSampleProbe(t *testing.T) {
	s := newSampled(100)
	probe := analytics.TraceIDOf(ProbeTraceHi, 7)
	s.send(t, analytics.Span{Trace: probe, ID: 2, Parent: 1}, false)
	s.send(t, analytics.Span{Trace: probe, ID: 1, Entry: true, HTTPStatus: 200, Duration: 2_000_000}, false)
	if len(s.kept) != 0 {
		t.Fatalf("kept %d spans of a healthy, slow probe", len(s.kept))
	}
	failing := analytics.TraceIDOf(ProbeTraceHi, 8)
	s.send(t, analytics.Span{Trace: failing, ID: 2, Parent: 1}, false)
	s.send(t, analytics.Span{Trace: failing, ID: 1, Entry: true, HTTPStatus: 503}, false)
	if len(s.kept) != 2 {
		t.Errorf("kept %d spans of a failing probe, want 2", len(s.kept))
	}
	s.kept = nil
	uptime := trace(9)
	s.send(t, analytics.Span{Trace: uptime, ID: 2, Parent: 1}, false) // sampled, before the entry says what it is
	sp := analytics.Span{Service: 1, Trace: uptime, ID: 1, Entry: true, HTTPStatus: 200}
	line, _ := json.Marshal(&sp)
	if err := s.sm.span(&sp, line, signals{probe: true}, s.l, s.now, func([]byte) error { s.kept = append(s.kept, sp); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(s.kept) != 1 || s.kept[0].ID != 2 {
		t.Errorf("an uptime probe's trace kept %+v, want only the span sent before its entry", s.kept)
	}
}

// A kept span says the strongest reason its trace has so far: error,
// then slow, then sampled.
func TestSampleKeptReason(t *testing.T) {
	s := newSampled(100)
	id := trace(1)
	s.send(t, analytics.Span{Trace: id, ID: 3, Parent: 2}, false)
	s.send(t, analytics.Span{Trace: id, ID: 2, Parent: 1, HTTPStatus: 500}, false)
	s.send(t, analytics.Span{Trace: id, ID: 1, Entry: true, Duration: 3_000_000}, false)
	want := []analytics.Kept{analytics.KeptSampled, analytics.KeptError, analytics.KeptError}
	for i, w := range want {
		if i >= len(s.kept) || s.kept[i].Kept != w {
			t.Fatalf("kept %+v, want reasons %v", s.kept, want)
		}
	}
}

// The ring recycles its oldest chunk when full, so it stays within its
// size, and a rescue then finds only what is left.
func TestRingBounded(t *testing.T) {
	var r ring
	line := make([]byte, 1000)
	for i := range 5000 {
		r.add(traceKey{trace: trace(uint64(i))}, line, 2*ringChunk)
	}
	if len(r.chunks) != 2 {
		t.Fatalf("%d chunks, want 2", len(r.chunks))
	}
	n := 0
	r.take(traceKey{trace: trace(0)}, func([]byte) { n++ })
	r.take(traceKey{trace: trace(4999)}, func([]byte) { n += 10 })
	if n != 10 {
		t.Errorf("took %d: want only the newest trace's line", n)
	}
}

// Decisions and dropped spans belong to an org: another org's app, which
// may know a trace's ID from a call it made, can neither rescue its
// spans nor drop them.
func TestSampleOrgsApart(t *testing.T) {
	s := newSampled(1)
	s.send(t, analytics.Span{Org: 1, Trace: trace(1), ID: 1, Entry: true}, false) // org 1's sampled trace
	theirs := trace(2)
	s.send(t, analytics.Span{Org: 1, Trace: theirs, ID: 2, Parent: 1}, false) // dropped, in the ring
	// Org 2's service spends its budget, so its error below is a rescue
	// that scans the ring, not a sampled trace of its own.
	s.send(t, analytics.Span{Org: 2, Service: 9, Trace: trace(3), ID: 6, Entry: true}, false)
	s.send(t, analytics.Span{Org: 2, Service: 9, Trace: theirs, ID: 3, HTTPStatus: 500}, false)
	if len(s.kept) != 3 || s.kept[2].Org != 2 || s.kept[2].ID != 3 {
		t.Fatalf("kept %+v: org 2's error rescued org 1's span", s.kept)
	}
	sp := analytics.Span{Org: 2, Service: 9, Trace: trace(1), ID: 4, Entry: true}
	line, _ := json.Marshal(&sp)
	_ = s.sm.span(&sp, line, signals{probe: true}, s.l, s.now, func([]byte) error { return nil })
	s.send(t, analytics.Span{Org: 1, Trace: trace(1), ID: 5, Parent: 1}, false)
	if last := s.kept[len(s.kept)-1]; last.Org != 1 || last.ID != 5 {
		t.Errorf("org 2's probe span dropped org 1's sampled trace: %+v", s.kept)
	}
}

// A flood of new traces turns over the dropped decisions, never the kept
// ones: a trace a failed edge span rescued keeps its app spans coming in
// later. Past the minute's rescues, failing traces are dropped like any.
func TestSampleFloodKeepsRescues(t *testing.T) {
	s := newSampled(1)
	s.l.rescues = 2
	s.send(t, analytics.Span{Trace: trace(1), ID: 1, Entry: true}, false) // the budget's one
	failed := trace(2)
	s.send(t, analytics.Span{Trace: failed, ID: 1, Entry: true, Kind: analytics.SpanEdge, HTTPStatus: 502}, false)
	for i := range 2*decidedCap + 1 {
		sp := analytics.Span{Service: 1, Trace: trace(uint64(i) + 1000), ID: 1, Entry: true}
		_ = s.sm.span(&sp, []byte("{}"), signals{}, s.l, s.now, func([]byte) error { return nil })
	}
	s.kept = nil
	s.send(t, analytics.Span{Trace: failed, ID: 2, Parent: 1}, false)
	if len(s.kept) != 1 || s.kept[0].Kept != analytics.KeptError {
		t.Fatalf("after the flood the failed trace's app span: %+v", s.kept)
	}
	// The second rescue fits; the third doesn't.
	s.send(t, analytics.Span{Trace: trace(3), ID: 1, HTTPStatus: 500}, false)
	s.send(t, analytics.Span{Trace: trace(4), ID: 1, HTTPStatus: 500}, false)
	if len(s.kept) != 2 {
		t.Errorf("past the minute's rescues: kept %+v", s.kept)
	}
}
