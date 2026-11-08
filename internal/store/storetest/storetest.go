// Package storetest opens migrated databases for tests quickly. The
// migrations run once per test binary; every database after that starts
// as a copy of the result. Migrating a new database takes about 130 ms,
// and 3.6 s under -race (modernc's SQLite is Go, so the race detector
// instruments all of it), which made it most of the race jobs' time; a
// copy takes about a millisecond. Only tests import it.
package storetest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/NordTempo/tempoDock/internal/store"
)

// Open opens the database at path (which must not exist yet) with every
// migration applied and the test org (ID 1, slug "test") in it. Like
// store.Open, the caller closes it.
func Open(t testing.TB, path string) *store.Store {
	t.Helper()
	image, err := migrated()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, image, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// migrated is the file of a database with every migration and the test
// org, built on first use. VACUUM INTO writes it as one self-contained
// file, with no WAL beside it, and keeps the schema version.
var migrated = sync.OnceValues(func() ([]byte, error) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "storetest-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	st, err := store.Open(ctx, filepath.Join(dir, "migrating.db"))
	if err != nil {
		return nil, fmt.Errorf("migrate a test database: %w", err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.CreateOrg(ctx, store.CreateOrgParams{Slug: "test", Name: "test"}); err != nil {
		return nil, fmt.Errorf("create the test org: %w", err)
	}
	image := filepath.Join(dir, "image.db")
	if _, err := st.DB.ExecContext(ctx, "VACUUM INTO ?", image); err != nil {
		return nil, fmt.Errorf("copy the test database: %w", err)
	}
	return os.ReadFile(image)
})
