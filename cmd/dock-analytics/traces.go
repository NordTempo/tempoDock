package main

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/NordTempo/tempoDock/cmd/dock-analytics/store"
	"github.com/NordTempo/tempoDock/internal/analytics"
)

// tracesCmd runs the trace store's HTTP API until ctx ends: spans in a
// database of their own (store.Traces), in a process and container of
// their own, so a backlog of spans can't take the analytics store's
// memory. analytics.Client is its client too; the token comes from
// DOCK_ANALYTICS_TOKEN as for serve.
func tracesCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("traces", flag.ExitOnError)
	db := fs.String("db", envOr("DOCK_TRACES_DB", "/data/traces.duckdb"), "the database file")
	listen := fs.String("listen", envOr("DOCK_TRACES_LISTEN", ":7079"), "address to serve the API on")
	memoryMB := fs.Int64("memory-mb", 128, "DuckDB's memory_limit in MB; keep it well below the container's limit")
	threads := fs.Int("threads", 2, "threads per query")
	queries := fs.Int("queries", 1, "how many queries may run at once")
	trimMB := fs.Int64("trim-above-mb", 128, "give cached memory back after a query that leaves the process above this many MB")
	_ = fs.Parse(args)
	token := os.Getenv("DOCK_ANALYTICS_TOKEN")
	if len(token) < 32 {
		return errors.New("DOCK_ANALYTICS_TOKEN must be set to the install's analytics token (at least 32 characters); Dock sets it when it starts the container")
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	t, err := store.OpenTraces(ctx, *db, store.Options{MemoryLimit: *memoryMB << 20, Threads: *threads})
	if err != nil {
		return err
	}
	defer func() { _ = t.Close() }()
	a := newTracesAPI(t, token, log, max(1, *queries), *trimMB<<20)
	log.Info("dock-analytics traces listening", "version", version, "addr", *listen, "db", *db)
	return a.serve(ctx, *listen, a.tracesHandler(t))
}

// newTracesAPI returns the API of the trace store t.
func newTracesAPI(t *store.Traces, token string, log *slog.Logger, queries int, trimAbove int64) *api {
	return &api{trims: t, token: []byte(token), log: log, slots: make(chan struct{}, queries), trimAbove: trimAbove}
}

// tracesHandler routes the trace store's calls.
func (a *api) tracesHandler(t *store.Traces) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+analytics.HealthPath, func(w http.ResponseWriter, r *http.Request) {
		n, err := t.FileBytes(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		reply(w, analytics.Health{Version: version, FileBytes: n})
	})
	mux.Handle("POST "+analytics.SpansWritePath, a.auth(func(w http.ResponseWriter, r *http.Request) { a.writeSpans(w, r, t) }))
	mux.Handle("POST "+analytics.MetricsWritePath, a.auth(func(w http.ResponseWriter, r *http.Request) { a.writeMetrics(w, r, t) }))
	mux.Handle("GET "+analytics.SpansCursorPath, a.auth(func(w http.ResponseWriter, r *http.Request) {
		pos, err := t.SpansCursor(r.Context(), r.URL.Query().Get("source"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		reply(w, analytics.CursorResult{Position: pos})
	}))
	mux.Handle("POST "+analytics.TracesPath, a.auth(query(a, t.Traces)))
	mux.Handle("POST "+analytics.TracePath, a.auth(query(a, t.Trace)))
	mux.Handle("POST "+analytics.ErrorTracePath, a.auth(query(a, t.ErrorTrace)))
	mux.Handle("POST "+analytics.MetricNamesPath, a.auth(query(a, t.MetricNames)))
	mux.Handle("POST "+analytics.MetricsQueryPath, a.auth(query(a, t.QueryMetrics)))
	mux.Handle("POST "+analytics.TracesPrunePath, a.auth(func(w http.ResponseWriter, r *http.Request) {
		var q analytics.TracePrune
		if err := json.UnmarshalRead(io.LimitReader(r.Body, 4<<10), &q); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		res, err := t.PruneTraces(r.Context(), q)
		if err != nil {
			a.log.Error("prune traces", "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		reply(w, res)
	}))
	return mux
}

// writeSpans appends a batch of JSON-line spans (analytics.SpansWritePath)
// under the caps in its query. Like an events write, lines that aren't
// spans are skipped and counted, and the batch's position is written
// either way, so one bad line can't hold a spool up.
func (a *api) writeSpans(w http.ResponseWriter, r *http.Request, t *store.Traces) {
	q := r.URL.Query()
	source := q.Get("source")
	pos, err := strconv.ParseUint(q.Get("position"), 10, 64)
	if source == "" || err != nil {
		http.Error(w, "source and position are required", http.StatusBadRequest)
		return
	}
	var lim analytics.TraceLimits
	for name, v := range map[string]*int64{"service_day": &lim.ServiceDay, "org_day": &lim.OrgDay, "error_day": &lim.ErrorDay, "max_bytes": &lim.MaxBytes} {
		if s := q.Get(name); s != "" {
			if *v, err = strconv.ParseInt(s, 10, 64); err != nil {
				http.Error(w, name+" must be a number", http.StatusBadRequest)
				return
			}
		}
	}
	sc := bufio.NewScanner(r.Body)
	sc.Buffer(make([]byte, 0, 8<<10), analytics.MaxSpanBytes)
	var spans []analytics.Span
	skipped := 0
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		if len(spans)+skipped == analytics.MaxWriteBatch {
			http.Error(w, fmt.Sprintf("a write takes at most %d spans", analytics.MaxWriteBatch), http.StatusBadRequest)
			return
		}
		var sp analytics.Span
		if json.Unmarshal(sc.Bytes(), &sp) != nil {
			skipped++
			continue
		}
		spans = append(spans, sp)
	}
	if err := sc.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res, err := t.WriteSpans(r.Context(), source, pos, spans, lim)
	if err != nil {
		a.log.Error("write spans", "source", source, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if skipped > 0 {
		a.log.Warn("write spans: lines that aren't spans were skipped", "source", source, "skipped", skipped)
	}
	res.Skipped = skipped
	reply(w, res)
}

// writeMetrics appends a batch of JSON-line metric points
// (analytics.MetricsWritePath) under the caps in its query, as writeSpans
// does spans.
func (a *api) writeMetrics(w http.ResponseWriter, r *http.Request, t *store.Traces) {
	q := r.URL.Query()
	source := q.Get("source")
	pos, err := strconv.ParseUint(q.Get("position"), 10, 64)
	if source == "" || err != nil {
		http.Error(w, "source and position are required", http.StatusBadRequest)
		return
	}
	var lim analytics.MetricLimits
	for name, set := range map[string]func(int64){
		"series": func(v int64) { lim.Series = int(v) }, "names": func(v int64) { lim.Names = int(v) }, "max_bytes": func(v int64) { lim.MaxBytes = v },
	} {
		if s := q.Get(name); s != "" {
			v, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				http.Error(w, name+" must be a number", http.StatusBadRequest)
				return
			}
			set(v)
		}
	}
	sc := bufio.NewScanner(r.Body)
	sc.Buffer(make([]byte, 0, 4<<10), analytics.MaxMetricLine)
	var points []analytics.MetricPoint
	skipped := 0
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		if len(points)+skipped == analytics.MaxWriteBatch {
			http.Error(w, fmt.Sprintf("a write takes at most %d points", analytics.MaxWriteBatch), http.StatusBadRequest)
			return
		}
		var p analytics.MetricPoint
		if json.Unmarshal(sc.Bytes(), &p) != nil {
			skipped++
			continue
		}
		points = append(points, p)
	}
	if err := sc.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res, err := t.WriteMetrics(r.Context(), source, pos, points, lim)
	if err != nil {
		a.log.Error("write metric points", "source", source, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if skipped > 0 {
		a.log.Warn("write metric points: lines that aren't points were skipped", "source", source, "skipped", skipped)
	}
	res.Skipped = skipped
	reply(w, res)
}
