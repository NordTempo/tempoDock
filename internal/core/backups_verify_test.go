package core

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

func TestVerifyDue(t *testing.T) {
	at := func(day, hour, minute int) time.Time { return time.Date(2026, 11, day, hour, minute, 0, 0, time.UTC) }
	row := func(status apitypes.BackupStatus, started time.Time) *store.BackupCheck {
		return &store.BackupCheck{Status: string(status), StartedAt: started}
	}
	started := at(1, 14, 0)
	for name, tc := range map[string]struct {
		last *store.BackupCheck
		want time.Time
	}{
		"never checked: the first backup hour after the daemon started": {nil, at(2, 3, 0)},
		"passed: the same hour a week later":                            {row(apitypes.BackupSucceeded, at(8, 3, 2)), at(15, 3, 0)},
		"passed by hand in the afternoon: the backup hour a week on":    {row(apitypes.BackupSucceeded, at(8, 16, 0)), at(16, 3, 0)},
		"skipped counts as run":                                         {row(apitypes.BackupSkipped, at(8, 3, 2)), at(15, 3, 0)},
		"failed: again the next day":                                    {row(apitypes.BackupFailed, at(8, 3, 2)), at(9, 3, 0)},
		"running: not due":                                              {row(apitypes.BackupRunning, at(8, 3, 2)), time.Time{}},
	} {
		if got := verifyDue(tc.last, 3, started, time.UTC); !got.Equal(tc.want) {
			t.Errorf("%s: due %v, want %v", name, got, tc.want)
		}
	}
}

// waitCheck waits until the repository's latest check has finished.
func (h *harness) waitCheck(after int64) apitypes.BackupCheck {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		set, err := h.c.GetBackupSettings(h.ctx, testSystem, false)
		if err != nil {
			h.t.Fatal(err)
		}
		if len(set.Checks) == 1 && set.Checks[0].ID > after && set.Checks[0].Status != apitypes.BackupRunning {
			return set.Checks[0]
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("no finished check after %d: %+v", after, set.Checks)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestRepositoryCheck(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	if _, err := h.c.RunBackupChecks(h.ctx, testSystem); err == nil || !strings.Contains(err.Error(), "no backups to check") {
		t.Fatalf("a check with no backups: %v", err)
	}
	var damaged atomic.Bool
	damaged.Store(true)
	h.rt.OnJob(func(s node.JobSpec) (node.JobResult, error) {
		switch {
		case s.Script != backup.VerifyScript:
			return node.JobResult{Output: resticOutput("aaaa1111", 1, "aaaa1111")}, nil
		case damaged.Load():
			return node.JobResult{ExitCode: 1, Output: "Fatal: repository contains errors\n"}, nil
		}
		return node.JobResult{}, nil
	})
	h.runBackup(db)

	// A failed check is recorded, shows on the service and raises an alert.
	set, err := h.c.RunBackupChecks(h.ctx, testSystem)
	if err != nil || !set.Verify {
		t.Fatalf("check now: %+v, %v", set, err)
	}
	failed := h.waitCheck(0)
	key := checkAlertKey(checkTarget{serverID: h.c.localID})
	if failed.Status != apitypes.BackupFailed || !strings.Contains(failed.Error, "repository contains errors") || failed.Trigger != "manual" || !h.alertOpen(key) {
		t.Fatalf("failed check = %+v, alert open %v", failed, h.alertOpen(key))
	}
	b, err := h.c.ServiceBackups(h.ctx, testSystem, db)
	if err != nil || b.Check == nil || b.Check.ID != failed.ID || b.NextCheckAt == nil || time.Until(*b.NextCheckAt) > 25*time.Hour {
		t.Fatalf("service check = %+v, next %v, %v", b.Check, b.NextCheckAt, err)
	}

	// The next one that passes resolves it, and the weekly one follows.
	damaged.Store(false)
	if _, err := h.c.RunBackupChecks(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	if ok := h.waitCheck(failed.ID); ok.Status != apitypes.BackupSucceeded || ok.Detail == "" || h.alertOpen(key) {
		t.Fatalf("passed check = %+v, alert open %v", ok, h.alertOpen(key))
	}
	if set, err = h.c.GetBackupSettings(h.ctx, testSystem, false); err != nil || set.NextCheckAt == nil || time.Until(*set.NextCheckAt) < 5*24*time.Hour {
		t.Fatalf("next check = %v, %v", set.NextCheckAt, err)
	}

	// Off: nothing is due.
	off := false
	if set, err = h.c.UpdateBackupChecks(h.ctx, testSystem, apitypes.UpdateBackupChecksRequest{Enabled: &off}); err != nil || set.Verify || set.NextCheckAt != nil {
		t.Fatalf("off = %+v, %v", set, err)
	}
}
