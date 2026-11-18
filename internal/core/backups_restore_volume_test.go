package core

import (
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// The backup list names the volumes an in-place restore replaces, and
// each in-place restore that succeeded names its own safety snapshot: a
// second restore of the same backup never takes the first one's, nor the
// first one's when its own is gone.
func TestInPlaceRestoreLinksItsSafetySnapshot(t *testing.T) {
	h := newHarness(t)
	const snap = "abab1212abab"
	h.snapshotJobs(func(spec node.JobSpec) *node.JobResult {
		if strings.Contains(spec.Name, "_backup") {
			return &node.JobResult{Output: resticOutput(snap, 10, snap)}
		}
		return nil
	})
	app := h.volumeService("app")
	h.runBackup(app)
	restore := func() int64 {
		t.Helper()
		var res apitypes.RestoreResult
		h.untilFree(func() (err error) {
			res, err = h.c.RestoreBackup(h.ctx, testSystem, app, apitypes.RestoreBackupRequest{Snapshot: snap, InPlace: true})
			return err
		})
		if done := h.waitRun(app, res.Restore.ID); done.Status != apitypes.BackupSucceeded {
			t.Fatalf("restore = %+v", done)
		}
		return res.Restore.ID
	}
	first, second := restore(), restore()
	// The safety snapshots, newest first.
	list, err := h.c.ServiceSnapshots(h.ctx, testSystem, app)
	if err != nil {
		t.Fatal(err)
	}
	var safety []int64
	for _, s := range list.Snapshots {
		if s.Trigger == apitypes.SnapshotBeforeRestore {
			safety = append(safety, s.ID)
		}
	}
	if len(safety) != 2 {
		t.Fatalf("safety snapshots = %v, want two", safety)
	}
	linked := func() (a, b int64) {
		t.Helper()
		got, err := h.c.ServiceBackups(h.ctx, testSystem, app)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got.Volumes, []string{"files"}) {
			t.Errorf("volumes = %v, want files", got.Volumes)
		}
		for _, r := range got.Runs {
			switch r.ID {
			case first:
				a = r.SafetySnapshot
			case second:
				b = r.SafetySnapshot
			default:
				if r.SafetySnapshot != 0 {
					t.Errorf("run %d, not an in-place restore, names safety snapshot %d", r.ID, r.SafetySnapshot)
				}
			}
		}
		return a, b
	}
	if a, b := linked(); a != safety[1] || b != safety[0] {
		t.Fatalf("safety snapshots of the restores = %d, %d; want %d, %d", a, b, safety[1], safety[0])
	}
	h.untilFree(func() error { return h.c.DeleteSnapshot(h.ctx, testSystem, app, safety[0]) })
	if a, b := linked(); a != safety[1] || b != 0 {
		t.Errorf("with the second's deleted = %d, %d; want %d and none", a, b, safety[1])
	}
}
