package cli

import (
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// newMetricsCmd is `dock metrics`: a service's app metrics over OTLP
// (ADR 0075, phase 6), its pinned charts' latest values and what else it
// sends; `show` reads one metric, `pin` and `unpin` change the charts.
func newMetricsCmd(g *globals) *cobra.Command {
	var f targetFlags
	var rng string
	cmd := &cobra.Command{
		Use:   "metrics",
		Short: "A service's app metrics: its pinned charts and the metrics its apps send over OTLP",
		Long: `The metrics a service's apps send with a stock OpenTelemetry SDK once an
operator turns tracing on (` + "`dock tracing on`" + `): its pinned charts, each
one's latest value and range, then every metric it sent, with the points
cut past the series caps (2,000 series and 200 metrics a service a day).
Until someone pins charts, the pinned ones are Dock's charts of the
runtime metrics the app sends.`,
		Example: "  dock metrics\n  dock metrics show http.server.request.duration --agg p99 --by http.route\n  dock metrics pin queue.depth --agg max --by queue",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			m, err := get[apitypes.AppMetrics](cmd.Context(), s, t.servicePath()+"/app-metrics?"+url.Values{"range": {rng}}.Encode())
			if err != nil {
				return err
			}
			return g.io.Print(m, func() error {
				switch {
				case m.TracingOff:
					_, _ = fmt.Fprintln(g.io.Out, "Tracing is off, so Dock keeps no app metrics; an operator turns it on with `dock tracing on`.")
					return nil
				case m.MetricsElsewhere != "":
					_, _ = fmt.Fprintf(g.io.Out, "%s sets %s, so its apps send their metrics there, not to Dock.\n\n", t, m.MetricsElsewhere)
				}
				if len(m.Metrics) == 0 {
					g.io.Successf("No app metrics from %s in the last %s.", t, m.Range)
					return nil
				}
				pinned := "Pinned charts"
				if !m.Saved {
					pinned = "Dock's charts (nothing pinned yet)"
				}
				_, _ = fmt.Fprintf(g.io.Out, "%s, last %s:\n", pinned, m.Range)
				tb := g.io.Table("chart", "now", "min", "max", "lines")
				var broken []apitypes.AppMetricChart
				for _, c := range m.Charts {
					now, lo, hi := lineStats(c)
					tb.Row(chartName(c.MetricChartSpec, c.Agg), metricValue(now, c), metricValue(lo, c), metricValue(hi, c), strconv.Itoa(len(c.Lines)+c.More))
					if c.Error != "" {
						broken = append(broken, c)
					}
				}
				if err := tb.Flush(); err != nil {
					return err
				}
				for _, c := range broken {
					_, _ = fmt.Fprintf(g.io.Out, "\n%s can't be read: %s. Unpin it with `dock metrics unpin %s%s`.\n", chartName(c.MetricChartSpec, c.Agg), c.Error, c.Name, chartFlags(c.MetricChartSpec))
				}
				_, _ = fmt.Fprintf(g.io.Out, "\nMetrics sent, last %s:\n", m.Range)
				tb = g.io.Table("metric", "kind", "unit", "series", "cut")
				var cut []apitypes.AppMetric
				for _, x := range m.Metrics {
					tb.Row(x.Name, dash(x.Kind), dash(x.Unit), strconv.Itoa(x.Series), cutText(x.Cut))
					if x.Cut > 0 {
						cut = append(cut, x)
					}
				}
				if err := tb.Flush(); err != nil {
					return err
				}
				for _, x := range cut {
					why := fmt.Sprintf("\n%s: %d points of new series were cut past the service's caps (%d series, %d metrics a day).", x.Name, x.Cut, m.MaxSeries, m.MaxNames)
					if x.CutBy != "" {
						why += fmt.Sprintf(" Its attribute %s has the most values; drop it from the metric.", x.CutBy)
					}
					_, _ = fmt.Fprintln(g.io.Out, why)
				}
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().StringVar(&rng, "range", "24h", "1h, 24h, 7d or 30d")
	cmd.AddCommand(newMetricShowCmd(g), newMetricPinCmd(g, true), newMetricPinCmd(g, false))
	return cmd
}

// newMetricShowCmd is `dock metrics show`: one metric, a line per value
// of an attribute, with its latest value and range.
func newMetricShowCmd(g *globals) *cobra.Command {
	var f targetFlags
	var spec apitypes.MetricChartSpec
	var rng string
	cmd := &cobra.Command{
		Use:   "show <metric>",
		Short: "Show one app metric: a line per value of an attribute, with its latest value and range",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			spec.Name = args[0]
			v := url.Values{"name": {spec.Name}, "range": {rng}}
			if spec.Agg != "" {
				v.Set("agg", spec.Agg)
			}
			if spec.By != "" {
				v.Set("by", spec.By)
			}
			r, err := get[apitypes.AppMetricChartResult](cmd.Context(), s, t.servicePath()+"/app-metrics/chart?"+v.Encode())
			if err != nil {
				return err
			}
			c := r.Chart
			return g.io.Print(r, func() error {
				if len(c.Lines) == 0 {
					g.io.Successf("No points of %s from %s in the last %s.", spec.Name, t, r.Range)
					return nil
				}
				_, _ = fmt.Fprintf(g.io.Out, "%s, a %s (%s), %s of %s, last %s:\n", spec.Name, dash(c.Shape), dash(c.Unit), strconv.Itoa(c.Series)+" series", dash(chartAgg(c)), r.Range)
				label := "line"
				if spec.By != "" {
					label = spec.By
				}
				tb := g.io.Table(label, "now", "min", "max")
				for _, l := range c.Lines {
					now, lo, hi := valuesStats(l.Values)
					tb.Row(dash(l.Label), metricValue(now, c), metricValue(lo, c), metricValue(hi, c))
				}
				if err := tb.Flush(); err != nil {
					return err
				}
				if c.More > 0 {
					_, _ = fmt.Fprintf(g.io.Out, "\n%d more lines; the 20 biggest are shown.\n", c.More)
				}
				if len(c.Attrs) > 0 && spec.By == "" {
					keys := make([]string, len(c.Attrs))
					for i, a := range c.Attrs {
						keys[i] = fmt.Sprintf("%s (%d)", a.Key, a.Values)
					}
					_, _ = fmt.Fprintf(g.io.Out, "\nSplit it by one of its attributes with --by: %s.\n", strings.Join(keys, ", "))
				}
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().StringVar(&spec.Agg, "agg", "", "sum, max, min or avg for levels; rate or sum for counters; p50, p95, p99, avg or rate for histograms (default: the metric's own)")
	cmd.Flags().StringVar(&spec.By, "by", "", "an attribute that splits the metric into lines")
	cmd.Flags().StringVar(&rng, "range", "24h", "1h, 24h, 7d or 30d")
	return cmd
}

// newMetricPinCmd is `dock metrics pin` (or unpin): a chart on the
// service's dashboard, a service setting.
func newMetricPinCmd(g *globals, pin bool) *cobra.Command {
	var f targetFlags
	var spec apitypes.MetricChartSpec
	use, short := "pin <metric>", "Pin a chart of an app metric to the service's Metrics tab"
	if !pin {
		use, short = "unpin <metric>", "Unpin a chart from the service's Metrics tab"
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			spec.Name = args[0]
			// The whole dashboard is saved: the charts shown now (Dock's,
			// until someone pinned charts), changed.
			m, err := get[apitypes.AppMetrics](cmd.Context(), s, t.servicePath()+"/app-metrics?range=1h")
			if err != nil {
				return err
			}
			charts := make([]apitypes.MetricChartSpec, 0, len(m.Charts)+1)
			for _, c := range m.Charts {
				charts = append(charts, c.MetricChartSpec)
			}
			switch {
			case pin && slices.Contains(charts, spec):
				g.io.Successf("%s is pinned already.", chartName(spec, spec.Agg))
				return nil
			case pin:
				charts = append(charts, spec)
			default:
				i := slices.IndexFunc(charts, func(c apitypes.MetricChartSpec) bool {
					return c.Name == spec.Name && (spec.Agg == "" || c.Agg == spec.Agg) && (spec.By == "" || c.By == spec.By)
				})
				if i < 0 {
					return fmt.Errorf("no chart of %s is pinned to %s; `dock metrics` lists them", spec.Name, t)
				}
				charts = slices.Delete(charts, i, i+1)
			}
			out, err := send[apitypes.MetricCharts](cmd.Context(), s, http.MethodPut, t.servicePath()+"/app-metrics/charts", apitypes.SetMetricChartsRequest{Charts: charts})
			if err != nil {
				return err
			}
			return g.io.Print(out, func() error {
				verb := "Pinned"
				if !pin {
					verb = "Unpinned"
				}
				g.io.Successf("%s %s on %s: %s.", verb, chartName(spec, spec.Agg), t, plural(len(out.Charts), "chart"))
				if pin && !slices.ContainsFunc(m.Metrics, func(x apitypes.AppMetric) bool { return x.Name == spec.Name }) {
					g.io.Warnf("%s sent no %s in the last hour; the chart stays empty until it does (`dock metrics` lists what it sends).", t, spec.Name)
				}
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().StringVar(&spec.Agg, "agg", "", "sum, max, min, avg, rate, p50, p95 or p99, as the metric takes (dock metrics show --help); empty is its default")
	cmd.Flags().StringVar(&spec.By, "by", "", "an attribute that splits the chart into lines")
	return cmd
}

// chartName is how a chart is named: "queue.depth max by queue".
func chartName(s apitypes.MetricChartSpec, agg string) string {
	out := s.Name
	if agg != "" {
		out += " " + agg
	}
	if s.By != "" {
		out += " by " + s.By
	}
	return out
}

// chartFlags are the flags that name a chart's aggregation and
// attribute in a command: " --agg p99 --by route".
func chartFlags(s apitypes.MetricChartSpec) string {
	out := ""
	if s.Agg != "" {
		out += " --agg " + s.Agg
	}
	if s.By != "" {
		out += " --by " + s.By
	}
	return out
}

// chartAgg is the aggregation a chart was read with: its own, or its
// metric's default (the first of Aggs).
func chartAgg(c apitypes.AppMetricChart) string {
	if c.Agg == "" && len(c.Aggs) > 0 {
		return c.Aggs[0]
	}
	return c.Agg
}

// lineStats are a chart's latest value and its lowest and highest of any
// line. With several lines, the latest is their sum where lines add up
// (a sum or a rate), the lowest line's for min, and the highest line's
// otherwise: p95s or averages summed are no latency or level.
func lineStats(c apitypes.AppMetricChart) (now, lo, hi *float64) {
	agg := chartAgg(c)
	for _, l := range c.Lines {
		n, a, b := valuesStats(l.Values)
		switch {
		case n == nil:
		case now == nil:
			v := *n
			now = &v
		case agg == "sum" || agg == "rate":
			*now += *n
		case agg == "min":
			*now = min(*now, *n)
		default:
			*now = max(*now, *n)
		}
		if a != nil && (lo == nil || *a < *lo) {
			lo = a
		}
		if b != nil && (hi == nil || *b > *hi) {
			hi = b
		}
	}
	return now, lo, hi
}

// valuesStats are a line's latest value and its lowest and highest.
func valuesStats(vs []*float64) (now, lo, hi *float64) {
	for _, v := range vs {
		if v == nil {
			continue
		}
		now = v
		if lo == nil || *v < *lo {
			lo = v
		}
		if hi == nil || *v > *hi {
			hi = v
		}
	}
	return now, lo, hi
}

// metricValue formats v in its chart's unit: bytes, durations, a
// counter's rate a minute, percent for ratios ("1").
func metricValue(v *float64, c apitypes.AppMetricChart) string {
	if v == nil {
		return "-"
	}
	x := *v
	if chartAgg(c) == "rate" {
		return compact(x) + "/min"
	}
	switch c.Unit {
	case "By":
		return bytesize(int64(math.Round(x)))
	case "s":
		return msString(x * 1000)
	case "ms":
		return msString(x)
	case "us":
		return msString(x / 1000)
	case "ns":
		return msString(x / 1e6)
	case "1":
		return compact(x*100) + "%"
	}
	if u := strings.Trim(c.Unit, "{}"); u != "" {
		return compact(x) + " " + u
	}
	return compact(x)
}

// compact is a number with three significant digits, thousands as k
// and millions as M.
func compact(x float64) string {
	switch a := math.Abs(x); {
	case a >= 1e6:
		return strconv.FormatFloat(x/1e6, 'f', 1, 64) + "M"
	case a >= 1e4:
		return strconv.FormatFloat(x/1e3, 'f', 1, 64) + "k"
	case a >= 100 || a == 0:
		return strconv.FormatFloat(x, 'f', 0, 64)
	}
	return strconv.FormatFloat(x, 'g', 3, 64)
}

// cutText is a metric's cut points, or nothing.
func cutText(n int64) string {
	if n == 0 {
		return ""
	}
	return strconv.FormatInt(n, 10) + " points"
}
