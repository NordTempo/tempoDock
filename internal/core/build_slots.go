package core

import (
	"context"
	"slices"
	"sync"
	"time"
)

// buildSlots is every builder's one build slot and the releases waiting
// for it, in the order they came (deploy queue view): builds run one at
// a time per builder, since they share its BuildKit memory cap. It
// replaces a channel semaphore so the queue can be read: which release
// builds where, what waits behind it, and since when. Only in memory, like
// the deploys themselves (deployRuns): after a restart, recovery fails
// what was in flight.
type buildSlots struct {
	mu     sync.Mutex
	queues map[int64]*buildQueue // by builder server ID, while not empty
	// changed is called after the queue of a builder changed, outside mu,
	// with the orgs of the releases in it then and before.
	changed func(orgs []int64)
}

// buildQueue is one builder's slot holder and its waiters, oldest first.
type buildQueue struct {
	holder  *buildTicket
	waiting []*buildTicket
}

// buildTicket is one release's place at a builder, from join to leave.
type buildTicket struct {
	server, release, org int64
	// since is when it joined the queue, then when it got the slot.
	since time.Time
	// ready closes when the slot is handed to it.
	ready chan struct{}
	// left is set once leave ran.
	left bool
}

// buildPlace is a ticket as the deploy queue view reads it.
type buildPlace struct {
	server  int64
	holding bool
	// position is its place among the waiters, 1 first; 0 while holding.
	position int
	since    time.Time
}

// join puts release in server's queue: in the slot when it is free, else
// last in line. ahead is how many builds come first: 0 when it holds the
// slot.
func (b *buildSlots) join(server, release, org int64) (t *buildTicket, ahead int) {
	t = &buildTicket{server: server, release: release, org: org, since: now(), ready: make(chan struct{})}
	b.mu.Lock()
	if b.queues == nil {
		b.queues = map[int64]*buildQueue{}
	}
	q := b.queues[server]
	if q == nil {
		q = &buildQueue{}
		b.queues[server] = q
	}
	if q.holder == nil {
		q.holder = t
		close(t.ready)
	} else {
		q.waiting = append(q.waiting, t)
		ahead = len(q.waiting)
	}
	orgs := q.orgs()
	b.mu.Unlock()
	b.notify(orgs)
	return t, ahead
}

// wait blocks until t holds the slot, or ctx ends: then it leaves the
// queue and returns ctx's error.
func (b *buildSlots) wait(ctx context.Context, t *buildTicket) error {
	select {
	case <-t.ready:
		return nil
	case <-ctx.Done():
		b.leave(t)
		return ctx.Err()
	}
}

// leave gives up t's place: the slot goes to the next waiter, or t leaves
// the line. Calling it again does nothing.
func (b *buildSlots) leave(t *buildTicket) {
	b.mu.Lock()
	q := b.queues[t.server]
	if t.left || q == nil {
		b.mu.Unlock()
		return
	}
	t.left = true
	before := q.orgs()
	if q.holder == t {
		q.holder = nil
		if len(q.waiting) > 0 {
			next := q.waiting[0]
			q.waiting = slices.Delete(q.waiting, 0, 1)
			next.since = now()
			q.holder = next
			close(next.ready)
		}
	} else if i := slices.Index(q.waiting, t); i >= 0 {
		q.waiting = slices.Delete(q.waiting, i, i+1)
	}
	if q.holder == nil && len(q.waiting) == 0 {
		delete(b.queues, t.server)
	}
	b.mu.Unlock()
	b.notify(before)
}

// places is where each release at a builder stands, by release ID.
func (b *buildSlots) places() map[int64]buildPlace {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[int64]buildPlace{}
	for server, q := range b.queues {
		if t := q.holder; t != nil {
			out[t.release] = buildPlace{server: server, holding: true, since: t.since}
		}
		for i, t := range q.waiting {
			out[t.release] = buildPlace{server: server, position: i + 1, since: t.since}
		}
	}
	return out
}

// orgs are the orgs of the releases in the queue, each once; mu is held.
func (q *buildQueue) orgs() []int64 {
	var out []int64
	add := func(t *buildTicket) {
		if t != nil && !slices.Contains(out, t.org) {
			out = append(out, t.org)
		}
	}
	add(q.holder)
	for _, t := range q.waiting {
		add(t)
	}
	return out
}

// notify tells changed, when set, which orgs' view of the queue moved.
func (b *buildSlots) notify(orgs []int64) {
	if b.changed != nil {
		b.changed(orgs)
	}
}
