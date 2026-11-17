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
