package api

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicLimiter(t *testing.T) {
	l := &ipLimiter{buckets: map[string]*bucket{}}
	t0 := time.Now()
	for i := range publicBurst {
		if !l.allow("203.0.113.1", t0) {
			t.Fatalf("request %d of the burst refused", i)
		}
	}
	if l.allow("203.0.113.1", t0) {
		t.Error("a request past the burst allowed")
	}
	if !l.allow("203.0.113.2", t0) {
		t.Error("another address refused")
	}
	if !l.allow("203.0.113.1", t0.Add(1100*time.Millisecond)) {
		t.Error("no token a second later")
	}
}

func TestPublicCache(t *testing.T) {
	p := &publicStats{cache: map[publicKey]*publicEntry{}}
	var loads atomic.Int32
	online := true
	p.load = func(context.Context, publicKey) ([]byte, bool, error) {
		n := loads.Add(1)
		return []byte{byte(n)}, online, nil
	}
	ctx := context.Background()
	k := publicKey{"stats.example.com", "7d", 0}
	if b, err := p.answer(ctx, k); err != nil || b[0] != 1 {
		t.Fatalf("first answer %v, %v", b, err)
	}
	if b, _ := p.answer(ctx, k); b[0] != 1 || loads.Load() != 1 {
		t.Errorf("a fresh answer was read again (%d reads)", loads.Load())
	}
	// Stale, and the store offline: the last numbers stay.
	p.cache[k].at = time.Now().Add(-2 * publicFresh)
	online = false
	if b, _ := p.answer(ctx, k); b[0] != 1 || loads.Load() != 2 {
		t.Errorf("offline: answer %v after %d reads, want the old one", b, loads.Load())
	}
	// A dashboard that can't be read and was never cached is an error.
	p.load = func(context.Context, publicKey) ([]byte, bool, error) { return nil, false, errors.New("boom") }
	if _, err := p.answer(ctx, publicKey{"other.example.com", "", 0}); err == nil {
		t.Error("no error for a dashboard that couldn't be read")
	}
	if len(p.cache) != 1 {
		t.Errorf("cache holds %d entries", len(p.cache))
	}
	// Bounded.
	p.load = func(context.Context, publicKey) ([]byte, bool, error) { return []byte{1}, true, nil }
	for i := range publicCacheEntries + 10 {
		if _, err := p.answer(ctx, publicKey{"stats.example.com", "30d", int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.cache) > publicCacheEntries {
		t.Errorf("cache holds %d entries, cap %d", len(p.cache), publicCacheEntries)
	}
}
