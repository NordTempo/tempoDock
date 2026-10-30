package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// Daily rollups (ADR 0037): per site and UTC day, each dimension's values
// with their visitors and counts, and the day's totals. An unfiltered
// dashboard adds up the days in its range and reads raw events only at
// the edges, so it costs the same however busy the site is; filtered
// queries read raw events. Rollups are kept when retention deletes old
// events.
//
// Days are UTC days, the days the visitor ID's salt changes on, so a
// day's visitors are nobody else's: visitors and sessions add up across
// days exactly.
//
// A day is rolled up once it is over. Events that arrive later for it (a
// spool's backlog) mark it stale, in the same transaction that writes
// them, and it is rolled up again. Queries treat stale and unbuilt days
// as raw.
const rollupSchema = `
CREATE TABLE IF NOT EXISTS rollups (
    day      DATE NOT NULL,
    site_id  UINTEGER NOT NULL,
    dim      VARCHAR NOT NULL,
    value    VARCHAR NOT NULL,
    visitors UINTEGER NOT NULL,
    n        UINTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS rollup_totals (
    day       DATE NOT NULL,
    site_id   UINTEGER NOT NULL,
    visitors  UINTEGER NOT NULL,
    pageviews UINTEGER NOT NULL,
    sessions  UINTEGER NOT NULL,
    engaged   UINTEGER NOT NULL,
    grew_ms   UBIGINT NOT NULL,
    events    UINTEGER NOT NULL
);
-- The days rolled up per site; stale ones got events after.
CREATE TABLE IF NOT EXISTS rollup_days (
    site_id UINTEGER NOT NULL,
    day     DATE NOT NULL,
    stale   BOOLEAN NOT NULL,
    PRIMARY KEY (site_id, day)
);
`

// span is how a query's range is read: rolled-up days [from, to) and raw
// events in the raw ranges (Unix ms), all disjoint.
type span struct {
	from, to time.Time
	raw      [][2]int64
}

// rolled reports whether the span has rolled-up days.
func (s span) rolled() bool { return s.to.After(s.from) }

const day = 24 * time.Hour

// rollupSpan splits [from, to) (Unix ms) into the whole days that are
// rolled up for site, and raw events before and after them. The rolled-up
// days end at the first stale one and at the builder's progress.
func (s *Store) rollupSpan(ctx context.Context, site, from, to int64) (span, error) {
	first := time.UnixMilli(from).UTC().Add(day - time.Millisecond).Truncate(day)
	last := time.UnixMilli(to).UTC().Truncate(day)
	end := last
	if b := s.builtThrough(); b.Before(end) {
		end = b
	}
	if !end.After(first) {
		return span{raw: [][2]int64{{from, to}}}, nil
	}
	var stale sql.NullTime
	if err := s.db.QueryRowContext(ctx, "SELECT min(day)::TIMESTAMP FROM rollup_days WHERE site_id = $1 AND stale AND day >= $2 AND day < $3",
		uint32(site), first, end).Scan(&stale); err != nil {
		return span{}, err
	}
	if stale.Valid {
		end = stale.Time.UTC()
	}
	if !end.After(first) {
		return span{raw: [][2]int64{{from, to}}}, nil
	}
	sp := span{from: first, to: end}
	if from < first.UnixMilli() {
		sp.raw = append(sp.raw, [2]int64{from, first.UnixMilli()})
	}
	if end.UnixMilli() < to {
		sp.raw = append(sp.raw, [2]int64{end.UnixMilli(), to})
	}
	return sp, nil
}

// builtThrough is the day the builder has rolled up every site to.
func (s *Store) builtThrough() time.Time {
	s.rollMu.Lock()
	defer s.rollMu.Unlock()
	return s.rolledTo
}

// siteDay names one site's day.
type siteDay struct {
	site int64
	day  time.Time
}

// markStale records, in the open write transaction, that events arrived
// for days that are over (a backlog, or a write across midnight), so
// their rollups are rebuilt. It reports whether it marked any. Called
// with writeMu held.
func (s *Store) markStale(ctx context.Context, ex driver.ExecerContext, events []analytics.Event, now time.Time) (bool, error) {
	today := now.UTC().Truncate(day).UnixMilli()
	var days map[siteDay]bool
	for i := range events {
		if t := events[i].Time; t < today {
			if days == nil {
				days = map[siteDay]bool{}
			}
			days[siteDay{events[i].Site, time.UnixMilli(t).UTC().Truncate(day)}] = true
		}
	}
	for d := range days {
		args := []driver.NamedValue{{Ordinal: 1, Value: uint32(d.site)}, {Ordinal: 2, Value: d.day}}
		if _, err := ex.ExecContext(ctx, "INSERT OR REPLACE INTO rollup_days VALUES ($1, $2, true)", args); err != nil {
			return false, err
		}
		s.staleGen[d]++
	}
	return len(days) > 0, nil
}

// kickRollups wakes the builder (RunRollups) after a write that made work
// for it: stale days, or the first write of a new day. Called with
// writeMu held.
func (s *Store) kickRollups(now time.Time, stale bool) {
	today := now.UTC().Truncate(day)
	if !stale && today.Equal(s.lastWriteDay) {
		return
	}
	s.lastWriteDay = today
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// RunRollups builds rollups whenever a write makes work (kickRollups),
// until ctx ends. It waits on a channel, with no timer: an idle store
// builds nothing. It starts with one pass, for what an earlier run left.
func (s *Store) RunRollups(ctx context.Context, logf func(string, ...any)) {
	for {
		if err := s.BuildRollups(ctx); err != nil && ctx.Err() == nil {
			logf("rollups: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
		}
	}
}

// BuildRollups rolls up every day that is over and isn't (or is stale),
// oldest first, one day per transaction. A day that got events while it
// was being rolled up is marked stale and left for the next pass (the
// write that brought them kicks it); any other failure stops the pass
// there, so later days wait.
func (s *Store) BuildRollups(ctx context.Context) error {
	work, err := s.rollupWork(ctx)
	if err != nil {
		return err
	}
	today := s.now().UTC().Truncate(day)
	for i := 0; i < len(work); {
		j := i
		var sites []int64
		for ; j < len(work) && work[j].day.Equal(work[i].day); j++ {
			sites = append(sites, work[j].site)
		}
		if err := s.buildDay(ctx, work[i].day, sites); err != nil {
			s.setBuiltThrough(work[i].day)
			return fmt.Errorf("roll up %s: %w", work[i].day.Format(time.DateOnly), err)
		}
		i = j
	}
	s.setBuiltThrough(today)
	return nil
}

// rollupWork lists the site days to roll up: the stale ones, and those
// with events since the builder's progress that aren't rolled up, oldest
// first.
func (s *Store) rollupWork(ctx context.Context) ([]siteDay, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	rows, err := s.db.QueryContext(ctx, `
SELECT site_id, day::TIMESTAMP FROM rollup_days WHERE stale
UNION
(SELECT DISTINCT site_id, ts::DATE::TIMESTAMP FROM events WHERE ts >= $1 AND ts < $2
 EXCEPT SELECT site_id, day::TIMESTAMP FROM rollup_days)
ORDER BY 2`, s.builtThrough(), s.now().UTC().Truncate(day))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var work []siteDay
	for rows.Next() {
		var d siteDay
		if err := rows.Scan(&d.site, &d.day); err != nil {
			return nil, err
		}
		d.day = d.day.UTC()
		work = append(work, d)
	}
	return work, rows.Err()
}

// setBuiltThrough records the builder's progress (builtThrough).
func (s *Store) setBuiltThrough(t time.Time) {
	s.rollMu.Lock()
	defer s.rollMu.Unlock()
	s.rolledTo = t
}

// rollupDims are the dimensions rolled up: all of them.
var rollupDims = analytics.Dimensions

// buildDay rolls up one day for sites in one transaction, replacing what
// was there. The events are read in the transaction's snapshot; if a
// write marked one of the days stale meanwhile, the transaction is
// dropped and the days stay stale, for the next pass. rollup_days, which
// writes touch too, is only written at the commit, with writes held off,
// so a write never conflicts with an open rollup.
func (s *Store) buildDay(ctx context.Context, d time.Time, sites []int64) error {
	s.gate.RLock()
	defer s.gate.RUnlock()
	gens := make([]int, len(sites))
	s.writeMu.Lock()
	for i, site := range sites {
		gens[i] = s.staleGen[siteDay{site, d}]
	}
	s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var p params
	ids := make([]string, len(sites))
	for i, site := range sites {
		ids[i] = p.add(uint32(site))
	}
	in := "site_id IN (" + strings.Join(ids, ", ") + ")"
	dayP := p.add(d)
	lo, hi := p.add(d), p.add(d.Add(day))
	scope := in + " AND ts >= " + lo + " AND ts < " + hi
	for _, q := range []string{
		"DELETE FROM rollups WHERE day = " + dayP + " AND " + in,
		"DELETE FROM rollup_totals WHERE day = " + dayP + " AND " + in,
	} {
		if _, err := tx.ExecContext(ctx, q, p...); err != nil {
			return err
		}
	}
	parts := make([]string, 0, len(rollupDims))
	for _, dim := range rollupDims {
		parts = append(parts, rollupSQL(dim, dayP, scope))
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO rollups "+strings.Join(parts, "\nUNION ALL\n"), p...); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO rollup_totals
SELECT `+dayP+`, site_id, count(DISTINCT visitor_id), count(*) FILTER (WHERE kind = 1), count(*) FILTER (WHERE entry),
       count(*) FILTER (WHERE engaged), coalesce(sum(grew_ms), 0), count(*) FILTER (WHERE kind <> 1)
FROM events WHERE `+scope+` GROUP BY site_id`, p...); err != nil {
		return err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	for i, site := range sites {
		if s.staleGen[siteDay{site, d}] != gens[i] {
			// Events arrived meanwhile: drop this rollup, and make sure
			// the days read as stale, also after a restart.
			_ = tx.Rollback()
			for _, site := range sites {
				if _, err := s.db.ExecContext(ctx, "INSERT OR REPLACE INTO rollup_days VALUES ($1, $2, true)", uint32(site), d); err != nil {
					return err
				}
			}
			return nil
		}
	}
	for _, site := range sites {
		if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO rollup_days VALUES ($1, $2, false)", uint32(site), d); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, site := range sites {
		delete(s.staleGen, siteDay{site, d})
	}
	return nil
}

// rollupSQL selects one dimension's rollup rows for a day: (day, site,
// dim, value, visitors, count), with day the placeholder of the day and
// scope the WHERE clause for its sites and events.
func rollupSQL(d analytics.Dimension, dayP, scope string) string {
	head := "SELECT " + dayP + ", site_id, '" + string(d) + "', "
	switch d {
	case analytics.DimEntryPage:
		return head + "path, count(DISTINCT visitor_id), count(*) FROM events WHERE " + scope + " AND entry GROUP BY site_id, path"
	case analytics.DimExitPage:
		return head + fmt.Sprintf(`v, count(DISTINCT vis), count(*) FROM (
    SELECT site_id, %s AS v, any_value(visitor_id) AS vis FROM events WHERE %s AND kind = %d GROUP BY site_id, session_id
) GROUP BY site_id, v`, exitPage, scope, analytics.KindPageview)
	}
	c := columns[d]
	kind := ""
	if c.kind != 0 {
		kind = fmt.Sprintf(" AND kind = %d", c.kind)
	}
	return head + c.expr + ", count(DISTINCT visitor_id), count(*) FROM events WHERE " + scope + kind + " GROUP BY site_id, " + c.expr
}

// loadRollupState reads how far rollups were built, at open.
func (s *Store) loadRollupState(ctx context.Context) error {
	var last sql.NullTime
	if err := s.db.QueryRowContext(ctx, "SELECT max(day)::TIMESTAMP FROM rollup_days WHERE NOT stale").Scan(&last); err != nil {
		return err
	}
	if last.Valid {
		s.setBuiltThrough(last.Time.UTC().Add(day))
	}
	return nil
}

// pruneStep is how much of a site's events one delete covers, so a first
// prune of a long history doesn't hold writes up for long.
const pruneStep = 7 * day

// Prune deletes a site's raw events before q.Before (Unix ms), a week at
// a time from the oldest, letting writes in between; but never from a day
// that isn't rolled up yet, so the day's totals stay. The space is reused
// after the next checkpoint.
func (s *Store) Prune(ctx context.Context, q analytics.PruneQuery) (analytics.PruneResult, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	before := time.UnixMilli(q.Before).UTC()
	if b := s.builtThrough(); b.Before(before) {
		before = b
	}
	var stale, oldest sql.NullTime
	if err := s.db.QueryRowContext(ctx, "SELECT min(day)::TIMESTAMP FROM rollup_days WHERE site_id = $1 AND stale", uint32(q.Site)).Scan(&stale); err != nil {
		return analytics.PruneResult{}, err
	}
	if stale.Valid && stale.Time.Before(before) {
		before = stale.Time
	}
	if err := s.db.QueryRowContext(ctx, "SELECT min(ts) FROM events WHERE site_id = $1", uint32(q.Site)).Scan(&oldest); err != nil || !oldest.Valid {
		return analytics.PruneResult{}, err
	}
	var out analytics.PruneResult
	for from := oldest.Time.UTC(); from.Before(before); from = from.Add(pruneStep) {
		to := from.Add(pruneStep)
		if to.After(before) {
			to = before
		}
		s.writeMu.Lock()
		res, err := s.db.ExecContext(ctx, "DELETE FROM events WHERE site_id = $1 AND ts < $2", uint32(q.Site), to)
		s.writeMu.Unlock()
		if err != nil {
			return out, err
		}
		n, _ := res.RowsAffected()
		out.Deleted += n
	}
	return out, nil
}
