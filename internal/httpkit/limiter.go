package httpkit

import (
	"sync"
	"time"
)

// Limiter is an in-memory token bucket per key (client IP, email). Each
// key may spend burst attempts at once, refilled one per every. No timer:
// the map never holds more than maxBuckets keys.
type Limiter struct {
	mu      sync.Mutex
	burst   float64
	every   time.Duration
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// maxBuckets bounds memory: past it, buckets that have refilled are
// dropped, and if none has, the one used longest ago.
const maxBuckets = 10_000

// NewLimiter returns a limiter allowing burst attempts per key, refilled
// one per every.
func NewLimiter(burst int, every time.Duration) *Limiter {
	return &Limiter{burst: float64(burst), every: every, buckets: map[string]*bucket{}}
}

// Allow spends one attempt for key at t, reporting false when none is left.
func (l *Limiter) Allow(key string, t time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxBuckets {
			l.prune(t)
		}
		if len(l.buckets) >= maxBuckets {
			l.evictOldest()
		}
		b = &bucket{tokens: l.burst, last: t}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+float64(t.Sub(b.last))/float64(l.every))
	b.last = t
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Exhausted reports whether key has no attempt left at t, without
// spending one.
func (l *Limiter) Exhausted(key string, t time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	return ok && b.tokens+float64(t.Sub(b.last))/float64(l.every) < 1
}

// prune drops the buckets that have refilled.
func (l *Limiter) prune(t time.Time) {
	for k, b := range l.buckets {
		if b.tokens+float64(t.Sub(b.last))/float64(l.every) >= l.burst {
			delete(l.buckets, k)
		}
	}
}

// evictOldest drops the bucket used longest ago, so a flood of new keys
// can't grow the map past maxBuckets.
func (l *Limiter) evictOldest() {
	var oldest string
	var at time.Time
	for k, b := range l.buckets {
		if oldest == "" || b.last.Before(at) {
			oldest, at = k, b.last
		}
	}
	delete(l.buckets, oldest)
}
