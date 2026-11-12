package store

import (
	"context"
	"encoding/json/jsontext"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// gauge is a point of org 1's service 4 at offset after traceNow's hour
// began.
func gauge(name, attrs string, v float64, offset time.Duration) analytics.MetricPoint {
	return analytics.MetricPoint{
		Org: 1, Project: 2, Env: 3, Service: 4, Release: 40, Name: name, Kind: analytics.MetricGauge,
		Attrs: jsontext.Value(attrs), Time: traceNow.Truncate(time.Hour).Add(offset).Unix(), Value: v,
	}
}

// histogram is a histogram point of the duration metric with count
// values in bucket 3.
func histogram(count uint64, lo, hi float64, offset time.Duration) analytics.MetricPoint {
	p := gauge("http.client.duration", "", lo*float64(count), offset)
	p.Kind, p.Unit, p.Count, p.Min, p.Max = analytics.MetricHistogram, "ms", count, &lo, &hi
	p.Buckets = make([]uint64, analytics.MetricBuckets)
	p.Buckets[3] = count
	return p
}

func writeMetrics(t *testing.T, s *Traces, pos uint64, lim analytics.MetricLimits, points ...analytics.MetricPoint) analytics.MetricWriteResult {
	t.Helper()
	res, err := s.WriteMetrics(context.Background(), "metrics:server:1", pos, points, lim)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// count is the number of rows q (a COUNT) finds.
func count(t *testing.T, s *Traces, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A service keeps at most lim's series and metric names a day: past
// either a new series is cut and counted by metric, while series it sent
// that day go on, after a restart too, and a new day starts over. A batch
// sent twice is written once.
func TestMetricsCaps(t *testing.T) {
	s, path := openTraces(t)
	lim := analytics.MetricLimits{Series: 3, Names: 2}
	heap := func(attrs string) analytics.MetricPoint { return gauge("v8js.memory.heap.used", attrs, 1, time.Minute) }
	if res := writeMetrics(t, s, 1, lim, heap(""), histogram(4, 1, 3, time.Minute)); res.Written != 2 {
		t.Fatalf("first write = %+v", res)
	}
	if res := writeMetrics(t, s, 1, lim, heap("")); res.Written != 0 {
		t.Errorf("a batch sent again was written: %+v", res)
	}
	res := writeMetrics(t, s, 2, lim, heap(`{"space":"old"}`), heap(`{"space":"new"}`), gauge("queue.depth", "", 9, time.Minute), heap(""))
	if res.Written != 2 || res.Cut != 2 {
		t.Errorf("past the caps = %+v, want 2 written (a third series, one seen) and 2 cut", res)
	}
	if n := count(t, s, "SELECT count(*) FROM metric_series"); n != 3 {
		t.Errorf("%d series, want 3", n)
	}
	if n := count(t, s, "SELECT count(*) FROM metric_cuts WHERE points = 1 AND name IN ('v8js.memory.heap.used', 'queue.depth')"); n != 2 {
		t.Errorf("%d cut rows, want one per cut metric", n)
	}
	names, err := s.MetricNames(context.Background(), analytics.MetricNamesQuery{Org: 1, Services: []int64{4}, From: traceNow.Add(-time.Hour).Unix(), To: traceNow.Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]analytics.MetricName{}
	for _, m := range names.Metrics {
		got[m.Name] = m
	}
	for name, want := range map[string]analytics.MetricName{
		"v8js.memory.heap.used": {Service: 4, Name: "v8js.memory.heap.used", Kind: analytics.MetricGauge, Series: 2, Cut: 1, CutBy: "space"},
		"http.client.duration":  {Service: 4, Name: "http.client.duration", Unit: "ms", Kind: analytics.MetricHistogram, Series: 1},
		"queue.depth":           {Service: 4, Name: "queue.depth", Cut: 1},
	} {
		if got[name] != want {
			t.Errorf("%s = %+v, want %+v", name, got[name], want)
		}
	}
	if len(got) != 3 {
		t.Errorf("metrics = %+v", names.Metrics)
	}
	invalid := gauge("x", "", 1, -31*24*time.Hour)
	noKind := heap("")
	noKind.Kind = 0
	if res := writeMetrics(t, s, 3, lim, invalid, noKind); res.Rejected != 2 {
		t.Errorf("a month-old point and one without a kind = %+v, want both rejected", res)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenTraces(context.Background(), path, Options{MemoryLimit: 128 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	s2.now = func() time.Time { return traceNow }
	if res := writeMetrics(t, s2, 4, lim, heap(`{"space":"old"}`), heap(`{"space":"code"}`)); res.Written != 1 || res.Cut != 1 {
		t.Errorf("after a restart = %+v, want today's series kept and a new one cut", res)
	}
	s2.now = func() time.Time { return traceNow.Add(24 * time.Hour) }
	if res := writeMetrics(t, s2, 5, lim, heap(`{"space":"code"}`)); res.Written != 1 {
		t.Errorf("a new day = %+v, want the series kept", res)
	}
	if n := count(t, s2, "SELECT count(*) FROM metrics_20261110"); n != 6 {
		t.Errorf("%d points, want 6", n)
	}
}

// A prune rolls each day of points it drops up into hours, then drops
// the hours and series past HoursBefore.
func TestMetricsRollUpIntoHours(t *testing.T) {
	s, _ := openTraces(t)
	writeMetrics(t, s, 1, analytics.MetricLimits{},
		gauge("heap", "", 1, time.Minute), gauge("heap", "", 3, 2*time.Minute), gauge("heap", "", 5, time.Hour+time.Minute),
		histogram(4, 2, 6, time.Minute), histogram(2, 1, 9, 30*time.Minute))
	res, err := s.PruneTraces(context.Background(), analytics.TracePrune{Before: traceNow.Add(24 * time.Hour).UnixMilli()})
	if err != nil || len(res.Days) != 1 || res.Days[0] != "2026-11-10" {
		t.Fatalf("prune = %+v, %v", res, err)
	}
	if s.hasMetricDay(utcDay(traceNow)) || count(t, s, "SELECT count(*) FROM duckdb_tables() WHERE table_name = 'metrics_20261110'") != 0 {
		t.Error("the day's points table is still there")
	}
	type hourRow struct {
		points      int
		value, last float64
		n           int
		min, max    float64
		b3          int
	}
	rows, err := s.db.Query(`SELECT points, value, last, n, min, max, coalesce(buckets[4], 0) FROM metric_hours ORDER BY n, hour`)
	if err != nil {
		t.Fatal(err)
	}
	var got []hourRow
	for rows.Next() {
		var r hourRow
		if err := rows.Scan(&r.points, &r.value, &r.last, &r.n, &r.min, &r.max, &r.b3); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	_ = rows.Close()
	want := []hourRow{
		{points: 2, value: 4, last: 3, min: 1, max: 3},               // the gauge's first hour
		{points: 1, value: 5, last: 5, min: 5, max: 5},               // its second
		{points: 2, value: 10, last: 2, n: 6, min: 1, max: 9, b3: 6}, // the histogram's
	}
	if len(got) != len(want) {
		t.Fatalf("hours = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("hour %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	if _, err := s.PruneTraces(context.Background(), analytics.TracePrune{Before: traceNow.Add(24 * time.Hour).UnixMilli(), HoursBefore: traceNow.Add(48 * time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "SELECT (SELECT count(*) FROM metric_hours) + (SELECT count(*) FROM metric_series)"); n != 0 {
		t.Errorf("%d hours and series left past HoursBefore", n)
	}
}
