// Command dock-analytics is Dock Analytics' store (ADR 0037): DuckDB behind
// a small HTTP API on loopback, run by Dock as an infrastructure container
// on the analytics server.
//
//	dock-analytics serve                 the store (the container's command)
//	dock-analytics traces                the trace store (its container's)
//	dock-analytics seed -db F -events N  fill F with N synthetic events
//	dock-analytics seed-traces -db F -spans N  fill a trace store with N spans
//	dock-analytics bench -db F           time the dashboards' queries on F
//	                     [-traces]       or a trace store's views
//	dock-analytics rollup -db F          build F's daily rollups
//
// seed and bench are the engine spike's tools and the query budget of
// `make perf`.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/NordTempo/tempoDock/cmd/dock-analytics/bench"
	"github.com/NordTempo/tempoDock/cmd/dock-analytics/store"
	"github.com/NordTempo/tempoDock/internal/analytics"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cmd := "serve"
	args := os.Args[1:]
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serveCmd(ctx, args)
	case "traces":
		err = tracesCmd(ctx, args)
	case "seed-traces":
		err = seedTracesCmd(ctx, args)
	case "seed":
		err = seedCmd(ctx, args)
	case "bench":
		err = benchCmd(ctx, args)
	case "rollup":
		err = rollupCmd(ctx, args)
	case "load":
		err = loadCmd(ctx, args)
	case "sqlite-compare":
		err = sqliteCmd(ctx, args)
	default:
		err = fmt.Errorf("unknown command %q; use serve, seed or bench", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "dock-analytics:", err)
		os.Exit(1)
	}
}

// storeFlags are the DuckDB settings every command takes.
type storeFlags struct {
	db       string
	memoryMB int64
	threads  int
}

// register adds the flags to fs.
func (f *storeFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.db, "db", envOr("DOCK_ANALYTICS_DB", "/data/analytics.duckdb"), "the database file")
	fs.Int64Var(&f.memoryMB, "memory-mb", 192, "DuckDB's memory_limit in MB; keep it below the container's limit")
	fs.IntVar(&f.threads, "threads", 2, "threads per query")
}

// open opens the store the flags describe.
func (f *storeFlags) open(ctx context.Context) (*store.Store, error) {
	return store.Open(ctx, f.db, store.Options{MemoryLimit: f.memoryMB << 20, Threads: f.threads})
}

// envOr is the environment variable key, or def when it is unset.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// seedCmd fills a database with synthetic traffic (bench.Seed).
func seedCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	var sf storeFlags
	sf.register(fs)
	events := fs.Int("events", 1_000_000, "about how many events to write")
	days := fs.Int("days", 365, "days of traffic, ending at -end")
	end := fs.String("end", "2026-10-30", "the day the traffic ends (UTC, exclusive)")
	_ = fs.Parse(args)
	endT, err := time.Parse(time.DateOnly, *end)
	if err != nil {
		return err
	}
	s, err := sf.open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	start := time.Now()
	n, err := bench.Seed(ctx, storeWriter{s}, bench.SeedOptions{
		Events: *events, Days: *days, End: endT, Seed: 1,
		Progress: func(day, days, written int) {
			if day%30 == 0 || day == days {
				el := time.Since(start)
				fmt.Fprintf(os.Stderr, "day %d/%d: %d events, %.0f events/s\n", day, days, written, float64(written)/el.Seconds())
			}
		},
	})
	if err != nil {
		return err
	}
	el := time.Since(start)
	rstart := time.Now()
	if err := s.BuildRollups(ctx); err != nil {
		return err
	}
	fmt.Printf("rolled up in %s\n", time.Since(rstart).Round(time.Millisecond))
	if err := s.Checkpoint(ctx); err != nil {
		return err
	}
	st, err := s.Stats(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("seeded %d events in %s (%.0f events/s); database %.1f MB, %.1f bytes/event\n",
		n, el.Round(time.Second), float64(n)/el.Seconds(), float64(st.FileBytes)/1e6, float64(st.FileBytes)/float64(max(1, st.Events)))
	return nil
}

// seedTracesCmd fills a trace store with about -spans spans over -days
// days ending now (bench.SeedSpans), and reports the rate and the file's
// size.
func seedTracesCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("seed-traces", flag.ExitOnError)
	db := fs.String("db", "", "the trace store's database file")
	n := fs.Int("spans", 2_000_000, "about how many spans to write (the store keeps 30 days)")
	days := fs.Int("days", 7, "days of traces, ending now")
	_ = fs.Parse(args)
	if *db == "" {
		return errors.New("-db is required")
	}
	t, err := store.OpenTraces(ctx, *db, store.Options{MemoryLimit: 96 << 20})
	if err != nil {
		return err
	}
	defer func() { _ = t.Close() }()
	start := time.Now()
	written, err := bench.SeedSpans(ctx, t, bench.SeedSpansOptions{
		Spans: *n, Days: *days, End: time.Now().UTC(), Seed: 1,
		Progress: func(day, days, written int) {
			fmt.Fprintf(os.Stderr, "day %d/%d: %d spans, %.0f spans/s\n", day, days, written, float64(written)/time.Since(start).Seconds())
		},
	})
	if err != nil {
		return err
	}
	el := time.Since(start)
	if err := t.Checkpoint(ctx); err != nil {
		return err
	}
	size, err := t.FileBytes(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("seeded %d spans in %s (%.0f spans/s); database %.1f MB, %.1f bytes/span\n",
		written, el.Round(time.Second), float64(written)/el.Seconds(), float64(size)/1e6, float64(size)/float64(max(1, written)))
	return nil
}

// storeWriter writes seeded events through the store's real write path,
// with the seed's stand-in locations.
type storeWriter struct{ s *store.Store }

// Write implements bench.Writer.
func (w storeWriter) Write(ctx context.Context, source string, pos uint64, events []analytics.Event) (int, error) {
	n, _, err := w.s.Write(ctx, source, pos, events, func(p string) store.Location {
		c, r, city := bench.SeedLocator(p)
		return store.Location{Country: c, Region: r, City: city}
	})
	return n, err
}

// benchCmd times the dashboards on a seeded database (bench.Run).
func benchCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	var sf storeFlags
	sf.register(fs)
	runs := fs.Int("runs", 20, "dashboard loads per case")
	parallel := fs.Int("parallel", 4, "queries of a view run at once (a dashboard is one where the store has it)")
	ranges := fs.String("ranges", "24h,30d,365d", "the ranges to time")
	end := fs.String("end", "2026-10-30", "the end of every range (the seed's -end)")
	url := fs.String("url", "", "time a running store's HTTP API (token in DOCK_ANALYTICS_TOKEN) instead of opening -db")
	asJSON := fs.Bool("json", false, "print the results as JSON (for make perf) instead of a table")
	traces := fs.Bool("traces", false, "time a trace store's views instead (-db or -url is the trace store), on spans seed-traces wrote; ranges end now")
	spanDays := fs.Int("span-days", 7, "the days of traces seeded (seed-traces -days)")
	_ = fs.Parse(args)
	endT, err := time.Parse(time.DateOnly, *end)
	if err != nil {
		return err
	}
	if *traces {
		var tq bench.TraceQuerier
		if *url != "" {
			tq = &analytics.Client{BaseURL: *url, Token: os.Getenv("DOCK_ANALYTICS_TOKEN")}
		} else {
			t, err := store.OpenTraces(ctx, sf.db, store.Options{MemoryLimit: sf.memoryMB << 20, Threads: sf.threads})
			if err != nil {
				return err
			}
			defer func() { _ = t.Close() }()
			tq = t
		}
		opts := bench.TraceOptions{End: time.Now().UTC(), Days: *spanDays, Runs: *runs, Out: os.Stdout}
		if *asJSON {
			opts.Out = nil
		}
		res, err := bench.RunTraces(ctx, tq, opts)
		if err != nil || !*asJSON {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(res)
	}
	var q bench.Querier
	if *url != "" {
		q = &analytics.Client{BaseURL: *url, Token: os.Getenv("DOCK_ANALYTICS_TOKEN")}
	} else {
		s, err := sf.open(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = s.Close() }()
		q = s
	}
	opts := bench.Options{End: endT, Runs: *runs, Parallel: *parallel, Ranges: bench.PickRanges(*ranges), Filters: bench.Filters, Out: os.Stdout}
	if *asJSON {
		opts.Out = nil
	}
	res, err := bench.Run(ctx, q, opts)
	if err != nil || !*asJSON {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(res)
}

// rollupCmd builds the daily rollups of a database (Store.BuildRollups),
// for one seeded before they existed.
func rollupCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("rollup", flag.ExitOnError)
	var sf storeFlags
	sf.register(fs)
	_ = fs.Parse(args)
	s, err := sf.open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	start := time.Now()
	if err := s.BuildRollups(ctx); err != nil {
		return err
	}
	fmt.Printf("rolled up in %s\n", time.Since(start).Round(time.Millisecond))
	return s.Checkpoint(ctx)
}

// loadCmd sends synthetic events to a running store over its HTTP API,
// in batches like the forwarder's, and reports the rate: the ingest
// budget of ADR 0037.
func loadCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("load", flag.ExitOnError)
	url := fs.String("url", "http://127.0.0.1:7076", "the store's address")
	events := fs.Int("events", 200_000, "about how many events to send")
	batch := fs.Int("batch", 1000, "events per write")
	rate := fs.Int("rate", 0, "events per second to send at; 0 is as fast as the store takes them")
	asJSON := fs.Bool("json", false, "print the result as JSON (for make perf)")
	spans := fs.Bool("spans", false, "send -events spans to a trace store (-url) instead of events")
	_ = fs.Parse(args)
	c := &analytics.Client{BaseURL: *url, Token: os.Getenv("DOCK_ANALYTICS_TOKEN")}
	if *spans {
		return loadSpans(ctx, c, *events, *batch, *asJSON)
	}
	lw := &loadWriter{c: c, batch: *batch, rate: *rate, source: "load-" + strconv.FormatInt(time.Now().UnixNano(), 36)}
	start := time.Now()
	// Today's traffic, so the events land after anything seeded.
	n, err := bench.Seed(ctx, lw, bench.SeedOptions{Events: *events, Days: 1, End: time.Now().UTC().Add(24 * time.Hour), Seed: uint64(time.Now().UnixNano())})
	if err != nil {
		return err
	}
	el := time.Since(start)
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(LoadResult{Events: n, Seconds: el.Seconds(), WriteP95: pctOf(lw.times, 95)})
	}
	fmt.Printf("sent %d events in %d writes in %s: %.0f events/s; write p50 %.1f ms, p95 %.1f ms\n",
		n, len(lw.times), el.Round(time.Millisecond), float64(n)/el.Seconds(), msOf(pctOf(lw.times, 50)), msOf(pctOf(lw.times, 95)))
	return nil
}

// LoadResult is loadCmd's -json output.
type LoadResult struct {
	Events   int
	Seconds  float64
	WriteP95 time.Duration
}

// loadWriter sends seeded events in batches of batch, at most rate a
// second.
type loadWriter struct {
	c      *analytics.Client
	batch  int
	rate   int
	source string
	pos    uint64
	sent   int
	start  time.Time
	times  []time.Duration
}

// Write implements bench.Writer: it sends events over HTTP in batches.
func (w *loadWriter) Write(ctx context.Context, _ string, _ uint64, events []analytics.Event) (int, error) {
	if w.start.IsZero() {
		w.start = time.Now()
	}
	n := 0
	for len(events) > 0 {
		b := events[:min(w.batch, len(events))]
		events = events[len(b):]
		if w.rate > 0 {
			due := w.start.Add(time.Duration(float64(w.sent) / float64(w.rate) * float64(time.Second)))
			time.Sleep(time.Until(due))
		}
		w.pos++
		t := time.Now()
		k, err := w.c.Write(ctx, w.source, w.pos, b)
		w.times = append(w.times, time.Since(t))
		if err != nil {
			return n, err
		}
		n += k
		w.sent += len(b)
	}
	return n, nil
}

// loadSpans sends about n spans of the last hour to a running store in
// batches of batch, as fast as it takes them, and reports the rate.
func loadSpans(ctx context.Context, c *analytics.Client, n, batch int, asJSON bool) error {
	lw := &spanLoadWriter{c: c, source: "load-spans-" + strconv.FormatInt(time.Now().UnixNano(), 36)}
	start := time.Now()
	written, err := bench.SeedSpans(ctx, lw, bench.SeedSpansOptions{Spans: n, Days: 1, End: time.Now().UTC(), Seed: uint64(time.Now().UnixNano()), Batch: batch, Source: lw.source})
	if err != nil {
		return err
	}
	el := time.Since(start)
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(LoadResult{Events: written, Seconds: el.Seconds(), WriteP95: pctOf(lw.times, 95)})
	}
	fmt.Printf("sent %d spans in %d writes in %s: %.0f spans/s; write p50 %.1f ms, p95 %.1f ms\n",
		written, len(lw.times), el.Round(time.Millisecond), float64(written)/el.Seconds(), msOf(pctOf(lw.times, 50)), msOf(pctOf(lw.times, 95)))
	return nil
}

// spanLoadWriter sends spans over HTTP, timing each write.
type spanLoadWriter struct {
	c      *analytics.Client
	source string
	times  []time.Duration
}

// WriteSpans implements bench.SpanWriter.
func (w *spanLoadWriter) WriteSpans(ctx context.Context, source string, pos uint64, spans []analytics.Span, lim analytics.TraceLimits) (analytics.SpanWriteResult, error) {
	t := time.Now()
	res, err := w.c.WriteSpans(ctx, source, pos, spans, lim)
	w.times = append(w.times, time.Since(t))
	return res, err
}

// pctOf is the p-th percentile of ds.
func pctOf(ds []time.Duration, p int) time.Duration {
	s := slices.Clone(ds)
	slices.Sort(s)
	if len(s) == 0 {
		return 0
	}
	return s[min(len(s)-1, len(s)*p/100)]
}

// msOf is d in milliseconds.
func msOf(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// sqliteCmd copies a seeded database into SQLite and times the same
// dashboards there (bench.SQLite), for the record in ADR 0037.
func sqliteCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sqlite-compare", flag.ExitOnError)
	var sf storeFlags
	sf.register(fs)
	out := fs.String("sqlite", "", "the SQLite file to fill (created)")
	runs := fs.Int("runs", 5, "dashboard loads per case")
	end := fs.String("end", "2026-10-30", "the end of every range (the seed's -end)")
	_ = fs.Parse(args)
	endT, err := time.Parse(time.DateOnly, *end)
	if err != nil {
		return err
	}
	lite, err := bench.OpenSQLite(*out)
	if err != nil {
		return err
	}
	defer func() { _ = lite.Close() }()
	if _, err := os.Stat(*out + ".done"); err != nil {
		duck, err := sql.Open("duckdb", sf.db+"?access_mode=READ_ONLY")
		if err != nil {
			return err
		}
		rows, err := duck.QueryContext(ctx, "SELECT epoch_ms(ts), "+strings.TrimPrefix(bench.SQLiteColumns, "ts, ")+" FROM events")
		if err != nil {
			return err
		}
		start := time.Now()
		n, err := lite.CopyFrom(ctx, rows)
		_ = rows.Close()
		_ = duck.Close()
		if err != nil {
			return err
		}
		fi, _ := os.Stat(*out)
		fmt.Printf("copied %d events in %s; SQLite file %.1f MB\n", n, time.Since(start).Round(time.Second), float64(fi.Size())/1e6)
		_ = os.WriteFile(*out+".done", nil, 0o600)
	}
	_, err = bench.Run(ctx, lite, bench.Options{
		End: endT, Runs: *runs, Parallel: 4, Ranges: bench.Ranges, Out: os.Stdout,
		Filters: []analytics.Filter{bench.Filters[0], bench.Filters[1], bench.Filters[6]},
	})
	return err
}
