package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
