// Package store is the platform's SQLite state: connection setup, embedded
// migrations and sqlc-generated typed queries (Queries).
//
// Queries live in queries/*.sql and the schema in migrations/*.sql. After
// changing either, run `go generate ./internal/store` and commit the output.
package store

//go:generate sqlc generate

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

//go:embed migrations/*.sql
var migrations embed.FS

// poolSize is how many SQLite connections the store opens, all kept open
// (the default keeps 2 idle): reopening one reruns the pragmas and
// re-prepares the statements used on it. Each holds its own schema copy,
// page cache and prepared statements in SQLite's C heap, which modernc
// never gives back, so the idle daemon pays for every connection start-up
// happened to use: 23-29 MB with 8, a steady 24-25 with 4
// (docs/performance.md). The cost: a transaction waiting for the write
// lock holds a connection, so with poolSize writers queued, reads wait
// too. Writes are short; busy_timeout bounds the wait.
const poolSize = 4

// Store is an open platform database.
type Store struct {
	*Queries
	DB    *sql.DB
	stmts *stmts
}

// Open opens (creating if needed) the SQLite database at path and applies any
// pending migrations. WAL mode lets the admin CLI write while the daemon runs.
func Open(ctx context.Context, path string) (*Store, error) {
	q := url.Values{}
	for _, p := range []string{"foreign_keys(1)", "journal_mode(WAL)", "busy_timeout(10000)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	// Writers take the lock up front, so read-then-write transactions never
	// fail with SQLITE_BUSY halfway through.
	q.Set("_txlock", "immediate")
	q.Set("_time_format", "sqlite")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(poolSize)
	db.SetMaxIdleConns(poolSize)
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Every query is a prepared statement, prepared on first use (stmts) so
	// SQLite doesn't parse its SQL on each call; transactions reuse them
	// (Tx). Only after migrating, since statements bind to the schema.
	st := newStmts(db)
	return &Store{Queries: New(st), DB: db, stmts: st}, nil
}

// Close closes the prepared statements and the database.
func (s *Store) Close() error { return errors.Join(s.stmts.Close(), s.DB.Close()) }

// Tx runs fn in a write transaction, committing if fn returns nil.
func (s *Store) Tx(ctx context.Context, fn func(q *Queries) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := fn(New(txStmts{tx: tx, s: s.stmts})); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// IsUnique reports whether err is a UNIQUE or PRIMARY KEY constraint violation.
func IsUnique(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	return se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || se.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
}

// IsNotFound reports whether err means a :one query matched no row.
func IsNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// migrate applies migrations/NNNN_*.sql files newer than PRAGMA user_version,
// each in its own transaction. Several processes may migrate one file at
// once (install.sh runs dock admin commands while a new daemon starts): each
// transaction takes the write lock first (_txlock=immediate) and re-reads
// the version under it, so a migration another process applied is skipped.
func migrate(ctx context.Context, db *sql.DB) error {
	var current int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	current, err := repairRenumbered(ctx, db, current)
	if err != nil {
		return err
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		num, _, _ := strings.Cut(base, "_")
		version, err := strconv.Atoi(num)
		if err != nil {
			return fmt.Errorf("migration %s: name must start with a number", base)
		}
		if version <= current {
			continue
		}
		body, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		if err := migrateOne(ctx, db, version, string(body)); err != nil {
			return fmt.Errorf("migration %s: %w", base, err)
		}
		current = version
	}
	return nil
}

// renumbered are migrations that shared a number on main and moved: a
// database that ran the moved one under the old number, known by a table
// it made, has its version at that number and never ran the one it
// shared it with. On 2026-11-14 custom roles (#326) ran as 0109 beside
// SSO presets' 0109 (#325) and became 0110.
var renumbered = []struct {
	was, now int
	// made is a table only the moved migration creates.
	made string
	// skipped is the migration that kept the number.
	skipped string
}{
	{109, 110, "roles", "migrations/0109_sso_presets.sql"},
}

// repairRenumbered applies to such a database the migration it skipped,
// as the moved one's new number, so neither runs twice. It returns the
// version after.
func repairRenumbered(ctx context.Context, db *sql.DB, current int) (int, error) {
	for _, r := range renumbered {
		if current != r.was {
			continue
		}
		var n int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", r.made).Scan(&n); err != nil {
			return current, err
		}
		if n == 0 {
			continue
		}
		body, err := migrations.ReadFile(r.skipped)
		if err != nil {
			return current, err
		}
		if err := migrateOne(ctx, db, r.now, string(body)); err != nil {
			return current, fmt.Errorf("migration %s (skipped while %s held its number): %w", r.skipped, r.made, err)
		}
		current = r.now
	}
	return current, nil
}

// fkOffMarker starts a migration that rebuilds a table other tables
// reference. It runs with foreign keys off (SQLite's documented way to
// rebuild a table), and commits only if foreign_key_check finds nothing.
const fkOffMarker = "-- foreign_keys: off\n"

// migrateOne applies one migration on its own connection, so a
// foreign-keys-off migration doesn't change the pool's other connections.
func migrateOne(ctx context.Context, db *sql.DB, version int, body string) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	fkOff := strings.HasPrefix(body, fkOffMarker)
	if fkOff {
		// Outside the transaction: SQLite ignores it inside one.
		if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
			return err
		}
		defer func() { _, _ = conn.ExecContext(context.WithoutCancel(ctx), "PRAGMA foreign_keys = ON") }()
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var current int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version <= current {
		return nil
	}
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	if fkOff {
		if err := foreignKeyCheck(ctx, tx); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return err
	}
	return tx.Commit()
}

// foreignKeyCheck fails if any row references a missing parent.
func foreignKeyCheck(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		var table, parent string
		var rowid sql.NullInt64
		var fk int
		if err := rows.Scan(&table, &rowid, &parent, &fk); err != nil {
			return err
		}
		return fmt.Errorf("foreign key check: a %s row (rowid %d) references a missing %s", table, rowid.Int64, parent)
	}
	return rows.Err()
}
