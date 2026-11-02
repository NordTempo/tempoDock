package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// Errors and Web Vitals (ADR 0037, phase A2) have tables of their own:
// they aren't page views or custom events, so the dashboards' counts,
// sessions and rollups never see them. An error carries its visitor and
// the session it happened in, looked up without changing the session
// (sessions.peek): an error doesn't lengthen a visit.
//
// error_groups holds one row per site and fingerprint for as long as the
// group has errors: when and in which release it first appeared, which
// outlives the range a query reads, and whether a write's group is new
// (the "new error" alert). Rows are only ever inserted: the group's
// latest message and last time are read from its errors.
const errorsSchema = `
CREATE TABLE IF NOT EXISTS errors (
    ts              TIMESTAMP NOT NULL,
    site_id         UINTEGER NOT NULL,
    service_id      UINTEGER NOT NULL,
    release_id      UINTEGER NOT NULL,
    hostname        VARCHAR NOT NULL,
    path            VARCHAR NOT NULL,
    grp             UBIGINT NOT NULL,
    type            VARCHAR NOT NULL,
    message         VARCHAR NOT NULL,
    frame           VARCHAR NOT NULL,
    stack           VARCHAR NOT NULL,
    visitor_id      UBIGINT NOT NULL,
    session_id      UBIGINT NOT NULL,
    country         VARCHAR NOT NULL,
    browser         VARCHAR NOT NULL,
    browser_version VARCHAR NOT NULL,
    os              VARCHAR NOT NULL,
    device          VARCHAR NOT NULL
);
CREATE TABLE IF NOT EXISTS error_groups (
    site_id       UINTEGER NOT NULL,
    grp           UBIGINT NOT NULL,
    first_seen    TIMESTAMP NOT NULL,
    first_release UINTEGER NOT NULL,
    PRIMARY KEY (site_id, grp)
);
-- One row per page load; a vital it didn't measure is NULL. Times are
-- whole milliseconds and CLS a FLOAT, and no visitor or session: random
-- 64-bit IDs don't compress and would double the table.
CREATE TABLE IF NOT EXISTS vitals (
    ts              TIMESTAMP NOT NULL,
    site_id         UINTEGER NOT NULL,
    service_id      UINTEGER NOT NULL,
    release_id      UINTEGER NOT NULL,
    hostname        VARCHAR NOT NULL,
    path            VARCHAR NOT NULL,
    country         VARCHAR NOT NULL,
    browser         VARCHAR NOT NULL,
    browser_version VARCHAR NOT NULL,
    os              VARCHAR NOT NULL,
    device          VARCHAR NOT NULL,
    lcp             UINTEGER,
    inp             UINTEGER,
    cls             FLOAT,
    fcp             UINTEGER,
    ttfb            UINTEGER
);
`

// splitKinds separates a batch's errors and vitals from the events the
// events table keeps, in their order. A batch without either is returned
// as it is.
func splitKinds(events []analytics.Event) (regular, errs, vitals []analytics.Event) {
	n := 0
	for i := range events {
		if k := events[i].Kind; k == analytics.KindError || k == analytics.KindVitals {
			n++
		}
	}
	if n == 0 {
		return events, nil, nil
	}
	regular = make([]analytics.Event, 0, len(events)-n)
	for _, e := range events {
		switch {
		case e.Kind == analytics.KindError && e.Error != nil:
			errs = append(errs, e)
		case e.Kind == analytics.KindVitals && e.Vitals != nil:
			vitals = append(vitals, e)
		case e.Kind != analytics.KindError && e.Kind != analytics.KindVitals:
			regular = append(regular, e)
		}
	}
	return regular, errs, vitals
}

// appendVitals writes page loads' vitals in the open transaction on c.
func appendVitals(c driver.Conn, events []analytics.Event, locate Locator) error {
	a, err := duckdb.NewAppenderFromConn(c, "", "vitals")
	if err != nil {
		return err
	}
	for i := range events {
		e := &events[i]
		v := e.Vitals
		loc := lookup(locate, e.IPPrefix)
		err := a.AppendRow(time.UnixMilli(e.Time).UTC(), uint32(e.Site), uint32(e.Service), uint32(e.Release), e.Hostname, e.Path,
			loc.Country, e.Browser, e.BrowserVersion, e.OS, e.Device,
			ms(v.LCP), ms(v.INP), score(v.CLS), ms(v.FCP), ms(v.TTFB))
		if err != nil {
			_ = a.Close()
			return err
		}
	}
	return a.Close()
}

// ms is a vital in milliseconds for the appender: nil (NULL) when
// unmeasured.
func ms(v float64) any {
	if v < 0 {
		return nil
	}
	return uint32(v)
}

// score is CLS for the appender: nil (NULL) when unmeasured.
func score(v float64) any {
	if v < 0 {
		return nil
	}
	return float32(v)
}

// lookup is locate(prefix), or no location.
func lookup(locate Locator, prefix string) Location {
	if locate == nil || prefix == "" {
		return Location{}
	}
	return locate(prefix)
}

// groupKey names an error group: its site and fingerprint.
type groupKey struct {
	site int64
	grp  uint64
}

// appendErrors writes errors in the open transaction on c and records
// the groups seen for the first time, which it returns. The store keeps
// the groups it has in memory (known, primed at open), so a batch of
// known errors costs no query; the new ones join known once the write
// commits (Write).
func (s *Store) appendErrors(ctx context.Context, c driver.Conn, ex driver.ExecerContext, events []analytics.Event, locate Locator) ([]analytics.NewError, error) {
	a, err := duckdb.NewAppenderFromConn(c, "", "errors")
	if err != nil {
		return nil, err
	}
	first := map[groupKey]*analytics.Event{}
	var order []groupKey
	for i := range events {
		e := &events[i]
		er := e.Error
		loc := lookup(locate, e.IPPrefix)
		err := a.AppendRow(time.UnixMilli(e.Time).UTC(), uint32(e.Site), uint32(e.Service), uint32(e.Release), e.Hostname, e.Path,
			er.Group, er.Type, e.Name, er.Frame, er.Stack, e.Visitor, s.sessions.peek(e.Site, e.Visitor, e.Time),
			loc.Country, e.Browser, e.BrowserVersion, e.OS, e.Device)
		if err != nil {
			_ = a.Close()
			return nil, err
		}
		k := groupKey{e.Site, er.Group}
		if _, ok := s.known[k]; ok {
			continue
		}
		if f, ok := first[k]; !ok {
			first[k] = e
			order = append(order, k)
		} else if e.Time < f.Time {
			first[k] = e
		}
	}
	if err := a.Close(); err != nil || len(order) == 0 {
		return nil, err
	}
	var b strings.Builder
	args := make([]driver.NamedValue, 0, 4*len(order))
	b.WriteString("INSERT INTO error_groups VALUES ")
	news := make([]analytics.NewError, 0, len(order))
	for i, k := range order {
		f := first[k]
		if i > 0 {
			b.WriteByte(',')
		}
		n := len(args)
		fmt.Fprintf(&b, "($%d, $%d, $%d, $%d)", n+1, n+2, n+3, n+4)
		args = append(args, driver.NamedValue{Ordinal: n + 1, Value: uint32(k.site)}, driver.NamedValue{Ordinal: n + 2, Value: k.grp},
			driver.NamedValue{Ordinal: n + 3, Value: time.UnixMilli(f.Time).UTC()}, driver.NamedValue{Ordinal: n + 4, Value: uint32(f.Release)})
		news = append(news, analytics.NewError{Site: k.site, Group: groupHex(k.grp), Type: f.Error.Type, Message: f.Name, Path: f.Path, Release: f.Release})
	}
	// A group known to the file but not to memory can't happen (known is
	// primed from the file), but must not fail the write if it does.
	b.WriteString(" ON CONFLICT DO NOTHING")
	if _, err := ex.ExecContext(ctx, b.String(), args); err != nil {
		return nil, err
	}
	s.newGroups = order
	return news, nil
}

// loadGroups primes known from the database: every site's error groups.
// It holds as many as the sites' error retention keeps, a few bytes
// each. Called at open and after a prune, with writeMu held or before
// any write.
func (s *Store) loadGroups(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "SELECT site_id, grp FROM error_groups")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	known := map[groupKey]struct{}{}
	for rows.Next() {
		var site uint32
		var grp uint64
		if err := rows.Scan(&site, &grp); err != nil {
			return err
		}
		known[groupKey{int64(site), grp}] = struct{}{}
	}
	s.known = known
	return rows.Err()
}

// groupHex is a fingerprint as the API writes it.
func groupHex(g uint64) string { return fmt.Sprintf("%016x", g) }

// parseGroup reads a fingerprint the API wrote.
func parseGroup(s string) (uint64, error) {
	g, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		return 0, badQuery("error group %q isn't one; groups are 16 hex digits", s)
	}
	return g, nil
}

// errorColumns are the dimensions of errors and vitals rows: both tables
// name them alike.
var errorColumns = map[analytics.Dimension]string{
	analytics.DimPage:           "path",
	analytics.DimHostname:       "hostname",
	analytics.DimCountry:        "country",
	analytics.DimBrowser:        "browser",
	analytics.DimBrowserVersion: "browser_version",
	analytics.DimOS:             "os",
	analytics.DimDevice:         "device",
	analytics.DimService:        "service_id::VARCHAR",
	analytics.DimRelease:        "release_id::VARCHAR",
}

// errorWhere is the WHERE clause over the errors or vitals of q's site
// and range that match its filters, binding values to p.
func errorWhere(p *params, q analytics.Query) (string, error) {
	if q.To <= q.From {
		return "", badQuery("the range is empty: from %d is not before to %d", q.From, q.To)
	}
	if len(q.Filters) > analytics.MaxFilters {
		return "", badQuery("at most %d filters", analytics.MaxFilters)
	}
	var b strings.Builder
	b.WriteString("site_id = " + p.add(uint32(q.Site)) + " AND ts >= " + p.add(time.UnixMilli(q.From).UTC()) +
		" AND ts < " + p.add(time.UnixMilli(q.To).UTC()))
	for _, f := range q.Filters {
		col, ok := errorColumns[f.Dimension]
		if !ok {
			return "", badQuery("errors and Web Vitals can't be filtered by %s; they record the page, hostname, country, browser, OS, device, service and release", f.Dimension)
		}
		switch f.Op {
		case analytics.OpIs:
			b.WriteString(" AND " + col + " = " + p.add(f.Value))
		case analytics.OpIsNot:
			b.WriteString(" AND " + col + " <> " + p.add(f.Value))
		case analytics.OpContains:
			b.WriteString(" AND " + col + " ILIKE " + p.add("%"+escapeLike(f.Value)+"%") + " ESCAPE '\\'")
		default:
			return "", badQuery("unknown filter operator %q; use is, is_not or contains", f.Op)
		}
	}
	return b.String(), nil
}

// bucketOf checks a series' bucket over q's range: "" is no series.
func bucketOf(b analytics.Bucket, q analytics.Query) (string, error) {
	if b == "" {
		return "", nil
	}
	width, ok := bucketWidths[b]
	if !ok {
		return "", badQuery("unknown bucket %q; use minute, hour or day", b)
	}
	if (q.To-q.From)/width.ms > maxPoints {
		return "", badQuery("%s buckets over this range are more than %d points; use a larger bucket", b, maxPoints)
	}
	return width.sql, nil
}

// Errors returns q's error groups with errors in its range, the most
// frequent first, every error's series when q asks for one, and the
// range's totals.
func (s *Store) Errors(ctx context.Context, q analytics.ErrorsQuery) (analytics.ErrorsResult, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	out := analytics.ErrorsResult{Groups: []analytics.ErrorGroup{}}
	var p params
	w, err := errorWhere(&p, q.Query)
	if err != nil {
		return out, err
	}
	width, err := bucketOf(q.Bucket, q.Query)
	if err != nil {
		return out, err
	}
	limit := q.Limit
	if limit <= 0 || limit > analytics.MaxErrorGroups {
		limit = analytics.MaxErrorGroups
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.grp, e.type, e.message, e.frame, e.n, e.sessions, e.visitors,
       epoch_ms(coalesce(g.first_seen, e.first)), epoch_ms(e.last), coalesce(g.first_release, 0)
FROM (
    SELECT grp, arg_max(type, ts) AS type, arg_max(message, ts) AS message, arg_max(frame, ts) AS frame,
           count(*) AS n, count(DISTINCT session_id) FILTER (WHERE session_id <> 0) AS sessions,
           count(DISTINCT visitor_id) AS visitors, min(ts) AS first, max(ts) AS last
    FROM errors WHERE `+w+` GROUP BY grp
) e LEFT JOIN error_groups g ON g.site_id = `+p.add(uint32(q.Site))+` AND g.grp = e.grp
ORDER BY e.n DESC, e.last DESC LIMIT `+strconv.Itoa(limit), p...)
	if err != nil {
		return out, err
	}
	index := map[uint64]int{}
	for rows.Next() {
		var g analytics.ErrorGroup
		var grp uint64
		var rel uint32
		if err := rows.Scan(&grp, &g.Type, &g.Message, &g.Frame, &g.Count, &g.Sessions, &g.Visitors, &g.FirstSeen, &g.LastSeen, &rel); err != nil {
			_ = rows.Close()
			return out, err
		}
		g.Group, g.FirstRelease = groupHex(grp), int64(rel)
		index[grp] = len(out.Groups)
		out.Groups = append(out.Groups, g)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	// The totals: new groups are those whose first error ever is in the
	// range.
	var sp params
	sw, _ := errorWhere(&sp, q.Query)
	if err := s.db.QueryRowContext(ctx, `SELECT count(*), count(DISTINCT session_id) FILTER (WHERE session_id <> 0),
       count(DISTINCT visitor_id), count(DISTINCT grp),
       count(DISTINCT grp) FILTER (WHERE grp IN (SELECT grp FROM error_groups WHERE site_id = $1 AND first_seen >= $2 AND first_seen < $3))
FROM errors WHERE `+sw, sp...).Scan(&out.Summary.Errors, &out.Summary.Sessions, &out.Summary.Visitors, &out.Summary.Groups, &out.Summary.NewGroups); err != nil {
		return out, err
	}
	if width == "" || out.Summary.Errors == 0 {
		return out, nil
	}
	series, err := s.db.QueryContext(ctx, `SELECT grp, epoch_ms(time_bucket(`+width+`, ts)) AS b, count(*)
FROM errors WHERE `+sw+` GROUP BY grp, b ORDER BY b`, sp...)
	if err != nil {
		return out, err
	}
	defer func() { _ = series.Close() }()
	total := map[int64]int64{}
	var buckets []int64
	for series.Next() {
		var grp uint64
		var pt analytics.CountPoint
		if err := series.Scan(&grp, &pt.T, &pt.Count); err != nil {
			return out, err
		}
		if i, ok := index[grp]; ok {
			out.Groups[i].Series = append(out.Groups[i].Series, pt)
		}
		if _, ok := total[pt.T]; !ok {
			buckets = append(buckets, pt.T)
		}
		total[pt.T] += pt.Count
	}
	for _, t := range buckets {
		out.Series = append(out.Series, analytics.CountPoint{T: t, Count: total[t]})
	}
	return out, series.Err()
}

// errorBreakdowns are the breakdowns of an error group's detail.
var errorBreakdowns = []analytics.Dimension{analytics.DimPage, analytics.DimBrowser, analytics.DimOS, analytics.DimDevice, analytics.DimRelease}

// errorBreakdownRows caps each breakdown of an error group.
const errorBreakdownRows = 10

// Error returns one error group in q's range: its counts, the latest
// stack, where it happened and its latest occurrences.
func (s *Store) Error(ctx context.Context, q analytics.ErrorQuery) (analytics.ErrorDetail, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	grp, err := parseGroup(q.Group)
	if err != nil {
		return analytics.ErrorDetail{}, err
	}
	var p params
	w, err := errorWhere(&p, q.Query)
	if err != nil {
		return analytics.ErrorDetail{}, err
	}
	width, err := bucketOf(q.Bucket, q.Query)
	if err != nil {
		return analytics.ErrorDetail{}, err
	}
	w += " AND grp = " + p.addU64(grp)
	d := analytics.ErrorDetail{Pages: []analytics.Row{}, Browsers: []analytics.Row{}, OS: []analytics.Row{}, Devices: []analytics.Row{},
		Releases: []analytics.Row{}, Recent: []analytics.ErrorOccurrence{}}
	var rel uint32
	err = s.db.QueryRowContext(ctx, `SELECT epoch_ms(first_seen), first_release FROM error_groups WHERE site_id = $1 AND grp = $2::UBIGINT`,
		uint32(q.Site), strconv.FormatUint(grp, 10)).Scan(&d.FirstSeen, &rel)
	if errors.Is(err, sql.ErrNoRows) {
		return d, badQuery("this site has no error group %s", q.Group)
	}
	if err != nil {
		return d, err
	}
	d.Group, d.FirstRelease = q.Group, int64(rel)
	var typ, msg, frame, stack sql.NullString
	var last sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*), count(DISTINCT session_id) FILTER (WHERE session_id <> 0),
       count(DISTINCT visitor_id), arg_max(type, ts), arg_max(message, ts), arg_max(frame, ts), arg_max(stack, ts), epoch_ms(max(ts))
FROM errors WHERE `+w, p...).Scan(&d.Count, &d.Sessions, &d.Visitors, &typ, &msg, &frame, &stack, &last); err != nil {
		return d, err
	}
	d.Type, d.Message, d.Frame, d.Stack, d.LastSeen = typ.String, msg.String, frame.String, stack.String, last.Int64
	for _, dim := range errorBreakdowns {
		rows, err := s.db.QueryContext(ctx, `SELECT `+errorColumns[dim]+` AS v, count(DISTINCT visitor_id), count(*) AS n
FROM errors WHERE `+w+` GROUP BY v ORDER BY n DESC, v LIMIT `+strconv.Itoa(errorBreakdownRows), p...)
		if err != nil {
			return d, err
		}
		var out []analytics.Row
		for rows.Next() {
			var r analytics.Row
			if err := rows.Scan(&r.Value, &r.Visitors, &r.Count); err != nil {
				_ = rows.Close()
				return d, err
			}
			out = append(out, r)
		}
		if err := rows.Close(); err != nil {
			return d, err
		}
		switch dim {
		case analytics.DimPage:
			d.Pages = append(d.Pages, out...)
		case analytics.DimBrowser:
			d.Browsers = append(d.Browsers, out...)
		case analytics.DimOS:
			d.OS = append(d.OS, out...)
		case analytics.DimDevice:
			d.Devices = append(d.Devices, out...)
		case analytics.DimRelease:
			d.Releases = append(d.Releases, out...)
		}
	}
	// Each occurrence with a recorded page load of the visitor's around it,
	// if one was.
	rows, err := s.db.QueryContext(ctx, `SELECT epoch_ms(e.ts), e.path, e.browser, e.browser_version, e.os, e.device, e.country,
       e.service_id, e.release_id, e.session_id,
       (SELECT min(r.segment) FROM replay_chunks r WHERE r.site_id = e.site_id AND r.visitor_id = e.visitor_id
          AND r.day >= e.ts::DATE - 1 AND r.day <= e.ts::DATE AND e.ts >= r.ts_first - INTERVAL 10 SECOND AND e.ts <= r.ts_last + INTERVAL 10 SECOND)
FROM (SELECT * FROM errors WHERE `+w+` ORDER BY ts DESC LIMIT `+strconv.Itoa(analytics.MaxErrorRecent)+`) e ORDER BY e.ts DESC`, p...)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var o analytics.ErrorOccurrence
		var svc, rel uint32
		var session uint64
		var replay sql.Null[uint64]
		if err := rows.Scan(&o.Time, &o.Path, &o.Browser, &o.BrowserVersion, &o.OS, &o.Device, &o.Country, &svc, &rel, &session, &replay); err != nil {
			_ = rows.Close()
			return d, err
		}
		o.Service, o.Release = int64(svc), int64(rel)
		if session != 0 {
			o.Session = groupHex(session)
		}
		if replay.Valid {
			o.Replay = groupHex(replay.V)
		}
		d.Recent = append(d.Recent, o)
	}
	if err := rows.Close(); err != nil || width == "" {
		return d, err
	}
	series, err := s.db.QueryContext(ctx, `SELECT epoch_ms(time_bucket(`+width+`, ts)) AS b, count(*) FROM errors WHERE `+w+` GROUP BY b ORDER BY b`, p...)
	if err != nil {
		return d, err
	}
	defer func() { _ = series.Close() }()
	for series.Next() {
		var pt analytics.CountPoint
		if err := series.Scan(&pt.T, &pt.Count); err != nil {
			return d, err
		}
		d.Series = append(d.Series, pt)
	}
	return d, series.Err()
}

// vitalsSample is the reservoir each percentile is taken from: p75 of
// 2,048 samples is within about a percentile point, and the reservoir is
// what the query's time goes to (8,192 took three times as long).
const vitalsSample = 2048

// p75 is the SQL for a vital's 75th percentile, -1 with no samples. It
// is of a reservoir sample: memory stays bounded however many page loads
// a range holds, and it is several times faster than a t-digest or an
// exact quantile.
func p75(col string) string {
	return "coalesce(reservoir_quantile(" + col + ", 0.75, " + strconv.Itoa(vitalsSample) + ")::DOUBLE, -1)"
}

// vitalsSelect computes every vital's 75th percentile and samples, and
// the good and poor counts of those counts says, in VitalNames' order.
// Breakdowns count only the vital the view shows: each count costs about
// as much as a percentile.
func vitalsSelect(counts func(analytics.Vital) bool) string {
	var b strings.Builder
	for _, v := range analytics.VitalNames {
		t := analytics.VitalThresholds[v]
		c := string(v)
		fmt.Fprintf(&b, ", %s, count(%s)", p75(c), c)
		if counts(v) {
			fmt.Fprintf(&b, ", count(*) FILTER (WHERE %[1]s <= %[2]g), count(*) FILTER (WHERE %[1]s > %[3]g)", c, t.Good, t.Poor)
		}
	}
	return b.String()
}

// scanVitals reads a row of vitalsSelect after its leading columns.
func scanVitals(sc interface{ Scan(...any) error }, lead []any, counts func(analytics.Vital) bool, r *analytics.VitalsRow) error {
	dest := lead
	for i, st := range []*analytics.VitalStat{&r.LCP, &r.INP, &r.CLS, &r.FCP, &r.TTFB} {
		dest = append(dest, &st.P75, &st.Samples)
		if counts(analytics.VitalNames[i]) {
			dest = append(dest, &st.Good, &st.Poor)
		}
	}
	return sc.Scan(dest...)
}

// allVitals counts every vital.
func allVitals(analytics.Vital) bool { return true }

// Vitals returns q's Web Vitals: over all its page loads (with good and
// poor counts), by each of its dimensions' values (the most loads first)
// and q.Metric over time.
func (s *Store) Vitals(ctx context.Context, q analytics.VitalsQuery) (analytics.VitalsResult, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	out := analytics.VitalsResult{Breakdowns: map[analytics.Dimension][]analytics.VitalsRow{}, Series: []analytics.VitalPoint{}}
	var p params
	w, err := errorWhere(&p, q.Query)
	if err != nil {
		return out, err
	}
	width, err := bucketOf(q.Bucket, q.Query)
	if err != nil {
		return out, err
	}
	if len(q.Dimensions) > len(errorColumns) {
		return out, badQuery("at most %d breakdowns", len(errorColumns))
	}
	for _, d := range q.Dimensions {
		if _, ok := errorColumns[d]; !ok {
			return out, badQuery("Web Vitals can't be broken down by %s; use page, hostname, country, browser, browser_version, os, device, service or release", d)
		}
	}
	if q.Metric != "" && !slices.Contains(analytics.VitalNames, q.Metric) {
		return out, badQuery("unknown vital %q; use lcp, inp, cls, fcp or ttfb", q.Metric)
	}
	if err := scanVitals(s.db.QueryRowContext(ctx, `SELECT count(*)`+vitalsSelect(allVitals)+` FROM vitals WHERE `+w, p...), []any{&out.All.Loads}, allVitals, &out.All); err != nil {
		return out, err
	}
	limit := q.Limit
	if limit <= 0 || limit > analytics.MaxBreakdown {
		limit = 100
	}
	metric := func(v analytics.Vital) bool { return v == q.Metric }
	for _, d := range q.Dimensions {
		col := errorColumns[d]
		// Percentiles only for the values shown: the top values by page
		// loads first, then their vitals.
		rows, err := s.db.QueryContext(ctx, `SELECT `+col+` AS v, count(*) AS n`+vitalsSelect(metric)+` FROM vitals WHERE `+w+
			` AND `+col+` IN (SELECT `+col+` FROM vitals WHERE `+w+` GROUP BY 1 ORDER BY count(*) DESC, 1 LIMIT `+strconv.Itoa(limit)+`)
GROUP BY v ORDER BY n DESC, v`, p...)
		if err != nil {
			return out, err
		}
		out.Breakdowns[d] = []analytics.VitalsRow{}
		for rows.Next() {
			var r analytics.VitalsRow
			if err := scanVitals(rows, []any{&r.Value, &r.Loads}, metric, &r); err != nil {
				_ = rows.Close()
				return out, err
			}
			out.Breakdowns[d] = append(out.Breakdowns[d], r)
		}
		if err := rows.Close(); err != nil {
			return out, err
		}
	}
	if width == "" || q.Metric == "" {
		return out, nil
	}
	// The metric is one of VitalNames: a column name, not a value.
	col := string(q.Metric)
	rows, err := s.db.QueryContext(ctx, `SELECT epoch_ms(time_bucket(`+width+`, ts)) AS b, `+p75(col)+`, count(`+col+`)
FROM vitals WHERE `+w+` GROUP BY b ORDER BY b`, p...)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var pt analytics.VitalPoint
		if err := rows.Scan(&pt.T, &pt.P75, &pt.Samples); err != nil {
			return out, err
		}
		if pt.Samples > 0 {
			out.Series = append(out.Series, pt)
		}
	}
	return out, rows.Err()
}

// pruneErrors deletes a site's errors before before and the groups left
// without errors; and its vitals before vitalsBefore, a step at a time
// from the oldest like its events. A group that comes back after that is
// new again.
func (s *Store) pruneErrors(ctx context.Context, site int64, before, vitalsBefore time.Time) (errs int64, err error) {
	if _, err := s.pruneTable(ctx, "vitals", site, vitalsBefore); err != nil {
		return 0, err
	}
	if before.IsZero() {
		return 0, nil
	}
	if errs, err = s.pruneTable(ctx, "errors", site, before); err != nil {
		return errs, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.ExecContext(ctx, "DELETE FROM error_groups WHERE site_id = $1 AND grp NOT IN (SELECT grp FROM errors WHERE site_id = $1)", uint32(site)); err != nil {
		return errs, err
	}
	return errs, s.loadGroups(ctx)
}

// pruneTable deletes a site's rows of table before before, pruneStep at a
// time from the oldest, letting writes in between. table is a constant.
func (s *Store) pruneTable(ctx context.Context, table string, site int64, before time.Time) (int64, error) {
	var oldest sql.NullTime
	if err := s.db.QueryRowContext(ctx, "SELECT min(ts) FROM "+table+" WHERE site_id = $1", uint32(site)).Scan(&oldest); err != nil || !oldest.Valid {
		return 0, err
	}
	var deleted int64
	for from := oldest.Time.UTC(); from.Before(before); from = from.Add(pruneStep) {
		to := from.Add(pruneStep)
		if to.After(before) {
			to = before
		}
		s.writeMu.Lock()
		res, err := s.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE site_id = $1 AND ts < $2", uint32(site), to)
		s.writeMu.Unlock()
		if err != nil {
			return deleted, err
		}
		n, _ := res.RowsAffected()
		deleted += n
	}
	return deleted, nil
}
