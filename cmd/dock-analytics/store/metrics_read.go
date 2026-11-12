package store

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// Reading app metrics: a chart is a metric of one service, its points
// in steps and its series grouped into lines by an attribute. Per chart
// one query over the day tables the range covers (minute points), or for
// steps of an hour or more over hours (metric_hours, the days rolled up)
// and the minute points summed into hours; and for all of a query's
// charts one query for their series and one for their attributes.
//
// A level's step is its points' average, and a line combines its series
// by the chart's aggregation. A counter's step is the sum of its deltas.
// A histogram's step sums its bucket counts, and its percentiles are
// interpolated within a bucket (MetricBucketScale). Cumulative points
// (older SDKs, and counters and histograms an SDK didn't make deltas)
// that aren't levels are read as each series' last total in a step, summed into the line;
// the step's value is what the total grew since the step before, or the
// total itself after a reset. A cumulative histogram's hours are sums of
// totals, so only its minute points' days read right.

// metricSteps caps a query's steps.
const metricSteps = 500

// chartSeries is the series a chart reads: its metric's most common
// kind, unit and temporality, and how many series have them.
type chartSeries struct {
	kind                  analytics.MetricKind
	unit                  string
	monotonic, cumulative bool
	count                 int
}

// QueryMetrics reads q's charts.
func (s *Traces) QueryMetrics(ctx context.Context, q analytics.MetricsQuery) (analytics.MetricsResult, error) {
	res := analytics.MetricsResult{Charts: make([]analytics.MetricChart, 0, len(q.Charts))}
	switch {
	case q.Org <= 0 || q.Service <= 0:
		return res, &QueryError{"a metrics query needs its org and service"}
	case q.To <= q.From:
		return res, &QueryError{"a metrics query needs a range that ends after it starts"}
	case q.Step < 60 || q.Step%60 != 0 || (q.To-q.From)/q.Step > metricSteps:
		return res, &QueryError{fmt.Sprintf("a metrics query's step is whole minutes, at most %d of them in its range", metricSteps)}
	case len(q.Charts) == 0 || len(q.Charts) > analytics.MaxCharts:
		return res, &QueryError{fmt.Sprintf("a metrics query reads 1 to %d charts", analytics.MaxCharts)}
	}
	for _, c := range q.Charts {
		if c.Name == "" || len(c.Name) > 256 || len(c.By) > 128 {
			return res, &QueryError{"a chart names a metric (at most 256 bytes) and splits it by an attribute of at most 128"}
		}
	}
	s.gate.RLock()
	defer s.gate.RUnlock()
	names := make([]string, 0, len(q.Charts))
	for _, c := range q.Charts {
		if !slices.Contains(names, c.Name) {
			names = append(names, c.Name)
		}
	}
	series, attrs, err := s.chartSeries(ctx, q, names)
	if err != nil {
		return res, err
	}
	for _, c := range q.Charts {
		cs, ok := series[c.Name]
		out := analytics.MetricChart{Name: c.Name, Agg: c.Agg, By: c.By, Lines: []analytics.MetricLine{}, Attrs: attrs[c.Name]}
		if out.Attrs == nil {
			out.Attrs = []analytics.MetricAttr{}
		}
		if !ok {
			res.Charts = append(res.Charts, out) // no series in the range
			continue
		}
		out.Unit, out.Kind, out.Shape, out.Series = cs.unit, cs.kind, analytics.ShapeOf(cs.kind, cs.monotonic, cs.cumulative), cs.count
		aggs := analytics.Aggs(out.Shape)
		if out.Agg == "" {
			out.Agg = aggs[0]
		} else if !slices.Contains(aggs, out.Agg) {
			return res, &QueryError{fmt.Sprintf("%s is a %s: use %s", c.Name, out.Shape, joinAggs(aggs))}
		}
		if err := s.readChart(ctx, q, cs, &out); err != nil {
			return res, fmt.Errorf("read %s: %w", c.Name, err)
		}
		res.Charts = append(res.Charts, out)
	}
	return res, nil
}

// joinAggs is aggs as a list for an error: "sum, max, min or avg".
func joinAggs(aggs []analytics.MetricAgg) string {
	s := make([]string, len(aggs))
	for i, a := range aggs {
		s[i] = string(a)
	}
	return strings.Join(s[:len(s)-1], ", ") + " or " + s[len(s)-1]
}

// chartSeries reads, for each of names, its series' most common shape
// and their attributes with their values' counts, among the series of
// q's service seen in q's days.
func (s *Traces) chartSeries(ctx context.Context, q analytics.MetricsQuery, names []string) (map[string]chartSeries, map[string][]analytics.MetricAttr, error) {
	args := []any{uint32(q.Service), uint32(q.Org), utcDay(time.Unix(q.From, 0)), utcDay(time.Unix(q.To-1, 0))}
	marks := make([]string, len(names))
	for i, n := range names {
		args = append(args, n)
		marks[i] = "$" + strconv.Itoa(len(args))
	}
	where := "service_id = $1 AND org_id = $2 AND last_day >= $3 AND first_day <= $4 AND name IN (" + strings.Join(marks, ", ") + ")"
	rows, err := s.db.QueryContext(ctx, "SELECT name, kind, unit, monotonic, cumulative, count(*) FROM metric_series WHERE "+where+" GROUP BY ALL", args...)
	if err != nil {
		return nil, nil, err
	}
	series := map[string]chartSeries{}
	for rows.Next() {
		var name string
		var cs chartSeries
		if err := rows.Scan(&name, &cs.kind, &cs.unit, &cs.monotonic, &cs.cumulative, &cs.count); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		if cs.count > series[name].count {
			series[name] = cs
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT name, key, count(DISTINCT json_extract_string(attrs, '$."' || replace(key, '"', '\"') || '"')) AS n
    FROM (SELECT name, attrs, unnest(json_keys(attrs)) AS key FROM metric_series WHERE `+where+`)
    GROUP BY name, key ORDER BY name, n DESC, key`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	attrs := map[string][]analytics.MetricAttr{}
	for rows.Next() {
		var name string
		var a analytics.MetricAttr
		if err := rows.Scan(&name, &a.Key, &a.Values); err != nil {
			return nil, nil, err
		}
		attrs[name] = append(attrs[name], a)
	}
	return series, attrs, rows.Err()
}

// readChart reads c's lines: the query's steps, and one before them
// for cumulative points' growth. Two queries: the chart's series (their
// labels read here), then their points by series and step, from hours
// where they are rolled up and minute points after.
func (s *Traces) readChart(ctx context.Context, q analytics.MetricsQuery, cs chartSeries, c *analytics.MetricChart) error {
	labels, nums, err := s.seriesLabels(ctx, q, cs, c)
	if err != nil || len(nums) == 0 {
		return err
	}
	shape := analytics.ShapeOf(cs.kind, cs.monotonic, cs.cumulative)
	hist := shape == analytics.ShapeHistogram
	// Cumulative counters and histograms grow; a cumulative level (an
	// UpDownCounter) is read like a gauge.
	grows := cs.cumulative && shape != analytics.ShapeLevel
	var cols, aggs []string
	switch {
	case grows && hist:
		cols, aggs = []string{"last", "n", "buckets"}, []string{"arg_max(last, t)", "arg_max(n, t)", "arg_max(buckets, t)"}
	case grows:
		cols, aggs = []string{"last"}, []string{"arg_max(last, t)"}
	case hist:
		sums := make([]string, analytics.MetricBuckets)
		for i := range sums {
			sums[i] = "coalesce(sum(buckets[" + strconv.Itoa(i+1) + "]), 0)::UBIGINT"
		}
		cols, aggs = []string{"v", "n", "buckets"}, []string{"sum(v)", "sum(n)::UBIGINT", "[" + strings.Join(sums, ", ") + "]"}
	case shape == analytics.ShapeCounter:
		cols, aggs = []string{"v"}, []string{"sum(v)"}
	default:
		cols, aggs = []string{"v", "pts"}, []string{"sum(v)", "sum(pts)"}
	}
	lo := q.From - q.Step
	rows := s.chartRows(cols, nums, lo, q.To, q.Step)
	if rows == "" {
		return nil
	}
	res, err := s.db.QueryContext(ctx, "SELECT series, (t - $1) // $2 AS i, "+strings.Join(aggs, ", ")+
		" FROM ("+rows+") GROUP BY ALL", q.From, q.Step, lo, q.To)
	if err != nil {
		return err
	}
	defer func() { _ = res.Close() }()
	steps := int((q.To - q.From + q.Step - 1) / q.Step)
	type cell struct {
		v       float64
		n       uint64
		buckets []uint64
		// level holds a level's values in the step, a series each, for
		// the chart's aggregation.
		level []float64
	}
	// By label, the cells from step -1 (the one before the range).
	lines := map[string][]*cell{}
	for res.Next() {
		var series uint32
		var i int64
		var v, pts float64
		var n uint64
		var b []any
		dest := []any{&series, &i, &v}
		switch {
		case grows && hist:
			dest = append(dest, &n, &b)
		case hist:
			dest = append(dest, &n, &b)
		case !grows && shape == analytics.ShapeLevel:
			dest = append(dest, &pts)
		}
		if err := res.Scan(dest...); err != nil {
			return err
		}
		if i < -1 || i >= int64(steps) {
			continue
		}
		label := labels[series]
		if lines[label] == nil {
			lines[label] = make([]*cell, steps+1)
		}
		x := lines[label][i+1]
		if x == nil {
			x = &cell{}
			lines[label][i+1] = x
		}
		if shape == analytics.ShapeLevel && !grows {
			if pts > 0 {
				x.level = append(x.level, v/pts)
			}
			continue
		}
		x.v += v
		x.n += n
		if len(b) > 0 && x.buckets == nil {
			x.buckets = make([]uint64, len(b))
		}
		for j, e := range b {
			if j < len(x.buckets) {
				k, _ := e.(uint64)
				x.buckets[j] += k
			}
		}
	}
	if err := res.Err(); err != nil {
		return err
	}
	perMinute := float64(q.Step) / 60
	scale := analytics.MetricBucketScale(cs.unit)
	type ranked struct {
		line  analytics.MetricLine
		total float64
	}
	var all []ranked
	for label, cells := range lines {
		l := ranked{line: analytics.MetricLine{Label: label, Values: make([]*float64, steps)}}
		for i := 1; i <= steps; i++ {
			x := cells[i]
			if x == nil {
				continue
			}
			if grows {
				// What the totals grew since the step before.
				prev := cells[i-1]
				if prev == nil {
					continue
				}
				d := cell{v: grew(x.v, prev.v), n: uint64(grew(float64(x.n), float64(prev.n)))}
				if hist {
					d.buckets = make([]uint64, len(x.buckets))
					reset := x.n < prev.n
					for j, n := range x.buckets {
						if reset || j >= len(prev.buckets) || n < prev.buckets[j] {
							d.buckets[j] = n
						} else {
							d.buckets[j] = n - prev.buckets[j]
						}
					}
				}
				x = &d
			}
			v, ok := x.v, true
			switch {
			case shape == analytics.ShapeLevel:
				v, ok = combine(x.level, c.Agg)
			case hist:
				switch c.Agg {
				case analytics.AggRate:
					v = float64(x.n) / perMinute
				case analytics.AggAvg:
					v, ok = x.v/float64(x.n), x.n > 0
				default:
					v, ok = percentile(x.buckets, aggQuantile(c.Agg), scale)
				}
			case c.Agg == analytics.AggRate:
				v = x.v / perMinute
			}
			if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			l.line.Values[i-1] = &v
			l.total += math.Abs(v)
		}
		all = append(all, l)
	}
	slices.SortFunc(all, func(a, b ranked) int {
		return cmp.Or(cmp.Compare(b.total, a.total), strings.Compare(a.line.Label, b.line.Label))
	})
	for i, l := range all {
		if i == analytics.MaxLines {
			c.More = len(all) - i
			break
		}
		c.Lines = append(c.Lines, l.line)
	}
	return nil
}

// combine is a level's series' values in a step, by agg: sum, max, min
// or avg; false without values.
func combine(vs []float64, agg analytics.MetricAgg) (float64, bool) {
	if len(vs) == 0 {
		return 0, false
	}
	out := vs[0]
	for _, v := range vs[1:] {
		switch agg {
		case analytics.AggMax:
			out = max(out, v)
		case analytics.AggMin:
			out = min(out, v)
		default:
			out += v
		}
	}
	if agg == analytics.AggAvg {
		out /= float64(len(vs))
	}
	return out, true
}

// seriesLabels are the numbers of c's series seen in q's days, and the
// line each one is in: its value of c.By, "" for none.
func (s *Traces) seriesLabels(ctx context.Context, q analytics.MetricsQuery, cs chartSeries, c *analytics.MetricChart) (map[uint32]string, []int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT num, attrs FROM metric_series
    WHERE service_id = $1 AND org_id = $2 AND name = $3 AND kind = $4 AND unit = $5 AND monotonic = $6 AND cumulative = $7
        AND last_day >= $8 AND first_day <= $9`,
		uint32(q.Service), uint32(q.Org), c.Name, uint8(cs.kind), cs.unit, cs.monotonic, cs.cumulative,
		utcDay(time.Unix(q.From-q.Step, 0)), utcDay(time.Unix(q.To-1, 0)))
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	labels := map[uint32]string{}
	var nums []int64
	for rows.Next() {
		var num uint32
		var attrs string
		if err := rows.Scan(&num, &attrs); err != nil {
			return nil, nil, err
		}
		label := ""
		if c.By != "" {
			var m map[string]string
			if json.Unmarshal([]byte(attrs), &m) == nil {
				label = m[c.By]
			}
		}
		labels[num] = label
		nums = append(nums, int64(num))
	}
	return labels, nums, rows.Err()
}

// grew is what a cumulative total grew from prev to now: now itself
// after a reset.
func grew(now, prev float64) float64 {
	if now < prev {
		return now
	}
	return now - prev
}

// aggQuantile is a percentile aggregation's quantile.
func aggQuantile(a analytics.MetricAgg) float64 {
	switch a {
	case analytics.AggP50:
		return 0.5
	case analytics.AggP99:
		return 0.99
	}
	return 0.95
}

// percentile is the q quantile of a histogram's counts in Dock's buckets
// of scale, interpolated within its bucket; false without counts.
func percentile(buckets []uint64, q, scale float64) (float64, bool) {
	var total uint64
	for _, n := range buckets {
		total += n
	}
	if total == 0 {
		return 0, false
	}
	rank, seen := q*float64(total), 0.0
	for j, n := range buckets {
		if n == 0 {
			continue
		}
		if seen+float64(n) >= rank {
			lo, hi := 0.0, scale
			if j > 0 {
				lo, hi = math.Ldexp(scale, j-1), math.Ldexp(scale, j)
			}
			return lo + (rank-seen)/float64(n)*(hi-lo), true
		}
		seen += float64(n)
	}
	return math.Ldexp(scale, len(buckets)-1), true
}

// chartRows is the FROM of readChart: the points of series (numbers)
// from lo ($3) to to ($4) as series, t and cols of v, pts, last, n and
// buckets: from hours before the first day of minute points not rolled
// up yet, then a row a point. "" when there are none.
func (s *Traces) chartRows(cols []string, series []int64, lo, to, step int64) string {
	in := "series IN (" + intList(series) + ")"
	minute := map[string]string{"v": "value AS v", "pts": "1 AS pts", "last": "value AS last", "n": "n", "buckets": "buckets"}
	hour := map[string]string{"v": "value AS v", "pts": "points AS pts", "last": "last", "n": "n", "buckets": "buckets"}
	pick := func(m map[string]string) string {
		out := make([]string, len(cols))
		for i, c := range cols {
			out[i] = m[c]
		}
		return strings.Join(out, ", ")
	}
	var parts []string
	days := s.metricDaysIn(lo, to)
	if step >= 3600 {
		// Hours up to the first day not rolled up, minute points from it.
		cut := s.rolledCut()
		if cut > lo {
			parts = append(parts, "SELECT series, hour AS t, "+pick(hour)+" FROM metric_hours WHERE "+in+
				" AND hour >= $3 AND hour < least($4, "+strconv.FormatInt(cut, 10)+")")
		}
		days = slices.DeleteFunc(days, func(d time.Time) bool { return d.Unix() < cut })
	}
	for _, d := range days {
		parts = append(parts, "SELECT series, minute AS t, "+pick(minute)+" FROM "+metricDayTable(d)+" WHERE "+in+" AND minute >= $3 AND minute < $4")
	}
	return strings.Join(parts, " UNION ALL ")
}
