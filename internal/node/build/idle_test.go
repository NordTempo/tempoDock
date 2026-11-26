package build

import (
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// BuildKit stops once nothing has used it for idleStop, not while a use
// holds it, and a use in between starts the wait over.
func TestIdleStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var stops atomic.Int32 // written by the timer's goroutine
		i := &idler{after: idleStop, stop: func() { stops.Add(1) }}

		release := i.hold(false)
		time.Sleep(2 * idleStop)
		if stops.Load() != 0 {
			t.Fatalf("stopped while held")
		}
		release()

		time.Sleep(idleStop - time.Second)
		i.hold(false)() // a quick use just before the stop
		time.Sleep(idleStop - time.Second)
		if stops.Load() != 0 {
			t.Fatalf("stopped %s after a use, want %s", idleStop-time.Second, idleStop)
		}
		time.Sleep(2 * time.Second)
		if stops.Load() != 1 {
			t.Fatalf("stops = %d after idleStop, want 1", stops.Load())
		}
		time.Sleep(10 * idleStop)
		if stops.Load() != 1 {
			t.Fatalf("stops = %d, want 1: an idle BuildKit is stopped once", stops.Load())
		}
	})
}

// A prune of a stopped BuildKit is skipped only when an earlier prune
// covers every build since; a build during a prune isn't covered by it.
func TestPruneCoverage(t *testing.T) {
	i := &idler{after: idleStop, stop: func() {}}
	if _, covered := i.pruneStart(); covered {
		t.Fatal("covered before any prune")
	}

	mark, _ := i.pruneStart()
	i.prunedAt(mark)
	if _, covered := i.pruneStart(); !covered {
		t.Fatal("not covered right after a prune")
	}

	i.hold(false)()
	if _, covered := i.pruneStart(); covered {
		t.Fatal("covered after a build")
	}

	mark, _ = i.pruneStart()
	i.hold(false)() // a build while the prune runs
	i.prunedAt(mark)
	if _, covered := i.pruneStart(); covered {
		t.Fatal("covered after a build that ran during the prune")
	}

	release := i.hold(false) // a build that started before the prune
	mark, _ = i.pruneStart()
	i.prunedAt(mark)
	release()
	if _, covered := i.pruneStart(); covered {
		t.Fatal("covered after a build that ran on past the prune")
	}
}
