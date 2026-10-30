// Package fakestore is an in-memory analytics store behind the real
// store's HTTP API (analytics.Client's paths), for core's tests and for
// running the daemon on the fake runtime. It keeps every event and
// answers queries by scanning them; it has no sessions or rollups.
package fakestore

import (
	"bufio"
	"encoding/json/v2"
	"net/http"
	"slices"
	"strconv"
	"sync"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// Store is the fake. The zero value is not usable; use New.
type Store struct {
	token string

	mu      sync.Mutex
	events  []analytics.Event
	cursors map[string]uint64
	// down makes every call fail with 503, as a store that is starting.
	down bool
}

// New returns an empty store that wants token.
func New(token string) *Store {
	return &Store{token: token, cursors: map[string]uint64{}}
}

// Events returns a copy of the events written.
func (s *Store) Events() []analytics.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events)
}

// SetDown makes the store answer 503 (true) or work again.
func (s *Store) SetDown(down bool) {
	s.mu.Lock()
	s.down = down
	s.mu.Unlock()
}

// Handler serves the store's API.
func (s *Store) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+analytics.HealthPath, func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		h := analytics.Health{Version: "fake", Events: int64(len(s.events))}
		s.mu.Unlock()
		reply(w, h)
	})
	mux.HandleFunc("POST "+analytics.WritePath, s.auth(s.write))
	mux.HandleFunc("GET "+analytics.CursorPath, s.auth(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		pos := s.cursors[r.URL.Query().Get("source")]
		s.mu.Unlock()
		reply(w, analytics.CursorResult{Position: pos})
	}))
	mux.HandleFunc("POST "+analytics.OverviewPath, s.auth(func(w http.ResponseWriter, r *http.Request) {
		var q analytics.Query
		if json.UnmarshalRead(r.Body, &q) != nil {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		reply(w, s.overview(q))
	}))
	mux.HandleFunc("POST "+analytics.SeriesPath, s.auth(func(w http.ResponseWriter, _ *http.Request) { reply(w, []analytics.Point{}) }))
	mux.HandleFunc("POST "+analytics.BreakdownPath, s.auth(func(w http.ResponseWriter, _ *http.Request) { reply(w, []analytics.Row{}) }))
	return mux
}

// auth checks the token and that the store is up.
func (s *Store) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		down := s.down
		s.mu.Unlock()
		switch {
		case down:
			http.Error(w, "starting", http.StatusServiceUnavailable)
		case r.Header.Get("Authorization") != "Bearer "+s.token:
			http.Error(w, "wrong token", http.StatusUnauthorized)
		default:
			next(w, r)
		}
	}
}

// write takes a batch with the real store's cursor rule: a batch at or
// before its source's cursor was written before.
func (s *Store) write(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	pos, err := strconv.ParseUint(r.URL.Query().Get("position"), 10, 64)
	if source == "" || err != nil {
		http.Error(w, "source and position are required", http.StatusBadRequest)
		return
	}
	var batch []analytics.Event
	sc := bufio.NewScanner(r.Body)
	sc.Buffer(nil, analytics.MaxEventBytes)
	for sc.Scan() {
		var e analytics.Event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			batch = append(batch, e)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if pos <= s.cursors[source] {
		reply(w, analytics.WriteResult{})
		return
	}
	s.cursors[source] = pos
	s.events = append(s.events, batch...)
	reply(w, analytics.WriteResult{Written: len(batch)})
}

// overview counts the site's events in range: visitors and page views.
func (s *Store) overview(q analytics.Query) analytics.Overview {
	s.mu.Lock()
	defer s.mu.Unlock()
	var o analytics.Overview
	visitors := map[uint64]bool{}
	for _, e := range s.events {
		if e.Site != q.Site || e.Time < q.From || e.Time >= q.To {
			continue
		}
		visitors[e.Visitor] = true
		if e.Kind == analytics.KindPageview {
			o.Pageviews++
		} else {
			o.Events++
		}
	}
	o.Visitors = int64(len(visitors))
	return o
}

// reply writes v as JSON.
func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.MarshalWrite(w, v)
}
