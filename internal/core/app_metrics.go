package core

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// App metrics (ADR 0075, phase 6): what a service's apps send over OTLP,
// read from the trace store, on its Metrics tab as a dashboard of pinned
// charts. A service that pinned nothing has Dock's default charts: those
// of the runtime metrics a stock SDK sends (defaultCharts) that it sends.
// Pinning is a service setting (deploy scope), saved whole: the first
// change saves the defaults it changed.

// defaultCharts are Dock's charts of runtime metrics, from OpenTelemetry's
// semantic conventions, in the order they are shown: Node, the JVM, Go,
// Python, and a database pool's connections by state.
// http.server.request.duration isn't one: Requests counts every request
// at the edge.
var defaultCharts = []apitypes.MetricChartSpec{
	{Name: "v8js.memory.heap.used"},
	{Name: "nodejs.eventloop.delay.p99", Agg: string(analytics.AggMax)},
	{Name: "v8js.gc.duration"},
	{Name: "jvm.memory.used"},
	{Name: "jvm.gc.duration"},
	{Name: "go.memory.used"},
	{Name: "go.goroutine.count", Agg: string(analytics.AggMax)},
	{Name: "process.runtime.cpython.memory"},
	{Name: "db.client.connection.count", By: "db.client.connection.state"},
}

// appMetricRanges are each range's span and step: 60 to 84 points a chart.
var appMetricRanges = map[apitypes.AppMetricsRange]struct{ span, step time.Duration }{
	apitypes.AppMetrics1h:  {time.Hour, time.Minute},
	apitypes.AppMetrics24h: {24 * time.Hour, 20 * time.Minute},
	apitypes.AppMetrics7d:  {7 * 24 * time.Hour, 2 * time.Hour},
	apitypes.AppMetrics30d: {30 * 24 * time.Hour, 12 * time.Hour},
}

// appMetricsWindow is rng's span and step, and its end: the next whole
// step after now, so steps stay put while a page reloads.
func appMetricsWindow(rng apitypes.AppMetricsRange) (from, to time.Time, step time.Duration, err error) {
	r, ok := appMetricRanges[rng]
	if !ok {
		return from, to, 0, invalid("range %q: use 1h, 24h, 7d or 30d", rng)
	}
	to = now().Truncate(r.step).Add(r.step)
	return to.Add(-r.span), to, r.step, nil
}

// AppMetrics reads a service's pinned charts (or Dock's defaults) over
// rng, and lists the metrics it sent then: three trace store requests,
// whatever the charts.
func (c *Core) AppMetrics(ctx context.Context, a Actor, ref ServiceRef, rng apitypes.AppMetricsRange) (apitypes.AppMetrics, error) {
	if rng == "" {
		rng = apitypes.AppMetrics24h
	}
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "reading app metrics")
	if err != nil {
		return apitypes.AppMetrics{}, err
	}
	from, to, step, err := appMetricsWindow(rng)
	if err != nil {
		return apitypes.AppMetrics{}, err
	}
	out := apitypes.AppMetrics{
		Range: rng, From: from, StepSeconds: int64(step.Seconds()), Charts: []apitypes.AppMetricChart{}, Metrics: []apitypes.AppMetric{},
		MaxSeries: c.cfg.Traces.MetricSeries, MaxNames: c.cfg.Traces.MetricNames,
	}
	specs, saved, err := c.metricCharts(ctx, ss.svc.ID)
	if err != nil {
		return out, err
	}
	out.Saved = saved
	cl, _ := c.tracesClient()
	if cl == nil {
		out.TracingOff = true
		return out, nil
	}
	mask, err := c.liveMask(ctx, a, ss)
	if err != nil {
		return out, err
	}
	out.MetricsElsewhere = mask.elsewhere
	names, err := cl.MetricNames(ctx, analytics.MetricNamesQuery{Org: ss.project.OrgID, Services: []int64{ss.svc.ID}, From: from.Unix(), To: to.Unix()})
	if err != nil {
		return out, traceStoreErr(err)
	}
	sent := map[string]bool{}
	for _, m := range names.Metrics {
		sent[m.Name] = m.Series > 0
		out.Metrics = append(out.Metrics, apitypes.AppMetric{
			Name: m.Name, Unit: m.Unit, Kind: metricKind(m.Kind), Series: m.Series, Cut: m.Cut, CutBy: m.CutBy,
		})
	}
	if !saved {
		// Dock's defaults: only those of metrics the app sends.
		specs = slices.DeleteFunc(specs, func(s apitypes.MetricChartSpec) bool { return !sent[s.Name] })
	}
	if len(specs) == 0 {
		return out, nil
	}
	charts, err := c.queryCharts(ctx, cl, ss, specs, from, to, step, mask.mask)
	if err != nil {
		return out, err
	}
	out.Charts = charts
	return out, nil
}

// AppMetricChart reads one chart of a service over rng, pinned or not:
// the CLI's, the add-a-chart preview's and agents'.
func (c *Core) AppMetricChart(ctx context.Context, a Actor, ref ServiceRef, spec apitypes.MetricChartSpec, rng apitypes.AppMetricsRange) (apitypes.AppMetricChartResult, error) {
	if rng == "" {
		rng = apitypes.AppMetrics24h
	}
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "reading app metrics")
	if err != nil {
		return apitypes.AppMetricChartResult{}, err
	}
	if err := checkChartSpecs([]apitypes.MetricChartSpec{spec}); err != nil {
		return apitypes.AppMetricChartResult{}, err
	}
	from, to, step, err := appMetricsWindow(rng)
	if err != nil {
		return apitypes.AppMetricChartResult{}, err
	}
	cl, err := c.traceReader()
	if err != nil {
		return apitypes.AppMetricChartResult{}, err
	}
	live, err := c.liveMask(ctx, a, ss)
	if err != nil {
		return apitypes.AppMetricChartResult{}, err
	}
	charts, err := c.queryCharts(ctx, cl, ss, []apitypes.MetricChartSpec{spec}, from, to, step, live.mask)
	if err != nil {
		return apitypes.AppMetricChartResult{}, err
	}
	return apitypes.AppMetricChartResult{Range: rng, From: from, StepSeconds: int64(step.Seconds()), Chart: charts[0]}, nil
}

// SetMetricCharts pins a service's charts, in order, replacing what was
// pinned: a service setting, audited.
func (c *Core) SetMetricCharts(ctx context.Context, a Actor, ref ServiceRef, req apitypes.SetMetricChartsRequest) (apitypes.MetricCharts, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "pinning charts")
	if err != nil {
		return apitypes.MetricCharts{}, err
	}
	if len(req.Charts) > analytics.MaxCharts {
		return apitypes.MetricCharts{}, invalid("a service pins at most %d charts; unpin some first", analytics.MaxCharts)
	}
	if err := checkChartSpecs(req.Charts); err != nil {
		return apitypes.MetricCharts{}, err
	}
	charts := req.Charts
	if charts == nil {
		charts = []apitypes.MetricChartSpec{}
	}
	before, _, err := c.metricCharts(ctx, ss.svc.ID)
	if err != nil {
		return apitypes.MetricCharts{}, err
	}
	raw, err := json.Marshal(charts)
	if err != nil {
		return apitypes.MetricCharts{}, err
	}
	if err := c.store.SetMetricDashboard(ctx, store.SetMetricDashboardParams{
		ServiceID: ss.svc.ID, Charts: string(raw), UpdatedBy: a.Name, UpdatedAt: now(),
	}); err != nil {
		return apitypes.MetricCharts{}, err
	}
	c.audit(ctx, a, "service.charts", &ss.project, &ss.env, ss.String(), chartsChange(before, charts))
	return apitypes.MetricCharts{Charts: charts}, nil
}

// checkChartSpecs refuses a chart without a metric or with an
// aggregation Dock doesn't have.
func checkChartSpecs(specs []apitypes.MetricChartSpec) error {
	for _, s := range specs {
		switch {
		case strings.TrimSpace(s.Name) == "" || len(s.Name) > 256:
			return invalid("a chart names the metric it shows (at most 256 bytes)")
		case len(s.By) > 128:
			return invalid("a chart's attribute is at most 128 bytes")
		case s.Agg != "" && !slices.Contains([]analytics.MetricAgg{analytics.AggSum, analytics.AggMax, analytics.AggMin, analytics.AggAvg,
			analytics.AggRate, analytics.AggP50, analytics.AggP95, analytics.AggP99}, analytics.MetricAgg(s.Agg)):
			return invalid("aggregation %q: use sum, max, min, avg, rate, p50, p95 or p99", s.Agg)
		}
	}
	return nil
}

// chartsChange is what an audit event says a pin changed: the charts
// pinned and unpinned, by name.
func chartsChange(before, after []apitypes.MetricChartSpec) string {
	label := func(s apitypes.MetricChartSpec) string {
		out := s.Name
		if s.Agg != "" {
			out += " " + s.Agg
		}
		if s.By != "" {
			out += " by " + s.By
		}
		return out
	}
	var pinned, unpinned []string
	for _, s := range after {
		if !slices.Contains(before, s) {
			pinned = append(pinned, label(s))
		}
	}
	for _, s := range before {
		if !slices.Contains(after, s) {
			unpinned = append(unpinned, label(s))
		}
	}
	var parts []string
	if len(pinned) > 0 {
		parts = append(parts, "pinned "+strings.Join(pinned, ", "))
	}
	if len(unpinned) > 0 {
		parts = append(parts, "unpinned "+strings.Join(unpinned, ", "))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("reordered %d charts", len(after))
	}
	return strings.Join(parts, "; ")
}

// metricCharts are a service's pinned charts, and whether someone saved
// them; without, Dock's defaults.
func (c *Core) metricCharts(ctx context.Context, serviceID int64) ([]apitypes.MetricChartSpec, bool, error) {
	row, err := c.store.GetMetricDashboard(ctx, serviceID)
	if store.IsNotFound(err) {
		return slices.Clone(defaultCharts), false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var specs []apitypes.MetricChartSpec
	if err := json.Unmarshal([]byte(row.Charts), &specs); err != nil {
		return nil, false, fmt.Errorf("service %d's pinned charts: %w", serviceID, err)
	}
	return specs, true, nil
}

// liveCtx is what reads of a service's app metrics need of its live
// release: the mask of its secret values for the actor, and the variable
// that sends its metrics elsewhere.
type liveCtx struct {
	mask      traceMask
	elsewhere string
}

// liveMask reads the service's live release, if it has one.
func (c *Core) liveMask(ctx context.Context, a Actor, ss svcScope) (liveCtx, error) {
	rel, err := c.store.GetActiveRelease(ctx, ss.svc.ID)
	if store.IsNotFound(err) {
		return liveCtx{}, nil
	}
	if err != nil {
		return liveCtx{}, err
	}
	r, err := c.logRedactor(a, ss.envScope, rel)
	if err != nil {
		return liveCtx{}, err
	}
	out := liveCtx{mask: traceMask{r}}
	if cfg, err := c.openConfig(rel); err == nil {
		out.elsewhere = metricsElsewhere(cfg.Env)
	}
	return out, nil
}

// metricsElsewhere is the variable a service set that keeps Dock from
// pointing its metrics at Dock, "" when it doesn't.
func metricsElsewhere(env map[string]string) string {
	if k := exportsElsewhere(env); k != "" {
		return k
	}
	var keys []string
	for k := range env {
		if strings.HasPrefix(k, "OTEL_EXPORTER_OTLP_") || k == "OTEL_METRICS_EXPORTER" {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

// queryCharts reads specs over [from, to) in steps, in one trace store
// request, masking attribute values with mask.
func (c *Core) queryCharts(ctx context.Context, cl *analytics.Client, ss svcScope, specs []apitypes.MetricChartSpec, from, to time.Time, step time.Duration, mask traceMask) ([]apitypes.AppMetricChart, error) {
	q := analytics.MetricsQuery{Org: ss.project.OrgID, Service: ss.svc.ID, From: from.Unix(), To: to.Unix(), Step: int64(step.Seconds())}
	for _, s := range specs {
		q.Charts = append(q.Charts, analytics.MetricChartQuery{Name: s.Name, Agg: analytics.MetricAgg(s.Agg), By: s.By})
	}
	res, err := cl.QueryMetrics(ctx, q)
	if err != nil {
		return nil, traceStoreErr(err)
	}
	out := make([]apitypes.AppMetricChart, 0, len(res.Charts))
	for i, ch := range res.Charts {
		spec := specs[i]
		chart := apitypes.AppMetricChart{
			MetricChartSpec: spec, Dock: slices.Contains(defaultCharts, spec), Unit: ch.Unit, Kind: metricKind(ch.Kind), Shape: string(ch.Shape),
			Aggs: []string{}, Series: ch.Series, Lines: make([]apitypes.AppMetricLine, 0, len(ch.Lines)), More: ch.More,
			Attrs: make([]apitypes.AppMetricAttr, 0, len(ch.Attrs)),
		}
		if ch.Shape != "" {
			for _, a := range analytics.Aggs(ch.Shape) {
				chart.Aggs = append(chart.Aggs, string(a))
			}
		}
		for _, l := range ch.Lines {
			chart.Lines = append(chart.Lines, apitypes.AppMetricLine{Label: mask.mask(l.Label), Values: l.Values})
		}
		for _, a := range ch.Attrs {
			chart.Attrs = append(chart.Attrs, apitypes.AppMetricAttr{Key: a.Key, Values: a.Values})
		}
		out = append(out, chart)
	}
	return out, nil
}

// metricKind is a metric kind's name.
func metricKind(k analytics.MetricKind) string {
	switch k {
	case analytics.MetricGauge:
		return "gauge"
	case analytics.MetricSum:
		return "sum"
	case analytics.MetricHistogram:
		return "histogram"
	}
	return ""
}
