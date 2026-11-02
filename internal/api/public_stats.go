package api

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// PublicStats serves sites' public dashboards (ADR 0037, phase A4) on
// [analytics] stats_listen, which only the control plane's Caddy reaches,
// for the hostnames it routes there. Nothing else of Dock is reachable
// through it.
//
// Anyone on the internet may ask, so it is bounded three ways: each
// client address gets publicBurst requests and then one a second, at most
// publicQueries dashboards are read from the store at once, and each
// dashboard and range is read at most once a publicFresh, the answer
// cached (at most publicCacheEntries of them). While the store can't be
// reached a dashboard shows the last numbers it had.
func PublicStats(c *core.Core, log *slog.Logger) http.Handler {
	p := &publicStats{c: c, log: log, slots: make(chan struct{}, publicQueries),
		limiter: &ipLimiter{buckets: map[string]*bucket{}}, cache: map[publicKey]*publicEntry{}}
	p.load, p.dashboard = p.read, c.PublicDashboard
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", p.page)
	return mux
}

// Bounds of the public dashboards.
const (
	publicBurst        = 20
	publicQueries      = 4
	publicFresh        = time.Minute
	publicCacheEntries = 1024
	publicLimitEntries = 10_000
)

// publicStats is the handler's state.
type publicStats struct {
	c       *core.Core
	log     *slog.Logger
	slots   chan struct{}
	limiter *ipLimiter
	// load reads a dashboard: read, or a stand-in in tests.
	load func(ctx context.Context, k publicKey) ([]byte, bool, error)
	// dashboard is core.PublicDashboard, or a stand-in in tests.
	dashboard func(host string) (gen int64, ok bool)

	mu    sync.Mutex
	cache map[publicKey]*publicEntry
}

// publicKey is a dashboard and range under a generation of the sites'
// settings (core.PublicDashboard): a change to any site's makes pages read
// before it stale.
type publicKey struct {
	host, rng string
	gen       int64
}

// publicEntry is a dashboard's answer: its page, when it was read and
// whether the store answered. loading is closed once a read in progress
// is done; nil while none is.
type publicEntry struct {
	body    []byte
	at      time.Time
	online  bool
	loading chan struct{}
}

// page answers GET /?range=, the dashboard's page.
func (p *publicStats) page(w http.ResponseWriter, r *http.Request) {
	if !p.limiter.allow(clientIP(r), time.Now()) {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "too many requests; try again in a few seconds", http.StatusTooManyRequests)
		return
	}
	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "30d"
	}
	if !slices.Contains(core.PublicRanges, rng) {
		http.Error(w, "range is one of 7d, 30d, 12m", http.StatusBadRequest)
		return
	}
	gen, ok := p.dashboard(host)
	if !ok {
		http.Error(w, "no public dashboard here", http.StatusNotFound)
		return
	}
	body, err := p.answer(r.Context(), publicKey{host, rng, gen})
	switch {
	case core.CodeOf(err) == apitypes.CodeNotFound:
		http.Error(w, "no public dashboard here", http.StatusNotFound)
		return
	case errors.Is(err, errPublicBusy):
		w.Header().Set("Retry-After", "5")
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	case err != nil:
		p.log.Error("public dashboard", "host", host, "err", err)
		http.Error(w, "the dashboard couldn't be read; try again later", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "public, max-age=60")
	h.Set("Content-Security-Policy", publicCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write(body)
}

// errPublicBusy is every query slot taken for longer than a request
// waits.
var errPublicBusy = errors.New("the dashboard is busy; try again in a few seconds")

// answer returns a dashboard's page: cached while fresh, read from the
// store otherwise, once however many ask at the same time. A read that
// finds the store offline keeps the last answer the store gave.
func (p *publicStats) answer(ctx context.Context, k publicKey) ([]byte, error) {
	for {
		p.mu.Lock()
		e := p.cache[k]
		if e != nil && e.loading == nil && time.Since(e.at) < publicFresh {
			body := e.body
			p.mu.Unlock()
			return body, nil
		}
		if e != nil && e.loading != nil {
			wait := e.loading
			p.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if e == nil {
			p.evictLocked()
			e = &publicEntry{}
			p.cache[k] = e
		}
		e.loading = make(chan struct{})
		p.mu.Unlock()
		body, online, err := p.load(ctx, k)
		p.mu.Lock()
		close(e.loading)
		e.loading = nil
		switch {
		case err != nil:
			if e.body == nil {
				delete(p.cache, k)
			}
		case online || !e.online:
			e.body, e.at, e.online = body, time.Now(), online
		default:
			// Offline now, online before: keep the last numbers, and ask
			// again after the next publicFresh.
			e.at = time.Now()
		}
		body = e.body
		p.mu.Unlock()
		if err != nil && body == nil {
			return nil, err
		}
		return body, nil
	}
}

// read asks core for a dashboard within a query slot and renders it.
func (p *publicStats) read(ctx context.Context, k publicKey) ([]byte, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case p.slots <- struct{}{}:
	case <-time.After(5 * time.Second):
		return nil, false, errPublicBusy
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	defer func() { <-p.slots }()
	st, err := p.c.PublicStats(ctx, k.host, k.rng)
	if err != nil {
		return nil, false, err
	}
	b, err := renderPublic(st)
	return b, st.StoreOnline, err
}

// evictLocked makes room for an entry: the stalest one goes once the
// cache is full. Called with mu held.
func (p *publicStats) evictLocked() {
	if len(p.cache) < publicCacheEntries {
		return
	}
	var oldest publicKey
	var at time.Time
	for k, e := range p.cache {
		if e.loading == nil && (at.IsZero() || e.at.Before(at)) {
			oldest, at = k, e.at
		}
	}
	delete(p.cache, oldest)
}

// clientIP is the client's address as Caddy set it (core.ClientIPHeader):
// only Caddy reaches this listener.
func clientIP(r *http.Request) string {
	if ip := r.Header.Get(core.ClientIPHeader); ip != "" {
		return ip
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

// ipLimiter is a token bucket per client address: publicBurst requests,
// refilled at one a second. It holds at most publicLimitEntries
// addresses; when full it forgets those whose buckets are full again, and
// refuses new ones while none are.
type ipLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

// bucket is one address's tokens as of at.
type bucket struct {
	tokens float64
	at     time.Time
}

// allow takes a token for ip, if it has one.
func (l *ipLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[ip]
	if b == nil {
		if len(l.buckets) >= publicLimitEntries {
			for k, o := range l.buckets {
				if o.tokens+now.Sub(o.at).Seconds() >= publicBurst {
					delete(l.buckets, k)
				}
			}
			if len(l.buckets) >= publicLimitEntries {
				return false
			}
		}
		b = &bucket{tokens: publicBurst, at: now}
		l.buckets[ip] = b
	}
	b.tokens = min(publicBurst, b.tokens+now.Sub(b.at).Seconds())
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
