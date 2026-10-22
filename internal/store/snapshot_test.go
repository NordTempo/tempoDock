package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// A snapshot taken while writers commit is the database as of one moment:
// every transaction is in it whole or not at all, and it passes
// quick_check. Writers pair rows in two tables, one transaction per pair;
// snapshots come from the daemon's own pool and from a second connection
// (`dock admin backup` beside a running daemon).
func TestSnapshotIsConsistentDuringWrites(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := Open(ctx, filepath.Join(dir, "dock.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.DB.ExecContext(ctx, `CREATE TABLE a (n INTEGER, pad BLOB); CREATE TABLE b (n INTEGER)`); err != nil {
		t.Fatal(err)
	}

	var commits atomic.Int64
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			tx, err := st.DB.BeginTx(ctx, nil)
			if err != nil {
				done <- err
				return
			}
			// The padding makes the database big enough that a snapshot
			// overlaps several commits.
			if _, err := tx.ExecContext(ctx, "INSERT INTO a (n, pad) VALUES (?, randomblob(2048))", i); err != nil {
				_ = tx.Rollback()
				done <- err
				return
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO b (n) VALUES (?)", i); err != nil {
				_ = tx.Rollback()
				done <- err
				return
			}
			if err := tx.Commit(); err != nil {
				done <- err
				return
			}
			commits.Add(1)
		}
	}()

	last := int64(-1)
	for k := range 6 {
		for commits.Load() < int64(200*(k+1)) {
			select {
			case err := <-done:
				t.Fatalf("writer stopped: %v", err)
			case <-time.After(time.Millisecond):
			}
		}
		dst := filepath.Join(dir, fmt.Sprintf("snap-%d.db", k))
		var info SnapshotInfo
		if k%2 == 0 {
			info, err = Snapshot(ctx, st.DB, dst)
		} else {
			info, err = SnapshotFile(ctx, filepath.Join(dir, "dock.db"), dst)
		}
		if err != nil {
			t.Fatal(err)
		}
		if info.Schema != LatestSchema() || info.InstallID == "" {
			t.Fatalf("snapshot info = %+v, want schema %d", info, LatestSchema())
		}
		snap, err := openPlain(dst)
		if err != nil {
			t.Fatal(err)
		}
		var na, nb int64
		if err := snap.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM a), (SELECT count(*) FROM b)").Scan(&na, &nb); err != nil {
			t.Fatal(err)
		}
		_ = snap.Close()
		if na != nb {
			t.Fatalf("snapshot %d has %d rows in a and %d in b: a transaction was split", k, na, nb)
		}
		if na <= last {
			t.Fatalf("snapshot %d has %d pairs, not more than the one before (%d)", k, na, last)
		}
		last = na
	}
	close(stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// LatestSchema is the number of the last migration, which Open applies.
func TestLatestSchema(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "dock.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	var v int
	if err := st.DB.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil || v != LatestSchema() || v < 21 {
		t.Fatalf("user_version %d, LatestSchema %d, %v", v, LatestSchema(), err)
	}
}
