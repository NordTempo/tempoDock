package tempobase

import (
	"sync"
	"time"
)

// limiter is an in-memory token bucket per key (client IP, email): each
// key may spend burst attempts at once, refilled one per every.
//
// Pending ADR 0024 A.2: a copy of Dock's core limiter until it moves to
// httpkit.Limiter; this file is then deleted and App uses that.
type limiter struct {
	mu      sync.Mutex
	burst   float64
	every   time.Duration
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// maxBuckets bounds memory: past it, buckets that have refilled are dropped.
const maxBuckets = 10_000

func newLimiter(burst int, every time.Duration) *limiter {
	return &limiter{burst: float64(burst), every: every, buckets: map[string]*bucket{}}
}

// allow spends one attempt for key, reporting false when none is left.
func (l *limiter) allow(key string, t time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxBuckets {
			for k, b := range l.buckets {
				if b.tokens+float64(t.Sub(b.last))/float64(l.every) >= l.burst {
					delete(l.buckets, k)
				}
			}
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
