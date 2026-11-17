package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// Budgets (ADR 0037, amended 2026-11-17). Anyone can send a site's ingest
// events and replay chunks, and the ingest limits rates, not totals, so
// the store bounds what it keeps (analytics.StoreLimits):
//
//   - a site keeps so many events and so many bytes of replay chunks per
//     UTC day of arrival; the day's later ones are dropped and counted;
//   - the database and the replay files together stay within a size:
//     past it the oldest days of replays go, then the oldest days of raw
//     events (their daily totals are in the rollups and stay), never
//     today's, which the day budgets hold.
//
// The size is checked every sizeCheckEvery writes and on a day's first:
// no timer. A write through Write or WriteReplay (seeding, benchmarks,
// tests) has no caps; the API's writes have the defaults below unless
// they carry their own.
const (
	// defaultSiteDayEvents is far above a busy site (a million events is
	// some 300,000 page loads with their vitals and clicks), and what the
	// ingest lets one site take in about two and a half minutes.
	defaultSiteDayEvents = 1_000_000
	// defaultSiteDayReplayBytes: chunks are 10-50 KB every 5 s of a page
	// load recording, so 1 GiB is 500 to 2,500 hours of recordings a day.
	defaultSiteDayReplayBytes = 1 << 30
	// defaultMaxBytes and maxBytesDiskShare: the store's default size is
	// the smaller of 10 GB and a tenth of the disk it is on, so a small
	// server's disk isn't the store's to fill.
	defaultMaxBytes   = 10 << 30
	maxBytesDiskShare = 10
)

// budgets is what each site has used today, guarded by Store.writeMu.
type budgets struct {
	day    time.Time
	events map[int64]int64
	replay map[int64]int64
	// over holds the sites already reported over a budget today.
	over map[int64]bool
	// writes counts writes since the last size check.
	writes int
}

// noLimits is what Write and WriteReplay apply.
var noLimits = analytics.StoreLimits{SiteDayEvents: -1, SiteDayReplayBytes: -1, MaxBytes: -1}

// limits fills lim's zero fields with the store's defaults; a negative
// field stays: no cap.
func (s *Store) limits(lim analytics.StoreLimits) analytics.StoreLimits {
	if lim.SiteDayEvents == 0 {
		lim.SiteDayEvents = defaultSiteDayEvents
	}
	if lim.SiteDayReplayBytes == 0 {
		lim.SiteDayReplayBytes = defaultSiteDayReplayBytes
	}
	if lim.MaxBytes == 0 {
		lim.MaxBytes = s.defaultMaxBytes()
	}
	return lim
}

// defaultMaxBytes is the store's default size: defaultMaxBytes, or a
// tenth of the disk the database is on when that is less.
func (s *Store) defaultMaxBytes() int64 {
	if s.path == "" {
		return defaultMaxBytes
	}
	if disk := diskBytes(filepath.Dir(s.path)); disk > 0 {
		return min(defaultMaxBytes, disk/maxBytesDiskShare)
	}
	return defaultMaxBytes
}

// budgetFor makes b today's: on a day's first write (and the first after
// a start) it counts what each site already has. Call with writeMu held.
func (s *Store) budgetFor(ctx context.Context, today time.Time) error {
	b := &s.budget
	if b.day.Equal(today) {
		return nil
	}
	events, replay := map[int64]int64{}, map[int64]int64{}
	for q, into := range map[string]map[int64]int64{
		"SELECT site_id, count(*) FROM events WHERE ts >= $1 GROUP BY site_id":                   events,
		"SELECT site_id, sum(length)::BIGINT FROM replay_chunks WHERE day = $1 GROUP BY site_id": replay,
	} {
		rows, err := s.db.QueryContext(ctx, q, today)
		if err != nil {
			return err
		}
		for rows.Next() {
			var site uint32
			var n int64
			if err := rows.Scan(&site, &n); err != nil {
				_ = rows.Close()
				return err
			}
			into[int64(site)] = n
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
	}
	// The size is checked on a day's first write: yesterday can go now.
	*b = budgets{day: today, events: events, replay: replay, over: map[int64]bool{}, writes: sizeCheckEvery}
	return nil
}

// spend is what a write would add to its sites' use: fits says whether
// one more unit of size for site is within limit (negative: no cap), and
// counts it if so.
type spend struct {
	used  map[int64]int64
	limit int64
	add   map[int64]int64
	// rejected counts what didn't fit, and over the sites it was for.
	rejected int
	over     []int64
}

func (sp *spend) fits(site, size int64) bool {
	if sp.limit >= 0 && sp.used[site]+sp.add[site]+size > sp.limit {
		sp.rejected++
		if !slices.Contains(sp.over, site) {
			sp.over = append(sp.over, site)
		}
		return false
	}
	if sp.add == nil {
		sp.add = map[int64]int64{}
	}
	sp.add[site] += size
	return true
}

// commit counts a written batch's spend, and returns the sites it took
// over a budget that weren't reported today yet. Call with writeMu held.
func (s *Store) commit(sp *spend) []int64 {
	for site, n := range sp.add {
		sp.used[site] += n
	}
	var first []int64
	for _, site := range sp.over {
		if !s.budget.over[site] {
			s.budget.over[site] = true
			first = append(first, site)
		}
	}
	return first
}

// eventsWithin is the events of a batch that fit their sites' budget for
// the day: the batch itself when all do.
func eventsWithin(events []analytics.Event, sp *spend) []analytics.Event {
	for i := range events {
		if sp.fits(events[i].Site, 1) {
			continue
		}
		// The first that doesn't fit: copy what did, and go on filtering.
		kept := append(make([]analytics.Event, 0, len(events)-1), events[:i]...)
		for j := i + 1; j < len(events); j++ {
			if sp.fits(events[j].Site, 1) {
				kept = append(kept, events[j])
			}
		}
		return kept
	}
	return events
}

// chunksWithin is eventsWithin for replay chunks, by their bytes as they
// came: what a chunk takes on disk is only known once it is written, and
// is what the day's use then counts (writeReplay), so a site passes its
// budget by one batch at most.
func chunksWithin(chunks []analytics.ReplayChunk, sp *spend) []analytics.ReplayChunk {
	kept := chunks[:0:0]
	for i := range chunks {
		if sp.fits(chunks[i].Site, int64(len(chunks[i].Data))) {
			kept = append(kept, chunks[i])
		}
	}
	return kept
}

// sizeDue reports whether this write is one to check the store's size
// after. Call with writeMu held.
func (s *Store) sizeDue() bool {
	s.budget.writes++
	if s.budget.writes < sizeCheckEvery {
		return false
	}
	s.budget.writes = 0
	return true
}

// keepWithin drops the oldest days of replays, then of raw events, never
// today's, while the database and the replay files use more than
// maxBytes. It stops when a dropped day of events freed nothing: DuckDB
// gives space back a block of rows at a time, so small days free none,
// and deleting on would only lose history. One runs at a time; a write
// that finds one running leaves it to it.
func (s *Store) keepWithin(ctx context.Context, maxBytes int64, today time.Time) error {
	if maxBytes < 0 || s.path == "" || !s.sizing.TryLock() {
		return nil
	}
	defer s.sizing.Unlock()
	used, err := s.usedBytes(ctx)
	for err == nil && used > maxBytes {
		var dropped bool
		if dropped, err = s.dropOldestReplayDay(ctx, today); err != nil || dropped {
			if err == nil {
				used, err = s.usedBytes(ctx)
			}
			continue
		}
		if dropped, err = s.dropOldestEventDay(ctx, today); err != nil || !dropped {
			break
		}
		before := used
		if used, err = s.usedBytes(ctx); err == nil && used >= before {
			break
		}
	}
	return err
}

// usedBytes is what the store uses on disk: the database's blocks in use
// as of a checkpoint made now (which also frees deleted rows' blocks),
// and the replay chunks the index knows. While a snapshot streams the
// file nothing is checkpointed, and the answer is 0: check again later.
func (s *Store) usedBytes(ctx context.Context) (int64, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	if s.copying.Load() {
		return 0, nil
	}
	if _, err := s.db.ExecContext(ctx, "CHECKPOINT"); err != nil {
		return 0, err
	}
	var db, replay int64
	if err := s.db.QueryRowContext(ctx, "SELECT used_blocks * block_size FROM pragma_database_size() WHERE database_name = current_database()").Scan(&db); err != nil {
		return 0, err
	}
	if err := s.db.QueryRowContext(ctx, "SELECT coalesce(sum(length), 0)::BIGINT FROM replay_chunks").Scan(&replay); err != nil {
		return 0, err
	}
	return db + replay, nil
}

// dropOldestReplayDay deletes every site's replays of the oldest day
// there is, unless that is today: the index rows, then the directories.
func (s *Store) dropOldestReplayDay(ctx context.Context, today time.Time) (bool, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	var oldest sql.NullTime
	if err := s.db.QueryRowContext(ctx, "SELECT min(day)::TIMESTAMP FROM replay_chunks").Scan(&oldest); err != nil || !oldest.Valid {
		return false, err
	}
	day := utcDay(oldest.Time)
	if !day.Before(today) {
		return false, nil
	}
	s.writeMu.Lock()
	_, err := s.db.ExecContext(ctx, "DELETE FROM replay_chunks WHERE day = $1", day)
	s.writeMu.Unlock()
	if err != nil {
		return false, err
	}
	sites, err := os.ReadDir(s.replayDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return true, err
	}
	for _, site := range sites {
		if err := os.RemoveAll(filepath.Join(s.replayDir(), site.Name(), day.Format(time.DateOnly))); err != nil {
			return true, err
		}
	}
	return true, nil
}

// dropOldestEventDay prunes every site's raw events, errors and vitals
// of the oldest day of events there is, unless that is today, and
// reports whether any went: Prune keeps a day that isn't rolled up yet.
func (s *Store) dropOldestEventDay(ctx context.Context, today time.Time) (bool, error) {
	var oldest sql.NullTime
	if err := s.db.QueryRowContext(ctx, "SELECT min(ts) FROM events").Scan(&oldest); err != nil || !oldest.Valid {
		return false, err
	}
	day := utcDay(oldest.Time)
	if !day.Before(today) {
		return false, nil
	}
	before := day.Add(24 * time.Hour)
	rows, err := s.db.QueryContext(ctx, "SELECT DISTINCT site_id FROM events WHERE ts < $1", before)
	if err != nil {
		return false, err
	}
	var sites []int64
	for rows.Next() {
		var site uint32
		if err := rows.Scan(&site); err != nil {
			_ = rows.Close()
			return false, err
		}
		sites = append(sites, int64(site))
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return false, err
	}
	var deleted int64
	for _, site := range sites {
		res, err := s.Prune(ctx, analytics.PruneQuery{Site: site, Before: before.UnixMilli(), ErrorsBefore: before.UnixMilli()})
		if err != nil {
			return deleted > 0, err
		}
		deleted += res.Deleted
	}
	return deleted > 0, nil
}
