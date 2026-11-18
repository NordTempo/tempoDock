package cli

import (
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// A schedule prints its hour and next run in the control plane's zone,
// the one next_at comes in, never the next run in this machine's.
func TestScheduleTextOneZone(t *testing.T) {
	cp := time.FixedZone("", 5*3600) // a zone no test machine is in
	next := time.Date(2026, 11, 17, 4, 0, 0, 0, cp)
	s := apitypes.BackupSchedule{Enabled: true, Hour: 4, NextAt: &next}
	if got, want := scheduleText(s), "daily at 04:00 +0500, next Tue 04:00"; got != want {
		t.Errorf("scheduleText = %q, want %q", got, want)
	}
	if got, want := controlPlaneText(apitypes.ControlPlaneBackup{NextAt: &next}), "no backup yet, next Tue 04:00 +0500"; got != want {
		t.Errorf("controlPlaneText = %q, want %q", got, want)
	}
}

// A check's or drill's line says how the latest one went and when, with
// the error of a failed one and the reason of a skipped one; before any
// ran it names the first, in the control plane's zone.
func TestCheckText(t *testing.T) {
	cp := time.FixedZone("", 5*3600)
	next := time.Date(2026, 11, 17, 4, 0, 0, 0, cp)
	started := time.Now().Add(-73 * time.Hour)
	done := started.Add(time.Hour)
	for _, c := range []struct {
		check *apitypes.BackupCheck
		next  *time.Time
		want  string
	}{
		{nil, nil, ""},
		{nil, &next, "first drill Tue 04:00 +0500"},
		{&apitypes.BackupCheck{Status: apitypes.BackupRunning, StartedAt: started}, &next, "running since 3d ago"},
		{&apitypes.BackupCheck{Status: apitypes.BackupSucceeded, Detail: "42 tables in 18 s", StartedAt: started, FinishedAt: &done}, &next, "passed 3d ago: 42 tables in 18 s"},
		{&apitypes.BackupCheck{Status: apitypes.BackupFailed, Detail: "1 GiB of data read", Error: "pack 4cf5 damaged", StartedAt: started, FinishedAt: &done}, nil, "failed 3d ago: pack 4cf5 damaged"},
		{&apitypes.BackupCheck{Status: apitypes.BackupSkipped, Detail: "snapshot is over 2 GB", StartedAt: started}, nil, "skipped 3d ago: snapshot is over 2 GB"},
	} {
		if got := checkText(c.check, c.next, "drill"); got != c.want {
			t.Errorf("checkText = %q, want %q", got, c.want)
		}
	}
}
