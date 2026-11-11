// Package store keeps Dock Analytics' events in DuckDB (ADR 0037) and
// answers the dashboards' queries over them.
//
// One table holds every event as a row, in arrival order. DuckDB keeps it
// in columns, compressed, with min/max zone maps per row group, so a
// query over a site and a time range reads only the columns it uses and
// skips row groups outside the range. There are no secondary indexes:
// they would slow every insert and the zone maps already cover time.
//
// Writes come from one writer, the control plane's forwarder, in batches
// (Write). Each batch carries its source's position, saved in the same
// transaction as the rows, so a batch that is sent again after a crash or
// a lost acknowledgement is skipped instead of counted twice.
package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// Options tune DuckDB for a small container.
type Options struct {
	// MemoryLimit is DuckDB's memory_limit in bytes: what its buffer pool
	// and operators may use before spilling to TempDir. Keep it well below
	// the container's limit; Go and the HTTP server need the rest.
	MemoryLimit int64
	// Threads is how many threads a query may use.
	Threads int
	// TempDir is where queries spill when they need more than
	// MemoryLimit. Empty is next to the database.
	TempDir string
}

// Store is the analytics database. It is safe for concurrent use: queries
// run in parallel, writes one at a time.
type Store struct {
	// path is the database file, "" in memory.
	path      string
	db        *sql.DB
	connector *duckdb.Connector
	// memoryLimit is DuckDB's memory_limit setting, which Trim restores.
	memoryLimit string

	// gate lets everything that uses the database run together (read
	// locked), and Trim alone (locked): DuckDB's low memory limit during
	// a trim would fail anything else.
	gate sync.RWMutex

	// copying is set while a Snapshot streams the file: a trim then does
	// nothing, since its checkpoint could change the file.
	copying atomic.Bool

	// writeMu serializes Write: one appender, one transaction at a time,
	// the sessions map and the rollup state below.
	writeMu  sync.Mutex
	sessions *sessions
	// staleGen counts the times a write marked a day stale, for days
	// waiting to be rolled up again (buildDay).
	staleGen map[siteDay]int
	// lastWriteDay is the UTC day of the last write (kickRollups).
	lastWriteDay time.Time
	// kick wakes RunRollups.
	kick chan struct{}
	// known are the error groups the database has (errors.go), and
	// newGroups those the open write adds once it commits.
	known     map[groupKey]struct{}
	newGroups []groupKey

	// rollMu guards rolledTo: every site is rolled up (but for stale
	// days) up to that day.
	rollMu   sync.Mutex
	rolledTo time.Time

	now func() time.Time
}

// Open opens (creating if needed) the database at path, first putting a
// database Receive wrote in its place. An empty path is an in-memory
// database, for tests.
func Open(ctx context.Context, path string, opts Options) (*Store, error) {
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		if err := applyReceived(path); err != nil {
			return nil, err
		}
	}
	connector, memoryLimit, err := connect(path, opts)
	if err != nil {
		return nil, fmt.Errorf("open analytics database: %w", err)
	}
	db := sql.OpenDB(connector)
	s := &Store{
		path: path, db: db, connector: connector, memoryLimit: memoryLimit, sessions: newSessions(),
		staleGen: map[siteDay]int{}, kick: make(chan struct{}, 1), now: time.Now,
	}
	if err := s.migrate(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	if err := s.sessions.prime(ctx, db); err != nil {
		_ = s.Close()
		return nil, err
	}
	if err := s.loadRollupState(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	if err := s.loadGroups(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// connect makes the DuckDB connector for the database at path ("" in
// memory) with opts' settings, and answers its memory_limit setting ("" for
// DuckDB's default), which trims restore. The analytics store and the trace
// store share it.
func connect(path string, opts Options) (*duckdb.Connector, string, error) {
	if opts.Threads <= 0 {
		opts.Threads = 2
	}
	if path != "" && opts.TempDir == "" {
		opts.TempDir = path + ".tmp"
	}
	// Database-wide settings go in the DSN: they are set once, when
	// DuckDB opens the file, not per connection.
	cfg := url.Values{
		"threads": {strconv.Itoa(opts.Threads)},
		// Dock never loads extensions; nothing may fetch one.
		"autoinstall_known_extensions": {"false"},
		"autoload_known_extensions":    {"false"},
		// Every query orders its result itself; without the guarantee
		// DuckDB needs less memory for big scans.
		"preserve_insertion_order": {"false"},
	}
	memoryLimit := ""
	if opts.MemoryLimit > 0 {
		memoryLimit = strconv.FormatInt(opts.MemoryLimit>>20, 10) + "MB"
		cfg.Set("memory_limit", memoryLimit)
	}
	if opts.TempDir != "" {
		cfg.Set("temp_directory", opts.TempDir)
	}
	c, err := duckdb.NewConnector(path+"?"+cfg.Encode(), nil)
	return c, memoryLimit, err
}

// Close checkpoints and closes the database.
func (s *Store) Close() error {
	_, _ = s.db.Exec("CHECKPOINT")
	err := s.db.Close()
	return errors.Join(err, s.connector.Close())
}

// schema is the database's layout. Columns are in the order Write appends
// them. Low-cardinality strings (browsers, countries, hostnames) cost
// little: DuckDB dictionary-encodes them per row group.
const schema = `
CREATE TABLE IF NOT EXISTS events (
    ts              TIMESTAMP NOT NULL,
    site_id         UINTEGER NOT NULL,
    service_id      UINTEGER NOT NULL,
    release_id      UINTEGER NOT NULL,
    hostname        VARCHAR NOT NULL,
    kind            UTINYINT NOT NULL,
    name            VARCHAR NOT NULL,
    path            VARCHAR NOT NULL,
    referrer        VARCHAR NOT NULL,
    channel         VARCHAR NOT NULL,
    utm_source      VARCHAR NOT NULL,
    utm_medium      VARCHAR NOT NULL,
    utm_campaign    VARCHAR NOT NULL,
    utm_term        VARCHAR NOT NULL,
    utm_content     VARCHAR NOT NULL,
    visitor_id      UBIGINT NOT NULL,
    session_id      UBIGINT NOT NULL,
    country         VARCHAR NOT NULL,
    region          VARCHAR NOT NULL,
    city            VARCHAR NOT NULL,
    browser         VARCHAR NOT NULL,
    browser_version VARCHAR NOT NULL,
    os              VARCHAR NOT NULL,
    os_version      VARCHAR NOT NULL,
    device          VARCHAR NOT NULL,
    screen_w        USMALLINT NOT NULL,
    screen_h        USMALLINT NOT NULL,
    language        VARCHAR NOT NULL,
    props           VARCHAR NOT NULL,
    -- Session marks (sessions.go): the session's first and second page
    -- views, and how much the event lengthened the session.
    entry           BOOLEAN NOT NULL,
    engaged         BOOLEAN NOT NULL,
    grew_ms         UINTEGER NOT NULL,
    -- When the session began: a range counts the marks of the sessions
    -- that began in it.
    session_start   TIMESTAMP NOT NULL
);
-- Where each source's events have been written up to (Write).
CREATE TABLE IF NOT EXISTS cursors (
    source   VARCHAR PRIMARY KEY,
    position UBIGINT NOT NULL
);
`

// migrate creates the tables.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schema+rollupSchema+errorsSchema+replaySchema); err != nil {
		return fmt.Errorf("create analytics tables: %w", err)
	}
	return nil
}

// Location is where an event came from, looked up from its IP prefix.
type Location struct {
	Country, Region, City string
}

// Locator looks up an IP prefix's location. The store calls it for every
// event it writes and never keeps the prefix.
type Locator func(ipPrefix string) Location

// Write appends events from source, a spool whose events up to position
// are now all written. A batch whose position isn't past the source's
// cursor was written before and is skipped (sent again after a crash or a
// lost acknowledgement); the result says how many events were written.
// locate may be nil (no locations). Errors and Web Vitals go to their own
// tables (errors.go); news are the error groups seen for the first time.
func (s *Store) Write(ctx context.Context, source string, position uint64, events []analytics.Event, locate Locator) (written int, news []analytics.NewError, err error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = conn.Close() }()

	var stale bool
	regular, errs, vitals := splitKinds(events)
	now := s.now()
	err = conn.Raw(func(dc any) error {
		c := dc.(driver.Conn)
		ex := dc.(driver.ExecerContext)
		qr := dc.(driver.QueryerContext)
		if _, err := ex.ExecContext(ctx, "BEGIN TRANSACTION", nil); err != nil {
			return err
		}
		committed := false
		s.sessions.begin()
		s.newGroups = nil
		defer func() {
			if !committed {
				_, _ = ex.ExecContext(context.WithoutCancel(ctx), "ROLLBACK", nil)
				s.sessions.rollback()
			}
		}()
		if source != "" {
			cur, err := cursorOf(ctx, qr, source)
			if err != nil {
				return err
			}
			if position <= cur {
				return nil // written before
			}
		}
		if len(regular) > 0 {
			if err := s.append(c, regular, locate); err != nil {
				return err
			}
			var err error
			if stale, err = s.markStale(ctx, ex, regular, now); err != nil {
				return err
			}
		}
		// After the events: an error or a page load's vitals belong to
		// the session their page view (in the same batch) started.
		if len(errs) > 0 {
			var err error
			if news, err = s.appendErrors(ctx, c, ex, errs, locate); err != nil {
				return err
			}
		}
		if len(vitals) > 0 {
			if err := appendVitals(c, vitals, locate); err != nil {
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
		s.sessions.commit()
		for _, k := range s.newGroups {
			s.known[k] = struct{}{}
		}
		written = len(events)
		return nil
	})
	if err != nil {
		// A stale mark counted for nothing only costs a rebuild.
		return 0, nil, fmt.Errorf("write analytics events: %w", err)
	}
	if len(regular) > 0 && written > 0 {
		s.kickRollups(now, stale)
	}
	return written, news, nil
}

// cursorOf reads source's position, 0 for a new source.
func cursorOf(ctx context.Context, qr driver.QueryerContext, source string) (uint64, error) {
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

// Cursor returns how far source's events have been written: the position
// of its last batch, 0 if none.
func (s *Store) Cursor(ctx context.Context, source string) (uint64, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	var pos uint64
	err := s.db.QueryRowContext(ctx, "SELECT position FROM cursors WHERE source = $1", source).Scan(&pos)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return pos, err
}

// append writes events with DuckDB's appender, in the open transaction on
// c, assigning sessions and locations.
func (s *Store) append(c driver.Conn, events []analytics.Event, locate Locator) error {
	a, err := duckdb.NewAppenderFromConn(c, "", "events")
	if err != nil {
		return err
	}
	for i := range events {
		e := &events[i]
		var loc Location
		if locate != nil && e.IPPrefix != "" {
			loc = locate(e.IPPrefix)
		}
		ts := time.UnixMilli(e.Time).UTC()
		m := s.sessions.assign(e.Site, e.Visitor, e.Time, e.Kind == analytics.KindPageview, source{
			referrer: e.Referrer, channel: e.Channel, utmSource: e.UTMSource, utmMedium: e.UTMMedium,
			utmCampaign: e.UTMCampaign, utmTerm: e.UTMTerm, utmContent: e.UTMContent,
		})
		src := m.src
		err := a.AppendRow(ts, uint32(e.Site), uint32(e.Service), uint32(e.Release), e.Hostname, uint8(e.Kind),
			e.Name, e.Path, src.referrer, src.channel, src.utmSource, src.utmMedium, src.utmCampaign, src.utmTerm, src.utmContent,
			e.Visitor, m.session, loc.Country, loc.Region, loc.City,
			e.Browser, e.BrowserVersion, e.OS, e.OSVersion, e.Device, e.ScreenW, e.ScreenH, e.Language, e.Props,
			m.entry, m.engaged, uint32(min(m.grew, 1<<31)), time.UnixMilli(m.start).UTC())
		if err != nil {
			_ = a.Close()
			return err
		}
	}
	return a.Close() // flushes
}

// Stats are the database's size, for health and the console.
type Stats struct {
	Events    int64
	FileBytes int64
	// MemoryBytes is what DuckDB's buffer manager holds now.
	MemoryBytes int64
}

// Stats counts the events and measures the database file.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	var st Stats
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM events").Scan(&st.Events); err != nil {
		return st, err
	}
	if err := s.db.QueryRowContext(ctx, "SELECT coalesce(sum(memory_usage_bytes), 0)::BIGINT FROM duckdb_memory()").Scan(&st.MemoryBytes); err != nil {
		return st, err
	}
	var path string
	if err := s.db.QueryRowContext(ctx, "SELECT coalesce(path, '') FROM duckdb_databases() WHERE database_name = current_database()").Scan(&path); err == nil && path != "" {
		if fi, err := os.Stat(path); err == nil {
			st.FileBytes = fi.Size()
		}
	}
	return st, nil
}

// Trim gives DuckDB's cached memory back. Its buffer pool keeps the
// blocks queries read, up to memory_limit, and DuckDB holds on to memory
// its operators freed; together a few busy queries leave the process at
// several times memory_limit. Lowering the limit for a moment makes it
// release both (the blocks are still in the operating system's page
// cache, which doesn't count against the container). It checkpoints
// first: written rows stay in memory until they are in the file. Trim
// waits for whatever runs and holds everything else off meanwhile.
func (s *Store) Trim(ctx context.Context) error {
	s.gate.Lock()
	defer s.gate.Unlock()
	if s.copying.Load() {
		return nil
	}
	return s.trim(ctx)
}

// TryTrim trims if nothing is running, and reports whether it did.
func (s *Store) TryTrim(ctx context.Context) (bool, error) {
	if !s.gate.TryLock() {
		return false, nil
	}
	defer s.gate.Unlock()
	if s.copying.Load() {
		return false, nil
	}
	return true, s.trim(ctx)
}

// trim is Trim with the gate held.
func (s *Store) trim(ctx context.Context) error {
	return trimDB(ctx, s.db, s.memoryLimit)
}

// trimDB checkpoints db and lowers its memory_limit for a moment, then
// sets it back to memoryLimit ("" leaves it alone), so DuckDB lets go of
// its cached blocks and freed operator memory (Store.Trim).
func trimDB(ctx context.Context, db *sql.DB, memoryLimit string) error {
	if _, err := db.ExecContext(ctx, "CHECKPOINT"); err != nil {
		return err
	}
	if memoryLimit == "" {
		return nil
	}
	var err error
	// Blocks a transaction still pins can't go; settle for what can.
	for _, floor := range []string{"16MB", "64MB"} {
		if _, err = db.ExecContext(ctx, "SET memory_limit = '"+floor+"'"); err == nil {
			break
		}
	}
	if _, rerr := db.ExecContext(ctx, "SET memory_limit = '"+memoryLimit+"'"); rerr != nil {
		return rerr
	}
	return err
}

// Checkpoint writes the write-ahead log into the database file, so the
// file alone is a consistent copy (backups, ADR 0019).
func (s *Store) Checkpoint(ctx context.Context) error {
	s.gate.RLock()
	defer s.gate.RUnlock()
	_, err := s.db.ExecContext(ctx, "CHECKPOINT")
	return err
}
