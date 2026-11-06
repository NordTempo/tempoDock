package store

import (
	"testing"
	"time"
)

// Volume snapshots' queries (ADR 0047) read through an index: the
// Backups tab's lists, retention's prune and the start-up recovery.
func TestSnapshotQueryPlans(t *testing.T) {
	f := newDataFixture(t)
	now := time.Now()
	checkPlans(t, f.s, []planCase{
		{"GetSnapshot", getSnapshot, []any{1}},
		{"ListServiceSnapshots", listServiceSnapshots, []any{1, 200}},
		{"ListExpiredSnapshots", listExpiredSnapshots, []any{now, 200}},
		{"DeleteSnapshot", deleteSnapshot, []any{1}},
		{"FinishSnapshot", finishSnapshot, []any{"ready", 1, 1, "", "", now, 1}},
		{"FailRunningSnapshots", failRunningSnapshots, []any{"e", now}},
		{"FinishSnapshotRestore", finishSnapshotRestore, []any{"succeeded", "", now, 1}},
		{"ListServiceSnapshotRestores", listServiceSnapshotRestores, []any{1, 20}},
		{"ListRunningSnapshotRestores", listRunningSnapshotRestores, nil},
	})
}
