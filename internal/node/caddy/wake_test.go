package caddy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// testWaker is asleep for key "7" and wakes it after delay.
type testWaker struct {
	delay time.Duration
	woke  atomic.Int32
	req   atomic.Value
}

func (w *testWaker) Asleep(key string) bool { return key == "7" }

func (w *testWaker) Wake(ctx context.Context, _, request string) error {
	w.req.Store(request)
	select {
	case <-time.After(w.delay):
		w.woke.Add(1)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func wakeRequest(key, ua string, page bool) *http.Request {
	r := httptest.NewRequest("GET", "/internal/caddy/wake", nil)
	r.Header.Set(wakeKeyHeader, key)
	r.Header.Set(forwardedMethod, "GET")
	r.Header.Set(forwardedURI, "/orders/12345?x=1")
	r.Header.Set("User-Agent", ua)
	if page {
		r.Header.Set("Sec-Fetch-Mode", "navigate")
	}
	return r
}

// The wake check lets awake routes on, holds an asleep one until it
// wakes, answers a page load that waited too long with the waking page,
// and never holds or wakes for Dock's uptime probes.
func TestWakeHandler(t *testing.T) {
	w := &testWaker{}
	h := WakeHandler(w)
	serve := func(r *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	if rec := serve(wakeRequest("8", "Mozilla/5.0", true)); rec.Code != 204 || w.woke.Load() != 0 {
		t.Fatalf("awake key: %d", rec.Code)
	}
	if rec := serve(wakeRequest("7", "dock-uptime/0.41", false)); rec.Code != 503 || w.woke.Load() != 0 {
		t.Fatalf("uptime probe: %d, woke %d", rec.Code, w.woke.Load())
	}
	if rec := serve(wakeRequest("7", "curl/8", false)); rec.Code != 204 || w.woke.Load() != 1 || w.req.Load() != "GET /orders/:id" {
		t.Fatalf("held request: %d, woke %d by %v", rec.Code, w.woke.Load(), w.req.Load())
	}
	w.delay = time.Hour
	start := time.Now()
	rec := serve(wakeRequest("7", "Mozilla/5.0", true))
	if rec.Code != 503 || rec.Header().Get("Refresh") != "2" || !strings.Contains(rec.Body.String(), "Waking up") || time.Since(start) < WakeHoldPage {
		t.Fatalf("slow wake, page load: %d %v after %s", rec.Code, rec.Header(), time.Since(start))
	}
}

// A node holds a request until the control plane's routes no longer
// hold its key, asking once however many wait; with no link it gives up.
func TestNodeWaker(t *testing.T) {
	p, err := New(Options{AdminSocket: "/x", HTTPPort: 80, HTTPSPort: 443, DisableTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	var asked atomic.Int32
	n := &NodeWaker{Proxy: p, Request: func(key, _ string) bool { asked.Add(1); return key == "7" }}
	if !n.Asleep("7") {
		t.Fatal("before routes were applied, a key must read asleep")
	}
	p.setWakeKeys([]node.Route{{Hostnames: []string{"a.test"}, Wake: "7"}})
	if !n.Asleep("7") || n.Asleep("8") {
		t.Fatal("asleep keys don't follow the routes")
	}
	done := make(chan error, 2)
	for range 2 {
		go func() { done <- n.Wake(context.Background(), "7", "GET /") }()
	}
	time.Sleep(20 * time.Millisecond)
	p.setWakeKeys([]node.Route{{Hostnames: []string{"a.test"}}}) // the control plane woke it
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if asked.Load() != 1 {
		t.Fatalf("asked the control plane %d times", asked.Load())
	}
	p.setWakeKeys([]node.Route{{Hostnames: []string{"b.test"}, Wake: "9"}})
	if err := n.Wake(context.Background(), "9", "GET /"); err == nil {
		t.Fatal("a wake without a link succeeded")
	}
}
