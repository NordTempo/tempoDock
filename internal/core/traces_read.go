package core

import (
	"bytes"
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
// outside the caller's token's project or environment, or in an
// environment whose logs the caller may not read, are hidden, and
// for a reader who can't reveal secret values, each span's name,
// attributes and events are masked with its release's values, as logs
// are (the ingest already masked sensitive attribute names). A span
// whose release is gone, its values with it, has all its text masked.

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
	masks, err := c.traceMasks(a, ss, rels, func(id int64) bool { return id == ss.svc.ID })
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
		red := masks.of(ss.svc.ID, t.Release)
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
	done, err := c.traceReadSlot(ctx)
	if err != nil {
		return apitypes.Trace{}, err
	}
	defer done()
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
	masks, err := c.traceMasks(a, ss, rels, func(id int64) bool {
		n, ok := names[id]
		return ok && n.Project == ss.project.Name && n.Environment == ss.env.Name
	})
	if err != nil {
		return apitypes.Trace{}, err
	}
	// Traces are logs: a span shows only where the reader holds
	// logs.read, not everywhere they see (ADR 0080).
	kinds, err := c.envKinds(ctx, a)
	if err != nil {
		return apitypes.Trace{}, err
	}
	start, end := res.Spans[0].Start, res.Spans[0].Start
	for _, sp := range res.Spans {
		start, end = min(start, sp.Start), max(end, sp.Start+sp.Duration)
	}
	out := apitypes.Trace{Trace: id, Start: time.UnixMicro(start), DurationMS: ms(end - start), Spans: make([]apitypes.TraceSpan, 0, len(res.Spans)), Truncated: res.Truncated}
	decode := maxTraceDecode
	for _, sp := range res.Spans {
		// A caller that left isn't decoded and masked for.
		if err := ctx.Err(); err != nil {
			return apitypes.Trace{}, err
		}
		ts := apitypes.TraceSpan{
			Span: sp.ID.String(), OffsetMS: ms(sp.Start - start), DurationMS: ms(sp.Duration),
			Kind: spanKind(sp.Kind), Status: spanStatus(sp.Status), Kept: kept(sp.Kept),
		}
		if sp.Parent != 0 {
			ts.Parent = sp.Parent.String()
		}
		if !a.seesEnv(sp.Project, sp.Env) || !a.holdsAt(PermLogsRead, sp.Project, sp.Env, kinds[sp.Env]) {
			ts.Hidden = true
			out.Spans = append(out.Spans, ts)
			continue
		}
		red := masks.of(sp.Service, sp.Release)
		n := names[sp.Service]
		ts.Project, ts.Environment, ts.Service, ts.Release = n.Project, n.Environment, n.Name, rels[sp.Release].Number
		ts.Name, ts.Method, ts.Route, ts.HTTPStatus = red.mask(sp.Name), sp.Method, red.mask(sp.Route), sp.HTTPStatus
		// Past maxTraceDecode a span comes without its attributes and
		// events, and the trace says it isn't whole.
		if n := decodedSize(sp.Attrs) + decodedSize(sp.Events); n <= decode {
			decode -= n
			ts.Attrs = red.attrs(sp.Attrs)
			ts.Events = red.events(sp.Events)
		} else {
			out.Truncated = true
		}
		out.Spans = append(out.Spans, ts)
	}
	return out, nil
}

// maxTraceReads is how many traces are read at once: a trace is up to
// 2,000 spans, about 33 MB as stored at the ingest's caps, so the daemon
// holds at most a few.
const maxTraceReads = 4

// maxTraceDecode is how much one read decodes of a trace's attributes
// and events, by what they take decoded (decodedSize). This, not the
// span count, bounds what a read holds: a trace of the most text the
// store returns (33 MB) fits whole, one of small nested values, which
// take many times their JSON decoded, doesn't.
const maxTraceDecode = 48 << 20

// decodedSize estimates, from above, the memory raw JSON takes decoded
// into maps, lists and strings: its bytes, about 96 a map or list and
// 32 a value in one. It counts the JSON's punctuation, without decoding
// or allocating; punctuation inside strings only makes it larger.
func decodedSize(raw []byte) int {
	return len(raw) + 96*(bytes.Count(raw, []byte{'{'})+bytes.Count(raw, []byte{'['})) + 32*bytes.Count(raw, []byte{','})
}

// traceReadSlot waits up to 10 s for one of maxTraceReads slots and
// returns its release, or that Dock is busy.
func (c *Core) traceReadSlot(ctx context.Context) (func(), error) {
	c.traces.mu.Lock()
	if c.traces.reads == nil {
		c.traces.reads = make(chan struct{}, maxTraceReads)
	}
	slots := c.traces.reads
	c.traces.mu.Unlock()
	wait := time.NewTimer(10 * time.Second)
	defer wait.Stop()
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-wait.C:
		return nil, errf(apitypes.CodeRateLimited, "Dock is reading %d traces already; try again in a moment", maxTraceReads)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
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
// or a backslash, which JSON escapes, is masked too. What masking a
// string costs depends on its length alone, not on how many values the
// service has (redactor), so a read's masking is bounded by what it
// decodes (maxTraceDecode).
type traceMask struct {
	r *redactor
	// all masks every string whole: for a release whose values are gone.
	all bool
}

// traceMaskSet is a read's masks by release (traceMasks).
type traceMaskSet struct {
	by map[int64]traceMask
	// reveal and inEnv are traceMasks'.
	reveal bool
	inEnv  func(serviceID int64) bool
}

// of is the mask for a span of a service's release. A release that is
// gone (deleted with its service or project, while the trace store
// still keeps its spans) has no values left to mask with, so all its
// span's text is masked for a reader who would get it masked.
func (s traceMaskSet) of(serviceID, releaseID int64) traceMask {
	if m, ok := s.by[releaseID]; ok {
		return m
	}
	if s.reveal && s.inEnv(serviceID) {
		return traceMask{}
	}
	return traceMask{all: true}
}

// traceMasks are the masks of the releases in rels for a, each opened
// once a request: a release's values are decrypted once, not per span.
// A reader who may reveal secret values in ss's environment gets none for
// the services there (inEnv); another environment's stay masked.
func (c *Core) traceMasks(a Actor, ss svcScope, rels map[int64]store.Release, inEnv func(serviceID int64) bool) (traceMaskSet, error) {
	reveal := a.revealsIn(ss.envScope)
	out := make(map[int64]traceMask, len(rels))
	for id, rel := range rels {
		if reveal && inEnv(rel.ServiceID) {
			out[id] = traceMask{}
			continue
		}
		r, err := c.releaseRedactor(rel)
		if err != nil {
			return traceMaskSet{}, err
		}
		out[id] = traceMask{r: r}
	}
	return traceMaskSet{by: out, reveal: reveal, inEnv: inEnv}, nil
}

// mask masks s.
func (m traceMask) mask(s string) string {
	if m.all && s != "" {
		return secretMask
	}
	out, _ := m.r.line(s)
	return out
}

// value masks the strings in a decoded JSON value, in place.
func (m traceMask) value(v any) any {
	if m.r == nil && !m.all {
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
