package mcp

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/mcpkit"
)

// App metrics over OTLP (ADR 0075, phase 6), for agents: read only. "Is
// api's memory growing?" is one metrics call: the pinned charts' latest,
// lowest and highest values; the same tool with name reads one metric
// split by an attribute. Values, not arrays of points: a chart's 60 to 84
// points a line would fill a context window to say what three numbers
// do. Pinning stays in the UI and CLI.

// metricsInput reads a service's dashboard, or with Name one metric.
type metricsInput struct {
	ServiceInput
	Name  string `json:"name,omitempty" doc:"One metric, as the app names it: its lines instead of the dashboard."`
	Agg   string `json:"agg,omitempty" enum:"sum,max,min,avg,rate,p50,p95,p99" doc:"With name; default the metric's own."`
	By    string `json:"by,omitempty" doc:"With name: an attribute to split it by."`
	Range string `json:"range,omitempty" enum:"1h,24h,7d,30d" doc:"Default 1h."`
}

// metricLine is a line's latest, lowest and highest values, and when
// the highest was.
type metricLine struct {
	Label string    `json:"label,omitempty"`
	Now   *float64  `json:"now,omitempty"`
	Min   *float64  `json:"min,omitempty"`
	Max   *float64  `json:"max,omitempty"`
	MaxAt time.Time `json:"max_at,omitzero"`
}

// metricChart is a chart for an agent: its lines' values, not points.
type metricChart struct {
	Name  string       `json:"name"`
	Agg   string       `json:"agg,omitempty"`
	By    string       `json:"by,omitempty"`
	Unit  string       `json:"unit,omitempty"`
	Shape string       `json:"shape,omitempty"`
	Lines []metricLine `json:"lines"`
	More  int          `json:"more,omitempty"`
	Attrs []string     `json:"attrs,omitempty"`
	Aggs  []string     `json:"aggs,omitempty"`
}

// addMetricTools registers the metrics tool.
func (h *handler) addMetricTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "metrics", "A service's app metrics",
		"A service's app metrics over OTLP: the metric list and pinned charts' latest, min and max; with name, one metric split by an attribute. Needs tracing on.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in metricsInput) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			rng := apitypes.AppMetricsRange(in.Range)
			if rng == "" {
				rng = apitypes.AppMetrics1h
			}
			if in.Name != "" {
				return h.metricChart(ctx, c, ref, in, rng)
			}
			return h.metricsDashboard(ctx, c, ref, rng)
		})
}

// metricsDashboard is the service's pinned charts' values and the
// metrics it sent.
func (h *handler) metricsDashboard(ctx context.Context, c *call, ref core.ServiceRef, rng apitypes.AppMetricsRange) (mcpkit.Reply, error) {
	m, err := h.core.AppMetrics(ctx, c.Actor, ref, rng)
	if err != nil {
		return mcpkit.Reply{}, err
	}
	data := struct {
		Range            apitypes.AppMetricsRange `json:"range"`
		Charts           []metricChart            `json:"charts"`
		Metrics          []apitypes.AppMetric     `json:"metrics"`
		TracingOff       bool                     `json:"tracing_off,omitempty"`
		MetricsElsewhere string                   `json:"metrics_elsewhere,omitempty"`
	}{Range: m.Range, Charts: []metricChart{}, Metrics: m.Metrics, TracingOff: m.TracingOff, MetricsElsewhere: m.MetricsElsewhere}
	switch {
	case m.TracingOff:
		return mcpkit.Reply{Summary: "Tracing is off, so Dock keeps no app metrics; an operator turns it on with `dock tracing on`.", Data: data}, nil
	case m.MetricsElsewhere != "" && len(m.Metrics) == 0:
		return mcpkit.Reply{Summary: fmt.Sprintf("%s sets %s, so it sends its metrics there, not to Dock.", ref, m.MetricsElsewhere), Data: data}, nil
	case len(m.Metrics) == 0:
		return mcpkit.Reply{Summary: fmt.Sprintf("No app metrics from %s in the last %s.", ref, m.Range), Data: data}, nil
	}
	from := m.From
	step := time.Duration(m.StepSeconds) * time.Second
	var parts []string
	for _, ch := range m.Charts {
		mc := chartOf(ch, from, step)
		data.Charts = append(data.Charts, mc)
		if len(parts) < 4 {
			parts = append(parts, chartSummary(mc))
		}
	}
	sum := fmt.Sprintf("%s, last %s: %s.", ref, m.Range, strings.Join(parts, "; "))
	if len(parts) == 0 {
		sum = fmt.Sprintf("%s pins no charts.", ref)
	}
	sum += fmt.Sprintf(" %d metrics sent; name one to read it.", len(m.Metrics))
	for _, x := range m.Metrics {
		if x.Cut > 0 {
			sum += fmt.Sprintf(" %s: %d points of new series cut past %d series a day", x.Name, x.Cut, m.MaxSeries)
			if x.CutBy != "" {
				sum += fmt.Sprintf("; drop its attribute %s", x.CutBy)
			}
			sum += "."
		}
	}
	return mcpkit.Reply{Summary: sum, Data: data}, nil
}

// metricChart is one metric's lines.
func (h *handler) metricChart(ctx context.Context, c *call, ref core.ServiceRef, in metricsInput, rng apitypes.AppMetricsRange) (mcpkit.Reply, error) {
	r, err := h.core.AppMetricChart(ctx, c.Actor, ref, apitypes.MetricChartSpec{Name: in.Name, Agg: in.Agg, By: in.By}, rng)
	if err != nil {
		return mcpkit.Reply{}, err
	}
	mc := chartOf(r.Chart, r.From, time.Duration(r.StepSeconds)*time.Second)
	if len(mc.Lines) == 0 {
		return mcpkit.Reply{Summary: fmt.Sprintf("No points of %s from %s in the last %s.", in.Name, ref, r.Range), Data: mc}, nil
	}
	sum := fmt.Sprintf("%s, last %s: %s", ref, r.Range, chartSummary(mc))
	if n := len(mc.Lines) + mc.More; n > 1 {
		sum += fmt.Sprintf("; %d lines", n)
	}
	return mcpkit.Reply{Summary: sum + ".", Data: mc}, nil
}

// chartOf reads a chart's lines into values.
func chartOf(ch apitypes.AppMetricChart, from time.Time, step time.Duration) metricChart {
	agg := ch.Agg
	if agg == "" && len(ch.Aggs) > 0 {
		agg = ch.Aggs[0]
	}
	mc := metricChart{Name: ch.Name, Agg: agg, By: ch.By, Unit: ch.Unit, Shape: ch.Shape, Lines: []metricLine{}, More: ch.More, Aggs: ch.Aggs}
	for _, a := range ch.Attrs {
		mc.Attrs = append(mc.Attrs, a.Key)
	}
	for _, l := range ch.Lines {
		ml := metricLine{Label: l.Label}
		for i, v := range l.Values {
			if v == nil {
				continue
			}
			ml.Now = v
			if ml.Min == nil || *v < *ml.Min {
				ml.Min = v
			}
			if ml.Max == nil || *v > *ml.Max {
				ml.Max, ml.MaxAt = v, from.Add(time.Duration(i)*step)
			}
		}
		if ml.Now != nil {
			mc.Lines = append(mc.Lines, ml)
		}
	}
	return mc
}

// chartSummary says a chart's top line: "v8js.memory.heap.used 212 MB
// now (min 120 MB, max 212 MB at 14:05)".
func chartSummary(mc metricChart) string {
	name := mc.Name
	if mc.Agg != "" {
		name += " " + mc.Agg
	}
	if len(mc.Lines) == 0 {
		return name + ": no points"
	}
	l := mc.Lines[0]
	if l.Label != "" {
		name += " " + mc.By + "=" + l.Label
	}
	f := func(v *float64) string { return metricValue(*v, mc.Unit, mc.Agg) }
	return fmt.Sprintf("%s %s now (min %s, max %s at %s UTC)", name, f(l.Now), f(l.Min), f(l.Max), l.MaxAt.UTC().Format("Jan 2 15:04"))
}

// metricValue is v in its unit, as the UI shows it: bytes, durations, a
// rate a minute, percent for ratios, else a number with the unit's word.
func metricValue(v float64, unit, agg string) string {
	num := func(x float64) string {
		if math.Abs(x) >= 100 || x == math.Trunc(x) {
			return strconv.FormatFloat(x, 'f', 0, 64)
		}
		return strconv.FormatFloat(x, 'g', 3, 64)
	}
	if agg == "rate" {
		return num(v) + "/min"
	}
	switch unit {
	case "By":
		return num(v/(1<<20)) + " MB"
	case "s":
		return num(v*1000) + " ms"
	case "ms":
		return num(v) + " ms"
	case "us":
		return num(v/1000) + " ms"
	case "ns":
		return num(v/1e6) + " ms"
	case "1":
		return num(v*100) + "%"
	}
	if w := strings.Trim(unit, "{}"); w != "" {
		return num(v) + " " + w
	}
	return num(v)
}
