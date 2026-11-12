package analytics

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"math"
	"net/http"
	"net/url"
	"strconv"
)

// App metrics over OTLP (ADR 0075, phase 6) take the spans' path: every
// server's ingest turns an app's export into MetricPoint lines in its
// metrics spool, the control plane's forwarder writes them to the trace
// store (WriteMetricLines), and the store keeps them in tables of their
// own: a series per service, metric and attributes, minute points by UTC
// day and hourly rollups.

// MetricKind is what a metric's points are.
type MetricKind uint8

// Metric kinds. Gauges are a value now, sums a count over an interval,
// histograms a distribution in MetricBuckets.
const (
	MetricGauge MetricKind = iota + 1
	MetricSum
	MetricHistogram
)

// MetricBuckets is how many buckets a histogram has in Dock: whatever an
// SDK sends (explicit bounds or exponential buckets) becomes these, so
// the store keeps one histogram shape.
const MetricBuckets = 20

// MetricPoint is one point as a spool line holds it and the store takes
// it. The ingest fills the IDs from the app's token, never from its
// attributes.
type MetricPoint struct {
	Org     int64      `json:"org"`
	Project int64      `json:"project"`
	Env     int64      `json:"env"`
	Service int64      `json:"service"`
	Release int64      `json:"release"`
	Name    string     `json:"name"`
	Unit    string     `json:"unit,omitzero"`
	Kind    MetricKind `json:"kind"`
	// Monotonic marks a sum that only grows (a counter).
	Monotonic bool `json:"mono,omitzero"`
	// Cumulative marks a sum or histogram whose points are totals since
	// the app started, not deltas: reads difference them.
	Cumulative bool `json:"cum,omitzero"`
	// Attrs is a JSON object of string values, keys sorted, as the ingest
	// cut and masked them: with the service, name, unit and kind it names
	// the series.
	Attrs jsontext.Value `json:"attrs,omitzero"`
	// Time is the point's Unix seconds; the store keeps its minute.
	Time int64 `json:"t"`
	// Value is a gauge's value, a sum's (its delta, or its total when
	// Cumulative) or a histogram's sum.
	Value float64 `json:"v,omitzero"`
	// Count, Min, Max and Buckets are a histogram's; Min and Max are
	// absent when the SDK didn't send them.
	Count   uint64   `json:"n,omitzero"`
	Min     *float64 `json:"min,omitzero"`
	Max     *float64 `json:"max,omitzero"`
	Buckets []uint64 `json:"b,omitzero"`
}

// MetricBucketScale is the upper bound of a histogram's first bucket for
// unit: bucket i ends at scale·2^i, and the last takes everything past
// the one before. Scaled so durations (in s, ms, µs or ns) all span 0.5
// ms to about 131 s, and sizes 64 B to 16 MB.
func MetricBucketScale(unit string) float64 {
	switch unit {
	case "s":
		return 0.0005
	case "ms":
		return 0.5
	case "us", "µs":
		return 500
	case "ns":
		return 500_000
	case "By":
		return 64
	}
	return 1
}

// MetricBucket is the bucket v falls in for a histogram with scale
// (MetricBucketScale): 0 for v at or under scale, MetricBuckets-1 past
// the last bound.
func MetricBucket(scale, v float64) int {
	switch {
	case !(v > scale): // also NaN
		return 0
	case math.IsInf(v, 1):
		return MetricBuckets - 1
	}
	_, exp := math.Frexp(v / scale) // v/scale = frac·2^exp, frac in [0.5, 1)
	if v/scale == math.Ldexp(0.5, exp) {
		exp-- // exactly a power of two: the bucket it bounds
	}
	return min(exp, MetricBuckets-1)
}

// MetricLimits are the caps a metrics write applies (zero is no cap),
// from the control plane's settings. Series and names are counted per
// service and UTC day: past either, new series are cut and counted by
// metric (the cut rows), while series already seen that day go on.
type MetricLimits struct {
	Series int
	Names  int
	// MaxBytes is TraceLimits.MaxBytes: metrics share the database.
	MaxBytes int64
}

// MetricWriteResult answers a metrics write.
type MetricWriteResult struct {
	Written int `json:"written"`
	// Skipped counts lines that weren't points, Rejected points outside
	// the days the store keeps or without a service or name, Cut points
	// of new series past a cap.
	Skipped  int `json:"skipped,omitempty"`
	Rejected int `json:"rejected,omitempty"`
	Cut      int `json:"cut,omitempty"`
}

// MaxMetricLine caps one point's JSON line in a write.
const MaxMetricLine = 8 << 10

// MetricsWritePath takes POST ?source=&position= and the MetricLimits
// (series, names, max_bytes) with a body of MetricPoint JSON lines, and
// answers MetricWriteResult. Its cursor is read at SpansCursorPath: the
// trace store keeps one cursor per source, spans or metrics.
const MetricsWritePath = "/v1/metrics/write"

// WriteMetricLines writes points that are already JSON lines, as a spool
// holds them, from source, whose points up to position are then all
// written. A batch at or before the source's cursor was written before
// and is skipped.
func (c *Client) WriteMetricLines(ctx context.Context, source string, position uint64, lines []byte, lim MetricLimits) (MetricWriteResult, error) {
	q := url.Values{"source": {source}, "position": {strconv.FormatUint(position, 10)}}
	for k, v := range map[string]int64{"series": int64(lim.Series), "names": int64(lim.Names), "max_bytes": lim.MaxBytes} {
		if v > 0 {
			q.Set(k, strconv.FormatInt(v, 10))
		}
	}
	var res MetricWriteResult
	err := c.do(ctx, http.MethodPost, MetricsWritePath+"?"+q.Encode(), bytes.NewReader(lines), &res)
	return res, err
}

// MetricNamesQuery lists the metrics Services of Org sent from From to To
// (Unix seconds), within the days of minute points the store keeps.
type MetricNamesQuery struct {
	Org      int64   `json:"org"`
	Services []int64 `json:"services"`
	From     int64   `json:"from"`
	To       int64   `json:"to"`
}

// MetricName is one metric of a service: its series and points in the
// range, and the points of its new series the caps cut.
type MetricName struct {
	Service int64      `json:"service"`
	Name    string     `json:"name"`
	Unit    string     `json:"unit,omitzero"`
	Kind    MetricKind `json:"kind"`
	Series  int        `json:"series"`
	Points  int64      `json:"points"`
	Cut     int64      `json:"cut,omitzero"`
}

// MetricNamesResult answers a MetricNamesQuery, by service and name.
type MetricNamesResult struct {
	Metrics []MetricName `json:"metrics"`
}

// MetricNamesPath takes POST with a MetricNamesQuery and answers
// MetricNamesResult.
const MetricNamesPath = "/v1/metrics/names"

// MetricNames lists metrics (MetricNamesPath).
func (c *Client) MetricNames(ctx context.Context, q MetricNamesQuery) (MetricNamesResult, error) {
	var res MetricNamesResult
	return res, c.post(ctx, MetricNamesPath, q, &res)
}
