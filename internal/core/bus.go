package core

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/NordTempo/platform/internal/apitypes"
)

// busEvent is an event plus the IDs used for permission filtering.
type busEvent struct {
	projectID int64
	envID     int64
	serviceID int64
	ev        apitypes.Event
}

// bus is an in-process pub/sub for live events. Publishing never blocks: a
// subscriber whose buffer is full misses the event and is marked lagged.
type bus struct {
	mu   sync.RWMutex
	subs map[*subscription]struct{}
}

type subscription struct {
	ch     chan apitypes.Event
	match  func(busEvent) bool
	lagged atomic.Bool
	bus    *bus
	once   sync.Once
}

const subscriptionBuffer = 256

func newBus() *bus { return &bus{subs: map[*subscription]struct{}{}} }

func (b *bus) subscribe(match func(busEvent) bool) *subscription {
	s := &subscription{ch: make(chan apitypes.Event, subscriptionBuffer), match: match, bus: b}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

func (b *bus) publish(e busEvent) {
	if e.ev.Time.IsZero() {
		e.ev.Time = time.Now().UTC()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		if !s.match(e) {
			continue
		}
		select {
		case s.ch <- e.ev:
		default:
			s.lagged.Store(true)
		}
	}
}

// takeLagged reports (and clears) whether events were dropped.
func (s *subscription) takeLagged() bool { return s.lagged.Swap(false) }

func (s *subscription) close() {
	s.once.Do(func() {
		s.bus.mu.Lock()
		delete(s.bus.subs, s)
		s.bus.mu.Unlock()
	})
}
