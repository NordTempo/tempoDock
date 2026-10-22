package tbstore

import (
	"context"
	"path/filepath"
	"testing"
)

// Several processes opening a new database at once (the server starting
// while `tempobase admin create-user` runs) all migrate it without
// applying a migration twice, and end at the latest version.
func TestConcurrentOpenMigratesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tempobase.db")
	const n = 4
	errs := make(chan error, n)
	for range n {
		go func() {
			s, err := Open(context.Background(), path)
			if err == nil {
				err = s.Close()
			}
			errs <- err
		}()
	}
	for range n {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var version int
	if err := s.DB.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	names, _ := migrations.ReadDir("migrations")
	if version != len(names) {
		t.Fatalf("user_version = %d, want %d", version, len(names))
	}
}
