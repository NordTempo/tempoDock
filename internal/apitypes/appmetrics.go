package apitypes

import "time"

// App metrics (ADR 0075, phase 6): what a service's apps send over OTLP,
// on its Metrics tab as a dashboard of pinned charts.

// AppMetricsRange is how far back app metrics are read.
type AppMetricsRange string

// App metrics ranges: minute points up to 7 days, hours past them.
const (
	AppMetrics1h  AppMetricsRange = "1h"
	AppMetrics24h AppMetricsRange = "24h"
	AppMetrics7d  AppMetricsRange = "7d"
	AppMetrics30d AppMetricsRange = "30d"
)

// MetricChartSpec is a pinned chart: a metric, how its points combine
// ("" is its shape's default: a level's sum, a counter's rate a minute,
// a histogram's p95) and the attribute that splits it into lines.
type MetricChartSpec struct {
	Name string `json:"name" minLength:"1" maxLength:"256"`
	Agg  string `json:"agg,omitempty" enum:"sum,max,min,avg,rate,p50,p95,p99"`
	By   string `json:"by,omitempty" maxLength:"128"`
}

// AppMetricChart is a pinned chart, read over the range.
type AppMetricChart struct {
	MetricChartSpec
	// Dock marks one of Dock's default charts (a runtime metric).
	Dock bool   `json:"dock"`
	Unit string `json:"unit,omitempty"`
	// Kind is gauge, sum or histogram, and Shape how it reads: level,
	// counter or histogram; both empty for a metric with no points in
	// the range.
	Kind  string `json:"kind,omitempty"`
	Shape string `json:"shape,omitempty"`
	// Aggs are the aggregations the metric takes, its default first;
	// Agg is the one used.
	Aggs   []string `json:"aggs"`
	Series int      `json:"series"`
	// Lines are at most 20, by size; More counts those left out.
	Lines []AppMetricLine `json:"lines"`
	More  int             `json:"more,omitempty"`
	// Attrs are the metric's attributes, the most values first.
	Attrs []AppMetricAttr `json:"attrs"`
}

// AppMetricLine is a chart's line: its series with one value of the
// chart's attribute (Label, "" for a series without it or a chart with
// no attribute), a value a step from AppMetrics.From, null where there
// was none. A counter's values are a minute's.
type AppMetricLine struct {
	Label  string     `json:"label"`
	Values []*float64 `json:"values" nullable:"true"`
}

// AppMetricAttr is an attribute of a metric's series and how many values
// it has.
type AppMetricAttr struct {
	Key    string `json:"key"`
	Values int    `json:"values"`
}

// AppMetric is a metric a service sent in the range: a series of it was
// seen in the range's UTC days.
type AppMetric struct {
	Name   string `json:"name"`
	Unit   string `json:"unit,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Series int    `json:"series"`
	// Cut counts its points of new series past the service's series or
	// names cap in the range's days, and CutBy is the attribute of its
	// series with the most values: the one to drop.
	Cut   int64  `json:"cut,omitempty"`
	CutBy string `json:"cut_by,omitempty"`
}

// AppMetrics is a service's app metrics: its pinned charts and the
// metrics it sent in the range.
type AppMetrics struct {
	Range       AppMetricsRange  `json:"range"`
	From        time.Time        `json:"from"`
	StepSeconds int64            `json:"step_seconds"`
	Charts      []AppMetricChart `json:"charts"`
	Metrics     []AppMetric      `json:"metrics"`
	// Saved says someone pinned the charts; without it they are Dock's
	// defaults for the runtime metrics the app sends.
	Saved bool `json:"saved"`
	// MaxSeries and MaxNames are the caps a day, for what Cut says.
	MaxSeries int `json:"max_series"`
	MaxNames  int `json:"max_names"`
	// TracingOff says nothing is collected: tracing is off. MetricsElsewhere
	// is the variable that sends the service's metrics somewhere else.
	TracingOff       bool   `json:"tracing_off,omitempty"`
	MetricsElsewhere string `json:"metrics_elsewhere,omitempty"`
}

// SetMetricChartsRequest pins a service's charts, in order: the whole
// dashboard, replacing what was pinned.
type SetMetricChartsRequest struct {
	Charts []MetricChartSpec `json:"charts" maxItems:"24"`
}

// AppMetricChartResult is one chart read over a range, pinned or not.
type AppMetricChartResult struct {
	Range       AppMetricsRange `json:"range"`
	From        time.Time       `json:"from"`
	StepSeconds int64           `json:"step_seconds"`
	Chart       AppMetricChart  `json:"chart"`
}

// MetricCharts are a service's pinned charts, in order.
type MetricCharts struct {
	Charts []MetricChartSpec `json:"charts"`
}
