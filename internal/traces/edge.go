package traces

import (
	"bytes"
	"encoding/hex"
	"encoding/json/v2"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// EdgeRequest is a request Caddy served on a traced route (ADR 0075),
// from its access log line: Caddy's tracing handler started its trace and
// passed the traceparent to the app, so the app's spans are its children.
type EdgeRequest struct {
	// Token is the route's trace token: the service and release the
	// span is for.
	Token string
	// Method and Route name the request; Route is its path as traffic
	// counts it, IDs as ":id".
	Method, Route string
	Status        int
	End           time.Time
	Duration      time.Duration
	// TraceID and SpanID are Caddy's, as hex.
	TraceID, SpanID string
}

// Edge makes the edge spans of an ingest's server: one per traced
// request, an entry span of kind edge, through the service's rate and the
// sampler like any span. The visitor's address, user agent and query
// aren't kept (ADR 0040).
type Edge struct {
	In *Ingest

	mu sync.Mutex
	// claims caches parsed tokens: one per traced route, reset when the
	// key changes.
	claims map[string]Claims
	key    []byte
	line   bytes.Buffer
}

// maxEdgeClaims bounds the token cache; past it, it starts over.
const maxEdgeClaims = 4096

// Request turns one traced request into its edge span.
func (e *Edge) Request(r EdgeRequest) {
	in := e.In
	in.init()
	trace, ok1 := edgeID(r.TraceID, 16)
	span, ok2 := edgeID(r.SpanID, 8)
	if !ok1 || !ok2 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.claimsOf(r.Token)
	if !ok {
		return
	}
	now := in.Now()
	end := r.End
	if end.Before(now.Add(-time.Hour)) || end.After(now.Add(time.Minute)) {
		end = now
	}
	status := r.Status
	if status == 0 {
		status = 200 // Caddy logs 0 when Go wrote the 200 itself
	}
	s := analytics.Span{
		Org: c.Org, Project: c.Project, Env: c.Env, Service: c.Service, Release: c.Release,
		Trace: analytics.TraceID(trace), ID: analytics.SpanID(be64(span)),
		Start: end.Add(-r.Duration).UnixMicro(), Duration: r.Duration.Microseconds(),
		Name: cut(r.Method+" "+r.Route, maxName), Kind: analytics.SpanEdge, Entry: true,
		Method: cut(r.Method, 16), Route: cut(r.Route, maxName), HTTPStatus: status,
	}
	if status >= 500 {
		s.Status = analytics.StatusError
	}
	if !in.rates.take(c.Service, in.Limits.ServiceRate, now) {
		return
	}
	e.line.Reset()
	if json.MarshalWrite(&e.line, &s) != nil {
		return
	}
	if err := in.sample.span(&s, bytes.TrimSuffix(e.line.Bytes(), []byte{'\n'}), signals{}, in.sampling, now, in.Spool.Append); err != nil && in.Log != nil {
		in.Log.Warn("traces: spool an edge span", "service", c.Service, "err", err)
	}
}

// claimsOf parses a route's token under the current key, cached.
func (e *Edge) claimsOf(token string) (Claims, bool) {
	key := e.In.Key.Get()
	if key == nil || token == "" {
		return Claims{}, false
	}
	if !bytes.Equal(key, e.key) || len(e.claims) >= maxEdgeClaims {
		e.key, e.claims = bytes.Clone(key), map[string]Claims{}
	}
	if c, ok := e.claims[token]; ok {
		return c, true
	}
	c, err := Parse(key, token)
	if err != nil {
		return Claims{}, false
	}
	e.claims[token] = c
	return c, true
}

// edgeID decodes a hex ID of n bytes; all zeros isn't one.
func edgeID(s string, n int) ([]byte, bool) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != n || bytes.Count(b, []byte{0}) == n {
		return nil, false
	}
	return b, true
}
