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
