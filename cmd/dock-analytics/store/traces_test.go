package store

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// traceNow is the store's clock in the trace tests.
var traceNow = time.Date(2026, 11, 10, 15, 0, 0, 0, time.UTC)

// openTraces returns a trace store on a temp file whose clock is
// traceNow.
func openTraces(t *testing.T) (*Traces, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "traces.duckdb")
	s, err := OpenTraces(context.Background(), path, Options{MemoryLimit: 128 << 20})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return traceNow }
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

// tid is a trace ID ending in n.
func tid(n byte) analytics.TraceID { return analytics.TraceID{15: n} }

// mkSpan is a span of org 1's service in trace n, starting offset after
// traceNow's hour began.
func mkSpan(n byte, id, parent analytics.SpanID, service int64, offset, dur time.Duration) analytics.Span {
	return analytics.Span{
		Org: 1, Project: 2, Env: 3, Service: service, Release: 40, Trace: tid(n), ID: id, Parent: parent,
		Start: traceNow.Truncate(time.Hour).Add(offset).UnixMicro(), Duration: dur.Microseconds(), Name: "work",
	}
}

// entry is span as its service's entry, a server span of route.
func entry(sp analytics.Span, method, route string, code int) analytics.Span {
	sp.Kind, sp.Entry, sp.Method, sp.Route, sp.HTTPStatus, sp.Name = analytics.SpanServer, true, method, route, code, method+" "+route
	return sp
}

func writeSpans(t *testing.T, s *Traces, pos uint64, lim analytics.TraceLimits, spans ...analytics.Span) analytics.SpanWriteResult {
	t.Helper()
	res, err := s.WriteSpans(context.Background(), "spans:server:1", pos, spans, lim)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// hour is the range of traceNow's hour.
func hour(q analytics.TracesQuery) analytics.TracesQuery {
	q.Org = 1
	if q.Services == nil {
		q.Services = []int64{10, 11}
	}
	q.From, q.To = traceNow.Truncate(time.Hour).UnixMicro(), traceNow.Truncate(time.Hour).Add(time.Hour).UnixMicro()
	return q
}

func TestSpansRoundTripAndSkipBatchesWrittenBefore(t *testing.T) {
	s, _ := openTraces(t)
	ctx := context.Background()
	root := entry(mkSpan(1, 1, 0, 10, time.Minute, 300*time.Millisecond), "POST", "/orders", 500)
	root.Status, root.ErrorFP, root.Kept = analytics.StatusError, "fp1", analytics.KeptError
	root.Attrs, root.Events = []byte(`{"http.route":"/orders"}`), []byte(`[{"name":"exception"}]`)
	child := mkSpan(1, 2, 1, 10, time.Minute+10*time.Millisecond, 200*time.Millisecond)
	child.Kind = analytics.SpanClient
	if res := writeSpans(t, s, 1, analytics.TraceLimits{}, root, child); res.Written != 2 || res.Rejected != 0 {
		t.Fatalf("write = %+v", res)
	}
	if res := writeSpans(t, s, 1, analytics.TraceLimits{}, root); res.Written != 0 {
		t.Errorf("the same batch again wrote %d", res.Written)
	}
	if pos, err := s.SpansCursor(ctx, "spans:server:1"); err != nil || pos != 1 {
		t.Errorf("cursor = %d, %v", pos, err)
	}
	for _, at := range []int64{root.Start, 0} {
		got, err := s.Trace(ctx, analytics.TraceQuery{Org: 1, Trace: tid(1), At: at})
		if err != nil || len(got.Spans) != 2 {
			t.Fatalf("trace at %d = %+v, %v", at, got, err)
		}
		r := got.Spans[0]
		if r.ID != 1 || r.Service != 10 || r.Release != 40 || r.Env != 3 || r.Project != 2 || !r.Entry || r.HTTPStatus != 500 ||
			r.Status != analytics.StatusError || r.Kept != analytics.KeptError || r.ErrorFP != "fp1" || r.Route != "/orders" ||
			string(r.Attrs) != `{"http.route":"/orders"}` || string(r.Events) != `[{"name":"exception"}]` || r.Duration != 300_000 {
			t.Errorf("root read back as %+v", r)
		}
		if c := got.Spans[1]; c.Parent != 1 || c.Kind != analytics.SpanClient || c.Attrs != nil {
			t.Errorf("child read back as %+v", c)
		}
	}
	// Another org never sees it, even with the ID.
	if got, err := s.Trace(ctx, analytics.TraceQuery{Org: 2, Trace: tid(1)}); err != nil || len(got.Spans) != 0 {
		t.Errorf("org 2 read %d spans, %v", len(got.Spans), err)
	}
}

// A trace reads in pages after a cursor, in start and span ID order, so
// one of big spans never meets the client's answer cap.
func TestTracePages(t *testing.T) {
	s, _ := openTraces(t)
	ctx := context.Background()
	var spans []analytics.Span
	for i := range 5 {
		// Spans 3 to 5 start together: the span ID breaks the tie.
		spans = append(spans, mkSpan(1, analytics.SpanID(i+1), 0, 10, time.Minute+time.Duration(min(i, 2))*time.Millisecond, time.Millisecond))
	}
	writeSpans(t, s, 1, analytics.TraceLimits{}, spans...)
	var got []analytics.SpanID
	q := analytics.TraceQuery{Org: 1, Trace: tid(1), Limit: 2}
	for range 4 {
		page, err := s.Trace(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		for _, sp := range page.Spans {
			got = append(got, sp.ID)
		}
		if !page.Truncated {
			break
		}
		last := page.Spans[len(page.Spans)-1]
		q.After = &analytics.TraceCursor{Start: last.Start, Span: last.ID}
	}
	if !slices.Equal(got, []analytics.SpanID{1, 2, 3, 4, 5}) {
		t.Errorf("pages read %v", got)
	}
}

func TestTracesListsEntriesWithTheirTraces(t *testing.T) {
	s, _ := openTraces(t)
	ctx := context.Background()
	slowErr := entry(mkSpan(1, 1, 0, 10, time.Minute, 900*time.Millisecond), "POST", "/orders", 500)
	slowErr.Status, slowErr.ErrorFP = analytics.StatusError, "fp1"
	db := mkSpan(1, 2, 1, 11, time.Minute+5*time.Millisecond, 800*time.Millisecond)
	db.Status, db.ErrorFP = analytics.StatusError, "fp0"
	fast := entry(mkSpan(2, 3, 0, 10, 2*time.Minute, 20*time.Millisecond), "GET", "/orders/:id", 200)
	newer := entry(mkSpan(3, 4, 0, 10, 3*time.Minute, 50*time.Millisecond), "GET", "/products", 200)
	newer.Release = 41
	// Another org's span in trace 1 counts for nothing.
	foreign := mkSpan(1, 9, 0, 99, time.Minute, time.Second)
	foreign.Org = 2
	// Service 12 isn't asked for.
	other := entry(mkSpan(4, 5, 0, 12, 4*time.Minute, time.Second), "GET", "/", 200)
	// Trace 2 enters service 10 again (it calls itself): listed once, by
	// its outer entry.
	inner := entry(mkSpan(2, 6, 3, 10, 2*time.Minute+5*time.Millisecond, 10*time.Millisecond), "GET", "/inner", 200)
	writeSpans(t, s, 1, analytics.TraceLimits{}, slowErr, db, fast, newer, foreign, other, inner)

	list := func(q analytics.TracesQuery) []analytics.TraceSummary {
		t.Helper()
		res, err := s.Traces(ctx, hour(q))
		if err != nil {
			t.Fatal(err)
		}
		return res.Traces
	}
	ids := func(ts []analytics.TraceSummary) []byte {
		var out []byte
		for _, x := range ts {
			out = append(out, x.Trace[15])
		}
		return out
	}
	newest := list(analytics.TracesQuery{})
	if got := ids(newest); string(got) != "\x03\x02\x01" || newest[1].Span != 3 {
		t.Errorf("newest first = %v, trace 2 by span %d", got, newest[1].Span)
	}
	slowest := list(analytics.TracesQuery{Sort: analytics.SortSlowest})
	if got := ids(slowest); string(got) != "\x01\x03\x02" {
		t.Errorf("slowest first = %v", got)
	}
	if first := slowest[0]; first.ErrorFP != "fp1" || first.Status != analytics.StatusError || first.Service != 10 || first.Release != 40 ||
		first.Name != "POST /orders" || first.Route != "/orders" || first.HTTPStatus != 500 || first.Duration != 900_000 || first.Span != 1 {
		t.Errorf("trace 1's row = %+v", first)
	}
	for name, c := range map[string]struct {
		q    analytics.TracesQuery
		want string
	}{
		"route":         {analytics.TracesQuery{Route: "/orders"}, "\x01"},
		"method":        {analytics.TracesQuery{Method: "GET"}, "\x03\x02"},
		"errors":        {analytics.TracesQuery{Status: analytics.TraceError}, "\x01"},
		"5xx":           {analytics.TracesQuery{Status: analytics.Trace5xx}, "\x01"},
		"slower than":   {analytics.TracesQuery{MinDuration: 40_000}, "\x03\x01"},
		"since release": {analytics.TracesQuery{SinceRelease: 41}, "\x03"},
		"release":       {analytics.TracesQuery{Release: 40}, "\x02\x01"},
		"one service":   {analytics.TracesQuery{Services: []int64{11}}, ""},
		"limit":         {analytics.TracesQuery{Limit: 1}, "\x03"},
	} {
		if got := ids(list(c.q)); string(got) != c.want {
			t.Errorf("%s: %v, want %v", name, got, []byte(c.want))
		}
	}
	if res, _ := s.Traces(ctx, hour(analytics.TracesQuery{Limit: 2})); !res.More {
		t.Error("a full page doesn't say there are more")
	}
	if _, err := s.Traces(ctx, hour(analytics.TracesQuery{Services: []int64{}})); err == nil {
		t.Error("a list of no services answered")
	}
}

func TestSpanCaps(t *testing.T) {
	s, path := openTraces(t)
	lim := analytics.TraceLimits{ServiceDay: 2, OrgDay: 4, ErrorDay: 1}
	sp := func(n byte, service int64, kept analytics.Kept) analytics.Span {
		x := mkSpan(n, analytics.SpanID(n), 0, service, time.Minute, time.Millisecond)
		x.Kept = kept
		return x
	}
	// Service 10 keeps two spans, then only its one error (errors count
	// towards the caps too).
	res := writeSpans(t, s, 1, lim, sp(1, 10, 0), sp(2, 10, 0), sp(3, 10, 0), sp(4, 10, analytics.KeptError), sp(5, 10, analytics.KeptForced))
	if res.Written != 3 || res.Rejected != 2 {
		t.Errorf("service cap: %+v, want 3 written, 2 rejected", res)
	}
	// The org's fourth span fills it, after a restart.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenTraces(context.Background(), path, Options{MemoryLimit: 128 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	s2.now = func() time.Time { return traceNow }
	if res := writeSpans(t, s2, 2, lim, sp(6, 11, 0), sp(7, 11, 0)); res.Written != 1 || res.Rejected != 1 {
		t.Errorf("org cap after a restart: %+v, want 1 written, 1 rejected", res)
	}
	// A new day starts over.
	s2.now = func() time.Time { return traceNow.Add(24 * time.Hour) }
	next := sp(8, 11, 0)
	next.Start = traceNow.Add(24 * time.Hour).UnixMicro()
	if res := writeSpans(t, s2, 3, lim, next); res.Written != 1 {
		t.Errorf("the next day: %+v", res)
	}
}

func TestSpansOutsideTheDaysKeptAreRejected(t *testing.T) {
	s, _ := openTraces(t)
	old := mkSpan(1, 1, 0, 10, -40*24*time.Hour, time.Millisecond)
	future := mkSpan(2, 2, 0, 10, 3*24*time.Hour, time.Millisecond)
	noOrg := mkSpan(3, 3, 0, 10, 0, time.Millisecond)
	noOrg.Org = 0
	if res := writeSpans(t, s, 1, analytics.TraceLimits{}, old, future, noOrg); res.Written != 0 || res.Rejected != 3 {
		t.Errorf("write = %+v, want 3 rejected", res)
	}
	if days := s.allDays(); len(days) != 0 {
		t.Errorf("tables were made for %v", days)
	}
}

func TestPruneTracesDropsWholeDays(t *testing.T) {
	s, _ := openTraces(t)
	ctx := context.Background()
	var spans []analytics.Span
	for d := range 3 {
		spans = append(spans, mkSpan(byte(d+1), analytics.SpanID(d+1), 0, 10, -time.Duration(d)*24*time.Hour, time.Millisecond))
	}
	writeSpans(t, s, 1, analytics.TraceLimits{}, spans...)
	res, err := s.PruneTraces(ctx, analytics.TracePrune{Before: traceNow.Add(-24 * time.Hour).UnixMilli()})
	if err != nil || len(res.Days) != 1 || res.Days[0] != "2026-11-08" {
		t.Fatalf("prune = %+v, %v", res, err)
	}
	if got, _ := s.Trace(ctx, analytics.TraceQuery{Org: 1, Trace: tid(3)}); len(got.Spans) != 0 {
		t.Error("a pruned day's trace is still there")
	}
	if got, _ := s.Trace(ctx, analytics.TraceQuery{Org: 1, Trace: tid(2)}); len(got.Spans) != 1 {
		t.Error("yesterday's trace went too")
	}
	// Late spans for a pruned day don't bring it back.
	if res := writeSpans(t, s, 2, analytics.TraceLimits{}, spans[2]); res.Rejected != 1 || len(s.allDays()) != 2 {
		t.Errorf("a late span for a pruned day: %+v, days %v", res, s.allDays())
	}
}

func TestTraceSizeCapDropsTheOldestDays(t *testing.T) {
	s, _ := openTraces(t)
	var spans []analytics.Span
	for d := range 3 {
		spans = append(spans, mkSpan(byte(d+1), analytics.SpanID(d+1), 0, 10, -time.Duration(d)*24*time.Hour, time.Millisecond))
	}
	// Any size is too much: everything but today goes.
	writeSpans(t, s, 1, analytics.TraceLimits{MaxBytes: 1}, spans...)
	if days := s.allDays(); len(days) != 1 || !days[0].Equal(utcDay(traceNow)) {
		t.Errorf("days left: %v, want today's", days)
	}
}

// Past a service's cap its failed spans are still written, up to the
// error cap, whether or not the node said why it kept them.
func TestErrorSpansPassTheServiceCap(t *testing.T) {
	s, _ := openTraces(t)
	lim := analytics.TraceLimits{ServiceDay: 1, ErrorDay: 2}
	sp := func(n byte) analytics.Span {
		return mkSpan(n, analytics.SpanID(n), 0, 10, time.Minute, time.Millisecond)
	}
	failed, fiveXX, third := sp(2), sp(3), sp(4)
	failed.Status = analytics.StatusError // no Kept: an older node
	fiveXX.HTTPStatus = 503
	third.Status = analytics.StatusError
	if res := writeSpans(t, s, 1, lim, sp(1), failed, fiveXX, third, sp(5)); res.Written != 3 || res.Rejected != 2 {
		t.Errorf("caps: %+v, want the first span and two failed ones", res)
	}
}

// An error fingerprint's newest span is its trace, within the range and
// the services asked for.
func TestErrorTrace(t *testing.T) {
	s, _ := openTraces(t)
	ctx := context.Background()
	older, newer, other := mkSpan(1, 1, 0, 10, time.Minute, time.Millisecond), mkSpan(2, 2, 0, 10, 2*time.Minute, time.Millisecond), mkSpan(3, 3, 0, 11, 3*time.Minute, time.Millisecond)
	older.ErrorFP, newer.ErrorFP, other.ErrorFP = "fp1", "fp1", "fp1"
	writeSpans(t, s, 1, analytics.TraceLimits{}, older, newer, other)
	q := hour(analytics.TracesQuery{})
	got, err := s.ErrorTrace(ctx, analytics.ErrorTraceQuery{Org: 1, Services: []int64{10}, Fingerprint: "fp1", From: q.From, To: q.To})
	if err != nil || got.Trace != tid(2) || got.Start != newer.Start {
		t.Errorf("error trace = %+v, %v", got, err)
	}
	if got, _ := s.ErrorTrace(ctx, analytics.ErrorTraceQuery{Org: 1, Services: []int64{10}, Fingerprint: "nope", From: q.From, To: q.To}); !got.Trace.IsZero() {
		t.Errorf("an unknown fingerprint found %+v", got)
	}
}
