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
// error_groups holds one row per site and fingerprint over the site's
// whole history, so first and last seen survive the errors' retention and
// a write knows at once whether a group is new (the "new error" alert).
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
    type          VARCHAR NOT NULL,
    message       VARCHAR NOT NULL,
    frame         VARCHAR NOT NULL,
    first_seen    TIMESTAMP NOT NULL,
    last_seen     TIMESTAMP NOT NULL,
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

// groupWrite is one error group's errors in a write.
type groupWrite struct {
	site        int64
	grp         uint64
	first, last *analytics.Event
}

// appendErrors writes errors in the open transaction on c and updates
// their groups, returning the groups seen for the first time.
func (s *Store) appendErrors(ctx context.Context, c driver.Conn, ex driver.ExecerContext, qr driver.QueryerContext, events []analytics.Event, locate Locator) ([]analytics.NewError, error) {
	a, err := duckdb.NewAppenderFromConn(c, "", "errors")
	if err != nil {
		return nil, err
	}
	groups := map[[2]uint64]*groupWrite{}
	var order [][2]uint64
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
		k := [2]uint64{uint64(e.Site), er.Group}
		g := groups[k]
		if g == nil {
			g = &groupWrite{site: e.Site, grp: er.Group, first: e, last: e}
			groups[k] = g
			order = append(order, k)
		}
		if e.Time < g.first.Time {
			g.first = e
		}
		if e.Time >= g.last.Time {
			g.last = e
		}
	}
	if err := a.Close(); err != nil {
		return nil, err
	}
	known, err := knownGroups(ctx, qr, order)
	if err != nil {
		return nil, err
	}
	var news []analytics.NewError
	for _, k := range order {
		g := groups[k]
		l := g.last
		// The latest error names the group: a message that varies only
		// in its numbers shows its newest form.
		args := []driver.NamedValue{
			{Ordinal: 1, Value: uint32(g.site)}, {Ordinal: 2, Value: g.grp}, {Ordinal: 3, Value: l.Error.Type},
			{Ordinal: 4, Value: l.Name}, {Ordinal: 5, Value: l.Error.Frame}, {Ordinal: 6, Value: time.UnixMilli(g.first.Time).UTC()},
			{Ordinal: 7, Value: time.UnixMilli(l.Time).UTC()}, {Ordinal: 8, Value: uint32(g.first.Release)},
		}
		if _, err := ex.ExecContext(ctx, `INSERT INTO error_groups VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (site_id, grp) DO UPDATE SET
    type = CASE WHEN excluded.last_seen >= last_seen THEN excluded.type ELSE type END,
    message = CASE WHEN excluded.last_seen >= last_seen THEN excluded.message ELSE message END,
    frame = CASE WHEN excluded.last_seen >= last_seen THEN excluded.frame ELSE frame END,
    first_release = CASE WHEN excluded.first_seen < first_seen THEN excluded.first_release ELSE first_release END,
    first_seen = least(first_seen, excluded.first_seen),
    last_seen = greatest(last_seen, excluded.last_seen)`, args); err != nil {
			return nil, err
		}
		if !known[k] {
			f := g.first
			news = append(news, analytics.NewError{Site: g.site, Group: groupHex(g.grp), Type: f.Error.Type, Message: f.Name, Path: f.Path, Release: f.Release})
		}
	}
	return news, nil
}

// knownGroups returns which of the groups (site, fingerprint) exist.
func knownGroups(ctx context.Context, qr driver.QueryerContext, groups [][2]uint64) (map[[2]uint64]bool, error) {
	known := map[[2]uint64]bool{}
	for chunk := range slices.Chunk(groups, 200) {
		var b strings.Builder
		args := make([]driver.NamedValue, 0, 2*len(chunk))
		b.WriteString("SELECT site_id, grp FROM error_groups WHERE (site_id, grp) IN (")
		for i, k := range chunk {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, "($%d::UINTEGER, $%d::UBIGINT)", 2*i+1, 2*i+2)
			args = append(args, driver.NamedValue{Ordinal: 2*i + 1, Value: uint32(k[0])}, driver.NamedValue{Ordinal: 2*i + 2, Value: k[1]})
		}
		b.WriteByte(')')
		rows, err := qr.QueryContext(ctx, b.String(), args)
		if err != nil {
			return nil, err
		}
		dest := make([]driver.Value, 2)
		for rows.Next(dest) == nil {
			site, _ := dest[0].(uint32)
			grp, _ := dest[1].(uint64)
			known[[2]uint64{uint64(site), grp}] = true
		}
		_ = rows.Close()
	}
	return known, nil
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
// frequent first, each with its series when q asks for one.
func (s *Store) Errors(ctx context.Context, q analytics.ErrorsQuery) ([]analytics.ErrorGroup, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	var p params
	w, err := errorWhere(&p, q.Query)
	if err != nil {
		return nil, err
	}
	width, err := bucketOf(q.Bucket, q.Query)
	if err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 || limit > analytics.MaxErrorGroups {
		limit = analytics.MaxErrorGroups
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.grp, g.type, g.message, g.frame, e.n, e.sessions, e.visitors,
       epoch_ms(g.first_seen), epoch_ms(g.last_seen), g.first_release
FROM (
    SELECT grp, count(*) AS n, count(DISTINCT session_id) FILTER (WHERE session_id <> 0) AS sessions,
           approx_count_distinct(visitor_id) AS visitors
    FROM errors WHERE `+w+` GROUP BY grp
) e JOIN error_groups g ON g.site_id = `+p.add(uint32(q.Site))+` AND g.grp = e.grp
ORDER BY e.n DESC, g.last_seen DESC LIMIT `+strconv.Itoa(limit), p...)
	if err != nil {
		return nil, err
	}
	out := []analytics.ErrorGroup{}
	index := map[uint64]int{}
	for rows.Next() {
		var g analytics.ErrorGroup
		var grp uint64
		var rel uint32
		if err := rows.Scan(&grp, &g.Type, &g.Message, &g.Frame, &g.Count, &g.Sessions, &g.Visitors, &g.FirstSeen, &g.LastSeen, &rel); err != nil {
			_ = rows.Close()
			return nil, err
		}
		g.Group, g.FirstRelease = groupHex(grp), int64(rel)
		index[grp] = len(out)
		out = append(out, g)
	}
	if err := rows.Close(); err != nil || width == "" || len(out) == 0 {
		return out, err
	}
	var sp params
	sw, _ := errorWhere(&sp, q.Query)
	series, err := s.db.QueryContext(ctx, `SELECT grp, epoch_ms(time_bucket(`+width+`, ts)) AS b, count(*)
FROM errors WHERE `+sw+` GROUP BY grp, b ORDER BY b`, sp...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = series.Close() }()
	for series.Next() {
		var grp uint64
		var pt analytics.CountPoint
		if err := series.Scan(&grp, &pt.T, &pt.Count); err != nil {
			return nil, err
		}
		if i, ok := index[grp]; ok {
			out[i].Series = append(out[i].Series, pt)
		}
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
	w += " AND grp = " + p.add(grp)
	d := analytics.ErrorDetail{Pages: []analytics.Row{}, Browsers: []analytics.Row{}, OS: []analytics.Row{}, Devices: []analytics.Row{},
		Releases: []analytics.Row{}, Recent: []analytics.ErrorOccurrence{}}
	var rel uint32
	err = s.db.QueryRowContext(ctx, `SELECT type, message, frame, epoch_ms(first_seen), epoch_ms(last_seen), first_release
FROM error_groups WHERE site_id = $1 AND grp = $2`, uint32(q.Site), grp).Scan(&d.Type, &d.Message, &d.Frame, &d.FirstSeen, &d.LastSeen, &rel)
	if errors.Is(err, sql.ErrNoRows) {
		return d, badQuery("this site has no error group %s", q.Group)
	}
	if err != nil {
		return d, err
	}
	d.Group, d.FirstRelease = q.Group, int64(rel)
	var stack sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT count(*), count(DISTINCT session_id) FILTER (WHERE session_id <> 0),
       approx_count_distinct(visitor_id), arg_max(stack, ts)
FROM errors WHERE `+w, p...).Scan(&d.Count, &d.Sessions, &d.Visitors, &stack); err != nil {
		return d, err
	}
	d.Stack = stack.String
	for _, dim := range errorBreakdowns {
		rows, err := s.db.QueryContext(ctx, `SELECT `+errorColumns[dim]+` AS v, approx_count_distinct(visitor_id), count(*) AS n
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
	rows, err := s.db.QueryContext(ctx, `SELECT epoch_ms(ts), path, browser, browser_version, os, device, country, service_id, release_id, session_id
FROM errors WHERE `+w+` ORDER BY ts DESC LIMIT `+strconv.Itoa(analytics.MaxErrorRecent), p...)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var o analytics.ErrorOccurrence
		var svc, rel uint32
		var session uint64
		if err := rows.Scan(&o.Time, &o.Path, &o.Browser, &o.BrowserVersion, &o.OS, &o.Device, &o.Country, &svc, &rel, &session); err != nil {
			_ = rows.Close()
			return d, err
		}
		o.Service, o.Release = int64(svc), int64(rel)
		if session != 0 {
			o.Session = groupHex(session)
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

// vitalsSelect computes every vital's 75th percentile, samples, good and
// poor counts, in VitalNames' order. The percentile is of a reservoir
// sample of 8,192 values per group: memory stays bounded however many
// page loads a range holds, and it is several times faster than a
// t-digest (a year of a busy site by page: 120 ms against 510).
var vitalsSelect = func() string {
	var b strings.Builder
	for _, v := range analytics.VitalNames {
		t := analytics.VitalThresholds[v]
		c := string(v)
		fmt.Fprintf(&b, ", coalesce(reservoir_quantile(%[1]s, 0.75)::DOUBLE, -1), count(%[1]s), count(*) FILTER (WHERE %[1]s <= %[2]g), count(*) FILTER (WHERE %[1]s > %[3]g)",
			c, t.Good, t.Poor)
	}
	return b.String()
}()

// scanVitals reads a row of vitalsSelect after its leading columns.
func scanVitals(sc interface{ Scan(...any) error }, lead []any, r *analytics.VitalsRow) error {
	dest := lead
	for _, st := range []*analytics.VitalStat{&r.LCP, &r.INP, &r.CLS, &r.FCP, &r.TTFB} {
		dest = append(dest, &st.P75, &st.Samples, &st.Good, &st.Poor)
	}
	return sc.Scan(dest...)
}

// Vitals returns q's Web Vitals: over all its page loads, by its
// dimension's values (the most loads first) and over time.
func (s *Store) Vitals(ctx context.Context, q analytics.VitalsQuery) (analytics.VitalsResult, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	out := analytics.VitalsResult{Rows: []analytics.VitalsRow{}, Series: []analytics.VitalsPoint{}}
	var p params
	w, err := errorWhere(&p, q.Query)
	if err != nil {
		return out, err
	}
	width, err := bucketOf(q.Bucket, q.Query)
	if err != nil {
		return out, err
	}
	col := ""
	if q.Dimension != "" {
		var ok bool
		if col, ok = errorColumns[q.Dimension]; !ok {
			return out, badQuery("Web Vitals can't be broken down by %s; use page, hostname, country, browser, browser_version, os, device, service or release", q.Dimension)
		}
	}
	if err := scanVitals(s.db.QueryRowContext(ctx, `SELECT count(*)`+vitalsSelect+` FROM vitals WHERE `+w, p...), []any{&out.All.Loads}, &out.All); err != nil {
		return out, err
	}
	if col != "" {
		limit := q.Limit
		if limit <= 0 || limit > analytics.MaxBreakdown {
			limit = 100
		}
		// Percentiles only for the values shown: the top values by
		// page loads first, then their vitals.
		rows, err := s.db.QueryContext(ctx, `SELECT `+col+` AS v, count(*) AS n`+vitalsSelect+` FROM vitals WHERE `+w+
			` AND `+col+` IN (SELECT `+col+` FROM vitals WHERE `+w+` GROUP BY 1 ORDER BY count(*) DESC, 1 LIMIT `+strconv.Itoa(limit)+`)
GROUP BY v ORDER BY n DESC, v`, p...)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var r analytics.VitalsRow
			if err := scanVitals(rows, []any{&r.Value, &r.Loads}, &r); err != nil {
				_ = rows.Close()
				return out, err
			}
			out.Rows = append(out.Rows, r)
		}
		if err := rows.Close(); err != nil {
			return out, err
		}
	}
	if width == "" {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT epoch_ms(time_bucket(`+width+`, ts)) AS b,
       coalesce(reservoir_quantile(lcp, 0.75)::DOUBLE, -1), coalesce(reservoir_quantile(inp, 0.75)::DOUBLE, -1), coalesce(reservoir_quantile(cls, 0.75)::DOUBLE, -1),
       coalesce(reservoir_quantile(fcp, 0.75)::DOUBLE, -1), coalesce(reservoir_quantile(ttfb, 0.75)::DOUBLE, -1)
FROM vitals WHERE `+w+` GROUP BY b ORDER BY b`, p...)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var pt analytics.VitalsPoint
		if err := rows.Scan(&pt.T, &pt.LCP, &pt.INP, &pt.CLS, &pt.FCP, &pt.TTFB); err != nil {
			return out, err
		}
		out.Series = append(out.Series, pt)
	}
	return out, rows.Err()
}

// pruneErrors deletes a site's errors before before and the groups not
// seen since; and its vitals before vitalsBefore, a step at a time from
// the oldest like its events.
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
	_, err = s.db.ExecContext(ctx, "DELETE FROM error_groups WHERE site_id = $1 AND last_seen < $2", uint32(site), before)
	return errs, err
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
