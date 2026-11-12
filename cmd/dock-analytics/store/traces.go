package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// Traces is the trace store: spans in a DuckDB database of their own, run
// as its own dock-analytics process (`dock-analytics traces`) with its own
// memory limit, so traces and analytics never share a container's
// headroom. Bursts of writes grow DuckDB's memory past its memory_limit;
// in one process, a trace backlog on top of analytics' left too little.
//
// Spans go in one table per UTC day of their start, spans_YYYYMMDD, in
// arrival order, so min/max zone maps on start_us skip row groups for any
// time-bounded read. Expiring a day is a DROP TABLE: no delete scan, no
// tombstones, and DuckDB reuses the freed blocks, so the file stays about
// the size of the days kept.
//
// Like events, spans have one writer, the control plane's forwarder, and
// each batch carries its source's position, saved with its rows: a batch
// sent again is skipped. Caps (analytics.TraceLimits) are counted per UTC
// day of arrival in the usage table and in memory. It is safe for
// concurrent use: queries run in parallel, writes one at a time.
type Traces struct {
	db          *sql.DB
	connector   *duckdb.Connector
	memoryLimit string
	// gate lets everything that uses the database run together (read
	// locked), and Trim alone (locked), as for the analytics store.
	gate sync.RWMutex

	// mu serializes span writes and prunes: one transaction at a time,
	// and the usage counts below.
	mu sync.Mutex
	// usageDay is the UTC day usage counts; a write on a new day starts
	// it over.
	usageDay time.Time
	usage    map[usageKey]*usageRow
	orgSpans map[int64]int64
	// keepFrom is the first day spans are taken for (the last prune's).
	keepFrom time.Time
	// writes counts writes since the last size check.
	writes int

	// daysMu guards days: the day tables there are, which queries read
	// without waiting for a write.
	daysMu sync.RWMutex
	days   map[time.Time]bool

	now func() time.Time
}

// tracesSchema is what the traces database holds besides its day tables.
const tracesSchema = `
CREATE TABLE IF NOT EXISTS cursors (
    source   VARCHAR PRIMARY KEY,
    position UBIGINT NOT NULL
);
-- What each service wrote and had rejected a day (arrival), for the caps.
CREATE TABLE IF NOT EXISTS usage (
    day        DATE NOT NULL,
    org_id     UINTEGER NOT NULL,
    service_id UINTEGER NOT NULL,
    spans      UBIGINT NOT NULL,
    errors     UBIGINT NOT NULL,
    bytes      UBIGINT NOT NULL,
    rejected   UBIGINT NOT NULL,
    PRIMARY KEY (day, org_id, service_id)
);
`

// dayTableSchema is one day's spans table; %s is its name. Columns are in
// the order WriteSpans appends them. Low-cardinality strings (names,
// routes, methods) are dictionary-encoded by DuckDB per row group.
const dayTableSchema = `
CREATE TABLE IF NOT EXISTS %s (
    start_us    BIGINT NOT NULL,
    dur_us      BIGINT NOT NULL,
    org_id      UINTEGER NOT NULL,
    project_id  UINTEGER NOT NULL,
    env_id      UINTEGER NOT NULL,
    service_id  UINTEGER NOT NULL,
    release_id  UINTEGER NOT NULL,
    -- IDs as BIGINT with their bits as they are: database/sql can't bind
    -- a uint64 with the high bit set.
    trace_hi    BIGINT NOT NULL,
    trace_lo    BIGINT NOT NULL,
    span_id     BIGINT NOT NULL,
    parent_id   BIGINT NOT NULL,
    name        VARCHAR NOT NULL,
    kind        UTINYINT NOT NULL,
    status      UTINYINT NOT NULL,
    entry       BOOLEAN NOT NULL,
    method      VARCHAR NOT NULL,
    route       VARCHAR NOT NULL,
    http_status USMALLINT NOT NULL,
    error_fp    VARCHAR NOT NULL,
    kept        UTINYINT NOT NULL,
    attrs       VARCHAR NOT NULL,
    events      VARCHAR NOT NULL
)`

// spanColumns are a day table's columns as a span is read back.
const spanColumns = `org_id, project_id, env_id, service_id, release_id, trace_hi, trace_lo, span_id, parent_id,
    start_us, dur_us, name, kind, status, entry, method, route, http_status, error_fp, kept, attrs, events`

// Days the store takes spans for, around now: a span for a day further
// back than maxTraceAge (or before the last prune) or after tomorrow is
// rejected, so a clock that is off can't create tables for random days.
const maxTraceAge = 30 * 24 * time.Hour

// sizeCheckEvery is how many writes go between two checks of the traces
// database's size against TraceLimits.MaxBytes (and a new day's first
// write checks too).
const sizeCheckEvery = 100

// usageKey names a service's usage row.
type usageKey struct{ org, service int64 }

// usageRow is a service's usage today.
type usageRow struct{ spans, errors, bytes, rejected int64 }

// OpenTraces opens (creating if needed) the trace store's database at
// path, "" in memory for tests.
func OpenTraces(ctx context.Context, path string, opts Options) (*Traces, error) {
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
	}
	connector, memoryLimit, err := connect(path, opts)
	if err != nil {
		return nil, fmt.Errorf("open the traces database: %w", err)
	}
	s := &Traces{db: sql.OpenDB(connector), connector: connector, memoryLimit: memoryLimit, days: map[time.Time]bool{}, now: time.Now}
	if err := s.load(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// load creates the tables and loads the day tables there are and
// today's usage.
func (s *Traces) load(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, tracesSchema); err != nil {
		return fmt.Errorf("create the traces tables: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, "SELECT table_name FROM duckdb_tables() WHERE database_name = current_database() AND starts_with(table_name, 'spans_')")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if d, err := time.Parse("20060102", strings.TrimPrefix(name, "spans_")); err == nil {
			s.days[d] = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return s.loadUsage(ctx, utcDay(s.now()))
}

// Close checkpoints and closes the database.
func (s *Traces) Close() error {
	_, _ = s.db.Exec("CHECKPOINT")
	err := s.db.Close()
	return errors.Join(err, s.connector.Close())
}

// Checkpoint writes the write-ahead log into the database file.
func (s *Traces) Checkpoint(ctx context.Context) error {
	s.gate.RLock()
	defer s.gate.RUnlock()
	_, err := s.db.ExecContext(ctx, "CHECKPOINT")
	return err
}

// Trim gives DuckDB's cached memory back, as Store.Trim does.
func (s *Traces) Trim(ctx context.Context) error {
	s.gate.Lock()
	defer s.gate.Unlock()
	return trimDB(ctx, s.db, s.memoryLimit)
}

// TryTrim trims if nothing is running, and reports whether it did.
func (s *Traces) TryTrim(ctx context.Context) (bool, error) {
	if !s.gate.TryLock() {
		return false, nil
	}
	defer s.gate.Unlock()
	return true, trimDB(ctx, s.db, s.memoryLimit)
}

// FileBytes is the database file's size, 0 in memory.
func (s *Traces) FileBytes(ctx context.Context) (int64, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	var path string
	if err := s.db.QueryRowContext(ctx, "SELECT coalesce(path, '') FROM duckdb_databases() WHERE database_name = current_database()").Scan(&path); err != nil || path == "" {
		return 0, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// loadUsage reads day's usage rows into memory.
func (s *Traces) loadUsage(ctx context.Context, day time.Time) error {
	t := s
	t.usageDay, t.usage, t.orgSpans = day, map[usageKey]*usageRow{}, map[int64]int64{}
	rows, err := s.db.QueryContext(ctx, "SELECT org_id, service_id, spans, errors, bytes, rejected FROM usage WHERE day = $1", day)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k usageKey
		var u usageRow
		if err := rows.Scan(&k.org, &k.service, &u.spans, &u.errors, &u.bytes, &u.rejected); err != nil {
			return err
		}
		t.usage[k] = &u
		t.orgSpans[k.org] += u.spans
	}
	return rows.Err()
}

// utcDay is t's UTC day at midnight.
func utcDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// dayTable is the name of day's spans table.
func dayTable(day time.Time) string { return "spans_" + day.Format("20060102") }

// spanBytes is about what a span costs on disk before compression, for
// the usage counts.
func spanBytes(sp *analytics.Span) int64 {
	return int64(64 + len(sp.Name) + len(sp.Method) + len(sp.Route) + len(sp.ErrorFP) + len(sp.Attrs) + len(sp.Events))
}

// WriteSpans appends spans from source, a spool whose spans up to
// position are then all written, applying lim. A batch whose position
// isn't past the source's cursor was written before and is skipped.
// Spans past a cap, or for a day the store doesn't take, are rejected and
// counted.
func (s *Traces) WriteSpans(ctx context.Context, source string, position uint64, spans []analytics.Span, lim analytics.TraceLimits) (res analytics.SpanWriteResult, err error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	t := s
	t.mu.Lock()
	defer t.mu.Unlock()

	now := s.now()
	today := utcDay(now)
	if !today.Equal(t.usageDay) {
		if err := s.loadUsage(ctx, today); err != nil {
			return res, err
		}
	}
	keepFrom := later(t.keepFrom, today.Add(-maxTraceAge))
	lastDay := today.Add(24 * time.Hour)

	// Decide every span first: the caps' counts change only once the
	// write commits.
	byDay := map[time.Time][]*analytics.Span{}
	delta := map[usageKey]*usageRow{}
	orgDelta := map[int64]int64{}
	invalid := 0
	for i := range spans {
		sp := &spans[i]
		if sp.Org <= 0 || sp.Service <= 0 {
			invalid++ // no service to count it for
			continue
		}
		k := usageKey{sp.Org, sp.Service}
		d := delta[k]
		if d == nil {
			d = &usageRow{}
			delta[k] = d
		}
		u := t.usage[k]
		if u == nil {
			u = &usageRow{}
		}
		day := utcDay(time.UnixMicro(sp.Start))
		if sp.Trace.IsZero() || sp.Duration < 0 || day.Before(keepFrom) || day.After(lastDay) {
			d.rejected++
			continue
		}
		// A failed span is an error span whatever it says it was kept
		// for: nodes before kept reasons left them unset.
		important := sp.Kept == analytics.KeptError || sp.Kept == analytics.KeptForced || sp.Error()
		switch {
		case important && lim.ErrorDay > 0 && u.errors+d.errors >= lim.ErrorDay,
			!important && lim.ServiceDay > 0 && u.spans+d.spans >= lim.ServiceDay,
			!important && lim.OrgDay > 0 && t.orgSpans[sp.Org]+orgDelta[sp.Org] >= lim.OrgDay:
			d.rejected++
			continue
		}
		d.spans++
		orgDelta[sp.Org]++
		if important {
			d.errors++
		}
		d.bytes += spanBytes(sp)
		byDay[day] = append(byDay[day], sp)
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return res, err
	}
	defer func() { _ = conn.Close() }()
	skip := false
	var newDays []time.Time
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
		if source != "" {
			cur, err := tracesCursorOf(ctx, qr, source)
			if err != nil {
				return err
			}
			if position <= cur {
				skip = true
				return nil
			}
		}
		days := slices.SortedFunc(func(yield func(time.Time) bool) {
			for d := range byDay {
				if !yield(d) {
					return
				}
			}
		}, func(a, b time.Time) int { return a.Compare(b) })
		for _, day := range days {
			if !s.hasDay(day) {
				if _, err := ex.ExecContext(ctx, fmt.Sprintf(dayTableSchema, dayTable(day)), nil); err != nil {
					return err
				}
				newDays = append(newDays, day)
			}
			if err := appendSpans(c, dayTable(day), byDay[day]); err != nil {
				return err
			}
		}
		for k, d := range delta {
			u := t.usage[k]
			if u == nil {
				u = &usageRow{}
			}
			if _, err := ex.ExecContext(ctx, "INSERT OR REPLACE INTO usage VALUES ($1, $2, $3, $4, $5, $6, $7)", []driver.NamedValue{
				{Ordinal: 1, Value: today}, {Ordinal: 2, Value: uint32(k.org)}, {Ordinal: 3, Value: uint32(k.service)},
				{Ordinal: 4, Value: uint64(u.spans + d.spans)}, {Ordinal: 5, Value: uint64(u.errors + d.errors)},
				{Ordinal: 6, Value: uint64(u.bytes + d.bytes)}, {Ordinal: 7, Value: uint64(u.rejected + d.rejected)},
			}); err != nil {
				return err
			}
		}
		if source != "" {
			if _, err := ex.ExecContext(ctx, "INSERT OR REPLACE INTO cursors VALUES ($1, $2)",
				[]driver.NamedValue{{Ordinal: 1, Value: source}, {Ordinal: 2, Value: position}}); err != nil {
				return err
			}
		}
		if _, err := ex.ExecContext(ctx, "COMMIT", nil); err != nil {
			return err
		}
		committed = true
		return nil
	})
	if err != nil {
		return analytics.SpanWriteResult{}, fmt.Errorf("write spans: %w", err)
	}
	if skip {
		return res, nil
	}
	res.Rejected = invalid
	for k, d := range delta {
		u := t.usage[k]
		if u == nil {
			u = &usageRow{}
			t.usage[k] = u
		}
		u.spans += d.spans
		u.errors += d.errors
		u.bytes += d.bytes
		u.rejected += d.rejected
		res.Written += int(d.spans)
		res.Rejected += int(d.rejected)
	}
	for org, n := range orgDelta {
		t.orgSpans[org] += n
	}
	if len(newDays) > 0 {
		t.daysMu.Lock()
		for _, d := range newDays {
			t.days[d] = true
		}
		t.daysMu.Unlock()
	}
	t.writes++
	if lim.MaxBytes > 0 && (t.writes >= sizeCheckEvery || len(newDays) > 0) {
		t.writes = 0
		if err := s.enforceTraceSize(ctx, lim.MaxBytes, today); err != nil {
			return res, fmt.Errorf("keep the traces database within its size: %w", err)
		}
	}
	return res, nil
}

// tracesCursorOf reads source's position in the traces database, 0 for a
// new source.
func tracesCursorOf(ctx context.Context, qr driver.QueryerContext, source string) (uint64, error) {
	rows, err := qr.QueryContext(ctx, "SELECT position FROM cursors WHERE source = $1", []driver.NamedValue{{Ordinal: 1, Value: source}})
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	dest := make([]driver.Value, 1)
	if err := rows.Next(dest); err != nil {
		return 0, nil // no row: a new source
	}
	v, _ := dest[0].(uint64)
	return v, nil
}

// SpansCursor returns how far source's spans have been written.
func (s *Traces) SpansCursor(ctx context.Context, source string) (uint64, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	var pos uint64
	err := s.db.QueryRowContext(ctx, "SELECT position FROM cursors WHERE source = $1", source).Scan(&pos)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return pos, err
}

// appendSpans writes spans to table with DuckDB's appender, in the open
// transaction on c.
func appendSpans(c driver.Conn, table string, spans []*analytics.Span) error {
	a, err := duckdb.NewAppenderFromConn(c, "", table)
	if err != nil {
		return err
	}
	for _, sp := range spans {
		hi, lo := sp.Trace.Halves()
		err := a.AppendRow(sp.Start, sp.Duration, uint32(sp.Org), uint32(sp.Project), uint32(sp.Env), uint32(sp.Service), uint32(sp.Release),
			int64(hi), int64(lo), int64(sp.ID), int64(sp.Parent), sp.Name, uint8(sp.Kind), uint8(sp.Status), sp.Entry,
			sp.Method, sp.Route, uint16(min(max(sp.HTTPStatus, 0), 999)), sp.ErrorFP, uint8(sp.Kept), string(sp.Attrs), string(sp.Events))
		if err != nil {
			_ = a.Close()
			return err
		}
	}
	return a.Close() // flushes
}

// hasDay reports whether day has a spans table.
func (s *Traces) hasDay(day time.Time) bool {
	s.daysMu.RLock()
	defer s.daysMu.RUnlock()
	return s.days[day]
}

// daysIn lists the day tables that hold spans starting in [from, to)
// (Unix µs), oldest first.
func (s *Traces) daysIn(from, to int64) []time.Time {
	s.daysMu.RLock()
	defer s.daysMu.RUnlock()
	first, last := utcDay(time.UnixMicro(from)), utcDay(time.UnixMicro(to-1))
	var days []time.Time
	for d := range s.days {
		if !d.Before(first) && !d.After(last) {
			days = append(days, d)
		}
	}
	slices.SortFunc(days, func(a, b time.Time) int { return a.Compare(b) })
	return days
}

// allDays lists every day table, oldest first.
func (s *Traces) allDays() []time.Time {
	s.daysMu.RLock()
	defer s.daysMu.RUnlock()
	days := make([]time.Time, 0, len(s.days))
	for d := range s.days {
		days = append(days, d)
	}
	slices.SortFunc(days, func(a, b time.Time) int { return a.Compare(b) })
	return days
}

// spansOf is a FROM clause reading cols of days' tables as one: a UNION
// ALL that DuckDB pushes filters into. Table names come from days, never
// from input.
func spansOf(days []time.Time, cols string) string {
	var b strings.Builder
	b.WriteString("(")
	for i, d := range days {
		if i > 0 {
			b.WriteString(" UNION ALL ")
		}
		b.WriteString("SELECT " + cols + " FROM " + dayTable(d))
	}
	b.WriteString(")")
	return b.String()
}

// traceSlack is how far Trace reads around the time it is given: a
// trace's spans start within it of each other.
const traceSlack = time.Hour

// Traces lists the entry spans that match q, in two queries however many
// rows. The first finds the page by start and duration alone, reading
// only the columns its filters need: every column it selects is carried
// through the sort (DuckDB's late materialization doesn't reach through
// the UNION ALL of day tables), and five columns took twice as long. The
// second reads the page's columns with the same filters within the box of
// starts and durations the page spans, which holds the page and nothing
// else but ties: for the newest a few minutes of starts, which zone maps
// narrow to a row group or two, for the slowest a band of durations,
// which a comparison of one column narrows to the page. At 10M spans over
// a week, looking the page up by its IDs instead took 250 ms.
func (s *Traces) Traces(ctx context.Context, q analytics.TracesQuery) (analytics.TracesResult, error) {
	res := analytics.TracesResult{Traces: []analytics.TraceSummary{}}
	limit := q.Limit
	if limit == 0 {
		limit = analytics.DefaultTraces
	}
	order := ""
	switch q.Sort {
	case analytics.SortNewest, "":
		order = "start_us DESC"
	case analytics.SortSlowest:
		// No tie-breaker: a second sort key doubles the sort, and equal
		// durations to the microsecond are rare.
		order = "dur_us DESC"
	default:
		return res, &QueryError{fmt.Sprintf("unknown order %q; use newest or slowest", q.Sort)}
	}
	// Every fragment is a constant and values are parameters, but for the
	// services, which are integer literals (intList). cols are what the
	// filters read, which the first query's day tables select.
	where := "entry AND org_id = $1 AND service_id IN (" + intList(q.Services) + ") AND start_us >= $2 AND start_us < $3"
	cols := []string{"start_us", "dur_us", "entry", "org_id", "service_id"}
	args := []any{uint32(q.Org), q.From, q.To}
	add := func(cond, col string, v any) {
		args = append(args, v)
		where += fmt.Sprintf(" AND "+cond, len(args))
		if col != "" {
			cols = append(cols, col)
		}
	}
	switch q.Status {
	case analytics.TraceAny:
	case analytics.TraceError:
		where += " AND (status = 2 OR http_status >= 500)"
		cols = append(cols, "status", "http_status")
	case analytics.Trace5xx:
		where += " AND http_status >= 500"
		cols = append(cols, "http_status")
	default:
		return res, &QueryError{fmt.Sprintf("unknown status %q; use error or 5xx", q.Status)}
	}
	switch {
	case len(q.Services) == 0:
		return res, &QueryError{"a trace list needs at least one service"}
	case q.Org <= 0:
		return res, &QueryError{"a trace list needs its org"}
	case q.To <= q.From:
		return res, &QueryError{"a trace list needs a range that ends after it starts"}
	case limit < 0 || limit > analytics.MaxTraces:
		return res, &QueryError{fmt.Sprintf("a trace list shows at most %d traces", analytics.MaxTraces)}
	}
	if q.Release > 0 {
		add("release_id = $%d", "release_id", uint32(q.Release))
	}
	if q.SinceRelease > 0 {
		add("release_id >= $%d", "release_id", uint32(q.SinceRelease))
	}
	if q.Method != "" {
		add("method = $%d", "method", q.Method)
	}
	if q.Route != "" {
		add("route = $%d", "route", q.Route)
	}
	if q.MinDuration > 0 {
		add("dur_us >= $%d", "", q.MinDuration)
	}
	// Twice the page: a trace can enter the service twice (Caddy's edge
	// span and the app's, or a call to itself), and is listed once.
	args = append(args, 2*limit+1)
	s.gate.RLock()
	defer s.gate.RUnlock()
	days := s.daysIn(q.From, q.To)
	if len(days) == 0 {
		return res, nil
	}

	// The page: entry spans by start and duration alone.
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("SELECT start_us, dur_us FROM %s WHERE %s ORDER BY %s LIMIT $%d",
		spansOf(days, columnList(cols)), where, order, len(args)), args...)
	if err != nil {
		return res, err
	}
	var page []pageKey
	for rows.Next() {
		var k pageKey
		if err := rows.Scan(&k.start, &k.dur); err != nil {
			_ = rows.Close()
			return res, err
		}
		page = append(page, k)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	if len(page) > 2*limit {
		page, res.More = page[:2*limit], true
	}
	if len(page) == 0 {
		return res, nil
	}

	// The page's spans: the same filters within the box of starts and
	// durations the page spans hold the page and only ties beside it,
	// which its starts and durations tell apart.
	first, last, shortest, longest := page[0].start, page[0].start, page[0].dur, page[0].dur
	at := make(map[pageKey][]int, len(page))
	for i, k := range page {
		first, last, shortest, longest = min(first, k.start), max(last, k.start), min(shortest, k.dur), max(longest, k.dur)
		at[k] = append(at[k], i)
	}
	args = append(args[:len(args)-1], first, last, shortest, longest)
	n := len(args)
	rows, err = s.db.QueryContext(ctx, fmt.Sprintf(`SELECT start_us, dur_us, trace_hi, trace_lo, span_id, service_id, release_id, name, kind, method, route, http_status, status, kept, error_fp
    FROM %s WHERE %s AND start_us >= $%d AND start_us <= $%d AND dur_us >= $%d AND dur_us <= $%d`,
		spansOf(s.daysIn(first, last+1), columnList(append(cols, "trace_hi", "trace_lo", "span_id", "release_id", "name", "kind", "method", "route", "http_status", "status", "kept", "error_fp"))), where, n-3, n-2, n-1, n),
		args...)
	if err != nil {
		return res, err
	}
	entries := make([]*analytics.TraceSummary, len(page))
	for rows.Next() {
		var k pageKey
		var hi, lo, span int64
		var t analytics.TraceSummary
		var kind, st, kept uint8
		var code uint16
		if err := rows.Scan(&k.start, &k.dur, &hi, &lo, &span, &t.Service, &t.Release, &t.Name, &kind, &t.Method, &t.Route, &code, &st, &kept, &t.ErrorFP); err != nil {
			_ = rows.Close()
			return res, err
		}
		slots := at[k]
		if len(slots) == 0 {
			continue // a tie beside the page
		}
		i := slots[0]
		at[k] = slots[1:]
		t.Trace, t.Span, t.Start, t.Duration = analytics.TraceIDOf(uint64(hi), uint64(lo)), analytics.SpanID(span), k.start, k.dur
		t.Kind, t.Status, t.Kept, t.HTTPStatus = analytics.SpanKind(kind), analytics.SpanStatus(st), analytics.Kept(kept), int(code)
		entries[i] = &t
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	res.Traces = analytics.OnePerTrace(res.Traces, entries)
	if len(res.Traces) > limit {
		res.Traces, res.More = res.Traces[:limit], true
	}
	return res, nil
}

// pageKey is an entry span of a trace list's page: its start and
// duration.
type pageKey struct{ start, dur int64 }

// columnList joins cols, each once, for a SELECT.
func columnList(cols []string) string {
	seen := make(map[string]bool, len(cols))
	var b strings.Builder
	for _, c := range cols {
		if seen[c] {
			continue
		}
		if len(seen) > 0 {
			b.WriteString(", ")
		}
		seen[c] = true
		b.WriteString(c)
	}
	return b.String()
}

// intList writes ids as a list of SQL integer literals, "1, 2, 3": safe
// in SQL, since they are numbers formatted here. DuckDB filters IN with
// literals while it scans.
func intList(ids []int64) string {
	var b strings.Builder
	for i, id := range ids {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.FormatInt(id, 10))
	}
	return b.String()
}

// Trace reads one trace's spans of q.Org, by start time and span ID, a
// page of q.Limit after q.After.
func (s *Traces) Trace(ctx context.Context, q analytics.TraceQuery) (analytics.TraceDetail, error) {
	res := analytics.TraceDetail{Spans: []analytics.Span{}}
	if q.Org <= 0 || q.Trace.IsZero() {
		return res, &QueryError{"a trace needs its org and ID"}
	}
	s.gate.RLock()
	defer s.gate.RUnlock()
	from, to := int64(0), int64(1<<62)
	days := s.allDays()
	if q.At > 0 {
		slack := traceSlack.Microseconds()
		from, to = q.At-slack, q.At+slack
		days = s.daysIn(from, to)
	}
	if len(days) == 0 {
		return res, nil
	}
	hi, lo := q.Trace.Halves()
	limit := q.Limit
	if limit <= 0 || limit > analytics.MaxTraceSpans {
		limit = analytics.MaxTraceSpans
	}
	// A page starts after the cursor, by start and span ID (the order).
	afterStart, afterSpan := int64(-1<<62), int64(-1<<63)
	if q.After != nil {
		afterStart, afterSpan = q.After.Start, int64(q.After.Span)
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+spanColumns+" FROM "+spansOf(days, spanColumns)+
		" WHERE trace_hi = $1 AND trace_lo = $2 AND org_id = $3 AND start_us >= $4 AND start_us < $5"+
		" AND (start_us > $6 OR (start_us = $6 AND span_id > $7)) ORDER BY start_us, span_id LIMIT $8",
		int64(hi), int64(lo), uint32(q.Org), from, to, afterStart, afterSpan, limit+1)
	if err != nil {
		return res, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var sp analytics.Span
		var thi, tlo, id, parent int64
		var kind, st, kept uint8
		var code uint16
		var attrs, events string
		if err := rows.Scan(&sp.Org, &sp.Project, &sp.Env, &sp.Service, &sp.Release, &thi, &tlo, &id, &parent,
			&sp.Start, &sp.Duration, &sp.Name, &kind, &st, &sp.Entry, &sp.Method, &sp.Route, &code, &sp.ErrorFP, &kept, &attrs, &events); err != nil {
			return res, err
		}
		sp.Trace, sp.ID, sp.Parent = analytics.TraceIDOf(uint64(thi), uint64(tlo)), analytics.SpanID(id), analytics.SpanID(parent)
		sp.Kind, sp.Status, sp.Kept, sp.HTTPStatus = analytics.SpanKind(kind), analytics.SpanStatus(st), analytics.Kept(kept), int(code)
		if attrs != "" {
			sp.Attrs = []byte(attrs)
		}
		if events != "" {
			sp.Events = []byte(events)
		}
		res.Spans = append(res.Spans, sp)
	}
	if err := rows.Err(); err != nil {
		return res, err
	}
	if len(res.Spans) > limit {
		res.Spans, res.Truncated = res.Spans[:limit], true
	}
	return res, nil
}

// ErrorTrace finds the newest span of q's fingerprint among its services
// in its range: one scan of the range's day tables on columns the filter
// reads.
func (s *Traces) ErrorTrace(ctx context.Context, q analytics.ErrorTraceQuery) (analytics.ErrorTrace, error) {
	var res analytics.ErrorTrace
	switch {
	case q.Org <= 0 || q.Fingerprint == "" || len(q.Services) == 0:
		return res, &QueryError{"an error's trace needs its org, services and fingerprint"}
	case q.To <= q.From:
		return res, &QueryError{"an error's trace needs a range that ends after it starts"}
	}
	s.gate.RLock()
	defer s.gate.RUnlock()
	days := s.daysIn(q.From, q.To)
	if len(days) == 0 {
		return res, nil
	}
	cols := "start_us, trace_hi, trace_lo, org_id, service_id, error_fp"
	var hi, lo int64
	err := s.db.QueryRowContext(ctx, "SELECT start_us, trace_hi, trace_lo FROM "+spansOf(days, cols)+
		" WHERE error_fp = $1 AND org_id = $2 AND service_id IN ("+intList(q.Services)+") AND start_us >= $3 AND start_us < $4 ORDER BY start_us DESC LIMIT 1",
		q.Fingerprint, uint32(q.Org), q.From, q.To).Scan(&res.Start, &hi, &lo)
	if errors.Is(err, sql.ErrNoRows) {
		return analytics.ErrorTrace{}, nil
	}
	if err != nil {
		return res, err
	}
	res.Trace = analytics.TraceIDOf(uint64(hi), uint64(lo))
	return res, nil
}

// PruneTraces drops the day tables before q.Before's day, and usage rows
// as old; spans for those days are rejected from then on.
func (s *Traces) PruneTraces(ctx context.Context, q analytics.TracePrune) (analytics.TracePruneResult, error) {
	res := analytics.TracePruneResult{Days: []string{}}
	before := utcDay(time.UnixMilli(q.Before))
	s.gate.RLock()
	defer s.gate.RUnlock()
	t := s
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, d := range s.allDays() {
		if !d.Before(before) {
			break
		}
		if err := s.dropDay(ctx, d); err != nil {
			return res, err
		}
		res.Days = append(res.Days, d.Format(time.DateOnly))
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM usage WHERE day < $1", before); err != nil {
		return res, err
	}
	t.keepFrom = later(t.keepFrom, before)
	return res, nil
}

// dropDay drops day's spans table.
func (s *Traces) dropDay(ctx context.Context, day time.Time) error {
	if _, err := s.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+dayTable(day)); err != nil {
		return fmt.Errorf("drop the spans of %s: %w", day.Format(time.DateOnly), err)
	}
	s.daysMu.Lock()
	delete(s.days, day)
	s.daysMu.Unlock()
	return nil
}

// enforceTraceSize drops the oldest days, never today's, while the traces
// database uses more than maxBytes. A checkpoint before each measure
// moves new rows out of the write-ahead log into blocks and frees a
// dropped day's.
func (s *Traces) enforceTraceSize(ctx context.Context, maxBytes int64, today time.Time) error {
	for {
		if _, err := s.db.ExecContext(ctx, "CHECKPOINT"); err != nil {
			return err
		}
		used, err := s.tracesUsed(ctx)
		if err != nil || used <= maxBytes {
			return err
		}
		days := s.allDays()
		if len(days) == 0 || !days[0].Before(today) {
			return nil // only today is left: the caps hold it
		}
		if err := s.dropDay(ctx, days[0]); err != nil {
			return err
		}
		s.keepFrom = later(s.keepFrom, days[0].Add(24*time.Hour))
	}
}

// tracesUsed is the bytes of blocks the traces database uses: its file
// less the blocks freed for reuse, as of its last checkpoint.
func (s *Traces) tracesUsed(ctx context.Context) (int64, error) {
	var used int64
	err := s.db.QueryRowContext(ctx, "SELECT used_blocks * block_size FROM pragma_database_size() WHERE database_name = current_database()").Scan(&used)
	return used, err
}

// later is the later of a and b.
func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
