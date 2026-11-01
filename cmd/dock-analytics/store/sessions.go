package store

import (
	"context"
	"database/sql"
	"time"
)

// SessionGap is the longest pause within a session: a visitor's next
// event after a longer one starts a new session (ADR 0037).
const SessionGap = 30 * time.Minute

// maxVisitorSessions bounds the sessions kept per visitor: enough for a
// spool's backlog that arrives after the same visitor's later sessions.
const maxVisitorSessions = 4

// sessions assigns events to sessions as they are written. Without
// cookies a session is a visitor's events with no gap over SessionGap;
// the visitor ID already changes every day (its salt does), so sessions
// never span days.
//
// Besides the session's ID, each event is marked with what the overview
// needs (mark), so headline numbers are one scan instead of a GROUP BY
// over every session.
//
// The map holds the visitors seen recently and is swept on writes, so it
// is bounded by the visitors of the last half hour of writing. Changes
// are journaled while a write's transaction is open, so a write that
// rolls back leaves the map as it was (begin, rollback). Not safe for
// concurrent use: Write holds writeMu.
type sessions struct {
	m map[sessionKey][]*sessionState
	// sweepAt is the map size at which the next write sweeps it.
	sweepAt int
	now     func() time.Time
	// journal holds the states of the keys changed since begin, as they
	// were, oldest first; nil when no write is open.
	journal []undo
}

type sessionKey struct {
	site    int64
	visitor uint64
}

// sessionState is one of a visitor's recent sessions.
type sessionState struct {
	id  uint64
	src source
	// start is when the session began (Unix ms): its first event as
	// written, kept even if an earlier one arrives later.
	start int64
	// pageviews counts the session's page views written, up to 2.
	pageviews uint8
	// first and last are its first and last events' times (Unix ms).
	first, last int64
	// touched is when it was last written to.
	touched time.Time
}

// undo is a key's sessions before a change: copies, so the change can't
// reach them.
type undo struct {
	key    sessionKey
	states []sessionState
	had    bool
}

// source is where a session came from, taken from its first event and
// written on all of them, so a breakdown or filter by referrer, channel
// or campaign counts whole sessions. Later events have no referrer of
// their own: navigation within a site isn't one.
type source struct {
	referrer, channel                 string
	utmSource, utmMedium, utmCampaign string
	utmTerm, utmContent               string
}

// mark is what assign decides for one event.
type mark struct {
	session uint64
	src     source
	// start is when the session began (Unix ms); a range counts a
	// session's marks only if it began in the range.
	start int64
	// entry marks the session's first page view, engaged its second: a
	// session with one is not a bounce.
	entry, engaged bool
	// grew is how many milliseconds the event added to the session's
	// length; a session's events add up to its duration.
	grew int64
}

// minSweep is the smallest map that is swept.
const minSweep = 4096

// newSessions returns an empty session map.
func newSessions() *sessions {
	return &sessions{m: map[sessionKey][]*sessionState{}, sweepAt: minSweep, now: time.Now}
}

// begin starts journaling, for a write's transaction.
func (s *sessions) begin() { s.journal = make([]undo, 0, 16) }

// commit keeps the changes since begin.
func (s *sessions) commit() { s.journal = nil }

// rollback undoes the changes since begin.
func (s *sessions) rollback() {
	for i := len(s.journal) - 1; i >= 0; i-- {
		u := s.journal[i]
		if !u.had {
			delete(s.m, u.key)
			continue
		}
		states := make([]*sessionState, len(u.states))
		for j := range u.states {
			st := u.states[j]
			states[j] = &st
		}
		s.m[u.key] = states
	}
	s.journal = nil
}

// save journals k's sessions before they change.
func (s *sessions) save(k sessionKey) {
	if s.journal == nil {
		return
	}
	states, had := s.m[k]
	u := undo{key: k, had: had, states: make([]sessionState, len(states))}
	for i, st := range states {
		u.states[i] = *st
	}
	s.journal = append(s.journal, u)
}

// assign returns the session of a visitor's event at t (Unix ms), with
// src the event's own source. An event within SessionGap of one of the
// visitor's recent sessions joins it, also when it is older than that
// session's last event: a spool's backlog arrives in order per server,
// so a late event belongs to the session it is near. Otherwise a new
// session starts.
func (s *sessions) assign(site int64, visitor uint64, t int64, pageview bool, src source) mark {
	gap := SessionGap.Milliseconds()
	now := s.now()
	k := sessionKey{site, visitor}
	s.save(k)
	states := s.m[k]
	for _, st := range states {
		if t < st.first-gap || t > st.last+gap {
			continue
		}
		m := mark{session: st.id, src: st.src, start: st.start, grew: max(0, t-st.last) + max(0, st.first-t)}
		if pageview && st.pageviews < 2 {
			st.pageviews++
			m.entry, m.engaged = st.pageviews == 1, st.pageviews == 2
		}
		st.first, st.last, st.touched = min(st.first, t), max(st.last, t), now
		return m
	}
	st := &sessionState{id: sessionID(site, visitor, t), src: src, start: t, first: t, last: t, touched: now}
	if pageview {
		st.pageviews = 1
	}
	if len(states) == maxVisitorSessions {
		// Forget the one written to longest ago.
		oldest := 0
		for i := range states {
			if states[i].touched.Before(states[oldest].touched) {
				oldest = i
			}
		}
		states = append(states[:oldest], states[oldest+1:]...)
	}
	s.m[k] = append(states, st)
	if len(s.m) >= s.sweepAt {
		s.sweep(now)
	}
	return mark{session: st.id, src: src, start: t, entry: pageview}
}

// peek returns the session a visitor's event at t (Unix ms) falls in,
// without changing it: errors and vitals happen within a session but
// don't make it longer. 0 when the visitor has no session around t.
func (s *sessions) peek(site int64, visitor uint64, t int64) uint64 {
	gap := SessionGap.Milliseconds()
	for _, st := range s.m[sessionKey{site, visitor}] {
		if t >= st.first-gap && t <= st.last+gap {
			return st.id
		}
	}
	return 0
}

// sweep forgets sessions nothing was written to for SessionGap: a live
// visitor's next event would start a new one anyway, and a backlog is
// written in minutes. It is journaled like assign.
func (s *sessions) sweep(now time.Time) {
	for k, states := range s.m {
		live := states[:0:0]
		for _, st := range states {
			if now.Sub(st.touched) <= SessionGap {
				live = append(live, st)
			}
		}
		if len(live) == len(states) {
			continue
		}
		s.save(k)
		if len(live) == 0 {
			delete(s.m, k)
		} else {
			s.m[k] = live
		}
	}
	s.sweepAt = max(minSweep, 2*len(s.m))
}

// prime loads the sessions of the last SessionGap before the newest event,
// so a restart doesn't split the sessions in progress.
func (s *sessions) prime(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `
SELECT site_id, visitor_id, session_id, epoch_ms(min(session_start)), epoch_ms(min(ts)), epoch_ms(max(ts)),
       bool_or(entry)::UTINYINT + bool_or(engaged)::UTINYINT,
       arg_max(referrer, ts), arg_max(channel, ts), arg_max(utm_source, ts), arg_max(utm_medium, ts),
       arg_max(utm_campaign, ts), arg_max(utm_term, ts), arg_max(utm_content, ts)
FROM events
WHERE ts > (SELECT max(ts) FROM events) - INTERVAL 30 MINUTE
GROUP BY site_id, visitor_id, session_id`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	now := s.now()
	for rows.Next() {
		var k sessionKey
		st := &sessionState{touched: now}
		src := &st.src
		if err := rows.Scan(&k.site, &k.visitor, &st.id, &st.start, &st.first, &st.last, &st.pageviews, &src.referrer, &src.channel,
			&src.utmSource, &src.utmMedium, &src.utmCampaign, &src.utmTerm, &src.utmContent); err != nil {
			return err
		}
		if len(s.m[k]) < maxVisitorSessions {
			s.m[k] = append(s.m[k], st)
		}
	}
	return rows.Err()
}

// sessionID derives a session's ID from its visitor and first event:
// stable for the same input, and spread over 64 bits (splitmix64).
func sessionID(site int64, visitor uint64, t int64) uint64 {
	x := visitor ^ uint64(t)*0x9e3779b97f4a7c15 ^ uint64(site)<<48
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}
