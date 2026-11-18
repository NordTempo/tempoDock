package store

import (
	"context"
	"path/filepath"
	"testing"
)

// Finding the restore that made a service reads through an index, for
// both kinds of restore.
func TestRestoreTargetQueryPlans(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	checkPlans(t, s, []planCase{
		{"GetRestoreInto", getRestoreInto, []any{1}},
		{"GetSnapshotRestoreInto", getSnapshotRestoreInto, []any{1}},
	})
}
