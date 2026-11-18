package remote

import (
	"testing"
	"time"
)

// A lost link is reported once, after five minutes, and its return only
// when the loss was reported.
func TestLinkWatchSaysLossAndReturnOnce(t *testing.T) {
	var w linkWatch
	t0 := time.Now()
	w.lost(t0)
	w.lost(t0.Add(time.Minute)) // a failed retry doesn't move the start
	if _, say := w.overdue(t0.Add(linkLostAfter - time.Second)); say {
		t.Fatal("reported before five minutes")
	}
	if since, say := w.overdue(t0.Add(linkLostAfter)); !say || !since.Equal(t0) {
		t.Fatalf("after five minutes: since %v, say %v", since, say)
	}
	if _, say := w.overdue(t0.Add(2 * linkLostAfter)); say {
		t.Fatal("reported twice")
	}
	if gone, say := w.back(t0.Add(7 * time.Minute)); !say || gone != 7*time.Minute {
		t.Fatalf("back: gone %v, say %v", gone, say)
	}

	// A short drop says nothing either way.
	w.lost(t0.Add(8 * time.Minute))
	if _, say := w.back(t0.Add(9 * time.Minute)); say {
		t.Fatal("a one minute drop was reported as back")
	}
	if _, say := w.overdue(t0.Add(time.Hour)); say {
		t.Fatal("reported while the link is up")
	}
}
