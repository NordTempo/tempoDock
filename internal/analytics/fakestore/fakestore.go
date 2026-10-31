// Package fakestore is an in-memory analytics store behind the real
// store's HTTP API (analytics.Client's paths), for core's tests and for
// running the daemon on the fake runtime. It keeps every event and
// answers queries by scanning them; it has no sessions or rollups, so
// sessions, bounce rate, durations and entry and exit pages stay empty.
package fakestore

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
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
	mux.HandleFunc("POST "+analytics.SeriesPath, s.auth(func(w http.ResponseWriter, r *http.Request) {
		var q analytics.SeriesQuery
		if json.UnmarshalRead(r.Body, &q) != nil {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		reply(w, s.series(q))
	}))
	mux.HandleFunc("POST "+analytics.BreakdownPath, s.auth(func(w http.ResponseWriter, r *http.Request) {
		var q analytics.BreakdownQuery
		if json.UnmarshalRead(r.Body, &q) != nil {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		reply(w, s.breakdown(q))
	}))
	mux.HandleFunc("POST "+analytics.LivePath, s.auth(func(w http.ResponseWriter, r *http.Request) {
		var q analytics.LiveQuery
		if json.UnmarshalRead(r.Body, &q) != nil {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		reply(w, s.live(q))
	}))
	mux.HandleFunc("POST "+analytics.PrunePath, s.auth(func(w http.ResponseWriter, r *http.Request) {
		var q analytics.PruneQuery
		if json.UnmarshalRead(r.Body, &q) != nil {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		kept := s.events[:0]
		var n int64
		for _, e := range s.events {
			if e.Site == q.Site && e.Time < q.Before {
				n++
				continue
			}
			kept = append(kept, e)
		}
		s.events = kept
		s.mu.Unlock()
		reply(w, analytics.PruneResult{Deleted: n})
	}))
	mux.HandleFunc("GET "+analytics.SnapshotPath, s.auth(func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		b, err := json.Marshal(snapshot{Events: s.events, Cursors: s.cursors})
		events := len(s.events)
		s.mu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.Header().Set(analytics.SnapshotEventsHeader, strconv.Itoa(events))
		_, _ = w.Write(b)
	}))
	mux.HandleFunc("PUT "+analytics.RestorePath, s.auth(s.restore))
	mux.HandleFunc("POST "+analytics.GeoIPPath, s.auth(func(w http.ResponseWriter, _ *http.Request) {
		reply(w, analytics.GeoIP{Month: "fake"})
	}))
	return mux
}

// snapshot is the fake's database file: its events and cursors as JSON.
type snapshot struct {
	Events  []analytics.Event `json:"events"`
	Cursors map[string]uint64 `json:"cursors"`
}

// restore takes a snapshot into an empty store at once (the real one
// restarts on it).
func (s *Store) restore(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var snap snapshot
	if err := json.UnmarshalRead(bytes.NewReader(raw), &snap); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) > 0 {
		http.Error(w, "this store holds events; only an empty store takes another's data", http.StatusConflict)
		return
	}
	s.events, s.cursors = snap.Events, snap.Cursors
	if s.cursors == nil {
		s.cursors = map[string]uint64{}
	}
	sum := sha256.Sum256(raw)
	reply(w, analytics.Restored{Bytes: int64(len(raw)), SHA256: hex.EncodeToString(sum[:])})
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

// overview counts the site's events in range that pass the filters:
// visitors, page views and other events.
func (s *Store) overview(q analytics.Query) analytics.Overview {
	s.mu.Lock()
	defer s.mu.Unlock()
	var o analytics.Overview
	visitors := map[uint64]bool{}
	for _, e := range s.events {
		if !matches(e, q) {
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

// series buckets the matching events by UTC hour or day (minutes for
// anything else), returning only buckets with events, in time order.
func (s *Store) series(q analytics.SeriesQuery) []analytics.Point {
	width := int64(60_000)
	switch q.Bucket {
	case analytics.BucketHour:
		width = 3_600_000
	case analytics.BucketDay:
		width = 86_400_000
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	type bucket struct {
		visitors  map[uint64]bool
		pageviews int64
	}
	buckets := map[int64]*bucket{}
	for _, e := range s.events {
		if !matches(e, q.Query) {
			continue
		}
		t := e.Time - e.Time%width
		b := buckets[t]
		if b == nil {
			b = &bucket{visitors: map[uint64]bool{}}
			buckets[t] = b
		}
		b.visitors[e.Visitor] = true
		if e.Kind == analytics.KindPageview {
			b.pageviews++
		}
	}
	out := make([]analytics.Point, 0, len(buckets))
	for t, b := range buckets {
		out = append(out, analytics.Point{T: t, Visitors: int64(len(b.visitors)), Pageviews: b.pageviews})
	}
	slices.SortFunc(out, func(a, b analytics.Point) int { return int(a.T - b.T) })
	return out
}

// breakdown counts the matching events by the dimension's value, most
// visitors first. Count is page views for pages and events for the rest.
func (s *Store) breakdown(q analytics.BreakdownQuery) []analytics.Row {
	s.mu.Lock()
	defer s.mu.Unlock()
	type row struct {
		visitors map[uint64]bool
		count    int64
	}
	rows := map[string]*row{}
	for _, e := range s.events {
		v, known := value(e, q.Dimension)
		if !known || v == "" || !applies(e, q.Dimension) || !matches(e, q.Query) {
			continue
		}
		r := rows[v]
		if r == nil {
			r = &row{visitors: map[uint64]bool{}}
			rows[v] = r
		}
		r.visitors[e.Visitor] = true
		r.count++
	}
	out := make([]analytics.Row, 0, len(rows))
	for v, r := range rows {
		out = append(out, analytics.Row{Value: v, Visitors: int64(len(r.visitors)), Count: r.count})
	}
	slices.SortFunc(out, func(a, b analytics.Row) int {
		if a.Visitors != b.Visitors {
			return int(b.Visitors - a.Visitors)
		}
		return strings.Compare(a.Value, b.Value)
	})
	return out[:min(len(out), max(q.Limit, 1))]
}

// matches says whether e is the query's site, in its range, and passes
// its filters.
func matches(e analytics.Event, q analytics.Query) bool {
	if e.Site != q.Site || e.Time < q.From || e.Time >= q.To {
		return false
	}
	for _, f := range q.Filters {
		v, known := value(e, f.Dimension)
		if !known {
			continue // a session dimension the fake doesn't keep
		}
		var ok bool
		switch f.Op {
		case analytics.OpIs:
			ok = v == f.Value
		case analytics.OpIsNot:
			ok = v != f.Value
		case analytics.OpContains:
			ok = strings.Contains(strings.ToLower(v), strings.ToLower(f.Value))
		}
		if !ok {
			return false
		}
	}
	return true
}

// applies says whether a breakdown of d counts e: pages count page views,
// custom events and outbound links their own kind, the rest every event.
func applies(e analytics.Event, d analytics.Dimension) bool {
	switch d {
	case analytics.DimPage:
		return e.Kind == analytics.KindPageview
	case analytics.DimEvent:
		return e.Kind == analytics.KindEvent
	case analytics.DimOutbound:
		return e.Kind == analytics.KindOutbound
	}
	return true
}

// value is e's value of a dimension; known is false for dimensions the
// fake doesn't keep (those of sessions, like entry pages, and UTM tags).
func value(e analytics.Event, d analytics.Dimension) (v string, known bool) {
	switch d {
	case analytics.DimPage:
		return e.Path, true
	case analytics.DimEvent, analytics.DimOutbound:
		return e.Name, true
	case analytics.DimHostname:
		return e.Hostname, true
	case analytics.DimReferrer:
		return e.Referrer, true
	case analytics.DimChannel:
		return e.Channel, true
	case analytics.DimCountry:
		return e.Country, true
	case analytics.DimRegion:
		return e.Region, true
	case analytics.DimCity:
		return e.City, true
	case analytics.DimBrowser:
		return e.Browser, true
	case analytics.DimOS:
		return e.OS, true
	case analytics.DimDevice:
		return e.Device, true
	case analytics.DimLanguage:
		return e.Language, true
	case analytics.DimService:
		return id(e.Service), true
	case analytics.DimRelease:
		return id(e.Release), true
	}
	return "", false
}

// live answers a LiveQuery by scanning the events.
func (s *Store) live(q analytics.LiveQuery) analytics.Live {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := analytics.Live{Visitors: []analytics.LiveVisitor{}, Recent: []analytics.Event{}}
	last := map[uint64]int64{}
	for i := len(s.events) - 1; i >= 0; i-- {
		e := s.events[i]
		if e.Site != q.Site || e.Time < q.Since {
			continue
		}
		last[e.Visitor] = max(last[e.Visitor], e.Time)
		if len(out.Recent) < 20 {
			out.Recent = append(out.Recent, e)
		}
	}
	for v, t := range last {
		out.Visitors = append(out.Visitors, analytics.LiveVisitor{Visitor: v, Last: t})
	}
	return out
}

// reply writes v as JSON.
func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.MarshalWrite(w, v)
}

// id is an ID as a dimension's value; "" for none, so events without a
// service or release don't count as one.
func id(n int64) string {
	if n == 0 {
		return ""
	}
	return strconv.FormatInt(n, 10)
}
