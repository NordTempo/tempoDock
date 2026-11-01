package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
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
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/cmd/dock-analytics/geo"
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
	// A restore ends the process once the database is on disk: the
	// container's restart opens it (store.Receive).
	ctx, exit := context.WithCancel(ctx)
	defer exit()
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
	a.db, a.exit = sf.db, exit
	// Locations: the database in the volume next to the store's, if one
	// was downloaded (POST /v1/geoip).
	if a.geo, err = geo.Open(filepath.Join(filepath.Dir(sf.db), "geoip")); err != nil {
		log.Error("location database: events get no location until it is downloaded again", "err", err)
	}
	a.locate = a.geo.Locate
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
	// locate looks up locations, from geo (which may have no database).
	locate store.Locator
	geo    *geo.DB

	// slots bounds how many queries run at once.
	slots chan struct{}
	// trimAbove is the anonymous memory (bytes) above which a finished
	// query trims.
	trimAbove int64
	// db is the database file a restore goes next to; exit ends the
	// process after one.
	db   string
	exit func()
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
	mux.Handle("POST "+analytics.LivePath, a.auth(query(a, a.s.Live)))
	mux.Handle("POST "+analytics.ErrorsPath, a.auth(query(a, a.s.Errors)))
	mux.Handle("POST "+analytics.ErrorPath, a.auth(query(a, a.s.Error)))
	mux.Handle("POST "+analytics.VitalsPath, a.auth(query(a, a.s.Vitals)))
	mux.Handle("POST "+analytics.TrafficPath, a.auth(query(a, a.s.Traffic)))
	mux.Handle("POST "+analytics.GoalsPath, a.auth(query(a, a.s.Goals)))
	mux.Handle("POST "+analytics.FunnelPath, a.auth(query(a, a.s.Funnel)))
	mux.Handle("POST "+analytics.ReplayWritePath, a.auth(a.writeReplay))
	mux.Handle("POST "+analytics.ReplaysPath, a.auth(query(a, a.s.Replays)))
	mux.Handle("POST "+analytics.ReplayPath, a.auth(query(a, a.s.Replay)))
	mux.Handle("GET "+analytics.ReplayDataPath, a.auth(a.replayData))
	mux.Handle("POST "+analytics.ReplayPrunePath, a.auth(a.pruneReplays))
	mux.Handle("POST "+analytics.GeoIPPath, a.auth(a.updateGeoIP))
	mux.Handle("POST "+analytics.PrunePath, a.auth(a.prune))
	mux.Handle("GET "+analytics.SnapshotPath, a.auth(a.snapshot))
	mux.Handle("PUT "+analytics.RestorePath, a.auth(a.restore))
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
	h := analytics.Health{Version: version, Events: st.Events, FileBytes: st.FileBytes}
	if a.geo != nil {
		h.GeoIP = a.geo.Month()
	}
	reply(w, h)
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
	n, news, err := a.s.Write(r.Context(), source, pos, events, a.locate)
	if err != nil {
		a.log.Error("write", "source", source, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	reply(w, analytics.WriteResult{Written: n, Skipped: skipped, NewErrors: news})
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

// writeReplay appends a batch of JSON-line replay chunks
// (analytics.ReplayWritePath). Like write, lines that aren't chunks are
// skipped and counted, the batch's position written either way.
func (a *api) writeReplay(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	pos, err := strconv.ParseUint(r.URL.Query().Get("position"), 10, 64)
	if source == "" || err != nil {
		http.Error(w, "source and position are required", http.StatusBadRequest)
		return
	}
	sc := bufio.NewScanner(r.Body)
	sc.Buffer(make([]byte, 0, 64<<10), analytics.MaxReplayLine)
	var chunks []analytics.ReplayChunk
	skipped := 0
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		if len(chunks)+skipped == analytics.MaxWriteBatch {
			http.Error(w, fmt.Sprintf("a write takes at most %d chunks", analytics.MaxWriteBatch), http.StatusBadRequest)
			return
		}
		var ch analytics.ReplayChunk
		if json.Unmarshal(sc.Bytes(), &ch) != nil {
			skipped++
			continue
		}
		chunks = append(chunks, ch)
	}
	if err := sc.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	n, bad, err := a.s.WriteReplay(r.Context(), source, pos, chunks, a.locate)
	if err != nil {
		a.log.Error("write replay", "source", source, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if skipped+bad > 0 {
		a.log.Warn("write replay: lines that aren't chunks were skipped", "source", source, "skipped", skipped+bad)
	}
	reply(w, analytics.WriteResult{Written: n, Skipped: skipped + bad})
}

// replayData streams segments' rrweb events (analytics.ReplayDataPath),
// gzipped for a caller that takes it: the control plane passes the bytes
// on to the browser as they are. It takes no query slot: past one
// lookup it reads files, not the database.
func (a *api) replayData(w http.ResponseWriter, r *http.Request) {
	site, err := strconv.ParseInt(r.URL.Query().Get("site"), 10, 64)
	if err != nil {
		http.Error(w, "site is required", http.StatusBadRequest)
		return
	}
	started := false
	var out io.Writer = w
	var zw *gzip.Writer
	err = a.s.ReplayData(r.Context(), site, r.URL.Query()["segment"], func() {
		started = true
		w.Header().Set("Content-Type", "application/x-ndjson")
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			zw, _ = gzip.NewWriterLevel(w, gzip.DefaultCompression)
			out = zw
		}
	}, writerFunc(func(p []byte) (int, error) { return out.Write(p) }))
	if zw != nil {
		_ = zw.Close()
	}
	var bad *store.QueryError
	switch {
	case err == nil:
	case started:
		a.log.Error("replay data", "err", err) // the answer is cut short
	case errors.As(err, &bad):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// writerFunc is a function as an io.Writer.
type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// pruneReplays deletes a site's old replays (analytics.ReplayPrunePath).
func (a *api) pruneReplays(w http.ResponseWriter, r *http.Request) {
	var q analytics.ReplayPrune
	if err := json.UnmarshalRead(io.LimitReader(r.Body, 4<<10), &q); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res, err := a.s.PruneReplays(r.Context(), q)
	if err != nil {
		a.log.Error("prune replays", "site", q.Site, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	reply(w, res)
}

// updateGeoIP has the location database fetched for this month, unless
// it is there (analytics.GeoIPPath). The control plane asks once a day;
// a month's first ask downloads it (about 130 MB).
func (a *api) updateGeoIP(w http.ResponseWriter, r *http.Request) {
	if a.geo == nil {
		http.Error(w, "no location database directory", http.StatusInternalServerError)
		return
	}
	month, err := a.geo.Update(r.Context(), time.Now())
	if err != nil && month == "" {
		a.log.Error("location database", "err", err)
		http.Error(w, "the location database couldn't be downloaded: "+err.Error(), http.StatusBadGateway)
		return
	}
	if err != nil {
		a.log.Warn("location database: keeping the one there is", "month", month, "err", err)
	}
	reply(w, analytics.GeoIP{Month: month})
}

// prune deletes a site's old raw events (analytics.PrunePath): a write,
// so neither a query slot nor the query timeout; a first prune of a long
// history may take minutes.
func (a *api) prune(w http.ResponseWriter, r *http.Request) {
	var q analytics.PruneQuery
	if err := json.UnmarshalRead(io.LimitReader(r.Body, 4<<10), &q); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res, err := a.s.Prune(r.Context(), q)
	if err != nil {
		a.log.Error("prune", "site", q.Site, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	reply(w, res)
}

// snapshot streams the database file (analytics.SnapshotPath) for a move
// to another server. Writes wait until it is sent.
func (a *api) snapshot(w http.ResponseWriter, r *http.Request) {
	started := false
	err := a.s.Snapshot(r.Context(), func(size, events int64) {
		started = true
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		w.Header().Set(analytics.SnapshotEventsHeader, strconv.FormatInt(events, 10))
	}, w)
	switch {
	case err != nil && !started:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	case err != nil:
		// The answer is cut short of its length, which the receiver sees.
		a.log.Error("snapshot", "err", err)
	}
}

// restore takes another store's snapshot (analytics.RestorePath) into an
// empty store, then exits so the container restarts on it.
func (a *api) restore(w http.ResponseWriter, r *http.Request) {
	st, err := a.s.Stats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if st.Events > 0 {
		http.Error(w, fmt.Sprintf("this store holds %d events; only an empty store takes another's data", st.Events), http.StatusConflict)
		return
	}
	got, err := store.Receive(a.db, r.Body)
	if err != nil {
		a.log.Error("restore", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.log.Info("received a database; restarting on it", "bytes", got.Bytes)
	reply(w, analytics.Restored{Bytes: got.Bytes, SHA256: got.SHA256})
	a.exit()
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
