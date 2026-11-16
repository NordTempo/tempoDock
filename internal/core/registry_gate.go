package core

import (
	"context"
	"sync"
)

// registryGate keeps work that stops or rewrites the platform registry (a
// prune's garbage collection, a migration, a storage change) from running
// next to builds and pulls that use it. Uses share it; a holder has it
// alone, taken only when no use runs (tryHold), so a holder never waits.
//
// A use waits while it is held, with its context: a deploy that waits
// shows it, can be canceled and has a deadline (deployment.registry). A
// holder that can stop early (a prune) passes yield: the first use that
// arrives calls it, so a build waits seconds for the registry to start
// again, not for a garbage collection that can run an hour in a bucket.
type registryGate struct {
	mu   sync.Mutex
	uses int
	held bool
	// free is closed when the holder lets go; nil while not held.
	free chan struct{}
	// yield stops the holder early; nil if it can't be stopped.
	yield func()
}

// use takes the gate for a build or pull until release is called, waiting
// while a holder has it. Uses may nest (a build's pull): a holder takes
// the gate only with no use running, so none comes between two.
func (g *registryGate) use(ctx context.Context) (release func(), err error) {
	for {
		g.mu.Lock()
		if !g.held {
			g.uses++
			g.mu.Unlock()
			return sync.OnceFunc(func() {
				g.mu.Lock()
				g.uses--
				g.mu.Unlock()
			}), nil
		}
		free, yield := g.free, g.yield
		g.yield = nil // once is enough
		g.mu.Unlock()
		if yield != nil {
			yield()
		}
		select {
		case <-free:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// busy reports whether a holder has the gate: a use would wait.
func (g *registryGate) busy() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.held
}

// tryHold takes the gate alone if no use runs and nobody holds it. yield,
// if not nil, is how a waiting use asks the holder to stop early.
func (g *registryGate) tryHold(yield func()) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.held || g.uses > 0 {
		return false
	}
	g.held, g.free, g.yield = true, make(chan struct{}), yield
	return true
}

// release ends a hold and wakes the uses that wait.
func (g *registryGate) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.held {
		return
	}
	close(g.free)
	g.held, g.free, g.yield = false, nil, nil
}
