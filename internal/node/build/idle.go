package build

import (
	"context"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// idleStop is how long the BuildKit container runs after its last use
// before it is stopped. buildkitd keeps the memory a build grew it to
// (314 MB on a server that built a few apps), and starting it again
// takes about 0.25 s, so it runs only while there is building to do.
// Five minutes keeps it up through a burst of deploys.
const idleStop = 5 * time.Minute

// idler stops BuildKit once nothing has used it for a while. Every use
// (a build, a warm-up, a prune) holds it while it talks to BuildKit; the
// last release arms a one-shot timer, and the next hold stops it. No
// timer runs while BuildKit is in use or already stopped.
type idler struct {
	after time.Duration
	stop  func() // called with mu held, so a hold waits for a stop under way

	mu    sync.Mutex
	users int
	t     *time.Timer
	// uses counts holds other than prunes. pruned is uses as a finished
	// prune saw it when it began, plus one (0: no prune yet): while it
	// matches, nothing can have added cache since, so a prune finds
	// nothing to do and needn't start a stopped BuildKit.
	uses, pruned uint64
}

// hold marks BuildKit in use until release is called, waiting for an
// idle stop under way to finish. A prune passes pruning: it adds no
// cache, so it doesn't count as a use.
func (i *idler) hold(pruning bool) (release func()) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.users++
	if i.t != nil {
		i.t.Stop()
	}
	if !pruning {
		i.uses++
	}
	return i.release
}

// release ends a hold; the last one arms the stop timer.
func (i *idler) release() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.users--
	if i.users > 0 {
		return
	}
	if i.t == nil {
		i.t = time.AfterFunc(i.after, i.fire)
	} else {
		i.t.Reset(i.after)
	}
}

// fire stops BuildKit unless a hold came in after the timer fired.
func (i *idler) fire() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.users == 0 {
		i.stop()
	}
}

// pruneStart returns the mark a prune starting now passes to pruned
// once it finished, and whether a finished prune already covers every
// use so far.
func (i *idler) pruneStart() (mark uint64, done bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.uses + 1, i.pruned == i.uses+1
}

// prunedAt records a finished prune that started at mark (pruneStart).
func (i *idler) prunedAt(mark uint64) {
	i.mu.Lock()
	i.pruned = mark
	i.mu.Unlock()
}

// stopTimeout bounds an idle stop: buildkitd exits on SIGTERM within
// a fraction of a second, Docker kills it after stopGrace.
const (
	stopTimeout = 30 * time.Second
	stopGrace   = 10
)

// stopIdle stops this runtime's BuildKit container if it runs. Its
// restart policy (unless-stopped) keeps it stopped across Docker
// restarts; the next build starts it (ensureDaemon). A container another
// runtime owns is left alone (checkOwner).
func (b *Builder) stopIdle() {
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	inspect, err := b.docker.ContainerInspect(ctx, b.name, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) || err == nil && !inspect.Container.State.Running {
		return
	}
	if err != nil || b.checkOwner(inspect.Container.Config.Labels) != nil {
		return // the next use reports it
	}
	grace := stopGrace
	_, _ = b.docker.ContainerStop(ctx, b.name, client.ContainerStopOptions{Timeout: &grace})
}
