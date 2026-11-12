package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// values are a line's values, -1 for a step without one.
func values(l analytics.MetricLine) []float64 {
	out := make([]float64, len(l.Values))
	for i, v := range l.Values {
		out[i] = -1
		if v != nil {
			out[i] = *v
		}
	}
	return out
}

func equal(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if d := a[i] - b[i]; d > 1e-9 || d < -1e-9 {
			return false
		}
	}
	return true
}

// Each shape of metric reads as its charts show it: a level's series
// combined by the aggregation or split by an attribute, a counter's rate
// a minute, a cumulative counter's growth (a reset is a new start), a
// histogram's percentiles, average and rate; and past the minute points'
// days, from hours.
func TestQueryMetrics(t *testing.T) {
	s, _ := openTraces(t)
	m := func(i int) time.Duration { return time.Duration(i) * time.Minute }
	var pts []analytics.MetricPoint
	for i, v := range []float64{10, 20, 30} {
		pts = append(pts, gauge("heap", `{"space":"old"}`, v, m(i)), gauge("heap", `{"space":"new"}`, v/10, m(i)))
	}
	for i := range 4 {
		p := gauge("orders.created", "", 60, m(i))
		p.Kind, p.Monotonic = analytics.MetricSum, true
		pts = append(pts, p)
	}
	for i, v := range []float64{100, 160, 220, 10} {
		p := gauge("jobs.done", "", v, m(i))
		p.Kind, p.Monotonic, p.Cumulative = analytics.MetricSum, true, true
		pts = append(pts, p)
	}
	for i, v := range []float64{5, 7} {
		p := gauge("pool.used", "", v, m(i))
		p.Kind, p.Cumulative = analytics.MetricSum, true // an UpDownCounter: a level
		pts = append(pts, p)
	}
	h0, h1 := histogram(4, 2, 3, m(0)), histogram(4, 9, 12, m(1))
	h1.Buckets[3], h1.Buckets[5] = 0, 4
	pts = append(pts, h0, h1)
	writeMetrics(t, s, 1, analytics.MetricLimits{}, pts...)

	start := traceNow.Truncate(time.Hour).Unix()
	query := func(from, to, step int64, charts ...analytics.MetricChartQuery) analytics.MetricsResult {
		t.Helper()
		res, err := s.QueryMetrics(context.Background(), analytics.MetricsQuery{Org: 1, Service: 4, From: from, To: to, Step: step, Charts: charts})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := query(start, start+180, 60,
		analytics.MetricChartQuery{Name: "heap"},
		analytics.MetricChartQuery{Name: "heap", Agg: analytics.AggMax},
		analytics.MetricChartQuery{Name: "heap", By: "space"},
		analytics.MetricChartQuery{Name: "pool.used"},
		analytics.MetricChartQuery{Name: "nothing"},
	)
	for i, c := range []struct {
		lines [][]float64
		label string
	}{
		{lines: [][]float64{{11, 22, 33}}},
		{lines: [][]float64{{10, 20, 30}}},
		{lines: [][]float64{{10, 20, 30}, {1, 2, 3}}, label: "old"},
		{lines: [][]float64{{5, 7, -1}}},
		{},
	} {
		got := res.Charts[i]
		if len(got.Lines) != len(c.lines) {
			t.Errorf("chart %d (%s %s by %q) = %+v, want %v", i, got.Name, got.Agg, got.By, got.Lines, c.lines)
			continue
		}
		for j, want := range c.lines {
			if v := values(got.Lines[j]); !equal(v, want) {
				t.Errorf("chart %d line %d = %v, want %v", i, j, v, want)
			}
		}
		if c.label != "" && got.Lines[0].Label != c.label {
			t.Errorf("chart %d's first line is %q, want the biggest, %q", i, got.Lines[0].Label, c.label)
		}
	}
	if heap := res.Charts[0]; heap.Shape != analytics.ShapeLevel || heap.Agg != analytics.AggSum || heap.Series != 2 ||
		len(heap.Attrs) != 1 || heap.Attrs[0] != (analytics.MetricAttr{Key: "space", Values: 2}) {
		t.Errorf("heap = %+v", heap)
	}
	if p := res.Charts[3]; p.Shape != analytics.ShapeLevel {
		t.Errorf("a cumulative sum that isn't monotonic is a %s, want a level", p.Shape)
	}

	res = query(start, start+240, 120, analytics.MetricChartQuery{Name: "orders.created"}, analytics.MetricChartQuery{Name: "orders.created", Agg: analytics.AggSum},
		analytics.MetricChartQuery{Name: "http.client.duration", Agg: analytics.AggP50}, analytics.MetricChartQuery{Name: "http.client.duration"},
		analytics.MetricChartQuery{Name: "http.client.duration", Agg: analytics.AggRate}, analytics.MetricChartQuery{Name: "http.client.duration", Agg: analytics.AggAvg})
	for i, want := range [][]float64{
		{60, 60},   // a minute's 60, in steps of two minutes
		{120, 120}, // the step's sum
		{4, -1},    // 4 in (2, 4] ms and 4 in (8, 16]: the median at 4
		{15.2, -1}, // the 95th: 90% into (8, 16]
		{4, -1},    // 8 a step of two minutes
		{5.5, -1},  // the sums the helper set, 8 and 36, over 8 values
	} {
		if got := res.Charts[i]; len(got.Lines) != 1 || !equal(values(got.Lines[0]), want) {
			t.Errorf("%s %s = %+v, want %v", got.Name, got.Agg, got.Lines, want)
		}
	}

	// The growth from the step before: 60, 60, then a reset to 10.
	res = query(start+60, start+240, 60, analytics.MetricChartQuery{Name: "jobs.done"})
	if got := res.Charts[0]; got.Shape != analytics.ShapeCounter || len(got.Lines) != 1 || !equal(values(got.Lines[0]), []float64{60, 60, 10}) {
		t.Errorf("cumulative counter = %+v", got)
	}

	var bad *QueryError
	if _, err := s.QueryMetrics(context.Background(), analytics.MetricsQuery{Org: 1, Service: 4, From: start, To: start + 60, Step: 60,
		Charts: []analytics.MetricChartQuery{{Name: "heap", Agg: analytics.AggRate}}}); !errors.As(err, &bad) {
		t.Errorf("a level's rate: %v, want a query error", err)
	}

	// Rolled up into hours: the level's average an hour.
	if _, err := s.PruneTraces(context.Background(), analytics.TracePrune{Before: traceNow.Add(24 * time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	res = query(start, start+3600, 3600, analytics.MetricChartQuery{Name: "heap"}, analytics.MetricChartQuery{Name: "orders.created"})
	if got := res.Charts[0]; len(got.Lines) != 1 || !equal(values(got.Lines[0]), []float64{22}) {
		t.Errorf("heap from hours = %+v, want the average 22", got.Lines)
	}
	if got := res.Charts[1]; len(got.Lines) != 1 || !equal(values(got.Lines[0]), []float64{4}) {
		t.Errorf("orders from hours = %+v, want 240 an hour, 4 a minute", got.Lines)
	}
}

// A day of points is rolled up into hours on the next day's first write,
// and charts in steps of an hour read the hours, while its minute points
// stay for charts in minutes.
func TestMetricsRollUpAtDayEnd(t *testing.T) {
	s, _ := openTraces(t)
	writeMetrics(t, s, 1, analytics.MetricLimits{}, gauge("heap", "", 10, time.Minute), gauge("heap", "", 30, 2*time.Minute))
	if n := count(t, s, "SELECT count(*) FROM metric_hours"); n != 0 {
		t.Fatalf("%d hours before the day ended", n)
	}
	s.now = func() time.Time { return traceNow.Add(24 * time.Hour) }
	writeMetrics(t, s, 2, analytics.MetricLimits{}, gauge("heap", "", 50, 24*time.Hour))
	if n := count(t, s, "SELECT count(*) FROM metric_hours"); n != 1 || !s.hasMetricDay(utcDay(traceNow)) {
		t.Fatalf("%d hours after the day ended, want 1 and the day's minute points kept", n)
	}
	// Hours there: remove the minute points' rows behind them, and the
	// hourly chart still reads the day.
	if _, err := s.db.Exec("DELETE FROM metrics_20261110"); err != nil {
		t.Fatal(err)
	}
	start := traceNow.Truncate(time.Hour).Unix()
	res, err := s.QueryMetrics(context.Background(), analytics.MetricsQuery{Org: 1, Service: 4, From: start, To: start + 25*3600, Step: 3600,
		Charts: []analytics.MetricChartQuery{{Name: "heap"}}})
	if err != nil {
		t.Fatal(err)
	}
	v := values(res.Charts[0].Lines[0])
	if v[0] != 20 || v[24] != 50 {
		t.Errorf("hourly heap = %v, want 20 from hours, then today's 50 from minute points", v)
	}
}
