package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// StatusPages serves published status pages (ADR 0073) on [status]
// listen, which only the control plane's Caddy reaches, for the
// hostnames it routes there: the page at /, its incidents at
// /incidents/<id>, /history, /feed.atom and /status.json, and 404 for
// everything else. A renamed page's old address answers those paths
// with a 301 to the same path at the page's address
// (core.StatusPageMoved). Nothing else of Dock is reachable through it, and no
// access protection applies: a status page is public by definition.
//
// A view never reads the database. Each page and format is rendered
// when it is asked for after something it shows may have changed
// (core.StatusPageAt's generation), or once it is statusFresh old, and
// the gzipped bytes are served until then. A page nobody views is never
// rendered. Anyone may ask, so: each client address gets statusBurst
// requests and then statusRate a second, a stale page is rendered at
// most once a statusMinAge (requests meanwhile get the previous bytes),
// at most statusRenders run at once, and the cache holds at most
// statusCacheBytes. A render that fails keeps the last bytes.
func StatusPages(c *core.Core, log *slog.Logger) http.Handler {
	return newStatusServer(c.StatusPageAt, c.StatusPageMoved, c, log).handler()
}

// statusSource is what the listener reads from core: *core.Core, or a
// stand-in in tests.
type statusSource interface {
	PublicStatusPage(ctx context.Context, id int64) (apitypes.PublicStatusPage, error)
	PublicStatusIncident(ctx context.Context, page int64, id string) (apitypes.PublicIncident, string, error)
	PublicStatusHistory(ctx context.Context, page int64) (apitypes.PublicStatusHistory, error)
	PublicStatusFeed(ctx context.Context, page int64) (apitypes.PublicStatusFeed, error)
}

// Bounds of the public status pages.
const (
	statusBurst      = 60
	statusRate       = 2
	statusRenders    = 2
	statusFresh      = time.Minute
	statusMinAge     = 5 * time.Second
	statusCacheBytes = 8 << 20
)

// statusFormat is what a path of a page serves.
type statusFormat uint8

const (
	statusHTML statusFormat = iota
	statusJSON
	statusIncident
	statusHistory
	statusFeed
)

// statusServer is the handler's state.
type statusServer struct {
	// at is core.StatusPageAt, or a stand-in in tests.
	at func(host string) (id, gen int64, ok bool)
	// moved is core.StatusPageMoved, or a stand-in in tests.
	moved   func(host string) (to string, ok bool)
	src     statusSource
	log     *slog.Logger
	limiter *ipLimiter
	slots   chan struct{}

	mu    sync.Mutex
	cache map[statusKey]*statusEntry
	bytes int
}

// statusKey is a page in a format; arg is the incident's ID for
// statusIncident, the page's address for statusFeed.
type statusKey struct {
	id     int64
	format statusFormat
	arg    string
}

// statusEntry is a rendered page: gzipped, under the generation it was
// rendered at. loading is closed once a render in progress is done; nil
// while none is.
type statusEntry struct {
	statusBody
	gen     int64
	at      time.Time
	loading chan struct{}
}

// statusBody is what a response sends.
type statusBody struct {
	gz          []byte
	etag, ctype string
}

// newStatusServer is the handler's state over what it reads from core.
func newStatusServer(at func(string) (int64, int64, bool), moved func(string) (string, bool), src statusSource, log *slog.Logger) *statusServer {
	return &statusServer{at: at, moved: moved, src: src, log: log, slots: make(chan struct{}, statusRenders),
		limiter: &ipLimiter{burst: statusBurst, rate: statusRate, buckets: map[string]*bucket{}}, cache: map[statusKey]*statusEntry{}}
}

// handler routes the paths a page has.
func (p *statusServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", p.serve(statusHTML))
	mux.HandleFunc("GET /status.json", p.serve(statusJSON))
	mux.HandleFunc("GET /history", p.serve(statusHistory))
	mux.HandleFunc("GET /feed.atom", p.serve(statusFeed))
	mux.HandleFunc("GET /incidents/{id}", p.serve(statusIncident))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { statusNotFound(w) })
	return mux
}

// statusNotFound is the one answer for an unknown host, path or an
// unpublished page, so none says more than another.
func statusNotFound(w http.ResponseWriter) {
	http.Error(w, "no status page here", http.StatusNotFound)
}

// errStatusBusy is every render slot taken for longer than a request
// waits.
var errStatusBusy = errors.New("the status page is busy; try again in a few seconds")

// serve answers a page in format f.
func (p *statusServer) serve(f statusFormat) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !p.limiter.allow(clientIP(r), time.Now()) {
			w.Header().Set("Retry-After", "5")
			http.Error(w, "too many requests; try again in a few seconds", http.StatusTooManyRequests)
			return
		}
		host := strings.ToLower(r.Host)
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if to, ok := p.moved(host); ok {
			// Cached an hour, not for good: another page may take the name.
			w.Header().Set("Cache-Control", "public, max-age=3600")
			http.Redirect(w, r, to+r.URL.RequestURI(), http.StatusMovedPermanently)
			return
		}
		id, gen, ok := p.at(host)
		k := statusKey{id: id, format: f}
		switch f {
		case statusIncident:
			k.arg = r.PathValue("id")
			ok = ok && core.ValidPublicID(k.arg)
		case statusFeed:
			scheme := "https"
			if r.Header.Get("X-Forwarded-Proto") == "http" {
				scheme = "http"
			}
			k.arg = scheme + "://" + host
		}
		if !ok {
			statusNotFound(w)
			return
		}
		b, err := p.answer(r.Context(), k, gen)
		switch {
		case core.CodeOf(err) == apitypes.CodeNotFound:
			statusNotFound(w)
			return
		case errors.Is(err, errStatusBusy):
			w.Header().Set("Retry-After", "5")
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		case err != nil:
			p.log.Error("status page", "host", host, "err", err)
			http.Error(w, "the status page couldn't be read; try again later", http.StatusInternalServerError)
			return
		}
		h := w.Header()
		h.Set("Content-Type", b.ctype)
		h.Set("Cache-Control", "public, max-age=15, stale-while-revalidate=60, stale-if-error=86400")
		h.Set("ETag", b.etag)
		h.Set("Vary", "Accept-Encoding")
		h.Set("Content-Security-Policy", statusCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if r.Header.Get("If-None-Match") == b.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			h.Set("Content-Encoding", "gzip")
			_, _ = w.Write(b.gz)
			return
		}
		// Every browser takes gzip; the rare client that doesn't gets it
		// unpacked as it is sent.
		zr, err := gzip.NewReader(bytes.NewReader(b.gz))
		if err != nil {
			http.Error(w, "the status page couldn't be read; try again later", http.StatusInternalServerError)
			return
		}
		_, _ = io.Copy(w, zr)
	}
}

// answer returns a page's bytes: cached while fresh, rendered otherwise,
// once however many ask at the same time. While a render runs, or for
// statusMinAge after one, requests get the bytes there are.
func (p *statusServer) answer(ctx context.Context, k statusKey, gen int64) (statusBody, error) {
	for {
		p.mu.Lock()
		e := p.cache[k]
		if e != nil && e.gz != nil {
			age := time.Since(e.at)
			if (e.gen == gen && age < statusFresh) || age < statusMinAge || e.loading != nil {
				b := e.statusBody
				p.mu.Unlock()
				return b, nil
			}
		}
		if e != nil && e.loading != nil {
			wait := e.loading
			p.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return statusBody{}, ctx.Err()
			}
		}
		if e == nil {
			e = &statusEntry{}
			p.cache[k] = e
		}
		e.loading = make(chan struct{})
		p.mu.Unlock()
		b, err := p.render(ctx, k)
		p.mu.Lock()
		close(e.loading)
		e.loading = nil
		switch {
		case core.CodeOf(err) == apitypes.CodeNotFound:
			p.bytes -= len(e.gz)
			delete(p.cache, k)
		case err != nil && e.gz == nil:
			delete(p.cache, k)
		case err != nil:
			// Keep the last bytes; try again after statusMinAge.
			e.at = time.Now()
			b, err = e.statusBody, nil
		default:
			p.bytes += len(b.gz) - len(e.gz)
			e.statusBody, e.gen, e.at = b, gen, time.Now()
			p.evictLocked(k)
		}
		p.mu.Unlock()
		return b, err
	}
}

// evictLocked drops the oldest entries but keep while the cache holds
// more than statusCacheBytes. Called with mu held.
func (p *statusServer) evictLocked(keep statusKey) {
	for p.bytes > statusCacheBytes {
		var oldest statusKey
		var at time.Time
		for k, e := range p.cache {
			if k != keep && e.loading == nil && (at.IsZero() || e.at.Before(at)) {
				oldest, at = k, e.at
			}
		}
		if at.IsZero() {
			return
		}
		p.bytes -= len(p.cache[oldest].gz)
		delete(p.cache, oldest)
	}
}

// render reads a page from core within a render slot and renders it in
// its format, gzipped.
func (p *statusServer) render(ctx context.Context, k statusKey) (statusBody, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case p.slots <- struct{}{}:
	case <-time.After(5 * time.Second):
		return statusBody{}, errStatusBusy
	case <-ctx.Done():
		return statusBody{}, ctx.Err()
	}
	defer func() { <-p.slots }()
	raw, ctype, err := p.read(ctx, k)
	if err != nil {
		return statusBody{}, err
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(raw)
	if err := zw.Close(); err != nil {
		return statusBody{}, err
	}
	sum := sha256.Sum256(raw)
	return statusBody{gz: buf.Bytes(), etag: `"` + hex.EncodeToString(sum[:8]) + `"`, ctype: ctype}, nil
}

// read reads what k shows from core and renders it.
func (p *statusServer) read(ctx context.Context, k statusKey) ([]byte, string, error) {
	const html = "text/html; charset=utf-8"
	switch k.format {
	case statusIncident:
		inc, title, err := p.src.PublicStatusIncident(ctx, k.id, k.arg)
		if err != nil {
			return nil, "", err
		}
		b, err := renderIncidentPage(title, inc, time.Now())
		return b, html, err
	case statusHistory:
		h, err := p.src.PublicStatusHistory(ctx, k.id)
		if err != nil {
			return nil, "", err
		}
		b, err := renderHistoryPage(h)
		return b, html, err
	case statusFeed:
		f, err := p.src.PublicStatusFeed(ctx, k.id)
		if err != nil {
			return nil, "", err
		}
		b, err := renderStatusFeed(f, k.arg)
		return b, "application/atom+xml; charset=utf-8", err
	}
	page, err := p.src.PublicStatusPage(ctx, k.id)
	if err != nil {
		return nil, "", err
	}
	if k.format == statusJSON {
		b, err := renderStatusJSON(page)
		return b, "application/json", err
	}
	b, err := renderStatusPage(page)
	return b, html, err
}
