package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// SnapshotInfo describes a database snapshot.
type SnapshotInfo struct {
	// Schema is the snapshot's PRAGMA user_version: the last migration it
	// has.
	Schema int
	// InstallID is the control plane's install ID (GetInstallID).
	InstallID string
}

// Snapshot writes a consistent copy of db to dst, a path that must not
// exist yet, with VACUUM INTO: one read transaction, so writers carry on
// and the copy is the database as of one moment, never a half-copied WAL
// (ADR 0021). The copy is checked with quick_check before it is returned.
func Snapshot(ctx context.Context, db *sql.DB, dst string) (SnapshotInfo, error) {
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
		return SnapshotInfo{}, fmt.Errorf("snapshot the database: %w", err)
	}
	return inspect(ctx, dst)
}

// SnapshotFile is Snapshot for the database file at path, opened without
// running migrations: `dock admin backup` must not change the schema under
// a daemon of another version.
func SnapshotFile(ctx context.Context, path, dst string) (SnapshotInfo, error) {
	db, err := openPlain(path)
	if err != nil {
		return SnapshotInfo{}, err
	}
	defer func() { _ = db.Close() }()
	return Snapshot(ctx, db, dst)
}

// Inspect checks the database file at path (quick_check) and reads its
// schema version and install ID, without migrating it.
func Inspect(ctx context.Context, path string) (SnapshotInfo, error) { return inspect(ctx, path) }

func inspect(ctx context.Context, path string) (SnapshotInfo, error) {
	db, err := openPlain(path)
	if err != nil {
		return SnapshotInfo{}, err
	}
	defer func() { _ = db.Close() }()
	var check string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil {
		return SnapshotInfo{}, fmt.Errorf("check the database: %w", err)
	}
	if check != "ok" {
		return SnapshotInfo{}, fmt.Errorf("the database is damaged: %s", check)
	}
	var info SnapshotInfo
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&info.Schema); err != nil {
		return info, fmt.Errorf("read schema version: %w", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT install_id FROM install WHERE id = 1").Scan(&info.InstallID); err != nil {
		return info, fmt.Errorf("not a dock database (no install ID): %w", err)
	}
	return info, nil
}

// openPlain opens one connection to the database at path, with a busy
// timeout and nothing else: no migrations, no journal mode change.
func openPlain(path string) (*sql.DB, error) {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// LatestSchema is the schema version this binary migrates to: a database
// with a higher user_version comes from a newer dock.
func LatestSchema() int {
	names, _ := fs.Glob(migrations, "migrations/*.sql")
	sort.Strings(names)
	if len(names) == 0 {
		return 0
	}
	num, _, _ := strings.Cut(strings.TrimPrefix(names[len(names)-1], "migrations/"), "_")
	v, _ := strconv.Atoi(num)
	return v
}
