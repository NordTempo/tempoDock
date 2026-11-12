package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// App metrics (ADR 0075, phase 6) live in the trace store's database, in
// tables of their own:
//
//   - metric_series: one row per service, metric, unit, kind and
//     attributes, its ID a hash of them, with a number of its own (dense,
//     so the points' series column packs into a few bits where a hash
//     takes 8 bytes) and the first and last UTC day it sent a point.
//   - metrics_YYYYMMDD: a day's points, one row per point, its minute and
//     its series number, in arrival order. A histogram's counts are a
//     list of analytics.MetricBuckets, NULL for gauges and sums, so only
//     histograms pay for them: as 20 columns every row did, 32 bytes a
//     point in all against 11.
//   - metric_hours: every day's points rolled up into hours once the day
//     is over (the first write of the next day rolls it up, and metric_rolled
//     names the days rolled), kept for HoursBefore: charts in steps of an
//     hour read them, and the minute points only of the days not rolled
//     up yet. A point that arrives for a day already rolled up stays in
//     its minute points only.
//   - metric_cuts: the points of new series cut past a cap, by day,
//     service and metric, so the Metrics tab can say which metric to fix.
//
// The caps count a service's series and metric names per UTC day, in
// memory (today's series, loaded from metric_series when the store opens
// or a day starts): a new series past either is cut; the series already
// seen that day go on, so a cardinality bomb costs the bomb's metric,
// not the app's runtime charts.

// metricsSchema is what metrics hold besides their day tables.
var metricsSchema = `
CREATE TABLE IF NOT EXISTS metric_series (
    id         BIGINT PRIMARY KEY,
    num        UINTEGER NOT NULL,
    org_id     UINTEGER NOT NULL,
    project_id UINTEGER NOT NULL,
    env_id     UINTEGER NOT NULL,
    service_id UINTEGER NOT NULL,
    name       VARCHAR NOT NULL,
    unit       VARCHAR NOT NULL,
    kind       UTINYINT NOT NULL,
    monotonic  BOOLEAN NOT NULL,
    cumulative BOOLEAN NOT NULL,
    attrs      VARCHAR NOT NULL,
    first_day  DATE NOT NULL,
    last_day   DATE NOT NULL
);
CREATE TABLE IF NOT EXISTS metric_hours (
    series     UINTEGER NOT NULL,
    hour       BIGINT NOT NULL,
    service_id UINTEGER NOT NULL,
    release_id UINTEGER NOT NULL,
    points     UINTEGER NOT NULL,
    value      DOUBLE NOT NULL,
    last       DOUBLE NOT NULL,
    n          UBIGINT NOT NULL,
    min        DOUBLE,
    max        DOUBLE,
    buckets    UBIGINT[]
);
CREATE TABLE IF NOT EXISTS metric_rolled (
    day DATE PRIMARY KEY
);
CREATE TABLE IF NOT EXISTS metric_cuts (
    day        DATE NOT NULL,
    service_id UINTEGER NOT NULL,
    name       VARCHAR NOT NULL,
    points     UBIGINT NOT NULL,
    PRIMARY KEY (day, service_id, name)
);
`

// metricDayTableSchema is one day's points table; %s is its name.
// Columns are in the order WriteMetrics appends them: value is a gauge's
// or sum's value or a histogram's sum, n a histogram's count, min and max
// a histogram's (NULL otherwise or when not sent), buckets a histogram's
// counts.
const metricDayTableSchema = `
CREATE TABLE IF NOT EXISTS %s (
    series     UINTEGER NOT NULL,
    minute     BIGINT NOT NULL,
    service_id UINTEGER NOT NULL,
    release_id UINTEGER NOT NULL,
    value      DOUBLE NOT NULL,
    n          UBIGINT NOT NULL,
    min        DOUBLE,
    max        DOUBLE,
    buckets    UBIGINT[]
)`

// maxCutNames bounds the metric names a service's cut rows name a day;
// past it cuts are counted under "".
const maxCutNames = 50

// todaySeries is a service's series (their IDs' numbers) and metric
// names today, for the caps, and the names cut.
type todaySeries struct {
	ids   map[int64]uint32
	names map[string]bool
	cuts  map[string]bool
}

// metricDayTable is the name of day's points table.
func metricDayTable(day time.Time) string { return "metrics_" + day.Format("20060102") }

// seriesID names p's series: its service, name, unit, kind, temporality
// and attributes, hashed.
func seriesID(p *analytics.MetricPoint) int64 {
	h := fnv.New64a()
	var b [32]byte
	_, _ = h.Write(strconv.AppendInt(b[:0], p.Service, 10))
	for _, s := range []string{p.Name, p.Unit} {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(s))
	}
	cum := byte(0)
	if p.Cumulative {
		cum = 1
	}
	_, _ = h.Write([]byte{0, byte(p.Kind), cum})
	_, _ = h.Write(p.Attrs)
	if id := int64(h.Sum64()); id != 0 {
		return id
	}
	return 1 // 0 marks a point WriteMetrics rejected
}

// loadSeries reads day's series and cut names into memory, and the next
// series number.
func (s *Traces) loadSeries(ctx context.Context, day time.Time) error {
	s.seriesDay, s.series = day, map[int64]*todaySeries{}
	if err := s.db.QueryRowContext(ctx, "SELECT coalesce(max(num), 0) + 1 FROM metric_series").Scan(&s.nextSeries); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id, num, service_id, name FROM metric_series WHERE last_day = $1", day)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, service int64
		var num uint32
		var name string
		if err := rows.Scan(&id, &num, &service, &name); err != nil {
			_ = rows.Close()
			return err
		}
		ss := s.todayOf(service)
		ss.ids[id], ss.names[name] = num, true
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = s.db.QueryContext(ctx, "SELECT service_id, name FROM metric_cuts WHERE day = $1", day)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var service int64
		var name string
		if err := rows.Scan(&service, &name); err != nil {
			return err
		}
		s.todayOf(service).cuts[name] = true
	}
	return rows.Err()
}

// todayOf is service's series today, made if it has none.
func (s *Traces) todayOf(service int64) *todaySeries {
	ss := s.series[service]
	if ss == nil {
		ss = &todaySeries{ids: map[int64]uint32{}, names: map[string]bool{}, cuts: map[string]bool{}}
		s.series[service] = ss
	}
	return ss
}

// cutKey names a cut row of today.
type cutKey struct {
	service int64
	name    string
}

// metricRow is a point to append, with its series' number.
type metricRow struct {
	series uint32
	p      *analytics.MetricPoint
}

// newSeries is a series a write sees for the first time today: its ID,
// its number and a point naming it.
type newSeries struct {
	id  int64
	num uint32
	p   *analytics.MetricPoint
}

// seriesNums are the numbers of the series ids that have one, read from
// metric_series: those new today may have been seen before.
func (s *Traces) seriesNums(ctx context.Context, ids []int64) (map[int64]uint32, error) {
	nums := make(map[int64]uint32, len(ids))
	for chunk := range slices.Chunk(ids, 1000) {
		rows, err := s.db.QueryContext(ctx, "SELECT id, num FROM metric_series WHERE id IN ("+intList(chunk)+")")
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var num uint32
			if err := rows.Scan(&id, &num); err != nil {
				_ = rows.Close()
				return nil, err
			}
			nums[id] = num
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return nums, nil
}

// WriteMetrics appends points from source, a spool whose points up to
// position are then all written, applying lim. A batch whose position
// isn't past the source's cursor was written before and is skipped.
// Points for a day the store doesn't take, or without a service, name or
// kind, are rejected; points of new series past a cap are cut and
// counted by metric.
func (s *Traces) WriteMetrics(ctx context.Context, source string, position uint64, points []analytics.MetricPoint, lim analytics.MetricLimits) (res analytics.MetricWriteResult, err error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if source != "" {
		cur, err := s.cursorLocked(ctx, source)
		if err != nil || position <= cur {
			return res, err // written before
		}
	}
	now := s.now()
	today := utcDay(now)
	if !today.Equal(s.seriesDay) {
		if err := s.loadSeries(ctx, today); err != nil {
			return res, err
		}
	}
	// A day's first write (since the store started, too) rolls the days
	// before it up into hours.
	if s.rolledTo.Before(today) {
		if err := s.rollupBefore(ctx, today); err != nil {
			return res, fmt.Errorf("roll up the days before %s: %w", today.Format(time.DateOnly), err)
		}
		s.rolledTo = today
	}
	// The in-memory series change as points are decided; if the write
	// fails they are read again.
	defer func() {
		if err != nil {
			s.seriesDay = time.Time{}
		}
	}()
	keepFrom := later(s.keepFrom, today.Add(-maxTraceAge))
	lastDay := today.Add(24 * time.Hour)

	// The points the store takes, and the IDs of their series; those not
	// seen today may have numbers from before.
	ids := make([]int64, len(points))
	var unseen []int64
	for i := range points {
		p := &points[i]
		day := utcDay(time.Unix(p.Time, 0))
		if p.Org <= 0 || p.Service <= 0 || p.Name == "" || p.Kind < analytics.MetricGauge || p.Kind > analytics.MetricHistogram ||
			len(p.Buckets) != 0 && len(p.Buckets) != analytics.MetricBuckets || day.Before(keepFrom) || day.After(lastDay) {
			res.Rejected++
			continue
		}
		ids[i] = seriesID(p)
		if _, ok := s.todayOf(p.Service).ids[ids[i]]; !ok {
			unseen = append(unseen, ids[i])
		}
	}
	known, err := s.seriesNums(ctx, unseen)
	if err != nil {
		return res, err
	}

	byDay := map[time.Time][]metricRow{}
	var touch []newSeries // to insert, or mark seen today
	cuts := map[cutKey]int64{}
	for i := range points {
		p, id := &points[i], ids[i]
		if id == 0 {
			continue // rejected
		}
		ss := s.todayOf(p.Service)
		num, ok := ss.ids[id]
		if !ok {
			if lim.Series > 0 && len(ss.ids) >= lim.Series || lim.Names > 0 && !ss.names[p.Name] && len(ss.names) >= lim.Names {
				name := p.Name
				if !ss.cuts[name] {
					if len(ss.cuts) < maxCutNames {
						ss.cuts[name] = true
					} else {
						name = ""
					}
				}
				cuts[cutKey{p.Service, name}]++
				res.Cut++
				continue
			}
			if num, ok = known[id]; !ok {
				num = s.nextSeries
				s.nextSeries++
			}
			ss.ids[id], ss.names[p.Name] = num, true
			touch = append(touch, newSeries{id, num, p})
		}
		day := utcDay(time.Unix(p.Time, 0))
		byDay[day] = append(byDay[day], metricRow{num, p})
		res.Written++
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return res, err
	}
	defer func() { _ = conn.Close() }()
	var newDays []time.Time
	err = conn.Raw(func(dc any) error {
		c := dc.(driver.Conn)
		ex := dc.(driver.ExecerContext)
		if _, err := ex.ExecContext(ctx, "BEGIN TRANSACTION", nil); err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
				_, _ = ex.ExecContext(context.WithoutCancel(ctx), "ROLLBACK", nil)
			}
		}()
		for _, day := range slices.SortedFunc(func(yield func(time.Time) bool) {
			for d := range byDay {
				if !yield(d) {
					return
				}
			}
		}, func(a, b time.Time) int { return a.Compare(b) }) {
			if !s.hasMetricDay(day) {
				if _, err := ex.ExecContext(ctx, fmt.Sprintf(metricDayTableSchema, metricDayTable(day)), nil); err != nil {
					return err
				}
				newDays = append(newDays, day)
			}
			if err := appendPoints(c, metricDayTable(day), byDay[day]); err != nil {
				return err
			}
		}
		if err := upsertSeries(ctx, ex, touch, today); err != nil {
			return err
		}
		for k, n := range cuts {
			if _, err := ex.ExecContext(ctx, `INSERT INTO metric_cuts VALUES ($1, $2, $3, $4)
    ON CONFLICT (day, service_id, name) DO UPDATE SET points = metric_cuts.points + excluded.points`, []driver.NamedValue{
				{Ordinal: 1, Value: today}, {Ordinal: 2, Value: uint32(k.service)}, {Ordinal: 3, Value: k.name}, {Ordinal: 4, Value: uint64(n)},
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
		return analytics.MetricWriteResult{}, fmt.Errorf("write metric points: %w", err)
	}
	if len(newDays) > 0 {
		s.daysMu.Lock()
		for _, d := range newDays {
			s.mdays[d] = true
		}
		s.daysMu.Unlock()
	}
	s.writes++
	if lim.MaxBytes > 0 && (s.writes >= sizeCheckEvery || len(newDays) > 0) {
		s.writes = 0
		if err := s.enforceTraceSize(ctx, lim.MaxBytes, today); err != nil {
			return res, fmt.Errorf("keep the traces database within its size: %w", err)
		}
	}
	return res, nil
}

// cursorLocked is source's position, read with mu held: a metrics write
// checks its batch against it before deciding anything.
func (s *Traces) cursorLocked(ctx context.Context, source string) (uint64, error) {
	var pos uint64
	err := s.db.QueryRowContext(ctx, "SELECT position FROM cursors WHERE source = $1", source).Scan(&pos)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	return pos, nil
}

// seriesChunk is how many series one statement upserts.
const seriesChunk = 200

// upsertSeries inserts the series new today, or marks them seen today:
// each once a write, and a series once a day.
func upsertSeries(ctx context.Context, ex driver.ExecerContext, series []newSeries, today time.Time) error {
	const cols = 14
	for chunk := range slices.Chunk(series, seriesChunk) {
		var q strings.Builder
		q.WriteString("INSERT INTO metric_series VALUES ")
		args := make([]driver.NamedValue, 0, cols*len(chunk))
		for i, ns := range chunk {
			p := ns.p
			if i > 0 {
				q.WriteByte(',')
			}
			q.WriteByte('(')
			for j := range cols {
				if j > 0 {
					q.WriteByte(',')
				}
				q.WriteString("$" + strconv.Itoa(i*cols+j+1))
			}
			q.WriteByte(')')
			attrs := string(p.Attrs)
			if attrs == "" {
				attrs = "{}"
			}
			for _, v := range []any{ns.id, ns.num, uint32(p.Org), uint32(p.Project), uint32(p.Env), uint32(p.Service), p.Name, p.Unit,
				uint8(p.Kind), p.Monotonic, p.Cumulative, attrs, today, today} {
				args = append(args, driver.NamedValue{Ordinal: len(args) + 1, Value: v})
			}
		}
		q.WriteString(" ON CONFLICT (id) DO UPDATE SET last_day = excluded.last_day")
		if _, err := ex.ExecContext(ctx, q.String(), args); err != nil {
			return err
		}
	}
	return nil
}

// appendPoints writes rows to table with DuckDB's appender, in the open
// transaction on c.
func appendPoints(c driver.Conn, table string, rows []metricRow) error {
	a, err := duckdb.NewAppenderFromConn(c, "", table)
	if err != nil {
		return err
	}
	vals := make([]driver.Value, 9)
	for _, r := range rows {
		p := r.p
		vals[0], vals[1], vals[2], vals[3] = r.series, p.Time/60*60, uint32(p.Service), uint32(p.Release)
		vals[4], vals[5], vals[6], vals[7], vals[8] = p.Value, p.Count, nil, nil, nil
		if p.Min != nil {
			vals[6] = *p.Min
		}
		if p.Max != nil {
			vals[7] = *p.Max
		}
		if len(p.Buckets) > 0 {
			vals[8] = p.Buckets
		}
		if err := a.AppendRow(vals...); err != nil {
			_ = a.Close()
			return err
		}
	}
	return a.Close() // flushes
}

// hasMetricDay reports whether day has a points table.
func (s *Traces) hasMetricDay(day time.Time) bool {
	s.daysMu.RLock()
	defer s.daysMu.RUnlock()
	return s.mdays[day]
}

// metricDays lists every points day table, oldest first.
func (s *Traces) metricDays() []time.Time {
	s.daysMu.RLock()
	defer s.daysMu.RUnlock()
	days := make([]time.Time, 0, len(s.mdays))
	for d := range s.mdays {
		days = append(days, d)
	}
	slices.SortFunc(days, func(a, b time.Time) int { return a.Compare(b) })
	return days
}

// rollupMetricDay rolls day's points up into hours, once: an hour of a
// series is its points' count, sum, last value, extremes (a gauge's or
// sum's values, a histogram's min and max) and bucket counts.
func (s *Traces) rollupMetricDay(ctx context.Context, day time.Time) error {
	s.daysMu.RLock()
	skip := !s.mdays[day] || s.rolled[day]
	s.daysMu.RUnlock()
	if skip {
		return nil
	}
	buckets := make([]string, analytics.MetricBuckets)
	for i := range buckets {
		buckets[i] = "coalesce(sum(p.buckets[" + strconv.Itoa(i+1) + "]), 0)::UBIGINT"
	}
	histogram := strconv.Itoa(int(analytics.MetricHistogram))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO metric_hours
    SELECT p.series, p.minute // 3600 * 3600, any_value(p.service_id), max(p.release_id), count(*), sum(p.value), arg_max(p.value, p.minute), sum(p.n),
        min(CASE WHEN s.kind = `+histogram+` THEN p.min ELSE p.value END),
        max(CASE WHEN s.kind = `+histogram+` THEN p.max ELSE p.value END),
        CASE WHEN any_value(s.kind) = `+histogram+` THEN [`+strings.Join(buckets, ", ")+`] END
    FROM `+metricDayTable(day)+` p LEFT JOIN metric_series s ON s.num = p.series
    GROUP BY p.series, p.minute // 3600 * 3600`); err != nil {
		return fmt.Errorf("roll the metric points of %s up into hours: %w", day.Format(time.DateOnly), err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO metric_rolled VALUES ($1)", day); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.daysMu.Lock()
	s.rolled[day] = true
	s.daysMu.Unlock()
	return nil
}

// dropMetricDay drops day's points, rolled up into hours first.
func (s *Traces) dropMetricDay(ctx context.Context, day time.Time) error {
	if err := s.rollupMetricDay(ctx, day); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+metricDayTable(day)); err != nil {
		return err
	}
	s.daysMu.Lock()
	delete(s.mdays, day)
	s.daysMu.Unlock()
	return nil
}

// rollupBefore rolls up the days of points before today not rolled up
// yet: on a day's first write, so the day before is in hours.
func (s *Traces) rollupBefore(ctx context.Context, today time.Time) error {
	for _, d := range s.metricDays() {
		if !d.Before(today) {
			break
		}
		if err := s.rollupMetricDay(ctx, d); err != nil {
			return err
		}
	}
	return nil
}

// rolledCut is where a chart's hours end and its minute points start
// (Unix seconds): the first day of points not rolled up yet, else the
// end of the last rolled up, else never.
func (s *Traces) rolledCut() int64 {
	s.daysMu.RLock()
	defer s.daysMu.RUnlock()
	var first, last time.Time
	for d := range s.mdays {
		if !s.rolled[d] && (first.IsZero() || d.Before(first)) {
			first = d
		}
	}
	if !first.IsZero() {
		return first.Unix()
	}
	for d := range s.rolled {
		if d.After(last) {
			last = d
		}
	}
	if last.IsZero() {
		return math.MaxInt64
	}
	return last.Add(24 * time.Hour).Unix()
}

// pruneMetrics rolls up and drops the points days before before, and
// deletes the hours, series and cut rows before hoursBefore (zero keeps
// them).
func (s *Traces) pruneMetrics(ctx context.Context, before, hoursBefore time.Time) ([]string, error) {
	var days []string
	for _, d := range s.metricDays() {
		if !d.Before(before) {
			break
		}
		if err := s.dropMetricDay(ctx, d); err != nil {
			return days, err
		}
		days = append(days, d.Format(time.DateOnly))
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM metric_cuts WHERE day < $1", before); err != nil {
		return days, err
	}
	if hoursBefore.IsZero() {
		return days, nil
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM metric_hours WHERE hour < $1", hoursBefore.Unix()); err != nil {
		return days, err
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM metric_rolled WHERE day < $1", hoursBefore); err != nil {
		return days, err
	}
	s.daysMu.Lock()
	for d := range s.rolled {
		if d.Before(hoursBefore) {
			delete(s.rolled, d)
		}
	}
	s.daysMu.Unlock()
	_, err := s.db.ExecContext(ctx, "DELETE FROM metric_series WHERE last_day < $1", hoursBefore)
	return days, err
}

// MetricNames lists the metrics q's services sent in its range, by
// service and name: two queries, the series seen in the range's days (by
// their first and last days: no scan of points, which took 170 ms for a
// week of 2,000 series) and the cut rows of its days.
func (s *Traces) MetricNames(ctx context.Context, q analytics.MetricNamesQuery) (analytics.MetricNamesResult, error) {
	res := analytics.MetricNamesResult{Metrics: []analytics.MetricName{}}
	switch {
	case len(q.Services) == 0:
		return res, &QueryError{"a metrics list needs at least one service"}
	case q.Org <= 0:
		return res, &QueryError{"a metrics list needs its org"}
	case q.To <= q.From:
		return res, &QueryError{"a metrics list needs a range that ends after it starts"}
	}
	s.gate.RLock()
	defer s.gate.RUnlock()
	type key struct {
		service int64
		name    string
	}
	at := map[key]int{}
	rows, err := s.db.QueryContext(ctx, `SELECT service_id, name, any_value(unit), any_value(kind), count(*) FROM metric_series
    WHERE service_id IN (`+intList(q.Services)+`) AND org_id = $1 AND last_day >= $2 AND first_day <= $3
    GROUP BY service_id, name ORDER BY service_id, name`, uint32(q.Org), utcDay(time.Unix(q.From, 0)), utcDay(time.Unix(q.To-1, 0)))
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var m analytics.MetricName
		if err := rows.Scan(&m.Service, &m.Name, &m.Unit, &m.Kind, &m.Series); err != nil {
			_ = rows.Close()
			return res, err
		}
		at[key{m.Service, m.Name}] = len(res.Metrics)
		res.Metrics = append(res.Metrics, m)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	// Cut rows name a service the caller may read (the services are the
	// caller's), by day.
	rows, err = s.db.QueryContext(ctx, `SELECT service_id, name, sum(points) FROM metric_cuts
    WHERE service_id IN (`+intList(q.Services)+`) AND day >= $1 AND day <= $2 GROUP BY ALL`,
		utcDay(time.Unix(q.From, 0)), utcDay(time.Unix(q.To-1, 0)))
	if err != nil {
		return res, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k key
		var n int64
		if err := rows.Scan(&k.service, &k.name, &n); err != nil {
			return res, err
		}
		if i, ok := at[k]; ok {
			res.Metrics[i].Cut = n
		} else {
			res.Metrics = append(res.Metrics, analytics.MetricName{Service: k.service, Name: k.name, Cut: n})
		}
	}
	if err := rows.Err(); err != nil {
		return res, err
	}
	// For each cut metric, the attribute with the most values: one query
	// for all of them, only when there are cuts.
	var cut []string
	var args []any
	for _, m := range res.Metrics {
		if m.Cut > 0 && m.Name != "" {
			args = append(args, m.Name)
			cut = append(cut, "$"+strconv.Itoa(len(args)))
		}
	}
	if len(cut) == 0 {
		return res, nil
	}
	rows, err = s.db.QueryContext(ctx, `SELECT service_id, name, arg_max(key, n) FROM (
    SELECT service_id, name, key, count(DISTINCT json_extract_string(attrs, '$."' || replace(key, '"', '\"') || '"')) AS n
    FROM (SELECT service_id, name, attrs, unnest(json_keys(attrs)) AS key FROM metric_series
        WHERE service_id IN (`+intList(q.Services)+`) AND name IN (`+strings.Join(cut, ", ")+`))
    GROUP BY ALL) GROUP BY service_id, name`, args...)
	if err != nil {
		return res, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k key
		var by string
		if err := rows.Scan(&k.service, &k.name, &by); err != nil {
			return res, err
		}
		if i, ok := at[k]; ok {
			res.Metrics[i].CutBy = by
		}
	}
	return res, rows.Err()
}

// metricDaysIn lists the points day tables of minutes in [from, to)
// (Unix seconds), oldest first.
func (s *Traces) metricDaysIn(from, to int64) []time.Time {
	first, last := utcDay(time.Unix(from, 0)), utcDay(time.Unix(to-1, 0))
	return slices.DeleteFunc(s.metricDays(), func(d time.Time) bool { return d.Before(first) || d.After(last) })
}

// RollUpMetrics rolls the days of points before today up into hours, as
// a day's first write does: for seed-traces, whose days are all written
// on one.
func (s *Traces) RollUpMetrics(ctx context.Context) error {
	s.gate.RLock()
	defer s.gate.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rollupBefore(ctx, utcDay(s.now()))
}
