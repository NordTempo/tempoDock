package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/mcpkit"
)

// Traces (tracing over OTLP), for agents: read only, with read
// permission. "The slowest traces since release N" is one traces_list
// call, and trace_get shows where one spent its time and failed.

// maxAgentSpans caps the spans trace_get returns: enough for a request's
// story, small enough for a context window.
const maxAgentSpans = 300

// addTraceTools registers traces_list and trace_get.
func (h *handler) addTraceTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "traces_list", "List a service's traces",
		"A service's traces (entry spans: route, status, duration, release), newest or slowest first. Then trace_get for one. Needs tracing on.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Range        string `json:"range,omitempty" enum:"1h,24h,7d" doc:"Default 24h."`
			SinceRelease int64  `json:"since_release,omitempty" doc:"This release and later."`
			Release      int64  `json:"release,omitempty"`
			Route        string `json:"route,omitempty" doc:"e.g. /orders/{id}."`
			Method       string `json:"method,omitempty"`
			Errors       bool   `json:"errors,omitempty"`
			SlowerThanMS int64  `json:"slower_than_ms,omitempty"`
			Slowest      bool   `json:"slowest,omitempty"`
			Limit        int    `json:"limit,omitempty" minimum:"0" maximum:"200" doc:"Default 20."`
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			l, err := h.core.ListTraces(ctx, c.Actor, ref, core.TraceFilter{
				Range: apitypes.TraceRange(in.Range), Release: in.Release, SinceRelease: in.SinceRelease, Route: in.Route, Method: in.Method,
				Errors: in.Errors, SlowerThan: time.Duration(in.SlowerThanMS) * time.Millisecond, Slowest: in.Slowest, Limit: cmpOr(in.Limit, 20),
			})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			if l.ExportsElsewhere != "" && len(l.Traces) == 0 {
				return mcpkit.Reply{Summary: fmt.Sprintf("%s sets %s, so it sends its traces there, not to Dock.", ref, l.ExportsElsewhere), Data: l}, nil
			}
			if len(l.Traces) == 0 {
				return mcpkit.Reply{Summary: fmt.Sprintf("No traces of %s match in the last %s.", ref, l.Range), Data: l}, nil
			}
			top := make([]string, 0, 3)
			for _, t := range l.Traces[:min(len(l.Traces), 3)] {
				top = append(top, fmt.Sprintf("%s %.0f ms (r%d, %s)", entryOf(t), t.DurationMS, t.Release, t.Kept))
			}
			sum := fmt.Sprintf("%d traces of %s in the last %s: %s.", len(l.Traces), ref, l.Range, list(top))
			if l.More {
				sum += " More match; narrow the filters."
			}
			return mcpkit.Reply{Summary: sum, Data: l}, nil
		})

	mcpkit.AddTool(s, "trace_get", "Show a trace",
		"One trace's spans across services, with where the time went and the first error.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Trace string `json:"trace"`
			At    string `json:"at,omitempty" doc:"Its start from traces_list, to find it fast."`
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			var at time.Time
			if in.At != "" {
				if at, err = time.Parse(time.RFC3339Nano, in.At); err != nil {
					return mcpkit.Reply{}, errf(apitypes.CodeValidation, "at %q: give an RFC 3339 time, like a traces_list start", in.At)
				}
			}
			tr, err := h.core.GetTrace(ctx, c.Actor, ref, in.Trace, at)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			if len(tr.Spans) > maxAgentSpans {
				tr.Spans, tr.Truncated = tr.Spans[:maxAgentSpans], true
			}
			sum := fmt.Sprintf("Trace %s: %.0f ms, %d spans.", tr.Trace, tr.DurationMS, len(tr.Spans))
			var slow, failed *apitypes.TraceSpan
			for i := range tr.Spans {
				sp := &tr.Spans[i]
				if sp.Hidden {
					continue
				}
				if failed == nil && (sp.Status == "error" || sp.HTTPStatus >= 500) && len(sp.Events) > 0 {
					failed = sp
				}
				// The slowest span without children doing the work: the
				// one that ends latest among the longest is enough here.
				if sp.Parent != "" && (slow == nil || sp.DurationMS > slow.DurationMS) {
					slow = sp
				}
			}
			if slow != nil {
				sum += fmt.Sprintf(" Slowest call: %s in %s (%.0f ms).", slow.Name, slow.Service, slow.DurationMS)
			}
			if failed != nil {
				e := failed.Events[0]
				sum += fmt.Sprintf(" Failed: %s in %s r%d: %v %v.", failed.Name, failed.Service, failed.Release, e.Attrs["exception.type"], e.Attrs["exception.message"])
			}
			return mcpkit.Reply{Summary: sum, Data: tr}, nil
		})
}

// entryOf names a trace's entry: method and route, or its span's name.
func entryOf(t apitypes.TraceSummary) string {
	if t.Route != "" {
		return t.Method + " " + t.Route
	}
	return t.Name
}

// cmpOr is v, or def when v is 0.
func cmpOr(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}
