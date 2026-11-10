// Package conncache is the connection cache every data engine uses (ADR
// 0016): connections keyed by (database, role), at most MaxPerKey per key
// and Max in total, each closed IdleTimeout after its last use by its own
// timer. There is no pool goroutine: an idle cache holds nothing but the
// timers of connections still open, and nothing at all once they closed.
package conncache

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/data"
)

// ErrClosed is returned by Acquire after Close.
var ErrClosed = errors.New("data engine closed")

// Key identifies a set of cached connections: one database (the
// Target's ServiceID) and one role.
type Key struct {
	ID   int64
	Role data.Role
}

// Options configure a Cache. Zero limits are package data's.
type Options[C any] struct {
	IdleTimeout time.Duration
	MaxPerKey   int
	Max         int
	// Reusable reports whether a released connection can go back to the
	// cache: not closed by the server, no transaction left open.
	Reusable func(c C) bool
	// Close closes a connection. broken means its state is unknown (a
	// result abandoned mid-stream): cut it short rather than waiting on
	// it.
	Close func(c C, broken bool)
	// Emptied, when set, is called when an idle timeout removes the last
	// connection of a database, with the cache's lock held so the two
	// happen at once: it must not call the cache. Engines drop what they
	// cache per database (introspection) with it.
	Emptied func(id int64)
}

// Cache hands out cached connections of type C. It is safe for
// concurrent use; create it with New.
type Cache[C any] struct {
	o       Options[C]
	mu      sync.Mutex
	conns   map[Key][]*Conn[C]
	total   int
	waiters int
	// wake is closed (and replaced) when a connection is released or
	// removed while someone waits in Acquire.
	wake   chan struct{}
	closed bool
}

// Conn is one cached connection, held by one caller between Acquire and
// Release. busy, stale, removed, idleAt and timer are guarded by the
// cache's lock.
type Conn[C any] struct {
	// Conn is the connection.
	Conn C
	// Broken: the holder saw the connection in an unknown state (a
	// result abandoned mid-stream, a canceled context); Release closes
	// it.
	Broken bool

	key    Key
	opened bool
	// busy: handed out (or still connecting).
	busy bool
	// stale: Drop or Close ran while busy; close it on release.
	stale   bool
	removed bool
	idleAt  time.Time
	// timer closes the connection IdleTimeout after its last release. It
	// is created on the first release and re-armed (Reset) on each one.
	timer *time.Timer
}

// New returns an empty cache.
func New[C any](o Options[C]) *Cache[C] {
	if o.IdleTimeout == 0 {
		o.IdleTimeout = data.IdleTimeout
	}
	if o.MaxPerKey == 0 {
		o.MaxPerKey = data.MaxConnsPerKey
	}
	if o.Max == 0 {
		o.Max = data.MaxConns
	}
	return &Cache[C]{o: o, conns: map[Key][]*Conn[C]{}, wake: make(chan struct{})}
}

// Acquire hands out a connection for k: an idle cached one, a new one from
// open within the limits (evicting the longest-idle connection of another
// key at the total limit), or the next one released, waiting until ctx
// ends. open's error is returned as is. Release it with Release.
func (c *Cache[C]) Acquire(ctx context.Context, k Key, open func(context.Context) (C, error)) (*Conn[C], error) {
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return nil, ErrClosed
		}
		list := c.conns[k]
		for _, cn := range list {
			if !cn.busy {
				cn.busy = true
				if cn.timer != nil {
					cn.timer.Stop()
				}
				c.mu.Unlock()
				return cn, nil
			}
		}
		if len(list) < c.o.MaxPerKey && c.total >= c.o.Max {
			if victim := c.evictIdleLocked(); victim != nil {
				c.mu.Unlock()
				c.close(victim)
				continue
			}
		}
		if len(list) < c.o.MaxPerKey && c.total < c.o.Max {
			cn := &Conn[C]{key: k, busy: true}
			c.conns[k] = append(list, cn)
			c.total++
			c.mu.Unlock()
			conn, err := open(ctx)
			if err != nil {
				c.mu.Lock()
				c.removeLocked(cn)
				c.mu.Unlock()
				return nil, err
			}
			cn.Conn, cn.opened = conn, true
			return cn, nil
		}
		c.waiters++
		wake := c.wake
		c.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
		}
		c.mu.Lock()
		c.waiters--
		c.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
}

// Release returns cn to the cache, or closes it when it is broken, stale
// or not Reusable.
func (c *Cache[C]) Release(cn *Conn[C]) {
	discard := cn.Broken || !c.o.Reusable(cn.Conn)
	c.mu.Lock()
	if discard || cn.stale || c.closed {
		c.removeLocked(cn)
		c.mu.Unlock()
		c.close(cn)
		return
	}
	cn.busy = false
	cn.idleAt = time.Now()
	if cn.timer == nil {
		cn.timer = time.AfterFunc(c.o.IdleTimeout, func() { c.closeIdle(cn) })
	} else {
		cn.timer.Reset(c.o.IdleTimeout)
	}
	c.broadcastLocked()
	c.mu.Unlock()
}

// Drop removes the connections whose key matches: idle ones are closed
// now, busy ones when they are released.
func (c *Cache[C]) Drop(match func(Key) bool) {
	c.mu.Lock()
	idle := c.dropLocked(match)
	c.mu.Unlock()
	for _, cn := range idle {
		c.close(cn)
	}
}

// Close closes every connection (busy ones when released); later
// Acquires fail with ErrClosed.
func (c *Cache[C]) Close() {
	c.mu.Lock()
	c.closed = true
	idle := c.dropLocked(func(Key) bool { return true })
	c.broadcastLocked()
	c.mu.Unlock()
	for _, cn := range idle {
		c.close(cn)
	}
}

// Len is how many connections are open or opening.
func (c *Cache[C]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

// LenKey is how many connections k has open or opening.
func (c *Cache[C]) LenKey(k Key) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.conns[k])
}

// closeIdle is cn's idle timer: it closes cn if it is still idle and tells
// Emptied when it was the database's last connection.
func (c *Cache[C]) closeIdle(cn *Conn[C]) {
	c.mu.Lock()
	if cn.busy || cn.removed || time.Since(cn.idleAt) < c.o.IdleTimeout {
		c.mu.Unlock()
		return
	}
	c.removeLocked(cn)
	if c.o.Emptied != nil && !c.hasIDLocked(cn.key.ID) {
		c.o.Emptied(cn.key.ID)
	}
	c.mu.Unlock()
	c.close(cn)
}

// evictIdleLocked removes the longest-idle connection to make room under
// Max, or returns nil when every connection is busy.
func (c *Cache[C]) evictIdleLocked() *Conn[C] {
	var victim *Conn[C]
	for _, list := range c.conns {
		for _, cn := range list {
			if !cn.busy && (victim == nil || cn.idleAt.Before(victim.idleAt)) {
				victim = cn
			}
		}
	}
	if victim != nil {
		c.removeLocked(victim)
	}
	return victim
}

// removeLocked takes cn out of the cache and wakes waiters.
func (c *Cache[C]) removeLocked(cn *Conn[C]) {
	if cn.removed {
		return
	}
	cn.removed = true
	list := c.conns[cn.key]
	for i, x := range list {
		if x == cn {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(c.conns, cn.key)
	} else {
		c.conns[cn.key] = list
	}
	c.total--
	c.broadcastLocked()
}

// hasIDLocked reports whether any connection of database id is cached.
func (c *Cache[C]) hasIDLocked(id int64) bool {
	for k := range c.conns {
		if k.ID == id {
			return true
		}
	}
	return false
}

// broadcastLocked wakes everyone waiting in Acquire.
func (c *Cache[C]) broadcastLocked() {
	if c.waiters > 0 {
		close(c.wake)
		c.wake = make(chan struct{})
	}
}

// dropLocked removes the matching connections: idle ones are returned
// for closing, busy ones are marked stale and closed on release.
func (c *Cache[C]) dropLocked(match func(Key) bool) []*Conn[C] {
	var idle []*Conn[C]
	for k, list := range c.conns {
		if !match(k) {
			continue
		}
		for _, cn := range list {
			if cn.busy {
				cn.stale = true
			} else {
				idle = append(idle, cn)
			}
		}
	}
	for _, cn := range idle {
		c.removeLocked(cn)
	}
	return idle
}

// close stops cn's timer and closes its connection, if it was opened.
func (c *Cache[C]) close(cn *Conn[C]) {
	if cn.timer != nil {
		cn.timer.Stop()
	}
	if cn.opened {
		c.o.Close(cn.Conn, cn.Broken)
	}
}
