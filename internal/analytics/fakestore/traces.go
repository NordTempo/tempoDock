package fakestore

import (
	"bufio"
	"encoding/json/v2"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// Traces is an in-memory trace store behind the real one's HTTP API
// (dock-analytics traces), for core's tests and the fake runtime. It keeps
// every span it is sent, applying the batch positions but not the caps.
type Traces struct {
	token string

	mu      sync.Mutex
	spans   []analytics.Span
	cursors map[string]uint64
	// prunes are the TracePrune.Before values asked, in order.
	prunes []int64
}

// NewTraces returns an empty trace store that wants token.
func NewTraces(token string) *Traces {
	return &Traces{token: token, cursors: map[string]uint64{}}
}

// Spans returns a copy of the spans written.
func (t *Traces) Spans() []analytics.Span {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.spans)
}

// Prunes returns the Before of every prune asked, in order.
func (t *Traces) Prunes() []int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.prunes)
}

// Handler serves the trace store's API.
func (t *Traces) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+analytics.HealthPath, func(w http.ResponseWriter, _ *http.Request) {
		reply(w, analytics.Health{Version: "fake"})
	})
	mux.HandleFunc("POST "+analytics.SpansWritePath, t.auth(func(w http.ResponseWriter, r *http.Request) {
		source := r.URL.Query().Get("source")
		pos, err := strconv.ParseUint(r.URL.Query().Get("position"), 10, 64)
		if source == "" || err != nil {
			http.Error(w, "source and position are required", http.StatusBadRequest)
			return
		}
		var res analytics.SpanWriteResult
		var spans []analytics.Span
		sc := bufio.NewScanner(r.Body)
		sc.Buffer(nil, analytics.MaxSpanBytes)
		for sc.Scan() {
			var sp analytics.Span
			if json.Unmarshal(sc.Bytes(), &sp) != nil {
				res.Skipped++
				continue
			}
			spans = append(spans, sp)
		}
		t.mu.Lock()
		if pos > t.cursors[source] {
			t.spans = append(t.spans, spans...)
			t.cursors[source] = pos
			res.Written = len(spans)
		}
		t.mu.Unlock()
		reply(w, res)
	}))
	mux.HandleFunc("GET "+analytics.SpansCursorPath, t.auth(func(w http.ResponseWriter, r *http.Request) {
		t.mu.Lock()
		pos := t.cursors[r.URL.Query().Get("source")]
		t.mu.Unlock()
		reply(w, analytics.CursorResult{Position: pos})
	}))
	mux.HandleFunc("POST "+analytics.TracesPath, t.auth(func(w http.ResponseWriter, r *http.Request) {
		var q analytics.TracesQuery
		if json.UnmarshalRead(r.Body, &q) != nil || len(q.Services) == 0 || q.To <= q.From {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		reply(w, t.list(q))
	}))
	mux.HandleFunc("POST "+analytics.TracePath, t.auth(func(w http.ResponseWriter, r *http.Request) {
		var q analytics.TraceQuery
		if json.UnmarshalRead(r.Body, &q) != nil {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		t.mu.Lock()
		res := analytics.TraceDetail{Spans: []analytics.Span{}}
		for _, sp := range t.spans {
			if sp.Org == q.Org && sp.Trace == q.Trace {
				res.Spans = append(res.Spans, sp)
			}
		}
		t.mu.Unlock()
		slices.SortStableFunc(res.Spans, func(a, b analytics.Span) int { return int(a.Start - b.Start) })
		reply(w, res)
	}))
	mux.HandleFunc("POST "+analytics.TracesPrunePath, t.auth(func(w http.ResponseWriter, r *http.Request) {
		var q analytics.TracePrune
		if json.UnmarshalRead(r.Body, &q) != nil {
			http.Error(w, "bad prune", http.StatusBadRequest)
			return
		}
		before := time.UnixMilli(q.Before).UTC().Truncate(24 * time.Hour).UnixMicro()
		t.mu.Lock()
		t.prunes = append(t.prunes, q.Before)
		t.spans = slices.DeleteFunc(t.spans, func(sp analytics.Span) bool { return sp.Start < before })
		t.mu.Unlock()
		reply(w, analytics.TracePruneResult{Days: []string{}})
	}))
	return mux
}

// auth requires the token.
func (t *Traces) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+t.token {
			http.Error(w, "wrong token", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// list answers a TracesQuery by scanning every span, as the real store's
// filters say.
func (t *Traces) list(q analytics.TracesQuery) analytics.TracesResult {
	t.mu.Lock()
	var out []analytics.TraceSummary
	for _, sp := range t.spans {
		switch {
		case !sp.Entry, sp.Org != q.Org, !slices.Contains(q.Services, sp.Service), sp.Start < q.From, sp.Start >= q.To,
			q.Release != 0 && sp.Release != q.Release, sp.Release < q.SinceRelease,
			q.Method != "" && sp.Method != q.Method, q.Route != "" && sp.Route != q.Route, sp.Duration < q.MinDuration,
			q.Status == analytics.TraceError && !sp.Error(), q.Status == analytics.Trace5xx && sp.HTTPStatus < 500:
			continue
		}
		out = append(out, analytics.TraceSummary{
			Trace: sp.Trace, Span: sp.ID, Service: sp.Service, Release: sp.Release, Start: sp.Start, Duration: sp.Duration,
			Name: sp.Name, Kind: sp.Kind, Method: sp.Method, Route: sp.Route, HTTPStatus: sp.HTTPStatus, Status: sp.Status, Kept: sp.Kept, ErrorFP: sp.ErrorFP,
		})
	}
	t.mu.Unlock()
	if q.Sort == analytics.SortSlowest {
		slices.SortStableFunc(out, func(a, b analytics.TraceSummary) int { return int(b.Duration - a.Duration) })
	} else {
		slices.SortStableFunc(out, func(a, b analytics.TraceSummary) int { return int(b.Start - a.Start) })
	}
	limit := q.Limit
	if limit == 0 {
		limit = analytics.DefaultTraces
	}
	res := analytics.TracesResult{Traces: out}
	if len(out) > limit {
		res.Traces, res.More = out[:limit], true
	}
	if res.Traces == nil {
		res.Traces = []analytics.TraceSummary{}
	}
	return res
}
