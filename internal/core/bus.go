package core

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// busEvent is an event plus the IDs used for permission filtering. An
// event in no org (orgID 0) is the platform's, for operators.
type busEvent struct {
	orgID     int64
	projectID int64
	envID     int64
	serviceID int64
	// serverStats marks platform-wide events (server statistics, alerts),
	// for operators only.
	serverStats bool
	ev          apitypes.Event
	// alert, when set, raises or resolves an alert along with the event
	// (alerts.go). Subscribers other than the alert engine never see it.
	alert *alertSignal
	// secrets marks a log line with secret values in it, published twice
	// (publishBuildLog): as written for subscribers that may reveal them,
	// masked for the rest.
	secrets secretsView
	// adminsOnly limits the event to admins and, when set, the token with
	// ID requester (access requests).
	adminsOnly bool
	requester  int64
}

// secretsView says which subscribers get an event (busEvent.secrets).
type secretsView uint8

const (
	secretsNone     secretsView = iota // no secrets: everyone
	secretsRevealed                    // as written: subscribers that reveal secrets
	secretsMasked                      // masked: the other subscribers
)

// seenBy reports whether the event's audience includes a, beyond canSee.
func (e busEvent) seenBy(a Actor) bool {
	return !e.adminsOnly || rank(a.Scope) >= rank(apitypes.ScopeAdmin) || (e.requester != 0 && a.TokenID == e.requester)
}

// LiveEvent is an event as delivered to subscribers. Every subscriber that
// receives an event shares one LiveEvent, so its JSON is encoded once
// however many clients it is streamed to. It is one pointer, so a
// subscription's buffer of subscriptionBuffer events costs 2 KB, not 40.
type LiveEvent struct{ *liveEvent }

type liveEvent struct {
	apitypes.Event
	projectID int64
	serviceID int64
	// signal is the event's alertSignal, for the alert engine.
	signal *alertSignal
	once   sync.Once
	json   []byte
	err    error
}

func newLiveEvent(ev apitypes.Event) LiveEvent {
	return LiveEvent{&liveEvent{Event: ev}}
}

// JSON returns the event encoded as JSON, encoding it on first use. The
// bytes are shared by every subscriber; don't modify them.
func (e LiveEvent) JSON() ([]byte, error) {
	e.once.Do(func() { e.json, e.err = json.Marshal(e.Event) })
	return e.json, e.err
}

// bus is an in-process pub/sub for live events. Publishing never blocks: a
// subscriber whose buffer is full misses the event and is marked lagged.
type bus struct {
	mu   sync.RWMutex
	subs map[*subscription]struct{}
	// names has at most one entry per project renamed during this process.
	// No historical aliases: long-running work only needs its current name.
	names map[int64]string
}

type subscription struct {
	ch    chan LiveEvent
	match func(busEvent) bool
	// reveal: the subscriber may see secret values (busEvent.secrets).
	reveal bool
	lagged atomic.Bool
	bus    *bus
	once   sync.Once
}

const subscriptionBuffer = 256

func newBus() *bus { return &bus{subs: map[*subscription]struct{}{}, names: map[int64]string{}} }

func (b *bus) subscribe(match func(busEvent) bool) *subscription {
	return b.subscribeAs(false, match)
}

// subscribeAs is subscribe for a subscriber that may reveal secret values
// or not: it gets log lines as written or masked (busEvent.secrets).
func (b *bus) subscribeAs(reveal bool, match func(busEvent) bool) *subscription {
	s := &subscription{ch: make(chan LiveEvent, subscriptionBuffer), match: match, reveal: reveal, bus: b}
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
	e.ev = currentProjectEvent(e.ev, b.names[e.projectID])
	live := newLiveEvent(e.ev)
	live.signal, live.projectID, live.serviceID = e.alert, e.projectID, e.serviceID
	for s := range b.subs {
		if (e.secrets == secretsRevealed && !s.reveal) || (e.secrets == secretsMasked && s.reveal) || !s.match(e) {
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

// renameProject remembers only the latest name for work already in flight.
func (b *bus) renameProject(id int64, name string) {
	b.mu.Lock()
	b.names[id] = name
	b.mu.Unlock()
}

// projectName canonicalizes an in-flight operation's label without a DB read.
func (b *bus) projectName(id int64, fallback string) string {
	b.mu.RLock()
	name := b.names[id]
	b.mu.RUnlock()
	if name != "" {
		return name
	}
	return fallback
}

// currentProjectEvent copies only payloads whose captured name is out of date;
// callers may still be using the original payload values.
func currentProjectEvent(ev apitypes.Event, name string) apitypes.Event {
	if name == "" || name == ev.Project {
		return ev
	}
	ev.Project = name
	if ev.Release != nil {
		r := *ev.Release
		r.Project = name
		ev.Release = &r
	}
	if ev.Domain != nil {
		d := *ev.Domain
		d.Project = name
		ev.Domain = &d
	}
	if ev.Alert != nil && ev.Alert.ResolvedAt == nil {
		a := *ev.Alert
		a.Title = strings.Replace(a.Title, a.Project+"/", name+"/", 1)
		a.Project = name
		ev.Alert = &a
	}
	if ev.Audit != nil {
		a := *ev.Audit
		a.Project = name
		ev.Audit = &a
	}
	return ev
}
