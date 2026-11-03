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

// TestLoginLimiter: a guesser on one address is stopped without locking
// the owner out from theirs; many addresses at once hit the email's limit.
func TestLoginLimiter(t *testing.T) {
	l := NewLoginLimiter()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for range 100 {
		l.Allow("ada@example.com", "198.51.100.7", at)
	}
	if l.Allow("ada@example.com", "198.51.100.7", at) {
		t.Fatal("the guesser keeps guessing")
	}
	if !l.Allow("Ada@Example.com", "192.0.2.1", at) {
		t.Fatal("the owner is locked out by someone else's guesses")
	}
	for i := range 100 {
		l.Allow("bob@example.com", "203.0.113."+strconv.Itoa(i), at)
	}
	if l.Allow("bob@example.com", "192.0.2.1", at) || !l.Allow("bob@example.com", "192.0.2.1", at.Add(2*time.Second)) {
		t.Fatal("guesses from many addresses spend the email's own bucket, which refills")
	}
}
