package core

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Reading traces: a service's list, and one trace, through the service
// the caller reads it from. Reading needs read in the service's
// environment, like logs. A trace may cross services: spans of services
// outside the caller's token's project or environment are hidden, and
// for a reader who can't reveal secret values, each span's name,
// attributes and events are masked with its release's values, as logs
// are (the ingest already masked sensitive attribute names).

// TraceFilter picks a service's traces.
type TraceFilter struct {
	// Range ends now; empty is 24h.
	Range apitypes.TraceRange
	// Release picks one release by number, SinceRelease those from it on.
	Release, SinceRelease int64
	Method, Route         string
	// Errors keeps traces whose entry span failed (an error status or a
	// 5xx answer).
	Errors bool
	// SlowerThan keeps traces at least that long.
	SlowerThan time.Duration
	// Slowest orders by duration instead of newest first.
	Slowest bool
	// Limit is at most 200; 0 is 50.
	Limit int
}

// traceRanges are the ranges a list takes.
var traceRanges = map[apitypes.TraceRange]time.Duration{
	apitypes.TraceRange1h: time.Hour, apitypes.TraceRange24h: 24 * time.Hour, apitypes.TraceRange7d: 7 * 24 * time.Hour,
}

// ListTraces lists a service's traces matching f.
func (c *Core) ListTraces(ctx context.Context, a Actor, ref ServiceRef, f TraceFilter) (apitypes.TraceList, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "reading traces")
	if err != nil {
		return apitypes.TraceList{}, err
	}
	cl, err := c.traceReader()
	if err != nil {
		return apitypes.TraceList{}, err
	}
	rng := f.Range
	if rng == "" {
		rng = apitypes.TraceRange24h
	}
	span, ok := traceRanges[rng]
	if !ok {
		return apitypes.TraceList{}, invalid("range %q: use 1h, 24h or 7d", rng)
	}
	if f.Limit < 0 || f.Limit > analytics.MaxTraces {
		return apitypes.TraceList{}, invalid("a list shows at most %d traces", analytics.MaxTraces)
	}
	to := now()
	q := analytics.TracesQuery{
		Org: ss.project.OrgID, Services: []int64{ss.svc.ID}, From: to.Add(-span).UnixMicro(), To: to.UnixMicro(),
		Method: f.Method, Route: f.Route, MinDuration: f.SlowerThan.Microseconds(), Limit: f.Limit,
	}
	if f.Errors {
		q.Status = analytics.TraceError
	}
	if f.Slowest {
		q.Sort = analytics.SortSlowest
	}
	for _, r := range []struct {
		number int64
		id     *int64
	}{{f.Release, &q.Release}, {f.SinceRelease, &q.SinceRelease}} {
		if r.number == 0 {
			continue
		}
		rel, err := c.releaseByNumber(ctx, ss, r.number)
		if err != nil {
			return apitypes.TraceList{}, err
		}
		*r.id = rel.ID
	}
	res, err := cl.Traces(ctx, q)
	if err != nil {
		return apitypes.TraceList{}, traceStoreErr(err)
	}
	ids := make([]int64, 0, len(res.Traces))
	for _, t := range res.Traces {
		ids = append(ids, t.Release)
	}
	rels, err := c.releasesByID(ctx, ids)
	if err != nil {
		return apitypes.TraceList{}, err
	}
	masks, err := c.traceMasks(a, rels)
	if err != nil {
		return apitypes.TraceList{}, err
	}
	out := apitypes.TraceList{Range: rng, Since: to.Add(-span), Traces: make([]apitypes.TraceSummary, 0, len(res.Traces)), More: res.More}
	if rel, err := c.store.GetActiveRelease(ctx, ss.svc.ID); err == nil {
		if cfg, err := c.openConfig(rel); err == nil {
			out.ExportsElsewhere = exportsElsewhere(cfg.Env)
		}
	}
	for _, t := range res.Traces {
		red := masks[t.Release]
		out.Traces = append(out.Traces, apitypes.TraceSummary{
			Trace: t.Trace.String(), Span: t.Span.String(), Release: rels[t.Release].Number,
			Start: time.UnixMicro(t.Start), DurationMS: ms(t.Duration), Name: red.mask(t.Name), Kind: spanKind(t.Kind),
			Method: t.Method, Route: red.mask(t.Route), HTTPStatus: t.HTTPStatus,
			Error: t.Status == analytics.StatusError || t.HTTPStatus >= 500, Kept: kept(t.Kept),
		})
	}
	return out, nil
}

// GetTrace reads one trace, through a service the caller may read; at
// (zero: unknown) is a time in it, which every link to a trace has.
func (c *Core) GetTrace(ctx context.Context, a Actor, ref ServiceRef, id string, at time.Time) (apitypes.Trace, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "reading traces")
	if err != nil {
		return apitypes.Trace{}, err
	}
	traceID, err := analytics.ParseTraceID(id)
	if err != nil {
		return apitypes.Trace{}, invalid("trace %q: %v", id, err)
	}
	cl, err := c.traceReader()
	if err != nil {
		return apitypes.Trace{}, err
	}
	q := analytics.TraceQuery{Org: ss.project.OrgID, Trace: traceID}
	if !at.IsZero() {
		q.At = at.UnixMicro()
	}
	res, err := cl.Trace(ctx, q)
	if err != nil {
		return apitypes.Trace{}, traceStoreErr(err)
	}
	// A trace is reached through a service of it: one that doesn't have
	// the service isn't this service's to show.
	if !slices.ContainsFunc(res.Spans, func(sp analytics.Span) bool { return sp.Service == ss.svc.ID }) {
		return apitypes.Trace{}, notFound("%s has no trace %s in the days Dock keeps traces", ss, id)
	}
	var svcIDs, relIDs []int64
	for _, sp := range res.Spans {
		svcIDs, relIDs = append(svcIDs, sp.Service), append(relIDs, sp.Release)
	}
	names := map[int64]store.ListServiceNamesByIDsRow{}
	rows, err := c.store.ListServiceNamesByIDs(ctx, uniq(svcIDs))
	if err != nil {
		return apitypes.Trace{}, err
	}
	for _, r := range rows {
		names[r.ID] = r
	}
	rels, err := c.releasesByID(ctx, relIDs)
	if err != nil {
		return apitypes.Trace{}, err
	}
	masks, err := c.traceMasks(a, rels)
	if err != nil {
		return apitypes.Trace{}, err
	}
	start, end := res.Spans[0].Start, res.Spans[0].Start
	for _, sp := range res.Spans {
		start, end = min(start, sp.Start), max(end, sp.Start+sp.Duration)
	}
	out := apitypes.Trace{Trace: id, Start: time.UnixMicro(start), DurationMS: ms(end - start), Spans: make([]apitypes.TraceSpan, 0, len(res.Spans)), Truncated: res.Truncated}
	for _, sp := range res.Spans {
		ts := apitypes.TraceSpan{
			Span: sp.ID.String(), OffsetMS: ms(sp.Start - start), DurationMS: ms(sp.Duration),
			Kind: spanKind(sp.Kind), Status: spanStatus(sp.Status), Kept: kept(sp.Kept),
		}
		if sp.Parent != 0 {
			ts.Parent = sp.Parent.String()
		}
		if (a.ProjectID != 0 && a.ProjectID != sp.Project) || (a.EnvironmentID != 0 && a.EnvironmentID != sp.Env) {
			ts.Hidden = true
			out.Spans = append(out.Spans, ts)
			continue
		}
		red := masks[sp.Release]
		n := names[sp.Service]
		ts.Project, ts.Environment, ts.Service, ts.Release = n.Project, n.Environment, n.Name, rels[sp.Release].Number
		ts.Name, ts.Method, ts.Route, ts.HTTPStatus = red.mask(sp.Name), sp.Method, red.mask(sp.Route), sp.HTTPStatus
		ts.Attrs = red.attrs(sp.Attrs)
		ts.Events = red.events(sp.Events)
		out.Spans = append(out.Spans, ts)
	}
	return out, nil
}

// traceReader is the trace store's client, or why traces can't be read.
func (c *Core) traceReader() (*analytics.Client, error) {
	cl, _ := c.tracesClient()
	if cl == nil {
		return nil, conflict("tracing is off; an operator turns it on with `dock tracing on`")
	}
	return cl, nil
}

// traceStoreErr says what a trace store's error means for the caller.
func traceStoreErr(err error) error {
	if se, ok := errors.AsType[*analytics.StoreError](err); ok && se.Status == http.StatusBadRequest {
		return invalid("%s", se.Message)
	}
	if errors.Is(err, analytics.ErrOffline) || errors.Is(err, analytics.ErrBusy) {
		return errf(apitypes.CodeServerOffline, "the trace store isn't answering; see `dock tracing`")
	}
	return err
}

// releasesByID loads releases by ID, in one query.
func (c *Core) releasesByID(ctx context.Context, ids []int64) (map[int64]store.Release, error) {
	out := map[int64]store.Release{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := c.store.ListReleasesByIDs(ctx, uniq(ids))
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out, nil
}

// traceMask masks a release's secret values in what a trace shows; nil
// masks nothing. Values are masked once decoded, so a secret with a quote
// or a backslash, which JSON escapes, is masked too.
type traceMask struct{ r *redactor }

// traceMasks are the masks of the releases in rels for a, each opened
// once a request: a release's values are decrypted once, not per span.
// A reader who may reveal secret values gets none.
func (c *Core) traceMasks(a Actor, rels map[int64]store.Release) (map[int64]traceMask, error) {
	out := make(map[int64]traceMask, len(rels))
	for id, rel := range rels {
		r, err := c.logRedactor(a, rel)
		if err != nil {
			return nil, err
		}
		out[id] = traceMask{r}
	}
	return out, nil
}

// mask masks s.
func (m traceMask) mask(s string) string {
	if m.r == nil {
		return s
	}
	out, _ := m.r.line(s)
	return out
}

// value masks the strings in a decoded JSON value, in place.
func (m traceMask) value(v any) any {
	if m.r == nil {
		return v
	}
	switch v := v.(type) {
	case string:
		return m.mask(v)
	case []any:
		for i := range v {
			v[i] = m.value(v[i])
		}
	case map[string]any:
		for k := range v {
			v[k] = m.value(v[k])
		}
	}
	return v
}

// attrs decodes a span's attributes, masked.
func (m traceMask) attrs(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	m.value(out)
	return out
}

// events decodes a span's events, masked.
func (m traceMask) events(raw []byte) []apitypes.TraceEvent {
	if len(raw) == 0 {
		return nil
	}
	var evs []struct {
		Name  string         `json:"name"`
		At    int64          `json:"at"`
		Attrs map[string]any `json:"attrs"`
	}
	if json.Unmarshal(raw, &evs) != nil {
		return nil
	}
	out := make([]apitypes.TraceEvent, len(evs))
	for i, e := range evs {
		m.value(e.Attrs)
		out[i] = apitypes.TraceEvent{Name: m.mask(e.Name), AtMS: ms(e.At), Attrs: e.Attrs}
	}
	return out
}

// ms is microseconds as milliseconds.
func ms(us int64) float64 { return float64(us) / 1000 }

// uniq is ids without repeats or zeros.
func uniq(ids []int64) []int64 {
	out := slices.Clone(ids)
	slices.Sort(out)
	out = slices.Compact(out)
	return slices.DeleteFunc(out, func(id int64) bool { return id == 0 })
}

// spanKind, spanStatus and kept name a span's enums for the API.
func spanKind(k analytics.SpanKind) string {
	return [...]string{"unspecified", "internal", "server", "client", "producer", "consumer", "edge"}[min(int(k), 6)]
}

func spanStatus(s analytics.SpanStatus) string {
	return [...]string{"unset", "ok", "error"}[min(int(s), 2)]
}

func kept(k analytics.Kept) string {
	return [...]string{"sampled", "error", "slow", "forced"}[min(int(k), 3)]
}
