package mcp

import (
	"cmp"
	"context"
	"fmt"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/mcpkit"
)

// Traces (tracing over OTLP), for agents: read only, with read
// permission. "The slowest traces since release N" is one traces call,
// and the same tool with trace shows where one spent its time and failed.

// maxAgentSpans caps the spans one trace returns: enough for a request's
// story, small enough for a context window.
const maxAgentSpans = 300

// tracesInput lists a service's traces, or with Trace reads one. One tool
// rather than two: every tool costs an agent's context on every turn.
type tracesInput struct {
	ServiceInput
	Trace        string `json:"trace,omitempty" doc:"One trace's ID: its spans instead of the list."`
	At           string `json:"at,omitempty" doc:"With trace: its start from the list, to find it fast."`
	Range        string `json:"range,omitempty" enum:"1h,24h,7d" doc:"Default 24h."`
	SinceRelease int64  `json:"since_release,omitempty" doc:"This release and later."`
	Release      int64  `json:"release,omitempty"`
	Route        string `json:"route,omitempty" doc:"e.g. /orders/{id}."`
	Method       string `json:"method,omitempty"`
	Errors       bool   `json:"errors,omitempty"`
	SlowerThanMS int64  `json:"slower_than_ms,omitempty"`
	Slowest      bool   `json:"slowest,omitempty"`
	Limit        int    `json:"limit,omitempty" minimum:"0" maximum:"200" doc:"Default 20."`
}

// addTraceTools registers the traces tool.
func (h *handler) addTraceTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "traces", "A service's traces",
		"A service's traces (entry spans: route, status, duration, release), newest or slowest first; with trace, one trace's spans across services, where the time went and the first error. Needs tracing on.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in tracesInput) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			if in.Trace != "" {
				return h.traceGet(ctx, c, ref, in)
			}
			return h.traceList(ctx, c, ref, in)
		})
}

// traceList is a service's traces, filtered.
func (h *handler) traceList(ctx context.Context, c *call, ref core.ServiceRef, in tracesInput) (mcpkit.Reply, error) {
	l, err := h.core.ListTraces(ctx, c.Actor, ref, core.TraceFilter{
		Range: apitypes.TraceRange(in.Range), Release: in.Release, SinceRelease: in.SinceRelease, Route: in.Route, Method: in.Method,
		Errors: in.Errors, SlowerThan: time.Duration(in.SlowerThanMS) * time.Millisecond, Slowest: in.Slowest, Limit: cmp.Or(in.Limit, 20),
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
	noun := "traces"
	if len(l.Traces) == 1 {
		noun = "trace"
	}
	sum := fmt.Sprintf("%d %s of %s in the last %s: %s.", len(l.Traces), noun, ref, l.Range, list(top))
	if l.More {
		sum += " More match; narrow the filters."
	}
	return mcpkit.Reply{Summary: sum, Data: l}, nil
}

// traceGet is one trace's spans, its slowest call and first failure named.
func (h *handler) traceGet(ctx context.Context, c *call, ref core.ServiceRef, in tracesInput) (mcpkit.Reply, error) {
	var at time.Time
	if in.At != "" {
		var err error
		if at, err = time.Parse(time.RFC3339Nano, in.At); err != nil {
			return mcpkit.Reply{}, errf(apitypes.CodeValidation, "at %q: give an RFC 3339 time, like a trace's start in the list", in.At)
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
}

// entryOf names a trace's entry: method and route, or its span's name.
func entryOf(t apitypes.TraceSummary) string {
	if t.Route != "" {
		return t.Method + " " + t.Route
	}
	return t.Name
}
