package core

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// busEvent is an event plus the IDs used for permission filtering.
type busEvent struct {
	projectID int64
	envID     int64
	serviceID int64
	// serverStats marks server-wide statistics, for unlimited admins only.
	serverStats bool
	ev          apitypes.Event
}

// LiveEvent is an event as delivered to subscribers. Every subscriber that
// receives an event shares one LiveEvent, so its JSON is encoded once
// however many clients it is streamed to.
type LiveEvent struct {
	apitypes.Event
	enc *encodedEvent
}

type encodedEvent struct {
	once sync.Once
	json []byte
	err  error
}

func newLiveEvent(ev apitypes.Event) LiveEvent {
	return LiveEvent{Event: ev, enc: &encodedEvent{}}
}

// JSON returns the event encoded as JSON, encoding it on first use. The
// bytes are shared by every subscriber; don't modify them.
func (e LiveEvent) JSON() ([]byte, error) {
	e.enc.once.Do(func() { e.enc.json, e.enc.err = json.Marshal(e.Event) })
	return e.enc.json, e.enc.err
}

// bus is an in-process pub/sub for live events. Publishing never blocks: a
// subscriber whose buffer is full misses the event and is marked lagged.
type bus struct {
	mu   sync.RWMutex
	subs map[*subscription]struct{}
}

type subscription struct {
	ch     chan LiveEvent
	match  func(busEvent) bool
	lagged atomic.Bool
	bus    *bus
	once   sync.Once
}

const subscriptionBuffer = 256

func newBus() *bus { return &bus{subs: map[*subscription]struct{}{}} }

func (b *bus) subscribe(match func(busEvent) bool) *subscription {
	s := &subscription{ch: make(chan LiveEvent, subscriptionBuffer), match: match, bus: b}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

func (b *bus) publish(e busEvent) {
	if e.ev.Time.IsZero() {
		e.ev.Time = time.Now().UTC()
	}
	live := newLiveEvent(e.ev)
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		if !s.match(e) {
			continue
		}
		select {
		case s.ch <- live:
		default:
			s.lagged.Store(true)
		}
	}
}

// takeLagged reports (and clears) whether events were dropped.
func (s *subscription) takeLagged() bool { return s.lagged.Swap(false) }

// drain returns first and the events already queued behind it, so a client
// gets them in one write. Where events were dropped, a lagged event goes
// first.
func (s *subscription) drain(first LiveEvent) []LiveEvent {
	batch := make([]LiveEvent, 0, 1+len(s.ch))
	add := func(ev LiveEvent) {
		if s.takeLagged() {
			batch = append(batch, newLiveEvent(apitypes.Event{Type: apitypes.EventLagged, Time: now()}))
		}
		batch = append(batch, ev)
	}
	add(first)
	for range cap(s.ch) {
		select {
		case ev := <-s.ch:
			add(ev)
		default:
			return batch
		}
	}
	return batch
}

func (s *subscription) close() {
	s.once.Do(func() {
		s.bus.mu.Lock()
		delete(s.bus.subs, s)
		s.bus.mu.Unlock()
	})
}
