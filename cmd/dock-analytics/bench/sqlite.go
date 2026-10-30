package bench

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // the comparison engine, Dock's own SQLite

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// SQLite is the same table and queries on SQLite (modernc, as Dock links
// it), with an index on (site_id, ts): the comparison ADR 0037 records
// once, so the choice of DuckDB rests on numbers.
type SQLite struct{ db *sql.DB }

// OpenSQLite opens (creating) a comparison database at path.
func OpenSQLite(path string) (*SQLite, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-65536)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	return &SQLite{db: db}, nil
}

// Close closes the database.
func (s *SQLite) Close() error { return s.db.Close() }

// SQLiteColumns are the events table's columns, as DuckDB's.
const SQLiteColumns = `ts, site_id, service_id, release_id, hostname, kind, name, path, referrer, channel,
utm_source, utm_medium, utm_campaign, utm_term, utm_content, visitor_id, session_id, country, region, city,
browser, browser_version, os, os_version, device, screen_w, screen_h, language, props, entry, engaged, grew_ms`

// CopyFrom fills the comparison database with the rows of a seeded DuckDB
// database, read through rows (SELECT of SQLiteColumns, ts as Unix ms),
// so both engines answer over the same events and session marks.
func (s *SQLite) CopyFrom(ctx context.Context, rows *sql.Rows) (int, error) {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS events (ts INTEGER, site_id INTEGER, service_id INTEGER, release_id INTEGER,
hostname TEXT, kind INTEGER, name TEXT, path TEXT, referrer TEXT, channel TEXT, utm_source TEXT, utm_medium TEXT,
utm_campaign TEXT, utm_term TEXT, utm_content TEXT, visitor_id INTEGER, session_id INTEGER, country TEXT, region TEXT,
city TEXT, browser TEXT, browser_version TEXT, os TEXT, os_version TEXT, device TEXT, screen_w INTEGER, screen_h INTEGER,
language TEXT, props TEXT, entry INTEGER, engaged INTEGER, grew_ms INTEGER)`,
	}
	for _, q := range stmts {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return 0, err
		}
	}
	cols := strings.Count(SQLiteColumns, ",") + 1
	vals := make([]any, cols)
	ptrs := make([]any, cols)
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	insert := "INSERT INTO events (" + SQLiteColumns + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?,", cols), ",") + ")"
	n := 0
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	st, err := tx.PrepareContext(ctx, insert)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return n, err
		}
		// SQLite's INTEGER is signed: visitor and session IDs go in as
		// their bit patterns.
		for i, v := range vals {
			if u, ok := v.(uint64); ok {
				vals[i] = int64(u)
			}
		}
		if _, err := st.ExecContext(ctx, vals...); err != nil {
			return n, err
		}
		if n++; n%100_000 == 0 {
			if err := tx.Commit(); err != nil {
				return n, err
			}
			if tx, err = s.db.BeginTx(ctx, nil); err != nil {
				return n, err
			}
			if st, err = tx.PrepareContext(ctx, insert); err != nil {
				return n, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return n, err
	}
	if _, err := s.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS events_site_ts ON events (site_id, ts)"); err != nil {
		return n, err
	}
	_, err = s.db.ExecContext(ctx, "ANALYZE")
	return n, err
}

// sqliteExpr is each dimension's column in SQLite, and the kind it is
// about.
var sqliteExpr = map[analytics.Dimension]struct {
	expr string
	kind int
}{
	analytics.DimPage: {"path", 1}, analytics.DimReferrer: {"referrer", 0}, analytics.DimChannel: {"channel", 0},
	analytics.DimUTMSource: {"utm_source", 0}, analytics.DimCountry: {"country", 0}, analytics.DimBrowser: {"browser", 0},
	analytics.DimOS: {"os", 0}, analytics.DimDevice: {"device", 0}, analytics.DimEvent: {"name", 2},
	analytics.DimService: {"CAST(service_id AS TEXT)", 0},
}

// where is SQLite's WHERE clause for q.
func (s *SQLite) where(q analytics.Query) (string, []any) {
	args := []any{q.Site, q.From, q.To}
	w := "site_id = +? AND ts >= ? AND ts < ?"
	for _, f := range q.Filters {
		switch {
		case f.Dimension == analytics.DimEntryPage:
			w += " AND session_id IN (SELECT session_id FROM events WHERE site_id = +? AND ts >= ? AND ts < ? AND entry AND path = ?)"
			args = append(args, q.Site, q.From, q.To, f.Value)
		case f.Op == analytics.OpContains:
			c := sqliteExpr[f.Dimension]
			w += " AND " + c.expr + " LIKE ?"
			args = append(args, "%"+f.Value+"%")
		default:
			c := sqliteExpr[f.Dimension]
			if c.kind != 0 {
				w += " AND kind = " + strconv.Itoa(c.kind)
			}
			w += " AND " + c.expr + " = ?"
			args = append(args, f.Value)
		}
	}
	return w, args
}

// Overview implements Querier, with the session marks as DuckDB's fast
// path uses them.
func (s *SQLite) Overview(ctx context.Context, q analytics.Query) (analytics.Overview, error) {
	w, args := s.where(q)
	var o analytics.Overview
	var engaged, grew int64
	err := s.db.QueryRowContext(ctx, `SELECT count(DISTINCT visitor_id), coalesce(sum(kind = 1), 0), coalesce(sum(entry), 0), coalesce(sum(engaged), 0), coalesce(sum(grew_ms), 0), coalesce(sum(kind <> 1), 0)
FROM events WHERE `+w, args...).Scan(&o.Visitors, &o.Pageviews, &o.Sessions, &engaged, &grew, &o.Events)
	if o.Sessions > 0 {
		o.BounceRate = 1 - float64(engaged)/float64(o.Sessions)
		o.Duration = grew / o.Sessions
	}
	return o, err
}

// Series implements Querier.
func (s *SQLite) Series(ctx context.Context, q analytics.SeriesQuery) ([]analytics.Point, error) {
	step := map[analytics.Bucket]int64{analytics.BucketMinute: 60_000, analytics.BucketHour: 3_600_000, analytics.BucketDay: 86_400_000}[q.Bucket]
	w, args := s.where(q.Query)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT ts / %d * %d AS b, count(DISTINCT visitor_id), sum(kind = 1)
FROM events WHERE %s GROUP BY b ORDER BY b`, step, step, w), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []analytics.Point
	for rows.Next() {
		var p analytics.Point
		if err := rows.Scan(&p.T, &p.Visitors, &p.Pageviews); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Breakdown implements Querier.
func (s *SQLite) Breakdown(ctx context.Context, q analytics.BreakdownQuery) ([]analytics.Row, error) {
	w, args := s.where(q.Query)
	var query string
	if q.Dimension == analytics.DimEntryPage {
		query = `SELECT path AS v, count(DISTINCT visitor_id) AS visitors, count(*) AS n FROM events WHERE ` + w +
			` AND entry GROUP BY v ORDER BY visitors DESC, n DESC, v LIMIT ` + strconv.Itoa(q.Limit)
	} else {
		c, ok := sqliteExpr[q.Dimension]
		if !ok {
			return nil, fmt.Errorf("the comparison has no %s", q.Dimension)
		}
		kind := ""
		if c.kind != 0 {
			kind = " AND kind = " + strconv.Itoa(c.kind)
		}
		query = `SELECT ` + c.expr + ` AS v, count(DISTINCT visitor_id) AS visitors, count(*) AS n FROM events WHERE ` + w + kind +
			` GROUP BY v ORDER BY visitors DESC, n DESC, v LIMIT ` + strconv.Itoa(q.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []analytics.Row
	for rows.Next() {
		var r analytics.Row
		if err := rows.Scan(&r.Value, &r.Visitors, &r.Count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
