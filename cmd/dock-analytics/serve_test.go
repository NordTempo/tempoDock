package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/cmd/dock-analytics/store"
	"github.com/NordTempo/tempoDock/internal/analytics"
)

const testToken = "0123456789abcdef0123456789abcdef"

// server runs the API on a temp store and returns a client for it.
func server(t *testing.T) (*analytics.Client, *httptest.Server) {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "a.duckdb"), store.Options{MemoryLimit: 128 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	srv := httptest.NewServer(newAPI(s, testToken, slog.New(slog.DiscardHandler), 1, 0).handler())
	t.Cleanup(srv.Close)
	return &analytics.Client{BaseURL: srv.URL, Token: testToken}, srv
}

func TestAPIWritesAndAnswers(t *testing.T) {
	c, _ := server(t)
	ctx := context.Background()
	day := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	events := []analytics.Event{
		{Time: day.UnixMilli(), Site: 1, Hostname: "a.example", Kind: analytics.KindPageview, Path: "/", Visitor: 1},
		{Time: day.Add(time.Minute).UnixMilli(), Site: 1, Hostname: "a.example", Kind: analytics.KindEvent, Name: "signup", Props: `{"plan":"pro"}`, Path: "/", Visitor: 1},
	}
	if n, err := c.Write(ctx, "server-1", 1, events); err != nil || n != 2 {
		t.Fatalf("write = %d, %v", n, err)
	}
	if n, err := c.Write(ctx, "server-1", 1, events); err != nil || n != 0 {
		t.Errorf("the same batch again = %d, %v; want 0", n, err)
	}
	if pos, err := c.Cursor(ctx, "server-1"); err != nil || pos != 1 {
		t.Errorf("cursor = %d, %v", pos, err)
	}
	q := analytics.Query{Site: 1, From: day.Add(-time.Hour).UnixMilli(), To: day.Add(time.Hour).UnixMilli()}
	o, err := c.Overview(ctx, q)
	if err != nil || o.Visitors != 1 || o.Pageviews != 1 || o.Events != 1 {
		t.Errorf("overview = %+v, %v", o, err)
	}
	rows, err := c.Breakdown(ctx, analytics.BreakdownQuery{Query: q, Dimension: analytics.DimEvent})
	if err != nil || len(rows) != 1 || rows[0].Value != "signup" {
		t.Errorf("events = %+v, %v", rows, err)
	}

	// A bad query is the caller's mistake, with the store's reason.
	_, err = c.Breakdown(ctx, analytics.BreakdownQuery{Query: q, Dimension: "nope"})
	var se *analytics.StoreError
	if !errors.As(err, &se) || se.Status != http.StatusBadRequest {
		t.Errorf("unknown dimension: %v, want a 400 StoreError", err)
	}
}

// A dashboard in one request has the numbers of its separate requests,
// which is how the client asks a store from before DashboardPath: over
// rolled-up days with raw events at both ends, and filtered.
func TestAPIDashboardMatchesItsQueries(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "a.duckdb"), store.Options{MemoryLimit: 128 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	h := newAPI(s, testToken, slog.New(slog.DiscardHandler), 1, 0).handler()
	srv := httptest.NewServer(h)
	defer srv.Close()
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == analytics.DashboardPath {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer old.Close()
	c, parts := &analytics.Client{BaseURL: srv.URL, Token: testToken}, &analytics.Client{BaseURL: old.URL, Token: testToken}

	day := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	var events []analytics.Event
	for i := range 60 {
		events = append(events, analytics.Event{Time: day.Add(time.Duration(i) * 2 * time.Hour).UnixMilli(), Site: 1, Hostname: "a.example",
			Kind: analytics.KindPageview, Path: []string{"/", "/a", "/b"}[i%3], Visitor: uint64(i%7 + 1),
			Browser: []string{"Firefox", "Chrome"}[i%2], Channel: "direct"})
	}
	if _, err := c.Write(ctx, "server-1", 1, events); err != nil {
		t.Fatal(err)
	}
	if err := s.BuildRollups(ctx); err != nil {
		t.Fatal(err)
	}
	q := analytics.DashboardQuery{Query: analytics.Query{Site: 1, From: day.Add(6 * time.Hour).UnixMilli(), To: day.Add(4*24*time.Hour + 6*time.Hour).UnixMilli()},
		Bucket: analytics.BucketDay, Dimensions: []analytics.Dimension{analytics.DimPage, analytics.DimEntryPage, analytics.DimBrowser}, Limit: 2}
	for _, fs := range [][]analytics.Filter{nil, {{Dimension: analytics.DimBrowser, Op: analytics.OpIs, Value: "Firefox"}}} {
		q.Filters = fs
		got, err := c.Dashboard(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		want, err := parts.Dashboard(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) || got.Overview.Pageviews == 0 || got.Previous.Pageviews == 0 || len(got.Breakdowns[analytics.DimPage]) != 2 {
			t.Errorf("filters %v: dashboard = %+v, want %+v from the separate requests", fs, got, want)
		}
	}

	q.Dimensions = append(q.Dimensions, "nope")
	_, err = c.Dashboard(ctx, q)
	if se, ok := errors.AsType[*analytics.StoreError](err); !ok || se.Status != http.StatusBadRequest {
		t.Errorf("an unknown dimension: %v, want a 400 StoreError", err)
	}
}

func TestAPINeedsTheToken(t *testing.T) {
	_, srv := server(t)
	for _, auth := range []string{"", "Bearer wrong", "Basic " + testToken} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+analytics.CursorPath+"?source=x", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Authorization %q: status %d, want 401", auth, resp.StatusCode)
		}
	}
	// Health is open: Dock asks it to see whether the container is up.
	resp, err := http.Get(srv.URL + analytics.HealthPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("health: %d", resp.StatusCode)
	}
}

func TestClientReportsAStoreThatIsDown(t *testing.T) {
	c := &analytics.Client{BaseURL: "http://127.0.0.1:1", Token: testToken}
	if _, err := c.Health(context.Background()); !errors.Is(err, analytics.ErrOffline) {
		t.Errorf("err = %v, want ErrOffline", err)
	}
}

func TestReadEvents(t *testing.T) {
	line := `{"t":1,"s":1,"p":"/` + strings.Repeat("a", analytics.MaxEventBytes) + `"}` + "\n"
	if _, _, err := readEvents(strings.NewReader(line)); err == nil {
		t.Error("an event over MaxEventBytes was read")
	}
	ev, skipped, err := readEvents(strings.NewReader(`{"t":1,"s":1,"p":"/"}` + "\nnot an event\n"))
	if err != nil || len(ev) != 1 || skipped != 1 {
		t.Errorf("a small event and a bad line: %d events, %d skipped, %v", len(ev), skipped, err)
	}
}

func TestClientReportsABusyStore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "busy", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := &analytics.Client{BaseURL: srv.URL, Token: testToken}
	if _, err := c.Overview(context.Background(), analytics.Query{}); !errors.Is(err, analytics.ErrBusy) || errors.Is(err, analytics.ErrOffline) {
		t.Errorf("err = %v, want ErrBusy (not offline)", err)
	}
}

// A move (ADR 0037): one store's snapshot fills an empty one, which exits
// to reopen on it with the same events, cursors and replays; a store with
// events refuses one.
func TestAPIMovesTheDatabase(t *testing.T) {
	ctx := context.Background()
	from, _ := server(t)
	day := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	if _, err := from.Write(ctx, "server-1", 7, []analytics.Event{{Time: day.UnixMilli(), Site: 1, Kind: analytics.KindPageview, Path: "/", Visitor: 1}}); err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(`[{"timestamp":1}]`))
	_ = zw.Close()
	line, _ := json.Marshal(analytics.ReplayChunk{Time: day.UnixMilli(), Site: 1, Segment: 0xa1, First: day.UnixMilli(), Last: day.UnixMilli(),
		Clock: day.UnixMilli(), Events: 1, Visitor: 1, Data: gz.Bytes()})
	if res, err := from.WriteReplayLines(ctx, "replay:server-1", 3, line); err != nil || res.Written != 1 {
		t.Fatalf("replay write: %+v, %v", res, err)
	}

	db := filepath.Join(t.TempDir(), "b.duckdb")
	s, err := store.Open(ctx, db, store.Options{MemoryLimit: 128 << 20})
	if err != nil {
		t.Fatal(err)
	}
	a := newAPI(s, testToken, slog.New(slog.DiscardHandler), 1, 0)
	exited := false
	a.db, a.exit = db, func() { exited = true }
	srv := httptest.NewServer(a.handler())
	defer srv.Close()
	to := &analytics.Client{BaseURL: srv.URL, Token: testToken}

	snap, err := from.Snapshot(ctx)
	if err != nil || snap.Events != 1 {
		t.Fatalf("snapshot = %+v, %v; want 1 event", snap, err)
	}
	got, err := to.Restore(ctx, snap, snap.Size)
	_ = snap.Close()
	if err != nil || got.Bytes != snap.Size || len(got.SHA256) != 64 || !exited {
		t.Fatalf("restore = %+v, %v (exited %v); want %d bytes", got, err, exited, snap.Size)
	}
	_ = s.Close()
	s, err = store.Open(ctx, db, store.Options{MemoryLimit: 128 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if st, err := s.Stats(ctx); err != nil || st.Events != 1 {
		t.Errorf("after the restore: %+v, %v; want 1 event", st, err)
	}
	if pos, err := s.Cursor(ctx, "server-1"); err != nil || pos != 7 {
		t.Errorf("cursor after the restore = %d, %v; want 7", pos, err)
	}
	var data bytes.Buffer
	if err := s.ReplayData(ctx, 1, []string{"00000000000000a1"}, func() {}, &data); err != nil || !strings.HasSuffix(data.String(), "\n[{\"timestamp\":1}]\n") {
		t.Errorf("replay after the restore: %q, %v", data.String(), err)
	}

	snap, err = from.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snap.Close() }()
	var se *analytics.StoreError
	if _, err := from.Restore(ctx, snap, snap.Size); !errors.As(err, &se) || se.Status != http.StatusConflict {
		t.Errorf("restore into a store with events = %v; want a 409", err)
	}
}

// A write's limits travel with it: the client's reach the store, which
// answers what it dropped and which site went over; and a limit that
// isn't a number is the caller's error.
func TestAPIAppliesAWritesLimits(t *testing.T) {
	c, srv := server(t)
	c.Limits = analytics.StoreLimits{SiteDayEvents: 2, SiteDayReplayBytes: -1, MaxBytes: -1}
	now := time.Now().UnixMilli()
	var lines []byte
	for i := range 5 {
		b, _ := json.Marshal(analytics.Event{Time: now, Site: 7, Service: 1, Kind: analytics.KindPageview, Path: "/", Visitor: uint64(i + 1)})
		lines = append(append(lines, b...), '\n')
	}
	res, err := c.WriteLines(context.Background(), "server-1", 1, lines)
	if err != nil || res.Written != 2 || res.Rejected != 3 || len(res.OverBudget) != 1 || res.OverBudget[0] != 7 {
		t.Fatalf("5 events with a budget of 2: %+v, %v", res, err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+analytics.WritePath+"?source=s&position=9&max_bytes=lots", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a limit that isn't a number: %d", resp.StatusCode)
	}
}
