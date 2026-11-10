package caddy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/traffic"
)

// The wake check of asleep environments' routes (ADR 0070, Route.Wake):
// Caddy asks it before proxying a request to an asleep environment, after
// the route's access check. It holds the request while the environment
// wakes. The control plane serves it with core as the Waker; a node with
// NodeWaker, which asks the control plane over the link and waits for its
// routes to come back awake.

// Waker wakes asleep environments for WakeHandler, by their route's key.
type Waker interface {
	// Asleep reports whether key's environment is asleep or waking.
	Asleep(key string) bool
	// Wake wakes it, and returns once it is awake (nil) or ctx ends.
	// request names what woke it, as traffic counts it ("GET /orders/:id").
	Wake(ctx context.Context, key, request string) error
}

// How long a held request waits for its environment, and how many may
// wait per environment: past them a page load gets the waking page (which
// reloads itself) and anything else a 503 to retry.
const (
	WakeHoldPage   = 3 * time.Second
	WakeHoldAPI    = 30 * time.Second
	wakeMaxWaiting = 256
)

// WakeHandler is the wake check, served on a server's internal listener.
// A key that isn't asleep lets the request on (204), as does a wake that
// ends in time. Dock's uptime probes are never held and never wake
// anything. The answers name nothing but the fact that it is waking.
func WakeHandler(w Waker) http.Handler {
	var mu sync.Mutex
	waiting := map[string]int{}
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", "no-store")
		key := r.Header.Get(wakeKeyHeader)
		if !validWakeKey(key) || !w.Asleep(key) {
			rw.WriteHeader(http.StatusNoContent)
			return
		}
		page := pageLoad(r)
		if strings.HasPrefix(r.UserAgent(), "dock-uptime/") {
			rw.Header().Set("Retry-After", "60")
			http.Error(rw, "Asleep.", http.StatusServiceUnavailable)
			return
		}
		mu.Lock()
		if waiting[key] >= wakeMaxWaiting {
			mu.Unlock()
			writeWaking(rw, page)
			return
		}
		waiting[key]++
		mu.Unlock()
		defer func() {
			mu.Lock()
			if waiting[key]--; waiting[key] == 0 {
				delete(waiting, key)
			}
			mu.Unlock()
		}()
		hold := WakeHoldAPI
		if page {
			hold = WakeHoldPage
		}
		ctx, cancel := context.WithTimeout(r.Context(), hold)
		defer cancel()
		if err := w.Wake(ctx, key, traffic.Endpoint(r.Header.Get(forwardedMethod), r.Header.Get(forwardedURI))); err != nil {
			writeWaking(rw, page)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	})
}

// writeWaking answers a held request whose environment isn't awake yet:
// a page load gets the waking page, which the Refresh header reloads,
// anything else a 503 to retry.
func writeWaking(w http.ResponseWriter, page bool) {
	if !page {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "Waking up; try again in a few seconds.", http.StatusServiceUnavailable)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Refresh", "2")
	h.Set("Retry-After", "2")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = io.WriteString(w, wakingPage)
}

// wakingPage is what a page load of an asleep environment sees while it
// wakes: the look of the stopped and 404 pages, nothing that moves.
const wakingPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Waking up</title>
<style>
html,body{margin:0;height:100%;background:#000;color:#fff;font:14px/1.5 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
body{display:flex;align-items:center;justify-content:center}
p{margin:0}
.m{color:#a1a1aa}
</style>
</head>
<body>
<div><p>503. Waking up.</p><p class="m">This page reloads when it is ready.</p></div>
</body>
</html>
`

// errNoLink: a node couldn't ask its control plane to wake anything.
var errNoLink = errors.New("the control plane can't be reached")

// wakeAskEvery is how often a node asks its control plane to wake one
// environment again while requests keep arriving: the control plane
// joins a wake in flight anyway, this only keeps the link quiet.
const wakeAskEvery = 2 * time.Second

// NodeWaker is a node's Waker. Asleep reads the routes the control plane
// last applied (any key before the first, so a node that restarted holds
// requests until the control plane applies routes again); Wake asks the
// control plane over the link (Request, at most every wakeAskEvery per
// environment) and waits for routes that no longer hold that key.
type NodeWaker struct {
	Proxy *Proxy
	// Request asks the control plane to wake key's environment; false
	// when no link to it is open.
	Request func(key, request string) bool

	mu    sync.Mutex
	asked map[string]time.Time // bounded by environments asleep here
}

// Asleep implements Waker.
func (n *NodeWaker) Asleep(key string) bool { return n.Proxy.wakeHeld(key) }

// Wake implements Waker.
func (n *NodeWaker) Wake(ctx context.Context, key, request string) error {
	t := time.Now()
	n.mu.Lock()
	if n.asked == nil {
		n.asked = map[string]time.Time{}
	}
	for k, at := range n.asked {
		if t.Sub(at) >= wakeAskEvery {
			delete(n.asked, k)
		}
	}
	_, recent := n.asked[key]
	if !recent {
		n.asked[key] = t
	}
	n.mu.Unlock()
	if !recent && !n.Request(key, request) {
		n.mu.Lock()
		delete(n.asked, key)
		n.mu.Unlock()
		return errNoLink
	}
	return n.Proxy.waitAwake(ctx, key)
}

// wakeState is the Proxy's view of asleep environments, from the routes
// it last applied: their keys, and channels closed for those waiting on
// one once routes no longer hold it.
type wakeState struct {
	mu      sync.Mutex
	keys    map[string]bool // nil until routes were applied
	waiters map[string]chan struct{}
}

// setWakeKeys records the keys of routes just applied and releases those
// waiting on keys no longer asleep.
func (p *Proxy) setWakeKeys(routes []node.Route) {
	keys := map[string]bool{}
	for _, r := range routes {
		if r.Wake != "" {
			keys[r.Wake] = true
		}
	}
	w := &p.wake
	w.mu.Lock()
	defer w.mu.Unlock()
	w.keys = keys
	for k, ch := range w.waiters {
		if !keys[k] {
			close(ch)
			delete(w.waiters, k)
		}
	}
}

// wakeHeld reports whether key's routes are held for a wake: asleep in
// the last applied routes, or any key before routes were applied.
func (p *Proxy) wakeHeld(key string) bool {
	w := &p.wake
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.keys == nil || w.keys[key]
}

// waitAwake waits until applied routes no longer hold key, or ctx ends.
func (p *Proxy) waitAwake(ctx context.Context, key string) error {
	w := &p.wake
	w.mu.Lock()
	if w.keys != nil && !w.keys[key] {
		w.mu.Unlock()
		return nil
	}
	if w.waiters == nil {
		w.waiters = map[string]chan struct{}{}
	}
	ch := w.waiters[key]
	if ch == nil {
		ch = make(chan struct{})
		w.waiters[key] = ch
	}
	w.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
