package store

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"
	"github.com/klauspost/compress/zstd"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// Session replay (ADR 0037, phase A3). A chunk's rrweb events are kept
// zstd-compressed in one file per site and UTC day,
// replay/<site>/<YYYY-MM-DD>/chunks.zst next to the database: each chunk
// one zstd frame, found by the offset and length replay_chunks keeps.
// Deleting a day is deleting its directory. A write appends the frames
// and syncs the files before its transaction commits, so the index never
// points at bytes that aren't there; a write that fails after leaves
// bytes nothing points at, which the day's deletion takes along.
//
// Replays are stitched when read: a visitor's segments (page loads) with
// no gap over the session gap are one replay.
const replaySchema = `
CREATE TABLE IF NOT EXISTS replay_chunks (
    site_id    UINTEGER NOT NULL,
    segment    UBIGINT NOT NULL,
    seq        UINTEGER NOT NULL,
    visitor_id UBIGINT NOT NULL,
    -- The chunk's first and last events in the server's time.
    ts_first   TIMESTAMP NOT NULL,
    ts_last    TIMESTAMP NOT NULL,
    -- What to add to the chunk's rrweb times for the server's clock.
    skew       BIGINT NOT NULL,
    day        DATE NOT NULL,
    off        UBIGINT NOT NULL,
    length     UINTEGER NOT NULL,
    events     UINTEGER NOT NULL,
    path       VARCHAR NOT NULL,
    release_id UINTEGER NOT NULL,
    country    VARCHAR NOT NULL,
    browser    VARCHAR NOT NULL,
    os         VARCHAR NOT NULL,
    device     VARCHAR NOT NULL
);
`

// replayDir is where a store's replay files are: next to its database.
func (s *Store) replayDir() string {
	if s.path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.path), "replay")
}

// replayFile is a site and day's file, relative to replayDir.
func replayFile(site int64, day time.Time) string {
	return filepath.Join(strconv.FormatInt(site, 10), day.Format(time.DateOnly), "chunks.zst")
}

// zstd coders, shared: EncodeAll and DecodeAll are safe for concurrent
// use.
var (
	zenc, _ = zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	zdec, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(64<<20))
)

// gunzipPool reuses gzip readers for chunks' data.
var gunzipPool sync.Pool

// gunzip decompresses a chunk's data, at most max bytes.
func gunzip(data []byte, max int64) ([]byte, error) {
	zr, _ := gunzipPool.Get().(*gzip.Reader)
	var err error
	if zr == nil {
		zr, err = gzip.NewReader(bytes.NewReader(data))
	} else {
		err = zr.Reset(bytes.NewReader(data))
	}
	if err != nil {
		return nil, err
	}
	defer gunzipPool.Put(zr)
	out, err := io.ReadAll(io.LimitReader(zr, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > max {
		return nil, fmt.Errorf("a replay chunk over %d bytes uncompressed", max)
	}
	return out, nil
}

// maxChunkRaw caps a chunk's events uncompressed: rrweb's JSON compresses
// about ten times, so this is far above anything a 384 KB chunk holds.
const maxChunkRaw = 32 << 20

// placed is a chunk where its frame went.
type placed struct {
	day    time.Time
	off    int64
	length int
}

// WriteReplay appends replay chunks from source, a replay spool whose
// lines up to position are now all written; a batch at or before the
// source's cursor was written before and is skipped. Chunks that aren't
// gzip are skipped and counted. locate may be nil.
// It applies no budgets: for tests.
func (s *Store) WriteReplay(ctx context.Context, source string, position uint64, chunks []analytics.ReplayChunk, locate Locator) (written, skipped int, err error) {
	res, err := s.WriteReplayLimited(ctx, source, position, chunks, locate, noLimits)
	return res.Written, res.Skipped, err
}

// WriteReplayLimited is WriteReplay within lim (budget.go): chunks past
// their site's bytes for the day are dropped and counted in the result,
// and the store's size is checked when it is due.
func (s *Store) WriteReplayLimited(ctx context.Context, source string, position uint64, chunks []analytics.ReplayChunk, locate Locator, lim analytics.StoreLimits) (analytics.WriteResult, error) {
	lim = s.limits(lim)
	res, today, due, err := s.writeReplay(ctx, source, position, chunks, locate, lim)
	if err != nil || !due {
		return res, err
	}
	if err := s.keepWithin(ctx, lim.MaxBytes, today); err != nil {
		return res, fmt.Errorf("the chunks are written; keeping the store within its size: %w", err)
	}
	return res, nil
}

// writeReplay is WriteReplayLimited's transaction; due says the store's
// size is to be checked now, with the locks let go.
func (s *Store) writeReplay(ctx context.Context, source string, position uint64, chunks []analytics.ReplayChunk, locate Locator, lim analytics.StoreLimits) (res analytics.WriteResult, today time.Time, due bool, err error) {
	dir := s.replayDir()
	if dir == "" {
		return res, today, false, errors.New("an in-memory store keeps no replays")
	}
	s.gate.RLock()
	defer s.gate.RUnlock()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	today = utcDay(s.now())
	if err := s.budgetFor(ctx, today); err != nil {
		return res, today, false, fmt.Errorf("write replay chunks: %w", err)
	}
	sp := &spend{used: s.budget.replay, limit: lim.SiteDayReplayBytes}
	chunks = chunksWithin(chunks, sp)
	var written, skipped int
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return res, today, false, err
	}
	defer func() { _ = conn.Close() }()
	err = conn.Raw(func(dc any) error {
		c := dc.(driver.Conn)
		ex := dc.(driver.ExecerContext)
		qr := dc.(driver.QueryerContext)
		if _, err := ex.ExecContext(ctx, "BEGIN TRANSACTION", nil); err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
				_, _ = ex.ExecContext(context.WithoutCancel(ctx), "ROLLBACK", nil)
			}
		}()
		cur, err := cursorOf(ctx, qr, source)
		if err != nil {
			return err
		}
		if position <= cur {
			return nil // written before
		}
		places, n, err := s.appendFrames(dir, chunks)
		if err != nil {
			return err
		}
		skipped = n
		// The day's use is what went to disk: the frames, not the
		// chunks as they came (which decided what fits).
		clear(sp.add)
		for i, p := range places {
			if p.length > 0 {
				sp.add[chunks[i].Site] += int64(p.length)
			}
		}
		a, err := duckdb.NewAppenderFromConn(c, "", "replay_chunks")
		if err != nil {
			return err
		}
		for i := range chunks {
			ch, p := &chunks[i], places[i]
			if p.length == 0 {
				continue
			}
			skew := ch.Time - ch.Clock
			loc := lookup(locate, ch.IPPrefix)
			if err := a.AppendRow(uint32(ch.Site), ch.Segment, ch.Seq, ch.Visitor,
				time.UnixMilli(ch.First+skew).UTC(), time.UnixMilli(ch.Last+skew).UTC(), skew, p.day, uint64(p.off), uint32(p.length),
				uint32(ch.Events), ch.Path, uint32(ch.Release), loc.Country, ch.Browser, ch.OS, ch.Device); err != nil {
				_ = a.Close()
				return err
			}
			written++
		}
		if err := a.Close(); err != nil {
			return err
		}
		if _, err := ex.ExecContext(ctx, "INSERT OR REPLACE INTO cursors VALUES ($1, $2)",
			[]driver.NamedValue{{Ordinal: 1, Value: source}, {Ordinal: 2, Value: position}}); err != nil {
			return err
		}
		if _, err := ex.ExecContext(ctx, "COMMIT", nil); err != nil {
			return err
		}
		committed = true
		res.Rejected, res.OverBudget = sp.rejected, s.commit(sp)
		return nil
	})
	if err != nil {
		return analytics.WriteResult{}, today, false, fmt.Errorf("write replay chunks: %w", err)
	}
	res.Written, res.Skipped = written, skipped
	return res, today, s.sizeDue(), nil
}

// appendFrames recompresses each chunk's events as a zstd frame and
// appends it to its site and day's file, then syncs the files. A chunk
// that isn't gzip gets no place (length 0) and is counted.
func (s *Store) appendFrames(dir string, chunks []analytics.ReplayChunk) ([]placed, int, error) {
	places := make([]placed, len(chunks))
	files := map[string]*os.File{}
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()
	skipped := 0
	for i := range chunks {
		ch := &chunks[i]
		raw, err := gunzip(ch.Data, maxChunkRaw)
		if err != nil {
			skipped++
			continue
		}
		day := time.UnixMilli(ch.Time).UTC().Truncate(24 * time.Hour)
		name := filepath.Join(dir, replayFile(ch.Site, day))
		f := files[name]
		if f == nil {
			if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
				return nil, 0, err
			}
			if f, err = os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600); err != nil {
				return nil, 0, err
			}
			files[name] = f
		}
		off, err := f.Seek(0, io.SeekEnd)
		if err != nil {
			return nil, 0, err
		}
		frame := zenc.EncodeAll(raw, nil)
		if _, err := f.Write(frame); err != nil {
			return nil, 0, err
		}
		places[i] = placed{day: day, off: off, length: len(frame)}
	}
	for _, f := range files {
		if err := f.Sync(); err != nil {
			return nil, 0, err
		}
	}
	return places, skipped, nil
}

// replayColumns are what replays filter by, as the chunks name them.
var replayColumns = map[analytics.Dimension]string{
	analytics.DimPage: "path", analytics.DimCountry: "country", analytics.DimBrowser: "browser",
	analytics.DimOS: "os", analytics.DimDevice: "device", analytics.DimRelease: "release_id::VARCHAR",
}

// segmentsSQL is the segments of a site's chunks between two days, with
// their visitors, times and what they share, bound to p.
func segmentsSQL(p *params, site int64, from, to time.Time, having string) string {
	return `SELECT segment, any_value(visitor_id) AS v, min(ts_first) AS s, max(ts_last) AS e,
       arg_min(path, seq) AS entry, arg_max(path, seq) AS exit, any_value(country) AS country, any_value(browser) AS browser,
       any_value(os) AS os, any_value(device) AS device, arg_min(release_id, seq) AS rel, sum(length) AS bytes,
       arg_min(skew, seq) AS skew
FROM replay_chunks WHERE site_id = ` + p.add(uint32(site)) + ` AND day >= ` + p.add(from) + ` AND day <= ` + p.add(to) + `
GROUP BY segment` + having
}

// Replays returns a site's replays that began in q's range, newest first:
// its visitors' recorded page loads stitched into sessions.
func (s *Store) Replays(ctx context.Context, q analytics.ReplaysQuery) ([]analytics.Replay, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	if q.To <= q.From {
		return nil, badQuery("the range is empty: from %d is not before to %d", q.From, q.To)
	}
	var p params
	var having []string
	for _, f := range q.Filters {
		col, ok := replayColumns[f.Dimension]
		if !ok {
			return nil, badQuery("replays can't be filtered by %s; they record the page, country, browser, OS, device and release", f.Dimension)
		}
		switch f.Op {
		case analytics.OpIs:
			having = append(having, "bool_or("+col+" = "+p.add(f.Value)+")")
		case analytics.OpIsNot:
			having = append(having, "bool_and("+col+" <> "+p.add(f.Value)+")")
		case analytics.OpContains:
			having = append(having, "bool_or("+col+" ILIKE "+p.add("%"+escapeLike(f.Value)+"%")+" ESCAPE '\\')")
		default:
			return nil, badQuery("unknown filter operator %q; use is, is_not or contains", f.Op)
		}
	}
	hv := ""
	if len(having) > 0 {
		hv = " HAVING " + strings.Join(having, " AND ")
	}
	limit := q.Limit
	if limit <= 0 || limit > analytics.MaxReplays {
		limit = analytics.MaxReplays
	}
	from, to := time.UnixMilli(q.From).UTC(), time.UnixMilli(q.To).UTC()
	// A replay that began in the range may have segments the day after.
	segs := segmentsSQL(&p, q.Site, from.Truncate(24*time.Hour), to.Add(24*time.Hour), hv)
	site := p.add(uint32(q.Site))
	lo, hi := p.add(from), p.add(to)
	errorsOnly := ""
	if q.ErrorsOnly {
		errorsOnly = " WHERE errs > 0"
	}
	rows, err := s.db.QueryContext(ctx, `WITH seg AS (`+segs+`),
brk AS (
    SELECT *, CASE WHEN lag(e) OVER w IS NULL OR s - lag(e) OVER w > INTERVAL `+strconv.Itoa(int(SessionGap.Minutes()))+` MINUTE THEN 1 ELSE 0 END AS b
    FROM seg WINDOW w AS (PARTITION BY v ORDER BY s)
),
grp AS (SELECT *, sum(b) OVER (PARTITION BY v ORDER BY s ROWS UNBOUNDED PRECEDING) AS n FROM brk),
r AS (
    SELECT v, arg_min(segment, s) AS id, min(s) AS s, max(e) AS e, count(*) AS segs, arg_min(entry, s) AS entry,
           arg_max(exit, s) AS exit, arg_min(country, s) AS country, arg_min(browser, s) AS browser, arg_min(os, s) AS os,
           arg_min(device, s) AS device, arg_min(rel, s) AS rel, sum(bytes)::BIGINT AS bytes
    FROM grp GROUP BY v, n
),
counted AS (
    SELECT r.*,
           (SELECT count(*) FROM errors WHERE site_id = `+site+` AND visitor_id = r.v AND ts >= r.s AND ts <= r.e + INTERVAL 5 SECOND) AS errs,
           (SELECT count(*) FROM events WHERE site_id = `+site+` AND visitor_id = r.v AND kind = 2 AND ts >= r.s AND ts <= r.e + INTERVAL 5 SECOND) AS evs
    FROM r WHERE r.s >= `+lo+` AND r.s < `+hi+`
)
SELECT id, v, epoch_ms(s), epoch_ms(e), segs, entry, exit, country, browser, os, device, rel, errs, evs, bytes
FROM counted`+errorsOnly+` ORDER BY s DESC LIMIT `+strconv.Itoa(limit), p...)
	if err != nil {
		return nil, err
	}
	out := []analytics.Replay{}
	var visitors []uint64
	for rows.Next() {
		var r analytics.Replay
		var id, v uint64
		var rel uint32
		if err := rows.Scan(&id, &v, &r.Start, &r.End, &r.Segments, &r.EntryPath, &r.ExitPath, &r.Country, &r.Browser, &r.OS, &r.Device,
			&rel, &r.Errors, &r.Events, &r.Bytes); err != nil {
			_ = rows.Close()
			return nil, err
		}
		r.ID, r.Release = groupHex(id), int64(rel)
		out = append(out, r)
		visitors = append(visitors, v)
	}
	if err := rows.Close(); err != nil || len(out) == 0 {
		return out, err
	}
	return out, s.replayMarks(ctx, q.Site, out, visitors)
}

// replayMarks fills listed replays' marks: their visitors' page views and
// errors in their time spans, in one query for the list.
func (s *Store) replayMarks(ctx context.Context, site int64, out []analytics.Replay, visitors []uint64) error {
	lo, hi := out[0].Start, out[0].End
	in := make([]string, 0, len(visitors))
	var p params
	sp := p.add(uint32(site))
	seen := map[uint64]bool{}
	for i, r := range out {
		lo, hi = min(lo, r.Start), max(hi, r.End)
		if !seen[visitors[i]] {
			seen[visitors[i]] = true
			in = append(in, p.addU64(visitors[i]))
		}
	}
	from, to := p.add(time.UnixMilli(lo-1000).UTC()), p.add(time.UnixMilli(hi+5000).UTC())
	vs := strings.Join(in, ",")
	rows, err := s.db.QueryContext(ctx, `SELECT * FROM (
    SELECT visitor_id, epoch_ms(ts) AS t, kind, '' FROM events
    WHERE site_id = `+sp+` AND kind = 1 AND visitor_id IN (`+vs+`) AND ts >= `+from+` AND ts <= `+to+`
    UNION ALL
    SELECT visitor_id, epoch_ms(ts), 4, type FROM errors
    WHERE site_id = `+sp+` AND visitor_id IN (`+vs+`) AND ts >= `+from+` AND ts <= `+to+`
) ORDER BY t LIMIT `+strconv.Itoa(analytics.MaxReplayMarks*len(out)), p...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var v uint64
		var m analytics.ReplayMark
		var kind uint8
		if err := rows.Scan(&v, &m.T, &kind, &m.Name); err != nil {
			return err
		}
		m.Kind = analytics.Kind(kind)
		for i := range out {
			r := &out[i]
			if visitors[i] == v && m.T >= r.Start-1000 && m.T <= r.End+5000 && len(r.Marks) < analytics.MaxReplayMarks {
				r.Marks = append(r.Marks, m)
			}
		}
	}
	return rows.Err()
}

// segmentRow is a segment as Replay reads it.
type segmentRow struct {
	id               uint64
	visitor          uint64
	start, end       time.Time
	entry, exit      string
	country, browser string
	os, device       string
	release, bytes   int64
	skew             int64
}

// Replay returns the replay one of whose segments is q.ID: its segments,
// and the page views, custom events and errors of its visitor while it
// was recorded.
func (s *Store) Replay(ctx context.Context, q analytics.ReplayQuery) (analytics.ReplayDetail, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	id, err := strconv.ParseUint(q.ID, 16, 64)
	if err != nil {
		return analytics.ReplayDetail{}, badQuery("replay %q isn't one; replays are 16 hex digits", q.ID)
	}
	var visitor uint64
	var at time.Time
	err = s.db.QueryRowContext(ctx, "SELECT any_value(visitor_id), min(ts_first) FROM replay_chunks WHERE site_id = $1 AND segment = $2::UBIGINT HAVING count(*) > 0",
		uint32(q.Site), strconv.FormatUint(id, 10)).Scan(&visitor, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return analytics.ReplayDetail{}, badQuery("this site has no replay %s; it may have been deleted with its retention", q.ID)
	}
	if err != nil {
		return analytics.ReplayDetail{}, err
	}
	// The visitor's segments around it: a visitor ID lasts a day.
	var p params
	segs := segmentsSQL(&p, q.Site, at.Truncate(24*time.Hour).Add(-24*time.Hour), at.Add(24*time.Hour), " HAVING any_value(visitor_id) = "+p.addU64(visitor))
	rows, err := s.db.QueryContext(ctx, `SELECT segment, v, s, e, entry, exit, country, browser, os, device, rel, bytes::BIGINT, skew FROM (`+segs+`) ORDER BY s`, p...)
	if err != nil {
		return analytics.ReplayDetail{}, err
	}
	var all []segmentRow
	for rows.Next() {
		var r segmentRow
		var rel uint32
		if err := rows.Scan(&r.id, &r.visitor, &r.start, &r.end, &r.entry, &r.exit, &r.country, &r.browser, &r.os, &r.device, &rel, &r.bytes, &r.skew); err != nil {
			_ = rows.Close()
			return analytics.ReplayDetail{}, err
		}
		r.release = int64(rel)
		all = append(all, r)
	}
	if err := rows.Close(); err != nil {
		return analytics.ReplayDetail{}, err
	}
	session := stitch(all, id)
	if len(session) == 0 {
		return analytics.ReplayDetail{}, badQuery("this site has no replay %s", q.ID)
	}
	first, last := session[0], session[len(session)-1]
	d := analytics.ReplayDetail{Replay: analytics.Replay{ID: groupHex(first.id), Start: first.start.UnixMilli(), Segments: len(session),
		EntryPath: first.entry, ExitPath: last.exit, Country: first.country, Browser: first.browser, OS: first.os, Device: first.device,
		Release: first.release}, SegmentList: []analytics.ReplaySegment{}, Timeline: []analytics.TimelineItem{}}
	end := first.end
	for _, sg := range session {
		if sg.end.After(end) {
			end = sg.end
		}
		d.Bytes += sg.bytes
		d.SegmentList = append(d.SegmentList, analytics.ReplaySegment{ID: groupHex(sg.id), Start: sg.start.UnixMilli(), End: sg.end.UnixMilli(),
			Path: sg.entry, Skew: sg.skew, Bytes: sg.bytes})
	}
	d.End = end.UnixMilli()
	from, to := first.start.Add(-time.Second), end.Add(5*time.Second)
	tl, err := s.db.QueryContext(ctx, `SELECT * FROM (
    SELECT epoch_ms(ts) AS t, kind, path, name, '' AS grp FROM events
    WHERE site_id = $1 AND visitor_id = $2::UBIGINT AND ts >= $3 AND ts <= $4 AND kind IN (1, 2)
    UNION ALL
    SELECT epoch_ms(ts), 4, path, type || ': ' || message, printf('%016x', grp) FROM errors
    WHERE site_id = $1 AND visitor_id = $2::UBIGINT AND ts >= $3 AND ts <= $4
) ORDER BY t LIMIT `+strconv.Itoa(analytics.MaxTimeline), uint32(q.Site), strconv.FormatUint(visitor, 10), from, to)
	if err != nil {
		return d, err
	}
	defer func() { _ = tl.Close() }()
	for tl.Next() {
		var it analytics.TimelineItem
		var kind uint8
		if err := tl.Scan(&it.T, &kind, &it.Path, &it.Name, &it.Group); err != nil {
			return d, err
		}
		it.Kind = analytics.Kind(kind)
		switch it.Kind {
		case analytics.KindPageview:
			it.Name = ""
		case analytics.KindError:
			d.Errors++
		case analytics.KindEvent:
			d.Events++
		}
		d.Timeline = append(d.Timeline, it)
	}
	return d, tl.Err()
}

// stitch returns the session among a visitor's segments (by start) that
// holds segment id: the run around it with no gap over the session gap.
func stitch(segs []segmentRow, id uint64) []segmentRow {
	start := 0
	for i := range segs {
		if i > 0 && segs[i].start.Sub(segs[i-1].end) > SessionGap {
			start = i
		}
		if segs[i].id != id {
			continue
		}
		end := i + 1
		for end < len(segs) && segs[end].start.Sub(segs[end-1].end) <= SessionGap {
			end++
		}
		return segs[start:end]
	}
	return nil
}

// ReplayData streams segments' rrweb events to w, in the order given:
// for each a {"segment": id} line, then each of its chunks' JSON array on
// a line of its own. start is called once every segment is found, before
// the first byte.
func (s *Store) ReplayData(ctx context.Context, site int64, segments []string, start func(), w io.Writer) error {
	if len(segments) == 0 || len(segments) > maxReplaySegments {
		return badQuery("ask for 1 to %d segments", maxReplaySegments)
	}
	ids := make([]any, len(segments))
	for i, sg := range segments {
		id, err := strconv.ParseUint(sg, 16, 64)
		if err != nil {
			return badQuery("segment %q isn't one; segments are 16 hex digits", sg)
		}
		ids[i] = id
	}
	type frame struct {
		segment uint64
		day     time.Time
		off     int64
		length  int
	}
	var p params
	in := make([]string, len(ids))
	for i, id := range ids {
		in[i] = p.addU64(id.(uint64))
	}
	s.gate.RLock()
	rows, err := s.db.QueryContext(ctx, "SELECT segment, day, off, length FROM replay_chunks WHERE site_id = "+p.add(uint32(site))+
		" AND segment IN ("+strings.Join(in, ",")+") ORDER BY segment, seq", p...)
	bySegment := map[uint64][]frame{}
	if err == nil {
		for rows.Next() {
			var f frame
			var off uint64
			var length uint32
			if err = rows.Scan(&f.segment, &f.day, &off, &length); err != nil {
				break
			}
			f.off, f.length = int64(off), int(length)
			bySegment[f.segment] = append(bySegment[f.segment], f)
		}
		if cerr := rows.Close(); err == nil {
			err = cerr
		}
	}
	s.gate.RUnlock()
	if err != nil {
		return err
	}
	for i, id := range ids {
		if len(bySegment[id.(uint64)]) == 0 {
			return badQuery("this site has no replay segment %s", segments[i])
		}
	}
	start()
	bw := bufio.NewWriterSize(w, 64<<10)
	var buf, raw []byte
	var f *os.File
	var open string
	defer func() {
		if f != nil {
			_ = f.Close()
		}
	}()
	for i, id := range ids {
		if _, err := bw.WriteString(`{"segment":"` + segments[i] + `"}` + "\n"); err != nil {
			return err
		}
		for _, fr := range bySegment[id.(uint64)] {
			name := filepath.Join(s.replayDir(), replayFile(site, fr.day))
			if name != open {
				if f != nil {
					_ = f.Close()
				}
				if f, err = os.Open(name); err != nil {
					return err
				}
				open = name
			}
			buf = slices.Grow(buf[:0], fr.length)[:fr.length]
			if _, err := f.ReadAt(buf, fr.off); err != nil {
				return err
			}
			if raw, err = zdec.DecodeAll(buf, raw[:0]); err != nil {
				return err
			}
			if _, err := bw.Write(raw); err != nil {
				return err
			}
			if err := bw.WriteByte('\n'); err != nil {
				return err
			}
		}
	}
	return bw.Flush()
}

// maxReplaySegments caps the segments of one ReplayData.
const maxReplaySegments = 200

// PruneReplays deletes a site's replays recorded on days before q.Before:
// their index rows, then their days' directories.
func (s *Store) PruneReplays(ctx context.Context, q analytics.ReplayPrune) (analytics.PruneResult, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	before := time.UnixMilli(q.Before).UTC().Truncate(24 * time.Hour)
	s.writeMu.Lock()
	res, err := s.db.ExecContext(ctx, "DELETE FROM replay_chunks WHERE site_id = $1 AND day < $2", uint32(q.Site), before)
	s.writeMu.Unlock()
	if err != nil {
		return analytics.PruneResult{}, err
	}
	n, _ := res.RowsAffected()
	dir := s.replayDir()
	if dir == "" {
		return analytics.PruneResult{Deleted: n}, nil
	}
	siteDir := filepath.Join(dir, strconv.FormatInt(q.Site, 10))
	days, err := os.ReadDir(siteDir)
	if errors.Is(err, os.ErrNotExist) {
		return analytics.PruneResult{Deleted: n}, nil
	}
	if err != nil {
		return analytics.PruneResult{Deleted: n}, err
	}
	for _, d := range days {
		t, err := time.Parse(time.DateOnly, d.Name())
		if err == nil && t.Before(before) {
			if err := os.RemoveAll(filepath.Join(siteDir, d.Name())); err != nil {
				return analytics.PruneResult{Deleted: n}, err
			}
		}
	}
	return analytics.PruneResult{Deleted: n}, nil
}
