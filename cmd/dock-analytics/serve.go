package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/cmd/dock-analytics/store"
	"github.com/NordTempo/tempoDock/internal/analytics"
)

// version is set at build time (-ldflags -X main.version=...).
var version = "dev"

// serveCmd runs the store's HTTP API (analytics.Client is its client)
// until ctx ends. The token comes from DOCK_ANALYTICS_TOKEN: Dock makes
// one per install and passes it to the container.
func serveCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	var sf storeFlags
	sf.register(fs)
	listen := fs.String("listen", envOr("DOCK_ANALYTICS_LISTEN", ":7076"), "address to serve the API on")
	queries := fs.Int("queries", 1, "how many queries may run at once; each can briefly hold a few hundred MB beyond -memory-mb, so raise it only with the container's memory limit")
	trimMB := fs.Int64("trim-above-mb", 192, "give cached memory back after a query that leaves the process above this many MB; keep it below the container's limit")
	_ = fs.Parse(args)
	token := os.Getenv("DOCK_ANALYTICS_TOKEN")
	if len(token) < 32 {
		return errors.New("DOCK_ANALYTICS_TOKEN must be set to the install's analytics token (at least 32 characters); Dock sets it when it starts the container")
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	s, err := sf.open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	rctx, stopRollups := context.WithCancel(ctx)
	rollupsDone := make(chan struct{})
	go func() {
		defer close(rollupsDone)
		s.RunRollups(rctx, func(format string, args ...any) { log.Error(fmt.Sprintf(format, args...)) })
	}()
	defer func() { stopRollups(); <-rollupsDone }()

	a := newAPI(s, token, log, max(1, *queries), *trimMB<<20)
	idle := &idleTrim{after: trimAfter, trim: a.trim}
	srv := &http.Server{
		Addr:              *listen,
		Handler:           idle.wrap(a.handler()),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("dock-analytics listening", "version", version, "addr", *listen, "db", sf.db)
	select {
	case err = <-errc:
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// trimAfter is how long the store waits after its last request before
// it gives back cached memory (idleTrim).
const trimAfter = time.Minute

// idleTrim runs trim once, after a minute without requests. Its timer is
// armed by a request and pushed back by the next, so it never runs while
// the store is idle and only fires once per busy spell. Without it,
// DuckDB keeps up to its memory limit cached after a dashboard or a
// backlog (Store.Trim), and Go keeps the heap a large write needed.
type idleTrim struct {
	after time.Duration
	trim  func()

	mu sync.Mutex
	t  *time.Timer
}

// wrap marks every request as activity.
func (it *idleTrim) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		it.touch()
		h.ServeHTTP(w, r)
	})
}

// touch arms the timer, or pushes it back.
func (it *idleTrim) touch() {
	it.mu.Lock()
	defer it.mu.Unlock()
	if it.t == nil {
		it.t = time.AfterFunc(it.after, it.fire)
		return
	}
	it.t.Reset(it.after)
}

// fire runs trim once the timer expires.
func (it *idleTrim) fire() {
	it.mu.Lock()
	it.t = nil
	it.mu.Unlock()
	it.trim()
}

// api serves the store over HTTP.
type api struct {
	s     *store.Store
	token []byte
	log   *slog.Logger
	// locate looks up locations; nil until a GeoIP database is loaded.
	locate store.Locator

	// slots bounds how many queries run at once.
	slots chan struct{}
	// trimAbove is the anonymous memory (bytes) above which a finished
	// query trims.
	trimAbove int64
}

// newAPI returns the API for s; every call but health needs token.
func newAPI(s *store.Store, token string, log *slog.Logger, queries int, trimAbove int64) *api {
	return &api{s: s, token: []byte(token), log: log, slots: make(chan struct{}, queries), trimAbove: trimAbove}
}

// handler routes the API's calls.
func (a *api) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+analytics.HealthPath, a.health)
	mux.Handle("POST "+analytics.WritePath, a.auth(a.write))
	mux.Handle("GET "+analytics.CursorPath, a.auth(a.cursor))
	mux.Handle("POST "+analytics.OverviewPath, a.auth(query(a, a.s.Overview)))
	mux.Handle("POST "+analytics.SeriesPath, a.auth(query(a, a.s.Series)))
	mux.Handle("POST "+analytics.BreakdownPath, a.auth(query(a, a.s.Breakdown)))
	return mux
}

// trim gives memory back: the store's (store.Trim, which waits for
// whatever runs) and Go's heap. The idle timer calls it.
func (a *api) trim() {
	if err := a.s.Trim(context.Background()); err != nil {
		a.log.Error("trim memory", "err", err)
	}
	debug.FreeOSMemory()
}

// trimIfBig trims after a query when the process holds more than
// trimAbove, unless something else is running: the next query or the
// idle trim gets it then.
func (a *api) trimIfBig() {
	if a.trimAbove <= 0 || rssAnon() < a.trimAbove {
		return
	}
	trimmed, err := a.s.TryTrim(context.Background())
	if err != nil {
		a.log.Error("trim memory", "err", err)
	}
	if trimmed {
		debug.FreeOSMemory()
	}
}

// rssAnon is the process's anonymous resident memory in bytes, what the
// container's memory limit counts; 0 if it can't be read.
func rssAnon() int64 {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	_, rest, ok := bytes.Cut(b, []byte("RssAnon:"))
	if !ok {
		return 0
	}
	f := bytes.Fields(rest)
	if len(f) == 0 {
		return 0
	}
	kb, _ := strconv.ParseInt(string(f[0]), 10, 64)
	return kb << 10
}

// auth requires the install's token.
func (a *api) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if len(got) < 7 || got[:7] != "Bearer " || subtle.ConstantTimeCompare([]byte(got[7:]), a.token) != 1 {
			http.Error(w, "this needs the install's analytics token", http.StatusUnauthorized)
			return
		}
		next(w, r)
	})
}

// health reports the store's size (analytics.HealthPath); no token, so
// Dock can tell a stopped container from a wrong token.
func (a *api) health(w http.ResponseWriter, r *http.Request) {
	st, err := a.s.Stats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	reply(w, analytics.Health{Version: version, Events: st.Events, FileBytes: st.FileBytes})
}

// write appends a batch of JSON-line events (analytics.WritePath).
func (a *api) write(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	pos, err := strconv.ParseUint(r.URL.Query().Get("position"), 10, 64)
	if source == "" || err != nil {
		http.Error(w, "source and position are required", http.StatusBadRequest)
		return
	}
	events, skipped, err := readEvents(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if skipped > 0 {
		a.log.Warn("write: lines that aren't events were skipped", "source", source, "skipped", skipped)
	}
	n, err := a.s.Write(r.Context(), source, pos, events, a.locate)
	if err != nil {
		a.log.Error("write", "source", source, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	reply(w, analytics.WriteResult{Written: n, Skipped: skipped})
}

// readEvents decodes up to analytics.MaxWriteBatch JSON-line events.
// Lines that aren't events are skipped and counted rather than failing
// the batch: the batch's position is written either way, so one bad line
// can't hold a server's spool up forever. A line over the size cap or a
// batch over the count cap is the caller's error.
func readEvents(body io.Reader) (events []analytics.Event, skipped int, err error) {
	sc := bufio.NewScanner(body)
	// The buffer's capacity counts as a limit too: keep it under the cap.
	sc.Buffer(make([]byte, 0, 4<<10), analytics.MaxEventBytes)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		if len(events)+skipped == analytics.MaxWriteBatch {
			return nil, 0, fmt.Errorf("a write takes at most %d events", analytics.MaxWriteBatch)
		}
		var e analytics.Event
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			skipped++
			continue
		}
		events = append(events, e)
	}
	return events, skipped, sc.Err()
}

// cursor answers a source's position (analytics.CursorPath).
func (a *api) cursor(w http.ResponseWriter, r *http.Request) {
	pos, err := a.s.Cursor(r.Context(), r.URL.Query().Get("source"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	reply(w, analytics.CursorResult{Position: pos})
}

// queryTimeout bounds one query, so a filter over years of a very busy
// site fails with advice instead of holding a slot.
const queryTimeout = 30 * time.Second

// query serves one of the store's queries: the request body is its input,
// the answer its result. Bad input is a 400 with the store's message.
// At most -queries run at once (DuckDB runs each on all its threads
// anyway), and a query that leaves the process big
// trims it (api.trimIfBig).
func query[Q, R any](a *api, run func(context.Context, Q) (R, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var q Q
		if err := json.UnmarshalRead(io.LimitReader(r.Body, 64<<10), &q); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
		defer cancel()
		select {
		case a.slots <- struct{}{}:
		case <-ctx.Done():
			http.Error(w, "the analytics store is busy with other queries; try again", http.StatusTooManyRequests)
			return
		}
		res, err := run(ctx, q)
		<-a.slots
		defer a.trimIfBig()
		var bad *store.QueryError
		switch {
		case errors.As(err, &bad):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded:
			http.Error(w, "this query took over 30 seconds; pick a shorter range or fewer filters", http.StatusBadRequest)
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		default:
			reply(w, res)
		}
	}
}

// reply writes v as the JSON answer.
func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.MarshalWrite(w, v)
}
