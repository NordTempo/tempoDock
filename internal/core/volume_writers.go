package core

import (
	"context"
	"sync"

	"github.com/NordTempo/tempoDock/internal/store"
)

// volumeWriters keeps apart the writers of a service's volumes that must
// never overlap. A restore replacing them (a snapshot copied in place, or
// a snapshot or backup filling a new service's volume) shuts out every
// other writer: deploys, stops and starts, a server's recovery starting
// the service again, and `dock run` with the volumes open for writing.
// Only one such run writes at a time; it may run next to the service
// itself, which is what --volumes write is for.
//
// Deploys are kept out by their in-flight release row, not by an entry
// here: lockForRestore marks the service and checks for one in a write
// transaction, and createRelease and StopService check for the mark in
// theirs. Write transactions take SQLite's write lock first, so the two
// are ordered and the later one always sees the earlier. mu is never held
// while waiting for the store.
type volumeWriters struct {
	mu sync.Mutex
	// restoring is the restore writing each service's volumes, by
	// service ID.
	restoring map[int64]volumeRestore
	// running holds the services a run writes the volumes of.
	running map[int64]bool
}

// volumeRestore names a restore for the errors it causes: what is "the
// restore of snapshot 4", watch the command that shows how it goes.
type volumeRestore struct{ what, watch string }

func newVolumeWriters() volumeWriters {
	return volumeWriters{restoring: map[int64]volumeRestore{}, running: map[int64]bool{}}
}

// lockForRestore shuts every other writer out of ss's volumes for restore
// r, or says what writes them now. unlockVolumes ends it.
func (c *Core) lockForRestore(ctx context.Context, ss svcScope, r volumeRestore) error {
	id := ss.svc.ID
	marked := false
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		c.volumes.mu.Lock()
		cur, restoring := c.volumes.restoring[id]
		running := c.volumes.running[id]
		if !restoring && !running {
			c.volumes.restoring[id], marked = r, true
		}
		c.volumes.mu.Unlock()
		switch {
		case restoring:
			return conflict("%s is writing %s's volumes; wait for it to finish (%s shows it)", cur.what, ss, cur.watch)
		case running:
			return conflict("a command (`dock run`) has %s's volumes open for writing; restore once it ends (`dock runs %s` shows it)", ss, ss.svc.Name)
		}
		if rel, err := q.GetInFlightRelease(ctx, id); err == nil {
			return deployInProgress(ss, rel)
		} else if !store.IsNotFound(err) {
			return err
		}
		return nil
	})
	if err != nil && marked {
		c.unlockVolumes(id)
	}
	return err
}

// unlockVolumes ends a restore's lockForRestore.
func (c *Core) unlockVolumes(serviceID int64) {
	c.volumes.mu.Lock()
	delete(c.volumes.restoring, serviceID)
	c.volumes.mu.Unlock()
}

// restoreWriting refuses verb ("deploy", "stop it") while a restore
// writes ss's volumes, saying which and what to do; nil when none does.
func (c *Core) restoreWriting(ss svcScope, verb string) error {
	c.volumes.mu.Lock()
	r, ok := c.volumes.restoring[ss.svc.ID]
	c.volumes.mu.Unlock()
	if !ok {
		return nil
	}
	return conflict("%s is writing %s's volumes; %s once it finishes (%s shows it)", r.what, ss, verb, r.watch)
}

// holdWritingRun takes ss's one slot for a run writing its volumes; false
// with the reason when a restore or another run has them.
func (c *Core) holdWritingRun(ss svcScope) (func(), error) {
	id := ss.svc.ID
	c.volumes.mu.Lock()
	defer c.volumes.mu.Unlock()
	if r, ok := c.volumes.restoring[id]; ok {
		return nil, conflict("%s is writing %s's volumes; run the command once it finishes (%s shows it), or mount them read-only", r.what, ss.svc.Name, r.watch)
	}
	if c.volumes.running[id] {
		return nil, conflict("another command has %s's volumes open for writing; wait for it to end, or mount them read-only", ss.svc.Name)
	}
	c.volumes.running[id] = true
	return func() {
		c.volumes.mu.Lock()
		delete(c.volumes.running, id)
		c.volumes.mu.Unlock()
	}, nil
}
