package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/cmd/dock-analytics/store"
	"github.com/NordTempo/tempoDock/internal/analytics"
)

// traceServer runs the trace store's API on a temp database and returns
// a client for it.
func traceServer(t *testing.T) *analytics.Client {
	t.Helper()
	ts, err := store.OpenTraces(context.Background(), filepath.Join(t.TempDir(), "traces.duckdb"), store.Options{MemoryLimit: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ts.Close() })
	a := newTracesAPI(ts, testToken, slog.New(slog.DiscardHandler), 1, 0)
	srv := httptest.NewServer(a.tracesHandler(ts))
	t.Cleanup(srv.Close)
	return &analytics.Client{BaseURL: srv.URL, Token: testToken}
}

// testSpan is an entry span of org 1's service 10 at t, in a trace whose
// ID has the high bit set (database/sql can't bind such a uint64).
func testSpan(t time.Time) analytics.Span {
	return analytics.Span{
		Org: 1, Project: 2, Env: 3, Service: 10, Release: 40, Trace: analytics.TraceID{0: 0xab, 15: 1}, ID: 1,
		Start: t.UnixMicro(), Duration: 1500, Name: "GET /", Kind: analytics.SpanServer, Entry: true, Method: "GET", Route: "/", HTTPStatus: 200,
	}
}

func TestTraceAPI(t *testing.T) {
	c := traceServer(t)
	ctx := context.Background()
	now := time.Now()
	sp := testSpan(now)
	if res, err := c.WriteSpans(ctx, "spans:server-1", 1, []analytics.Span{sp}, analytics.TraceLimits{ServiceDay: 1}); err != nil || res.Written != 1 {
		t.Fatalf("write = %+v, %v", res, err)
	}
	// A line that isn't a span is skipped; the cap holds the next.
	sp.ID = 2
	line, _ := json.Marshal(sp)
	res, err := c.WriteSpanLines(ctx, "spans:server-1", 2, append([]byte("nope\n"), line...), analytics.TraceLimits{ServiceDay: 1})
	if err != nil || res.Written != 0 || res.Skipped != 1 || res.Rejected != 1 {
		t.Errorf("second write = %+v, %v", res, err)
	}
	if pos, err := c.SpansCursor(ctx, "spans:server-1"); err != nil || pos != 2 {
		t.Errorf("cursor = %d, %v", pos, err)
	}
	list, err := c.Traces(ctx, analytics.TracesQuery{Org: 1, Services: []int64{10}, From: now.Add(-time.Minute).UnixMicro(), To: now.Add(time.Minute).UnixMicro()})
	if err != nil || len(list.Traces) != 1 || list.Traces[0].Trace != sp.Trace || list.Traces[0].Span != 1 {
		t.Errorf("traces = %+v, %v", list, err)
	}
	tr, err := c.Trace(ctx, analytics.TraceQuery{Org: 1, Trace: sp.Trace})
	if err != nil || len(tr.Spans) != 1 || tr.Spans[0].ID != 1 {
		t.Errorf("trace = %+v, %v", tr, err)
	}
	var se *analytics.StoreError
	if _, err := c.Traces(ctx, analytics.TracesQuery{Org: 1, Services: []int64{10}, From: 2, To: 1}); !errors.As(err, &se) || se.Status != http.StatusBadRequest {
		t.Errorf("a backwards range = %v; want a 400", err)
	}
	if h, err := c.Health(ctx); err != nil || h.FileBytes == 0 {
		t.Errorf("health = %+v, %v; want the file's size", h, err)
	}
	pruned, err := c.PruneTraces(ctx, analytics.TracePrune{Before: now.Add(48 * time.Hour).UnixMilli()})
	if err != nil || len(pruned.Days) == 0 {
		t.Errorf("prune = %+v, %v", pruned, err)
	}
}
