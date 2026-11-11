package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// TracesQuery picks a service's traces.
type TracesQuery struct {
	Range        apitypes.TraceRange `query:"range" enum:"1h,24h,7d" default:"24h" doc:"How far back, ending now"`
	Release      int64               `query:"release" doc:"Only this release (number)"`
	SinceRelease int64               `query:"since_release" doc:"Only this release (number) and later ones"`
	Method       string              `query:"method" doc:"Only this HTTP method"`
	Route        string              `query:"route" doc:"Only this route, as the app names it (http.route), e.g. /orders/{id}"`
	Errors       bool                `query:"errors" doc:"Only traces whose entry span failed: an error status or a 5xx"`
	SlowerThanMS int64               `query:"slower_than_ms" doc:"Only traces at least this long"`
	Slowest      bool                `query:"slowest" doc:"Slowest first instead of newest first"`
	Limit        int                 `query:"limit" minimum:"0" maximum:"200" doc:"At most this many (0: 50)"`
}

// registerTraces registers reading traces through a service.
func registerTraces(s *Server, a huma.API) {
	tags := []string{"traces"}
	authed(s, a, huma.Operation{
		OperationID: "list-traces", Method: http.MethodGet, Path: servicePathT + "/traces",
		Summary: "List a service's traces", Tags: tags,
		Description: "The traces that entered the service over the range: each one's entry span (name, route, status, duration, release) and why Dock kept it. Needs tracing on (`dock tracing on`).",
		Errors:      []int{http.StatusConflict, http.StatusServiceUnavailable},
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		TracesQuery
	}) (*out[apitypes.TraceList], error) {
		q := in.TracesQuery
		return reply(s.core.ListTraces(ctx, actor, in.ref(), core.TraceFilter{
			Range: q.Range, Release: q.Release, SinceRelease: q.SinceRelease, Method: q.Method, Route: q.Route,
			Errors: q.Errors, SlowerThan: time.Duration(q.SlowerThanMS) * time.Millisecond, Slowest: q.Slowest, Limit: q.Limit,
		}))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-trace", Method: http.MethodGet, Path: servicePathT + "/traces/{trace}",
		Summary: "Show a trace", Tags: tags,
		Description: "Every span of a trace that went through the service, by start time, with attributes and events: across services too, hiding those the caller can't see. Secret values are masked as in logs.",
		Errors:      []int{http.StatusNotFound, http.StatusConflict, http.StatusServiceUnavailable},
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Trace string    `path:"trace" doc:"Trace ID, 32 hex digits"`
		At    time.Time `query:"at" doc:"A time in the trace (RFC 3339), which makes finding it quick; a trace list's start"`
	}) (*out[apitypes.Trace], error) {
		return reply(s.core.GetTrace(ctx, actor, in.ref(), in.Trace, in.At))
	})
}
