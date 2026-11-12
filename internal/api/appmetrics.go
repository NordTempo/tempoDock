package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// registerAppMetrics registers a service's app metrics (ADR 0075, phase
// 6): its dashboard of pinned charts, one chart, and pinning.
func registerAppMetrics(s *Server, a huma.API) {
	tags := []string{"app metrics"}
	authed(s, a, huma.Operation{
		OperationID: "get-app-metrics", Method: http.MethodGet, Path: servicePathT + "/app-metrics",
		Summary: "Read a service's app metrics", Tags: tags,
		Description: "The service's pinned charts over the range (Dock's default charts of the runtime metrics it sends, until someone pins charts), " +
			"and every metric its apps sent then over OTLP, with the points the series caps cut. Needs tracing on (`dock tracing on`); " +
			"without it, tracing_off. Attribute values are masked as in logs.",
		Errors: []int{http.StatusServiceUnavailable},
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Range apitypes.AppMetricsRange `query:"range" enum:"1h,24h,7d,30d" default:"24h" doc:"How far back, ending now; past 7 days from hourly points"`
	}) (*out[apitypes.AppMetrics], error) {
		return reply(s.core.AppMetrics(ctx, actor, in.ref(), in.Range))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-app-metric-chart", Method: http.MethodGet, Path: servicePathT + "/app-metrics/chart",
		Summary: "Read one app metric as a chart", Tags: tags,
		Description: "One metric of the service over the range, pinned or not: its points in steps, combined by agg (a level's sum, max, min or avg; " +
			"a counter's rate a minute or sum; a histogram's p50, p95, p99, avg or rate) and split into lines by an attribute (at most 20, by size).",
		Errors: []int{http.StatusConflict, http.StatusServiceUnavailable},
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Name  string                   `query:"name" required:"true" doc:"The metric, as the app names it: v8js.memory.heap.used"`
		Agg   string                   `query:"agg" enum:"sum,max,min,avg,rate,p50,p95,p99" doc:"How points combine; empty is the metric's default"`
		By    string                   `query:"by" doc:"The attribute that splits it into lines"`
		Range apitypes.AppMetricsRange `query:"range" enum:"1h,24h,7d,30d" default:"24h" doc:"How far back, ending now"`
	}) (*out[apitypes.AppMetricChartResult], error) {
		return reply(s.core.AppMetricChart(ctx, actor, in.ref(), apitypes.MetricChartSpec{Name: in.Name, Agg: in.Agg, By: in.By}, in.Range))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-app-metric-charts", Method: http.MethodPut, Path: servicePathT + "/app-metrics/charts",
		Summary: "Pin a service's charts", Tags: tags,
		Description: "The service's dashboard, in order, replacing what was pinned (at most 24 charts): a service setting. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.SetMetricChartsRequest
	}) (*out[apitypes.MetricCharts], error) {
		return reply(s.core.SetMetricCharts(ctx, actor, in.ref(), in.Body))
	})
}
