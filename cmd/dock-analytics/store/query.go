package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// column is how a dimension reads from the events table: the SQL
// expression for its value, the event kind it is about (0: any), and
// whether it is the same on every event of a session (a visitor's
// location, browser and device, a session's source). Every SQL fragment
// here is a constant; values only ever travel as parameters.
type column struct {
	expr    string
	kind    analytics.Kind
	session bool
}

var columns = map[analytics.Dimension]column{
	analytics.DimPage:           {"path", analytics.KindPageview, false},
	analytics.DimHostname:       {"hostname", 0, false},
	analytics.DimReferrer:       {"referrer", 0, true},
	analytics.DimChannel:        {"channel", 0, true},
	analytics.DimUTMSource:      {"utm_source", 0, true},
	analytics.DimUTMMedium:      {"utm_medium", 0, true},
	analytics.DimUTMCampaign:    {"utm_campaign", 0, true},
	analytics.DimUTMTerm:        {"utm_term", 0, true},
	analytics.DimUTMContent:     {"utm_content", 0, true},
	analytics.DimCountry:        {"country", 0, true},
	analytics.DimRegion:         {"region", 0, true},
	analytics.DimCity:           {"city", 0, true},
	analytics.DimBrowser:        {"browser", 0, true},
	analytics.DimBrowserVersion: {"browser_version", 0, true},
	analytics.DimOS:             {"os", 0, true},
	analytics.DimOSVersion:      {"os_version", 0, true},
	analytics.DimDevice:         {"device", 0, true},
	analytics.DimScreen:         {"(screen_w::VARCHAR || 'x' || screen_h::VARCHAR)", 0, true},
	analytics.DimLanguage:       {"language", 0, true},
	analytics.DimEvent:          {"name", analytics.KindEvent, false},
	analytics.DimOutbound:       {"name", analytics.KindOutbound, false},
	analytics.DimService:        {"service_id::VARCHAR", 0, false},
	analytics.DimRelease:        {"release_id::VARCHAR", 0, false},
}

// The entry page is the path of the event marked entry, a session's first
// page view; the exit page is found per session at query time.
const exitPage = "arg_max(path, ts)"

// wholeSessions reports whether q's filters keep or drop whole sessions,
// so the events they keep carry every session mark: no filter, or only
// session dimensions and entry and exit pages.
func wholeSessions(q analytics.Query) bool {
	for _, f := range q.Filters {
		if f.Dimension != analytics.DimEntryPage && f.Dimension != analytics.DimExitPage && !columns[f.Dimension].session {
			return false
		}
	}
	return true
}

// QueryError is a query the store can't answer as asked: the caller's
// mistake, not the store's.
type QueryError struct{ msg string }

func (e *QueryError) Error() string { return e.msg }

// badQuery returns a QueryError.
func badQuery(format string, args ...any) error {
	return &QueryError{msg: fmt.Sprintf(format, args...)}
}

// check validates q's range and filters.
func check(q analytics.Query) error {
	if q.To <= q.From {
		return badQuery("the range is empty: from %d is not before to %d", q.From, q.To)
	}
	if len(q.Filters) > analytics.MaxFilters {
		return badQuery("at most %d filters", analytics.MaxFilters)
	}
	for _, f := range q.Filters {
		switch f.Op {
		case analytics.OpIs, analytics.OpIsNot, analytics.OpContains:
		default:
			return badQuery("unknown filter operator %q; use is, is_not or contains", f.Op)
		}
		if _, ok := columns[f.Dimension]; !ok && f.Dimension != analytics.DimEntryPage && f.Dimension != analytics.DimExitPage {
			return badQuery("unknown dimension %q", f.Dimension)
		}
	}
	return nil
}

// params collects a statement's parameters.
type params []any

// add binds v and returns its placeholder.
func (p *params) add(v any) string {
	*p = append(*p, v)
	return "$" + strconv.Itoa(len(*p))
}

// where is the WHERE clause for q's site and filters over events in
// [from, to) (Unix ms), binding its values to p. q must be checked.
func where(p *params, q analytics.Query, from, to int64) string {
	site, lo, hi := p.add(uint32(q.Site)), p.add(time.UnixMilli(from).UTC()), p.add(time.UnixMilli(to).UTC())
	scope := "site_id = " + site + " AND ts >= " + lo + " AND ts < " + hi
	var b strings.Builder
	b.WriteString(scope)
	for _, f := range q.Filters {
		var cmp string
		switch f.Op {
		case analytics.OpIs:
			cmp = " = " + p.add(f.Value)
		case analytics.OpIsNot:
			cmp = " <> " + p.add(f.Value)
		case analytics.OpContains:
			cmp = " ILIKE " + p.add("%"+escapeLike(f.Value)+"%") + " ESCAPE '\\'"
		}
		switch f.Dimension {
		case analytics.DimEntryPage:
			b.WriteString(" AND session_id IN (SELECT session_id FROM events WHERE " + scope + " AND entry AND path" + cmp + ")")
			continue
		case analytics.DimExitPage:
			fmt.Fprintf(&b, " AND session_id IN (SELECT session_id FROM events WHERE %s AND kind = %d GROUP BY session_id HAVING %s%s)",
				scope, analytics.KindPageview, exitPage, cmp)
			continue
		}
		c := columns[f.Dimension]
		if c.kind != 0 {
			fmt.Fprintf(&b, " AND kind = %d", c.kind)
		}
		b.WriteString(" AND " + c.expr + cmp)
	}
	return b.String()
}

// distinct is the SQL that counts the distinct values of col: exact for
// unfiltered queries, whose raw events are a day or two at the edges of
// rolled-up days, and HyperLogLog (about 2% off) for filtered ones, which
// read raw events over the whole range: an exact distinct count of
// millions of visitors needs memory DuckDB's limit doesn't cover.
func distinct(q analytics.Query, col string) string {
	if len(q.Filters) > 0 {
		return "approx_count_distinct(" + col + ")"
	}
	return "count(DISTINCT " + col + ")"
}

// escapeLike escapes LIKE's wildcards in s.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// plan splits q's range into rolled-up days and the raw events around
// them (rollupSpan); filtered queries read raw events only.
func (s *Store) plan(ctx context.Context, q analytics.Query) (span, error) {
	if len(q.Filters) > 0 {
		return span{raw: [][2]int64{{q.From, q.To}}}, nil
	}
	return s.rollupSpan(ctx, q.Site, q.From, q.To)
}

// Overview returns q's headline numbers. Over rolled-up days they are the
// days' totals; over raw events, when the filters keep whole sessions, one
// scan that adds up the session marks, and otherwise the events left
// grouped by session. Visitors and sessions add up across days: both
// change with the visitor ID's daily salt.
func (s *Store) Overview(ctx context.Context, q analytics.Query) (analytics.Overview, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	if err := check(q); err != nil {
		return analytics.Overview{}, err
	}
	sp, err := s.plan(ctx, q)
	if err != nil {
		return analytics.Overview{}, err
	}
	var p params
	var parts []string
	if sp.rolled() {
		parts = append(parts, `SELECT visitors, pageviews, sessions, engaged, grew_ms, events FROM rollup_totals
WHERE site_id = `+p.add(uint32(q.Site))+` AND day >= `+p.add(sp.from)+` AND day < `+p.add(sp.to))
	}
	for _, r := range sp.raw {
		w := where(&p, q, r[0], r[1])
		if wholeSessions(q) {
			// Only the sessions that began in the range: one that began
			// before it has its entry outside, so its other marks must
			// not count either.
			began := "session_start >= " + p.add(time.UnixMilli(r[0]).UTC())
			parts = append(parts, `SELECT `+distinct(q, "visitor_id")+`, count(*) FILTER (WHERE kind = 1), count(*) FILTER (WHERE entry),
       count(*) FILTER (WHERE engaged AND `+began+`), coalesce(sum(grew_ms) FILTER (WHERE `+began+`), 0), count(*) FILTER (WHERE kind <> 1)
FROM events WHERE `+w)
			continue
		}
		parts = append(parts, `SELECT `+distinct(q, "v")+`, coalesce(sum(pv), 0), count(*) FILTER (WHERE pv > 0),
       count(*) FILTER (WHERE pv > 1), coalesce(sum(dur) FILTER (WHERE pv > 0), 0), coalesce(sum(ev), 0)
FROM (
    SELECT any_value(visitor_id) AS v,
           count(*) FILTER (WHERE kind = 1) AS pv,
           count(*) FILTER (WHERE kind <> 1) AS ev,
           epoch_ms(max(ts)) - epoch_ms(min(ts)) AS dur
    FROM events WHERE `+w+`
    GROUP BY session_id
)`)
	}
	query := `SELECT coalesce(sum(v), 0), coalesce(sum(pv), 0), coalesce(sum(se), 0), coalesce(sum(en), 0), coalesce(sum(gr), 0), coalesce(sum(ev), 0)
FROM (` + strings.Join(parts, "\nUNION ALL\n") + `) AS parts (v, pv, se, en, gr, ev)`
	var o analytics.Overview
	var engaged, duration int64
	if err := s.db.QueryRowContext(ctx, query, p...).Scan(&o.Visitors, &o.Pageviews, &o.Sessions, &engaged, &duration, &o.Events); err != nil {
		return o, err
	}
	if o.Sessions > 0 {
		o.BounceRate = 1 - float64(engaged)/float64(o.Sessions)
		o.Duration = duration / o.Sessions
	}
	return o, nil
}

// bucketWidths are the SQL intervals of the buckets, and their length.
var bucketWidths = map[analytics.Bucket]struct {
	sql string
	ms  int64
}{
	analytics.BucketMinute: {"INTERVAL 1 MINUTE", 60_000},
	analytics.BucketHour:   {"INTERVAL 1 HOUR", 3_600_000},
	analytics.BucketDay:    {"INTERVAL 1 DAY", 86_400_000},
}

// maxPoints caps a series, so a minute series over a year is refused
// instead of returning half a million points.
const maxPoints = 2000

// Series returns visitors and page views per bucket, for buckets with
// events, oldest first. Buckets start at whole UTC minutes, hours or
// days; day series read rolled-up days.
func (s *Store) Series(ctx context.Context, q analytics.SeriesQuery) ([]analytics.Point, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	width, ok := bucketWidths[q.Bucket]
	if !ok {
		return nil, badQuery("unknown bucket %q; use minute, hour or day", q.Bucket)
	}
	if err := check(q.Query); err != nil {
		return nil, err
	}
	if (q.To-q.From)/width.ms > maxPoints {
		return nil, badQuery("%s buckets over this range are more than %d points; use a larger bucket", q.Bucket, maxPoints)
	}
	sp := span{raw: [][2]int64{{q.From, q.To}}}
	if q.Bucket == analytics.BucketDay {
		var err error
		if sp, err = s.plan(ctx, q.Query); err != nil {
			return nil, err
		}
	}
	var p params
	var parts []string
	if sp.rolled() {
		parts = append(parts, `SELECT epoch_ms(day::TIMESTAMP), visitors, pageviews FROM rollup_totals
WHERE site_id = `+p.add(uint32(q.Site))+` AND day >= `+p.add(sp.from)+` AND day < `+p.add(sp.to))
	}
	for _, r := range sp.raw {
		parts = append(parts, `SELECT epoch_ms(time_bucket(`+width.sql+`, ts)) AS b, `+distinct(q.Query, "visitor_id")+`, count(*) FILTER (WHERE kind = 1)
FROM events WHERE `+where(&p, q.Query, r[0], r[1])+` GROUP BY b`)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT b, sum(v)::BIGINT, sum(pv)::BIGINT FROM (`+strings.Join(parts, "\nUNION ALL\n")+`) AS parts (b, v, pv)
GROUP BY b ORDER BY b`, p...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []analytics.Point
	for rows.Next() {
		var pt analytics.Point
		if err := rows.Scan(&pt.T, &pt.Visitors, &pt.Pageviews); err != nil {
			return nil, err
		}
		out = append(out, pt)
	}
	return out, rows.Err()
}

// breakdownSQL is the raw-event part of a breakdown of q's dimension over
// the events matching w: one row per value with its visitors and count.
func breakdownSQL(q analytics.BreakdownQuery, w string) string {
	visitors := distinct(q.Query, "visitor_id")
	switch q.Dimension {
	case analytics.DimEntryPage:
		return `SELECT path, ` + visitors + `, count(*) FROM events WHERE ` + w + ` AND entry GROUP BY path`
	case analytics.DimExitPage:
		return fmt.Sprintf(`SELECT v, %s, count(*) FROM (
    SELECT %s AS v, any_value(visitor_id) AS vis FROM events WHERE %s AND kind = %d GROUP BY session_id
) GROUP BY v`, distinct(q.Query, "vis"), exitPage, w, analytics.KindPageview)
	}
	c := columns[q.Dimension]
	kind := ""
	if c.kind != 0 {
		kind = fmt.Sprintf(" AND kind = %d", c.kind)
	}
	return `SELECT ` + c.expr + `, ` + visitors + `, count(*) FROM events WHERE ` + w + kind + ` GROUP BY 1`
}

// Breakdown returns the top values of q's dimension, by visitors.
func (s *Store) Breakdown(ctx context.Context, q analytics.BreakdownQuery) ([]analytics.Row, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	if _, ok := columns[q.Dimension]; !ok && q.Dimension != analytics.DimEntryPage && q.Dimension != analytics.DimExitPage {
		return nil, badQuery("unknown dimension %q", q.Dimension)
	}
	if err := check(q.Query); err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 || limit > analytics.MaxBreakdown {
		limit = analytics.MaxBreakdown
	}
	sp, err := s.plan(ctx, q.Query)
	if err != nil {
		return nil, err
	}
	var p params
	var parts []string
	if sp.rolled() {
		parts = append(parts, `SELECT value, visitors, n FROM rollups WHERE site_id = `+p.add(uint32(q.Site))+
			` AND dim = `+p.add(string(q.Dimension))+` AND day >= `+p.add(sp.from)+` AND day < `+p.add(sp.to))
	}
	for _, r := range sp.raw {
		parts = append(parts, breakdownSQL(q, where(&p, q.Query, r[0], r[1])))
	}
	rows, err := s.db.QueryContext(ctx, `SELECT v, sum(vis)::BIGINT AS visitors, sum(n)::BIGINT AS n
FROM (`+strings.Join(parts, "\nUNION ALL\n")+`) AS parts (v, vis, n)
GROUP BY v ORDER BY visitors DESC, n DESC, v LIMIT `+strconv.Itoa(limit), p...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []analytics.Row
	for rows.Next() {
		var r analytics.Row
		var v sql.NullString
		if err := rows.Scan(&v, &r.Visitors, &r.Count); err != nil {
			return nil, err
		}
		r.Value = v.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// Live returns a site's visitors since q.Since with when each was last
// seen, and its 20 latest events: what a live view starts from.
func (s *Store) Live(ctx context.Context, q analytics.LiveQuery) (analytics.Live, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	out := analytics.Live{Visitors: []analytics.LiveVisitor{}, Recent: []analytics.Event{}}
	since := time.UnixMilli(q.Since).UTC()
	rows, err := s.db.QueryContext(ctx, `SELECT visitor_id, epoch_ms(max(ts)) FROM events WHERE site_id = $1 AND ts >= $2
GROUP BY visitor_id LIMIT `+strconv.Itoa(analytics.MaxLiveVisitors), uint32(q.Site), since)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v analytics.LiveVisitor
		if err := rows.Scan(&v.Visitor, &v.Last); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Visitors = append(out.Visitors, v)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT epoch_ms(ts), service_id, release_id, hostname, kind, name, path, referrer, country, city, browser, os, device, visitor_id
FROM events WHERE site_id = $1 AND ts >= $2 ORDER BY ts DESC LIMIT 20`, uint32(q.Site), since)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		e := analytics.Event{Site: q.Site}
		var svc, rel uint32
		var kind uint8
		var country, city string
		if err := rows.Scan(&e.Time, &svc, &rel, &e.Hostname, &kind, &e.Name, &e.Path, &e.Referrer, &country, &city, &e.Browser, &e.OS, &e.Device, &e.Visitor); err != nil {
			return out, err
		}
		e.Service, e.Release, e.Kind = int64(svc), int64(rel), analytics.Kind(kind)
		e.Country, e.City = country, city
		out.Recent = append(out.Recent, e)
	}
	return out, rows.Err()
}
