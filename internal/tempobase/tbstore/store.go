// Package tbstore is tempoBase's SQLite state (ADR 0024 section 2):
// connection setup, embedded migrations and sqlc-generated typed queries.
// It is set up like Dock's internal/store (WAL, immediate write
// transactions, every query prepared once) but has its own schema, so the
// tempobase binary never links Dock's.
//
// Queries live in queries/*.sql and the schema in migrations/*.sql. After
// changing either, run `go generate ./internal/tempobase/tbstore` and
// commit the output.
package tbstore

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
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is an open tempoBase database.
type Store struct {
	*Queries
	DB *sql.DB
}

// Open opens (creating if needed) the database at path and applies any
// pending migrations. WAL lets `tempobase admin` write while the server
// runs.
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
	// Four connections, all kept open: reopening one reruns the pragmas
	// and re-prepares every statement on it, and each holds its schema,
	// page cache and statements in SQLite's C heap, which modernc never
	// gives back. As Dock's store (internal/store poolSize).
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	// A new file switches to WAL on its first connection. Another process
	// opening it at that moment can get SQLITE_BUSY without waiting
	// (SQLite doesn't wait where it could deadlock), so opening retries
	// briefly, at start only.
	for attempt := 1; ; attempt++ {
		err = migrate(ctx, db)
		if !isBusy(err) || attempt == 10 {
			break
		}
		time.Sleep(time.Duration(attempt) * 20 * time.Millisecond)
	}
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	queries, err := Prepare(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("prepare queries: %w", err)
	}
	return &Store{Queries: queries, DB: db}, nil
}

// Close closes the prepared statements and the database.
func (s *Store) Close() error { return errors.Join(s.Queries.Close(), s.DB.Close()) }

// Tx runs fn in a write transaction, committing if fn returns nil.
func (s *Store) Tx(ctx context.Context, fn func(q *Queries) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := fn(s.WithTx(tx)); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Backup writes a consistent copy of the database to path (VACUUM INTO),
// which must not exist yet. It runs while the server serves.
func (s *Store) Backup(ctx context.Context, path string) error {
	_, err := s.DB.ExecContext(ctx, "VACUUM INTO ?", path)
	return err
}

// IsUnique reports whether err is a UNIQUE or PRIMARY KEY constraint violation.
func IsUnique(err error) bool {
	se, ok := errors.AsType[*sqlite.Error](err)
	return ok && (se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || se.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY)
}

// isBusy reports whether err is SQLITE_BUSY, in any of its variants.
func isBusy(err error) bool {
	se, ok := errors.AsType[*sqlite.Error](err)
	return ok && se.Code()&0xff == sqlite3.SQLITE_BUSY
}

// IsNotFound reports whether err means a :one query matched no row.
func IsNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// migrate applies migrations/NNNN_*.sql files newer than PRAGMA
// user_version, each in its own transaction. Like Dock's store, each
// transaction takes the write lock first and re-reads the version under
// it, so a migration another process applied meanwhile is skipped.
func migrate(ctx context.Context, db *sql.DB) error {
	var current int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
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
		if current, err = apply(ctx, db, base, version, string(body)); err != nil {
			return err
		}
	}
	return nil
}

// apply runs one migration and sets user_version to version, unless the
// database is already at it; it returns the version the database is at.
func apply(ctx context.Context, db *sql.DB, name string, version int, body string) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var current int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	if version <= current {
		return current, nil
	}
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return 0, fmt.Errorf("migration %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("migration %s: %w", name, err)
	}
	return version, nil
}
