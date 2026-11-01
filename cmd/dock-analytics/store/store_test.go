package store

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/cmd/dock-analytics/bench"
	"github.com/NordTempo/tempoDock/internal/analytics"
)

// open returns a store on a temp file.
func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "a.duckdb"), Options{MemoryLimit: 128 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// day0 is a fixed past day the tests write to; rollups only cover days
// that are over.
var day0 = time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)

// at is a page view by visitor on site 1 at day0 plus offset.
func at(visitor uint64, offset time.Duration, path string) analytics.Event {
	return analytics.Event{
		Time: day0.Add(offset).UnixMilli(), Site: 1, Service: 10, Release: 100, Hostname: "shop.example.com",
		Kind: analytics.KindPageview, Path: path, Visitor: visitor, Browser: "Firefox", Channel: "direct",
	}
}

func write(t *testing.T, s *Store, source string, pos uint64, events ...analytics.Event) int {
	t.Helper()
	n, _, err := s.Write(context.Background(), source, pos, events, nil)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// dayQuery covers day0 for site 1.
var dayQuery = analytics.Query{Site: 1, From: day0.UnixMilli(), To: day0.Add(24 * time.Hour).UnixMilli()}

func TestWriteSkipsBatchesWrittenBefore(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if n := write(t, s, "server-1", 5, at(1, 0, "/"), at(1, time.Minute, "/a")); n != 2 {
		t.Fatalf("first write: %d events, want 2", n)
	}
	// Sent again after a lost acknowledgement, and an older batch.
	for _, pos := range []uint64{5, 4} {
		if n := write(t, s, "server-1", pos, at(1, 0, "/")); n != 0 {
			t.Errorf("batch at %d written again: %d events", pos, n)
		}
	}
	// Another source has its own cursor.
	if n := write(t, s, "server-2", 1, at(2, 0, "/")); n != 1 {
		t.Errorf("other source: %d events, want 1", n)
	}
	if cur, err := s.Cursor(ctx, "server-1"); err != nil || cur != 5 {
		t.Errorf("cursor = %d, %v; want 5", cur, err)
	}
	st, err := s.Stats(ctx)
	if err != nil || st.Events != 3 {
		t.Errorf("events = %d, %v; want 3", st.Events, err)
	}
}

func TestSessions(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	google := at(1, 0, "/")
	google.Referrer, google.Channel = "google.com", "search"
	write(t, s, "a", 1,
		// Visitor 1: two page views ten minutes apart (one session, not
		// a bounce), then one after an hour (a new session, a bounce).
		google, at(1, 10*time.Minute, "/pricing"), at(1, 70*time.Minute, "/"),
		// Visitor 2: one page view.
		at(2, 5*time.Minute, "/docs"),
	)
	o, err := s.Overview(ctx, dayQuery)
	if err != nil {
		t.Fatal(err)
	}
	want := analytics.Overview{Visitors: 2, Pageviews: 4, Sessions: 3, BounceRate: 2.0 / 3, Duration: (10 * time.Minute).Milliseconds() / 3}
	if math.Abs(o.BounceRate-want.BounceRate) < 1e-9 {
		o.BounceRate = want.BounceRate
	}
	if o != want {
		t.Errorf("overview = %+v\nwant       %+v", o, want)
	}

	// The second page view has no referrer of its own; it is counted
	// with its session's.
	rows, err := s.Breakdown(ctx, analytics.BreakdownQuery{Query: dayQuery, Dimension: analytics.DimReferrer})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rows, []analytics.Row{{Value: "", Visitors: 2, Count: 2}, {Value: "google.com", Visitors: 1, Count: 2}}) {
		t.Errorf("referrers = %+v", rows)
	}
	entry, err := s.Breakdown(ctx, analytics.BreakdownQuery{Query: dayQuery, Dimension: analytics.DimEntryPage})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(entry, []analytics.Row{{Value: "/", Visitors: 1, Count: 2}, {Value: "/docs", Visitors: 1, Count: 1}}) {
		t.Errorf("entry pages = %+v", entry)
	}

	// A filter on a page groups what matches by session.
	q := dayQuery
	q.Filters = []analytics.Filter{{Dimension: analytics.DimPage, Op: analytics.OpIs, Value: "/pricing"}}
	o, err = s.Overview(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if o.Visitors != 1 || o.Pageviews != 1 || o.Sessions != 1 || o.BounceRate != 1 {
		t.Errorf("filtered overview = %+v", o)
	}
}

func TestSessionsSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.duckdb")
	ctx := context.Background()
	s, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	write(t, s, "a", 1, at(1, 0, "/"))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	write(t, s, "a", 2, at(1, 5*time.Minute, "/b"))
	o, err := s.Overview(ctx, dayQuery)
	if err != nil {
		t.Fatal(err)
	}
	if o.Sessions != 1 || o.BounceRate != 0 {
		t.Errorf("after a restart the session split: %+v", o)
	}
}

// answers is everything a dashboard asks for a query.
type answers struct {
	Overview   analytics.Overview
	Series     []analytics.Point
	Breakdowns map[analytics.Dimension][]analytics.Row
}

func ask(t *testing.T, s *Store, q analytics.Query) answers {
	t.Helper()
	ctx := context.Background()
	var a answers
	var err error
	if a.Overview, err = s.Overview(ctx, q); err != nil {
		t.Fatal(err)
	}
	if a.Series, err = s.Series(ctx, analytics.SeriesQuery{Query: q, Bucket: analytics.BucketDay}); err != nil {
		t.Fatal(err)
	}
	a.Breakdowns = map[analytics.Dimension][]analytics.Row{}
	for _, d := range analytics.Dimensions {
		if a.Breakdowns[d], err = s.Breakdown(ctx, analytics.BreakdownQuery{Query: q, Dimension: d, Limit: 20}); err != nil {
			t.Fatal(d, err)
		}
	}
	return a
}

// seedWriter writes seeded events with the seed's locations.
type seedWriter struct{ s *Store }

func (w seedWriter) Write(ctx context.Context, source string, pos uint64, events []analytics.Event) (int, error) {
	n, _, err := w.s.Write(ctx, source, pos, events, func(p string) Location {
		c, r, city := bench.SeedLocator(p)
		return Location{c, r, city}
	})
	return n, err
}

func TestRollupsAnswerAsRawEvents(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	end := day0.AddDate(0, 0, 10)
	if _, err := bench.Seed(ctx, seedWriter{s}, bench.SeedOptions{Events: 30_000, Days: 10, End: end, Seed: 3}); err != nil {
		t.Fatal(err)
	}
	// A range that starts and ends mid-day: raw edges around rolled-up
	// days.
	q := analytics.Query{Site: bench.MainSite, From: day0.Add(30 * time.Hour).UnixMilli(), To: end.Add(-5 * time.Hour).UnixMilli()}
	raw := ask(t, s, q)
	if raw.Overview.Visitors == 0 {
		t.Fatal("the seed wrote nothing in range")
	}
	if err := s.BuildRollups(ctx); err != nil {
		t.Fatal(err)
	}
	if sp, err := s.rollupSpan(ctx, q.Site, q.From, q.To); err != nil || !sp.rolled() || len(sp.raw) != 2 {
		t.Fatalf("span = %+v, %v; want rolled-up days between two raw edges", sp, err)
	}
	if rolled := ask(t, s, q); !reflect.DeepEqual(rolled, raw) {
		t.Errorf("rolled-up answers differ from raw ones\nrolled: %+v\nraw:    %+v", rolled.Overview, raw.Overview)
		for d, rows := range raw.Breakdowns {
			if !reflect.DeepEqual(rolled.Breakdowns[d], rows) {
				t.Errorf("%s: rolled %v, raw %v", d, rolled.Breakdowns[d], rows)
			}
		}
	}

	// Events for a day that is rolled up mark it stale: it is read raw
	// until it is rolled up again.
	late := at(99, 0, "/late")
	late.Time = day0.Add(3*24*time.Hour + time.Hour).UnixMilli()
	late.Site = bench.MainSite
	write(t, s, "late", 1, late)
	before := raw.Overview.Pageviews
	for _, step := range []string{"stale", "rebuilt"} {
		o, err := s.Overview(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if o.Pageviews != before+1 {
			t.Errorf("%s: page views = %d, want %d", step, o.Pageviews, before+1)
		}
		if err := s.BuildRollups(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBadQueries(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	for name, q := range map[string]analytics.BreakdownQuery{
		"unknown dimension": {Query: dayQuery, Dimension: "nope"},
		"empty range":       {Query: analytics.Query{Site: 1, From: 10, To: 10}, Dimension: analytics.DimPage},
		"unknown filter":    {Query: analytics.Query{Site: 1, From: 0, To: 10, Filters: []analytics.Filter{{Dimension: "x", Op: analytics.OpIs}}}, Dimension: analytics.DimPage},
		"unknown operator":  {Query: analytics.Query{Site: 1, From: 0, To: 10, Filters: []analytics.Filter{{Dimension: analytics.DimPage, Op: "like"}}}, Dimension: analytics.DimPage},
	} {
		var qe *QueryError
		if _, err := s.Breakdown(ctx, q); !errors.As(err, &qe) {
			t.Errorf("%s: err = %v, want a QueryError", name, err)
		}
	}
	var qe *QueryError
	year := analytics.Query{Site: 1, From: 0, To: 365 * 24 * 3600 * 1000}
	if _, err := s.Series(ctx, analytics.SeriesQuery{Query: year, Bucket: analytics.BucketMinute}); !errors.As(err, &qe) {
		t.Errorf("a year of minutes: err = %v, want a QueryError", err)
	}
}

func TestFilterValuesAreParameters(t *testing.T) {
	s := open(t)
	write(t, s, "a", 1, at(1, 0, "/100%_done"), at(2, 0, "/100x"))
	q := dayQuery
	q.Filters = []analytics.Filter{{Dimension: analytics.DimPage, Op: analytics.OpContains, Value: "100%_"}}
	o, err := s.Overview(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if o.Pageviews != 1 {
		t.Errorf("contains matched %d page views, want 1 (LIKE wildcards in the value are literal)", o.Pageviews)
	}
	q.Filters = []analytics.Filter{{Dimension: analytics.DimPage, Op: analytics.OpIs, Value: "' OR 1=1 --"}}
	if o, err = s.Overview(context.Background(), q); err != nil || o.Pageviews != 0 {
		t.Errorf("quote in a value: %+v, %v", o, err)
	}
}

func TestTrimKeepsAnswers(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	write(t, s, "a", 1, at(1, 0, "/"), at(2, 0, "/"))
	if err := s.Trim(ctx); err != nil {
		t.Fatal(err)
	}
	if o, err := s.Overview(ctx, dayQuery); err != nil || o.Visitors != 2 {
		t.Errorf("after a trim: %+v, %v", o, err)
	}
}

func TestSessionChangesRollBack(t *testing.T) {
	s := newSessions()
	s.begin()
	if m := s.assign(1, 7, 1000, true, source{}); !m.entry {
		t.Fatal("first page view is not the entry")
	}
	s.rollback()
	// The write is sent again: its first page view is still the entry.
	s.begin()
	if m := s.assign(1, 7, 1000, true, source{}); !m.entry || m.engaged {
		t.Errorf("after a rollback, mark = %+v; want the entry again", m)
	}
	s.commit()
	if m := s.assign(1, 7, 2000, true, source{}); m.entry || !m.engaged {
		t.Errorf("second page view, mark = %+v; want engaged", m)
	}
}

func TestLateEventsJoinTheirSession(t *testing.T) {
	s := open(t)
	// The afternoon session first, then the morning's arrives (a spool's
	// backlog): one session each, not one per late page view.
	write(t, s, "live", 1, at(1, 15*time.Hour, "/"))
	write(t, s, "backlog", 1, at(1, 9*time.Hour, "/"), at(1, 9*time.Hour+5*time.Minute, "/a"), at(1, 9*time.Hour+9*time.Minute, "/b"))
	o, err := s.Overview(context.Background(), dayQuery)
	if err != nil {
		t.Fatal(err)
	}
	if o.Sessions != 2 || o.BounceRate != 0.5 {
		t.Errorf("overview = %+v; want 2 sessions, one bounced", o)
	}
}

func TestSessionsThatBeganBeforeTheRange(t *testing.T) {
	s := open(t)
	write(t, s, "a", 1, at(1, 10*time.Hour, "/"), at(1, 10*time.Hour+20*time.Minute, "/b"))
	q := analytics.Query{Site: 1, From: day0.Add(10*time.Hour + 10*time.Minute).UnixMilli(), To: day0.Add(11 * time.Hour).UnixMilli()}
	o, err := s.Overview(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	// The page view in range counts; the session, which began before
	// it, doesn't, and neither do its marks.
	if o.Pageviews != 1 || o.Sessions != 0 || o.BounceRate != 0 || o.Duration != 0 {
		t.Errorf("overview = %+v", o)
	}
}

func TestPruneKeepsDailyTotals(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	write(t, s, "a", 1, at(1, time.Hour, "/"), at(2, 2*time.Hour, "/"))
	if err := s.BuildRollups(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := s.Prune(ctx, analytics.PruneQuery{Site: 1, Before: day0.Add(48 * time.Hour).UnixMilli()})
	if err != nil || res.Deleted != 2 {
		t.Fatalf("prune = %+v, %v", res, err)
	}
	// The day's numbers come from its rollup now.
	q := analytics.Query{Site: 1, From: day0.UnixMilli(), To: day0.Add(24 * time.Hour).UnixMilli()}
	if o, err := s.Overview(ctx, q); err != nil || o.Visitors != 2 {
		t.Errorf("after pruning: %+v, %v", o, err)
	}
	// A day not rolled up yet keeps its events.
	late := at(3, 50*time.Hour, "/")
	write(t, s, "a", 2, late)
	if res, _ = s.Prune(ctx, analytics.PruneQuery{Site: 1, Before: day0.Add(72 * time.Hour).UnixMilli()}); res.Deleted != 0 {
		t.Errorf("pruned %d events of a day without its rollup", res.Deleted)
	}
}
