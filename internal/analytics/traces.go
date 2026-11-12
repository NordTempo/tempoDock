package analytics

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

// Traces in the trace store: `dock-analytics traces`, a process and
// container of its own beside the analytics store, with the same HTTP
// conventions and token, so Client talks to it too (at its own BaseURL).
// Every server's trace ingest keeps the spans it samples in a spool, and
// the control plane's forwarder writes them to the store in batches, as
// it does events (WriteSpanLines). The store keeps one table per UTC day
// and answers trace lists and single traces over them.

// TraceID is a W3C trace ID: 16 random bytes, 32 hex digits in JSON.
type TraceID [16]byte

// ParseTraceID reads 32 hex digits.
func ParseTraceID(s string) (TraceID, error) {
	var id TraceID
	if len(s) != 32 {
		return id, errors.New("a trace ID is 32 hex digits")
	}
	if _, err := hex.Decode(id[:], []byte(s)); err != nil {
		return id, errors.New("a trace ID is 32 hex digits")
	}
	return id, nil
}

// String is the ID's 32 hex digits.
func (id TraceID) String() string { return hex.EncodeToString(id[:]) }

// IsZero reports whether id is all zeros, which W3C makes invalid.
func (id TraceID) IsZero() bool { return id == TraceID{} }

// Halves splits id into the two 64-bit numbers the store keeps it as.
func (id TraceID) Halves() (hi, lo uint64) {
	return binary.BigEndian.Uint64(id[:8]), binary.BigEndian.Uint64(id[8:])
}

// TraceIDOf joins two halves back into a TraceID.
func TraceIDOf(hi, lo uint64) TraceID {
	var id TraceID
	binary.BigEndian.PutUint64(id[:8], hi)
	binary.BigEndian.PutUint64(id[8:], lo)
	return id
}

// MarshalText implements encoding.TextMarshaler.
func (id TraceID) MarshalText() ([]byte, error) { return []byte(id.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (id *TraceID) UnmarshalText(b []byte) error {
	v, err := ParseTraceID(string(b))
	*id = v
	return err
}

// SpanID is a W3C span ID: 8 bytes, 16 hex digits in JSON. Zero is no
// span (a root's parent).
type SpanID uint64

// String is the ID's 16 hex digits.
func (id SpanID) String() string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(id))
	return hex.EncodeToString(b[:])
}

// MarshalText implements encoding.TextMarshaler.
func (id SpanID) MarshalText() ([]byte, error) { return []byte(id.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (id *SpanID) UnmarshalText(b []byte) error {
	var raw [8]byte
	if len(b) != 16 {
		return errors.New("a span ID is 16 hex digits")
	}
	if _, err := hex.Decode(raw[:], b); err != nil {
		return errors.New("a span ID is 16 hex digits")
	}
	*id = SpanID(binary.BigEndian.Uint64(raw[:]))
	return nil
}

// SpanKind is what a span is to its service: OTLP's kinds, and the edge
// span Caddy records for a request to a domain.
type SpanKind uint8

// Span kinds. The first six are OTLP's numbers.
const (
	SpanUnspecified SpanKind = iota
	SpanInternal
	SpanServer
	SpanClient
	SpanProducer
	SpanConsumer
	SpanEdge
)

// SpanStatus is OTLP's status code.
type SpanStatus uint8

// Span statuses, OTLP's numbers.
const (
	StatusUnset SpanStatus = iota
	StatusOK
	StatusError
)

// Kept says why the ingest kept a span: its trace was sampled, or it was
// rescued for an error or for being slow, or Dock asked for it (a deploy's
// health check).
type Kept uint8

// Why a span was kept.
const (
	KeptSampled Kept = iota
	KeptError
	KeptSlow
	KeptForced
)

// Span is one span as the store keeps it, and its JSON line in a spool.
// The ingest fills the IDs of the org, project, environment, service and
// release from the token that sent it, never from the span's attributes.
type Span struct {
	Org     int64   `json:"org"`
	Project int64   `json:"project"`
	Env     int64   `json:"env"`
	Service int64   `json:"service"`
	Release int64   `json:"release"`
	Trace   TraceID `json:"trace"`
	ID      SpanID  `json:"span"`
	Parent  SpanID  `json:"parent,omitzero"`
	// Start is Unix microseconds, Duration microseconds.
	Start    int64      `json:"start"`
	Duration int64      `json:"dur"`
	Name     string     `json:"name"`
	Kind     SpanKind   `json:"kind,omitzero"`
	Status   SpanStatus `json:"status,omitzero"`
	// Entry marks the span where the trace entered the service: a server
	// or consumer span, an edge span, or a root. Trace lists are lists
	// of entry spans.
	Entry      bool   `json:"entry,omitzero"`
	Method     string `json:"method,omitzero"`
	Route      string `json:"route,omitzero"`
	HTTPStatus int    `json:"http_status,omitzero"`
	// ErrorFP is the Errors issue fingerprint of the span's exception.
	ErrorFP string `json:"error_fp,omitzero"`
	Kept    Kept   `json:"kept,omitzero"`
	// Attrs is a JSON object and Events a JSON array, as the ingest cut
	// and masked them.
	Attrs  jsontext.Value `json:"attrs,omitzero"`
	Events jsontext.Value `json:"events,omitzero"`
}

// Error reports whether the span failed: its status, or a 5xx answer.
func (s *Span) Error() bool { return s.Status == StatusError || s.HTTPStatus >= 500 }

// TraceLimits are the caps a span write applies (zero is no cap), from
// the control plane's settings, so changing them needs no restart.
// Counts are per UTC day of arrival.
type TraceLimits struct {
	// ServiceDay and OrgDay cap the spans a service and an org keep a
	// day. Past either, only error and forced spans are written.
	ServiceDay int64
	OrgDay     int64
	// ErrorDay caps a service's error and forced spans a day.
	ErrorDay int64
	// MaxBytes caps the traces database's used size; past it the oldest
	// day goes early.
	MaxBytes int64
}

// SpanWriteResult answers a span write.
type SpanWriteResult struct {
	Written int `json:"written"`
	// Skipped counts lines that weren't spans; Rejected spans past a cap
	// or outside the days the store keeps.
	Skipped  int `json:"skipped,omitempty"`
	Rejected int `json:"rejected,omitempty"`
}

// TraceSort orders a trace list.
type TraceSort string

// Trace list orders.
const (
	SortNewest  TraceSort = "newest"
	SortSlowest TraceSort = "slowest"
)

// TraceStatus filters a trace list by how its entry span ended.
type TraceStatus string

// Trace status filters.
const (
	TraceAny   TraceStatus = ""
	TraceError TraceStatus = "error"
	Trace5xx   TraceStatus = "5xx"
)

// TracesQuery picks the traces a list shows: the entry spans of
// Services of Org in [From, To) (Unix µs), with the filters set.
type TracesQuery struct {
	Org int64 `json:"org"`
	// Services must name at least one service; the caller lists only
	// those the actor may read.
	Services []int64 `json:"services"`
	From     int64   `json:"from"`
	To       int64   `json:"to"`
	// Release picks one release; SinceRelease those from it on.
	Release      int64       `json:"release,omitzero"`
	SinceRelease int64       `json:"since_release,omitzero"`
	Method       string      `json:"method,omitzero"`
	Route        string      `json:"route,omitzero"`
	Status       TraceStatus `json:"status,omitzero"`
	// MinDuration is in microseconds.
	MinDuration int64     `json:"min_duration,omitzero"`
	Sort        TraceSort `json:"sort,omitzero"`
	// Limit is at most MaxTraces; 0 is DefaultTraces.
	Limit int `json:"limit,omitzero"`
}

// Trace list sizes.
const (
	DefaultTraces = 50
	MaxTraces     = 200
)

// TraceSummary is one row of a trace list: the span where the trace
// entered the service. Kept says whether the whole trace had an error or
// was slow; how many spans it has is the trace's to say (Trace).
type TraceSummary struct {
	Trace      TraceID    `json:"trace"`
	Span       SpanID     `json:"span"`
	Service    int64      `json:"service"`
	Release    int64      `json:"release"`
	Start      int64      `json:"start"`
	Duration   int64      `json:"dur"`
	Name       string     `json:"name"`
	Kind       SpanKind   `json:"kind,omitzero"`
	Method     string     `json:"method,omitzero"`
	Route      string     `json:"route,omitzero"`
	HTTPStatus int        `json:"http_status,omitzero"`
	Status     SpanStatus `json:"status,omitzero"`
	Kept       Kept       `json:"kept,omitzero"`
	ErrorFP    string     `json:"error_fp,omitzero"`
}

// OnePerTrace appends the entries to out, one per trace: a trace that
// enters a service twice (it calls itself) is listed once, by its
// earliest entry, the outer one, in the place its first entry had. Nil
// entries are skipped.
func OnePerTrace(out []TraceSummary, entries []*TraceSummary) []TraceSummary {
	at := make(map[TraceID]int, len(entries))
	for _, e := range entries {
		if e == nil {
			continue
		}
		i, seen := at[e.Trace]
		switch {
		case !seen:
			at[e.Trace] = len(out)
			out = append(out, *e)
		case e.Start < out[i].Start:
			out[i] = *e
		}
	}
	return out
}

// TracesResult answers a TracesQuery.
type TracesResult struct {
	Traces []TraceSummary `json:"traces"`
	// More says the range has more matching traces than Limit.
	More bool `json:"more,omitzero"`
}

// TraceQuery picks one trace of an org. At (Unix µs) is a time in it,
// which every link has: the store then reads an hour on either side.
// Without it the store reads every day it keeps, which takes longer.
type TraceQuery struct {
	Org   int64   `json:"org"`
	Trace TraceID `json:"trace"`
	At    int64   `json:"at,omitzero"`
	// After pages: only spans after it, by start and span ID. Limit is a
	// page's size, at most MaxTraceSpans; 0 is MaxTraceSpans.
	After *TraceCursor `json:"after,omitzero"`
	Limit int          `json:"limit,omitzero"`
}

// TraceCursor is where a page of a trace's spans ended: the last span's
// start and ID.
type TraceCursor struct {
	Start int64  `json:"start"`
	Span  SpanID `json:"span"`
}

// MaxTraceSpans caps the spans a trace answers with.
const MaxTraceSpans = 2000

// TracePage is how many spans Client.Trace asks for at a time: a span is
// at most about 17 KB (the ingest's caps), so a page stays well under the
// answer cap.
const TracePage = 250

// TraceDetail is a trace's spans, by start time. The caller removes those
// of services the actor may not read.
type TraceDetail struct {
	Spans []Span `json:"spans"`
	// Truncated says the trace has more spans than answered: past the
	// query's limit, or past MaxTraceSpans in all.
	Truncated bool `json:"truncated,omitzero"`
}

// ErrorTraceQuery finds the newest span of an Errors fingerprint among
// Services from From to To (Unix microseconds): an error issue's latest
// trace.
type ErrorTraceQuery struct {
	Org         int64   `json:"org"`
	Services    []int64 `json:"services"`
	Fingerprint string  `json:"fp"`
	From        int64   `json:"from"`
	To          int64   `json:"to"`
}

// ErrorTrace answers an ErrorTraceQuery: the span's trace and start, a
// zero trace when none matched.
type ErrorTrace struct {
	Trace TraceID `json:"trace"`
	Start int64   `json:"start,omitzero"`
}

// TracePrune drops the days of spans and metric points before Before
// (Unix ms; whole UTC days before the day it is in), for the retention,
// rolling each day of points up into hours first, and the hours and
// series of metrics before HoursBefore (0 keeps them).
type TracePrune struct {
	Before      int64 `json:"before"`
	HoursBefore int64 `json:"hours_before,omitzero"`
}

// TracePruneResult names the days a prune dropped, "2026-11-03".
type TracePruneResult struct {
	Days []string `json:"days"`
}

// MaxSpanBytes caps one span's JSON line in a write.
const MaxSpanBytes = 32 << 10

// Trace paths of the store's API.
const (
	// SpansWritePath takes POST ?source=&position= and the TraceLimits
	// (service_day, org_day, error_day, max_bytes) with a body of spans
	// as JSON lines, and answers SpanWriteResult.
	SpansWritePath = "/v1/spans/write"
	// SpansCursorPath answers GET ?source= with CursorResult: how far
	// source's spans are written.
	SpansCursorPath = "/v1/spans/cursor"
	// TracesPath takes POST with a TracesQuery and answers TracesResult;
	// TracePath a TraceQuery and TraceDetail.
	TracesPath = "/v1/traces"
	TracePath  = "/v1/trace"
	// ErrorTracePath takes POST with an ErrorTraceQuery and answers
	// ErrorTrace.
	ErrorTracePath = "/v1/error-trace"
	// TracesPrunePath takes POST with a TracePrune and answers
	// TracePruneResult.
	TracesPrunePath = "/v1/traces/prune"
)

// WriteSpans writes spans from source as WriteSpanLines does.
func (c *Client) WriteSpans(ctx context.Context, source string, position uint64, spans []Span, lim TraceLimits) (SpanWriteResult, error) {
	var body bytes.Buffer
	for i := range spans {
		if err := json.MarshalWrite(&body, &spans[i]); err != nil {
			return SpanWriteResult{}, err
		}
		body.WriteByte('\n')
	}
	return c.WriteSpanLines(ctx, source, position, body.Bytes(), lim)
}

// WriteSpanLines writes spans that are already JSON lines, as a spool
// holds them, from source, whose spans up to position are then all
// written. A batch at or before the source's cursor was written before
// and is skipped. Lines that aren't spans are skipped and counted.
func (c *Client) WriteSpanLines(ctx context.Context, source string, position uint64, lines []byte, lim TraceLimits) (SpanWriteResult, error) {
	q := url.Values{"source": {source}, "position": {strconv.FormatUint(position, 10)}}
	for k, v := range map[string]int64{"service_day": lim.ServiceDay, "org_day": lim.OrgDay, "error_day": lim.ErrorDay, "max_bytes": lim.MaxBytes} {
		if v > 0 {
			q.Set(k, strconv.FormatInt(v, 10))
		}
	}
	var res SpanWriteResult
	err := c.do(ctx, http.MethodPost, SpansWritePath+"?"+q.Encode(), bytes.NewReader(lines), &res)
	return res, err
}

// SpansCursor is source's position in the trace store (SpansCursorPath).
func (c *Client) SpansCursor(ctx context.Context, source string) (uint64, error) {
	var res CursorResult
	err := c.do(ctx, http.MethodGet, SpansCursorPath+"?"+url.Values{"source": {source}}.Encode(), nil, &res)
	return res.Position, err
}

// Traces lists traces (TracesPath).
func (c *Client) Traces(ctx context.Context, q TracesQuery) (TracesResult, error) {
	var res TracesResult
	return res, c.post(ctx, TracesPath, q, &res)
}

// Trace reads one trace (TracePath).
//
// It reads the trace a page of TracePage spans at a time, up to
// MaxTraceSpans, so a trace of big spans never meets the answer cap.
func (c *Client) Trace(ctx context.Context, q TraceQuery) (TraceDetail, error) {
	res := TraceDetail{Spans: []Span{}}
	q.Limit = TracePage
	for len(res.Spans) < MaxTraceSpans {
		var page TraceDetail
		if err := c.post(ctx, TracePath, q, &page); err != nil {
			return res, err
		}
		last := len(res.Spans)
		res.Spans = append(res.Spans, page.Spans...)
		if !page.Truncated || len(page.Spans) == 0 {
			return res, nil
		}
		end := res.Spans[len(res.Spans)-1]
		if q.After != nil && last > 0 && (end.Start < q.After.Start || (end.Start == q.After.Start && int64(end.ID) <= int64(q.After.Span))) {
			return res, errors.New("the trace store doesn't page traces; update it")
		}
		q.After = &TraceCursor{Start: end.Start, Span: end.ID}
	}
	res.Spans, res.Truncated = res.Spans[:MaxTraceSpans], true
	return res, nil
}

// ErrorTrace finds an error fingerprint's newest trace (ErrorTracePath).
func (c *Client) ErrorTrace(ctx context.Context, q ErrorTraceQuery) (ErrorTrace, error) {
	var res ErrorTrace
	return res, c.post(ctx, ErrorTracePath, q, &res)
}

// PruneTraces drops old days of spans (TracesPrunePath).
func (c *Client) PruneTraces(ctx context.Context, q TracePrune) (TracePruneResult, error) {
	var res TracePruneResult
	return res, c.post(ctx, TracesPrunePath, q, &res)
}
