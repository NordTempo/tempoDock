// Package fakestore is an in-memory analytics store behind the real
// store's HTTP API (analytics.Client's paths), for core's tests and for
// running the daemon on the fake runtime. It keeps every event and
// answers queries by scanning them; it has no sessions or rollups, so
// sessions, bounce rate, durations and entry and exit pages stay empty.
// Errors and Web Vitals are kept apart, as the real store keeps them.
package fakestore

import (
	"bufio"
	"bytes"
	"cmp"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"io"
	"maps"
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
	errs    []analytics.Event
	vitals  []analytics.Event
	groups  map[[2]uint64]bool
	replays []analytics.ReplayChunk
	cursors map[string]uint64
	// down makes every call fail with 503, as a store that is starting.
	down bool
	// old makes DashboardPath answer 404, as a store from before it.
	old bool
	// calls counts the requests to each path.
	calls map[string]int
}

// New returns an empty store that wants token.
func New(token string) *Store {
	return &Store{token: token, cursors: map[string]uint64{}, groups: map[[2]uint64]bool{}, calls: map[string]int{}}
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

// SetOld makes the store answer DashboardPath 404, as one from before
// the path (true), or answer it again.
func (s *Store) SetOld(old bool) {
	s.mu.Lock()
	s.old = old
	s.mu.Unlock()
}

// Calls returns how many requests each path has had.
func (s *Store) Calls() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.calls)
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
	mux.HandleFunc("POST "+analytics.DashboardPath, s.auth(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		old := s.old
		s.mu.Unlock()
		var q analytics.DashboardQuery
		switch {
		case old:
			http.NotFound(w, r)
			return
		case json.UnmarshalRead(r.Body, &q) != nil:
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		d := analytics.Dashboard{Overview: s.overview(q.Query), Previous: s.overview(q.Previous()),
			Series: s.series(analytics.SeriesQuery{Query: q.Query, Bucket: q.Bucket}), Breakdowns: map[analytics.Dimension][]analytics.Row{}}
		for _, dim := range q.Dimensions {
			d.Breakdowns[dim] = s.breakdown(analytics.BreakdownQuery{Query: q.Query, Dimension: dim, Limit: q.Limit})
		}
		reply(w, d)
	}))
	mux.HandleFunc("POST "+analytics.LivePath, s.auth(func(w http.ResponseWriter, r *http.Request) {
		var q analytics.LiveQuery
		if json.UnmarshalRead(r.Body, &q) != nil {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		reply(w, s.live(q))
	}))
	mux.HandleFunc("POST "+analytics.ErrorsPath, s.auth(func(w http.ResponseWriter, r *http.Request) {
		var q analytics.ErrorsQuery
		if json.UnmarshalRead(r.Body, &q) != nil {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		groups := s.errorGroups(q.Query)
		res := analytics.ErrorsResult{Groups: groups, Summary: analytics.ErrorSummary{Groups: int64(len(groups))}}
		for _, g := range groups {
			res.Summary.Errors += g.Count
		}
		res.Series, res.Summary.Visitors = s.errorSeries(q)
		res.Summary.Sessions = res.Summary.Visitors
		reply(w, res)
	}))
	mux.HandleFunc("POST "+analytics.ErrorPath, s.auth(func(w http.ResponseWriter, r *http.Request) {
		var q analytics.ErrorQuery
		if json.UnmarshalRead(r.Body, &q) != nil {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		for _, g := range s.errorGroups(q.Query) {
			if g.Group == q.Group {
				reply(w, analytics.ErrorDetail{ErrorGroup: g, Recent: s.recentErrors(q)})
				return
			}
		}
		http.Error(w, "this site has no error group "+q.Group, http.StatusBadRequest)
	}))
	mux.HandleFunc("POST "+analytics.VitalsPath, s.auth(func(w http.ResponseWriter, r *http.Request) {
		var q analytics.VitalsQuery
		if json.UnmarshalRead(r.Body, &q) != nil {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		reply(w, s.vitalsOf(q))
	}))
	mux.HandleFunc("POST "+analytics.TrafficPath, s.auth(func(w http.ResponseWriter, r *http.Request) {
		var q analytics.TrafficQuery
		if json.UnmarshalRead(r.Body, &q) != nil {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		counts := map[[2]int64]int64{}
		for _, e := range s.events {
			ago := (q.Hour + 3_599_999 - e.Time) / 86_400_000
			if e.Kind == analytics.KindPageview && e.Time <= q.Hour+3_599_999 && ago <= int64(q.Days) && e.Time >= q.Hour-ago*86_400_000 {
				counts[[2]int64{e.Site, ago}]++
			}
		}
		s.mu.Unlock()
		out := []analytics.TrafficRow{}
		for k, n := range counts {
			out = append(out, analytics.TrafficRow{Site: k[0], DaysAgo: int(k[1]), Pageviews: n})
		}
		reply(w, out)
	}))
	mux.HandleFunc("POST "+analytics.GoalsPath, s.auth(func(w http.ResponseWriter, r *http.Request) {
		var q analytics.GoalsQuery
		if json.UnmarshalRead(r.Body, &q) != nil {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		out := make([]analytics.GoalResult, len(q.Goals))
		for i, g := range q.Goals {
			visitors := map[uint64]bool{}
			for _, e := range s.events {
				if matches(e, q.Query) && reaches(e, g) {
					visitors[e.Visitor] = true
					out[i].Count++
				}
			}
			out[i].Visitors, out[i].Sessions = int64(len(visitors)), int64(len(visitors))
		}
		reply(w, out)
	}))
	mux.HandleFunc("POST "+analytics.FunnelPath, s.auth(func(w http.ResponseWriter, r *http.Request) {
		var q analytics.FunnelQuery
		if json.UnmarshalRead(r.Body, &q) != nil {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		// The fake has no sessions: a visitor's events are one.
		step := map[uint64]int{}
		for _, e := range s.events {
			if !matches(e, q.Query) {
				continue
			}
			if n := step[e.Visitor]; n < len(q.Steps) && reaches(e, q.Steps[n]) {
				step[e.Visitor] = n + 1
			}
		}
		out := make([]analytics.FunnelStep, len(q.Steps))
		for _, n := range step {
			for i := range n {
				out[i].Sessions++
				out[i].Visitors++
			}
		}
		reply(w, out)
	}))
	mux.HandleFunc("POST "+analytics.ReplayWritePath, s.auth(s.writeReplay))
	mux.HandleFunc("POST "+analytics.ReplaysPath, s.auth(func(w http.ResponseWriter, r *http.Request) {
		var q analytics.ReplaysQuery
		if json.UnmarshalRead(r.Body, &q) != nil {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		reply(w, s.replayList(q.Site, func(ch analytics.ReplayChunk) bool { return ch.Time >= q.From && ch.Time < q.To }))
	}))
	mux.HandleFunc("POST "+analytics.ReplayPath, s.auth(func(w http.ResponseWriter, r *http.Request) {
		var q analytics.ReplayQuery
		if json.UnmarshalRead(r.Body, &q) != nil {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		for _, rp := range s.replayList(q.Site, func(analytics.ReplayChunk) bool { return true }) {
			if rp.ID == q.ID {
				reply(w, analytics.ReplayDetail{Replay: rp, SegmentList: []analytics.ReplaySegment{{ID: rp.ID, Start: rp.Start, End: rp.End}},
					Timeline: []analytics.TimelineItem{}})
				return
			}
		}
		http.Error(w, "this site has no replay "+q.ID, http.StatusBadRequest)
	}))
	mux.HandleFunc("GET "+analytics.ReplayDataPath, s.auth(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, sg := range r.URL.Query()["segment"] {
			_, _ = io.WriteString(w, `{"segment":"`+sg+`"}`+"\n")
			for _, ch := range s.replays {
				if id(ch.Site) == r.URL.Query().Get("site") && fmt.Sprintf("%016x", ch.Segment) == sg {
					if zr, err := gzip.NewReader(bytes.NewReader(ch.Data)); err == nil {
						_, _ = io.Copy(w, zr)
						_, _ = w.Write([]byte{'\n'})
					}
				}
			}
		}
	}))
	mux.HandleFunc("POST "+analytics.ReplayPrunePath, s.auth(func(w http.ResponseWriter, r *http.Request) {
		var q analytics.ReplayPrune
		if json.UnmarshalRead(r.Body, &q) != nil {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		before := len(s.replays)
		s.replays = slices.DeleteFunc(s.replays, func(ch analytics.ReplayChunk) bool { return ch.Site == q.Site && ch.Time < q.Before })
		n := before - len(s.replays)
		s.mu.Unlock()
		reply(w, analytics.PruneResult{Deleted: int64(n)})
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

// auth checks the token and that the store is up, and counts the call.
func (s *Store) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		down := s.down
		s.calls[r.URL.Path]++
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
	var res analytics.WriteResult
	for _, e := range batch {
		switch {
		case e.Kind == analytics.KindError && e.Error != nil:
			s.errs = append(s.errs, e)
			if k := [2]uint64{uint64(e.Site), e.Error.Group}; !s.groups[k] {
				s.groups[k] = true
				res.NewErrors = append(res.NewErrors, analytics.NewError{Site: e.Site, Group: groupHex(e.Error.Group),
					Type: e.Error.Type, Message: e.Name, Path: e.Path, Release: e.Release})
			}
		case e.Kind == analytics.KindVitals && e.Vitals != nil:
			s.vitals = append(s.vitals, e)
		default:
			s.events = append(s.events, e)
		}
	}
	res.Written = len(batch)
	reply(w, res)
}

// writeReplay takes replay chunks with the cursor rule of write.
func (s *Store) writeReplay(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	pos, err := strconv.ParseUint(r.URL.Query().Get("position"), 10, 64)
	if source == "" || err != nil {
		http.Error(w, "source and position are required", http.StatusBadRequest)
		return
	}
	var batch []analytics.ReplayChunk
	sc := bufio.NewScanner(r.Body)
	sc.Buffer(nil, analytics.MaxReplayLine)
	for sc.Scan() {
		var ch analytics.ReplayChunk
		if json.Unmarshal(sc.Bytes(), &ch) == nil {
			batch = append(batch, ch)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if pos <= s.cursors[source] {
		reply(w, analytics.WriteResult{})
		return
	}
	s.cursors[source] = pos
	s.replays = append(s.replays, batch...)
	reply(w, analytics.WriteResult{Written: len(batch)})
}

// Replays returns a copy of the replay chunks written.
func (s *Store) Replays() []analytics.ReplayChunk {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.replays)
}

// replayList is a site's chunks that keep passes, one replay per segment
// (the fake doesn't stitch), newest first.
func (s *Store) replayList(site int64, keep func(analytics.ReplayChunk) bool) []analytics.Replay {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []analytics.Replay{}
	at := map[uint64]int{}
	for _, ch := range s.replays {
		if ch.Site != site || !keep(ch) {
			continue
		}
		i, ok := at[ch.Segment]
		if !ok {
			i = len(out)
			at[ch.Segment] = i
			out = append(out, analytics.Replay{ID: fmt.Sprintf("%016x", ch.Segment), Start: ch.Time, Segments: 1, EntryPath: ch.Path,
				Browser: ch.Browser, OS: ch.OS, Device: ch.Device, Release: ch.Release})
		}
		out[i].End, out[i].ExitPath, out[i].Bytes = max(out[i].End, ch.Time), ch.Path, out[i].Bytes+int64(len(ch.Data))
	}
	slices.Reverse(out)
	return out
}

// groupHex is a fingerprint as the store's API writes it.
func groupHex(g uint64) string { return fmt.Sprintf("%016x", g) }

// errorGroups groups the site's errors in range that pass the filters,
// the most frequent first.
func (s *Store) errorGroups(q analytics.Query) []analytics.ErrorGroup {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []analytics.ErrorGroup{}
	at := map[uint64]int{}
	seen := map[int]map[uint64]bool{}
	for _, e := range s.errs {
		if e.Site != q.Site {
			continue
		}
		i, ok := at[e.Error.Group]
		if !ok {
			i = len(out)
			at[e.Error.Group] = i
			out = append(out, analytics.ErrorGroup{Group: groupHex(e.Error.Group), FirstSeen: e.Time, FirstRelease: e.Release})
		}
		g := &out[i]
		g.Type, g.Message, g.Frame, g.LastSeen = e.Error.Type, e.Name, e.Error.Frame, max(g.LastSeen, e.Time)
		if matches(e, q) {
			g.Count++
			if seen[i] == nil {
				seen[i] = map[uint64]bool{}
			}
			seen[i][e.Visitor] = true
		}
	}
	// The fake counts each visitor who met an error as one session too.
	for i := range out {
		out[i].Visitors = int64(len(seen[i]))
		out[i].Sessions = out[i].Visitors
	}
	out = slices.DeleteFunc(out, func(g analytics.ErrorGroup) bool { return g.Count == 0 })
	slices.SortStableFunc(out, func(a, b analytics.ErrorGroup) int { return int(b.Count - a.Count) })
	return out
}

// recentErrors are a group's latest errors in q's range, newest first.
func (s *Store) recentErrors(q analytics.ErrorQuery) []analytics.ErrorOccurrence {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []analytics.ErrorOccurrence{}
	for i := len(s.errs) - 1; i >= 0 && len(out) < analytics.MaxErrorRecent; i-- {
		if e := s.errs[i]; matches(e, q.Query) && groupHex(e.Error.Group) == q.Group {
			out = append(out, analytics.ErrorOccurrence{Time: e.Time, Path: e.Path, Browser: e.Browser, OS: e.OS, Device: e.Device,
				Country: e.Country, Service: e.Service, Release: e.Release})
		}
	}
	return out
}

// vitalsOf summarises the site's page loads with vitals in range: every
// vital's 75th percentile (nearest rank) with good and poor counts, the
// same per value of each asked dimension, and the asked metric's series.
func (s *Store) vitalsOf(q analytics.VitalsQuery) analytics.VitalsResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := analytics.VitalsResult{Breakdowns: map[analytics.Dimension][]analytics.VitalsRow{}, Series: []analytics.VitalPoint{}}
	var loads []analytics.Event
	for _, e := range s.vitals {
		if matches(e, q.Query) {
			loads = append(loads, e)
		}
	}
	out.All = vitalsRow("", loads)
	for _, d := range q.Dimensions {
		groups := map[string][]analytics.Event{}
		for _, e := range loads {
			if v, ok := value(e, d); ok {
				groups[v] = append(groups[v], e)
			}
		}
		rows := make([]analytics.VitalsRow, 0, len(groups))
		for v, es := range groups {
			rows = append(rows, vitalsRow(v, es))
		}
		if len(rows) == 0 {
			continue
		}
		slices.SortFunc(rows, func(a, b analytics.VitalsRow) int { return int(b.Loads - a.Loads) })
		out.Breakdowns[d] = rows
	}
	metric := cmp.Or(q.Metric, analytics.VitalLCP)
	width := int64(86_400_000)
	if q.Bucket == analytics.BucketHour {
		width = 3_600_000
	}
	byBucket := map[int64][]float64{}
	for _, e := range loads {
		if v := vitalValue(e.Vitals, metric); v >= 0 {
			byBucket[e.Time-e.Time%width] = append(byBucket[e.Time-e.Time%width], v)
		}
	}
	for t, vs := range byBucket {
		out.Series = append(out.Series, analytics.VitalPoint{T: t, P75: p75(vs), Samples: int64(len(vs))})
	}
	slices.SortFunc(out.Series, func(a, b analytics.VitalPoint) int { return int(a.T - b.T) })
	return out
}

// vitalLimits are each vital's good and poor thresholds (Core Web Vitals).
var vitalLimits = map[analytics.Vital][2]float64{
	analytics.VitalLCP: {2500, 4000}, analytics.VitalINP: {200, 500}, analytics.VitalCLS: {0.1, 0.25}, analytics.VitalFCP: {1800, 3000}, analytics.VitalTTFB: {800, 1800},
}

// vitalValue is one vital of a page load, -1 when not measured.
func vitalValue(v *analytics.Vitals, m analytics.Vital) float64 {
	switch m {
	case analytics.VitalINP:
		return v.INP
	case analytics.VitalCLS:
		return v.CLS
	case analytics.VitalFCP:
		return v.FCP
	case analytics.VitalTTFB:
		return v.TTFB
	}
	return v.LCP
}

// vitalsRow is every vital over some page loads.
func vitalsRow(value string, loads []analytics.Event) analytics.VitalsRow {
	stat := func(m analytics.Vital) analytics.VitalStat {
		var vs []float64
		st := analytics.VitalStat{P75: -1}
		for _, e := range loads {
			v := vitalValue(e.Vitals, m)
			if v < 0 {
				continue
			}
			vs = append(vs, v)
			if v <= vitalLimits[m][0] {
				st.Good++
			} else if v > vitalLimits[m][1] {
				st.Poor++
			}
		}
		if len(vs) > 0 {
			st.P75, st.Samples = p75(vs), int64(len(vs))
		}
		return st
	}
	return analytics.VitalsRow{Value: value, Loads: int64(len(loads)), LCP: stat(analytics.VitalLCP), INP: stat(analytics.VitalINP), CLS: stat(analytics.VitalCLS), FCP: stat(analytics.VitalFCP), TTFB: stat(analytics.VitalTTFB)}
}

// p75 is the 75th percentile by nearest rank.
func p75(vs []float64) float64 {
	vs = slices.Clone(vs)
	slices.Sort(vs)
	return vs[(len(vs)*3+3)/4-1]
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
	o.Sessions, o.BounceRate, o.Duration = sessionsOf(s.events, q)
	return o
}

// sessionGap ends a visitor's session after this long without an event,
// as the real store does.
const sessionGap = 30 * 60 * 1000

// sessionsOf splits the matching events into sessions (one visitor's
// events with no gap over sessionGap), returning how many there are, the
// share with at most one page view and the average length in ms.
func sessionsOf(events []analytics.Event, q analytics.Query) (n int64, bounce float64, avg int64) {
	byVisitor := map[uint64][]analytics.Event{}
	for _, e := range events {
		if matches(e, q) {
			byVisitor[e.Visitor] = append(byVisitor[e.Visitor], e)
		}
	}
	var bounced, total int64
	for _, es := range byVisitor {
		slices.SortFunc(es, func(a, b analytics.Event) int { return int(a.Time - b.Time) })
		start, views := es[0].Time, 0
		end := func(last int64) {
			n++
			total += last - start
			if views <= 1 {
				bounced++
			}
		}
		for i, e := range es {
			if i > 0 && e.Time-es[i-1].Time > sessionGap {
				end(es[i-1].Time)
				start, views = e.Time, 0
			}
			if e.Kind == analytics.KindPageview {
				views++
			}
		}
		end(es[len(es)-1].Time)
	}
	if n > 0 {
		bounce, avg = float64(bounced)/float64(n), total/n
	}
	return n, bounce, avg
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

// reaches says whether e meets goal g: a page view of its path (* any
// run of characters, as a prefix or suffix) or a custom event by name.
func reaches(e analytics.Event, g analytics.GoalSpec) bool {
	switch g.Kind {
	case analytics.GoalPage:
		if e.Kind != analytics.KindPageview {
			return false
		}
		if pre, post, ok := strings.Cut(g.Match, "*"); ok {
			return strings.HasPrefix(e.Path, pre) && strings.HasSuffix(e.Path, post)
		}
		return e.Path == g.Match
	case analytics.GoalEvent:
		return e.Kind == analytics.KindEvent && e.Name == g.Match
	}
	return false
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

// errorSeries buckets the site's matching errors by q's bucket (days by
// default) and counts the visitors who met one.
func (s *Store) errorSeries(q analytics.ErrorsQuery) ([]analytics.CountPoint, int64) {
	width := int64(86_400_000)
	if q.Bucket == analytics.BucketHour {
		width = 3_600_000
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[int64]int64{}
	visitors := map[uint64]bool{}
	for _, e := range s.errs {
		if !matches(e, q.Query) {
			continue
		}
		counts[e.Time-e.Time%width]++
		visitors[e.Visitor] = true
	}
	out := make([]analytics.CountPoint, 0, len(counts))
	for t, c := range counts {
		out = append(out, analytics.CountPoint{T: t, Count: c})
	}
	slices.SortFunc(out, func(a, b analytics.CountPoint) int { return int(a.T - b.T) })
	return out, int64(len(visitors))
}
