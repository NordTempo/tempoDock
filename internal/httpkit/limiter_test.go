package httpkit

import (
	"strconv"
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	l := NewLimiter(2, time.Minute)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if l.Exhausted("a", at) || !l.Allow("a", at) || !l.Allow("a", at) {
		t.Fatal("a burst of 2 is allowed")
	}
	if !l.Exhausted("a", at) || l.Allow("a", at) || !l.Allow("b", at) {
		t.Fatal("a third attempt is refused, other keys are not")
	}
	if l.Exhausted("a", at.Add(time.Minute)) || !l.Allow("a", at.Add(time.Minute)) || l.Allow("a", at.Add(time.Minute)) {
		t.Fatal("one attempt refills per minute")
	}
	for i := range maxBuckets + 10 {
		l.Allow(strconv.Itoa(i), at.Add(time.Duration(i)*time.Minute))
	}
	if len(l.buckets) > maxBuckets {
		t.Fatalf("limiter holds %d buckets", len(l.buckets))
	}
}
