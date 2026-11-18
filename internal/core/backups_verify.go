package core

import (
	"context"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Checked backups (ADR 0096): the weekly repository checks. The restore
// drills are in backups_drill.go.

// verifyEvery is how long after a check or drill that ran the next one
// waits: six and a half days, so the weekly one falls on the same
// backup hour whenever the last one started within it.
const verifyEvery = 7*24*time.Hour - 12*time.Hour

// verifyDue is when a target's next check or drill is due, given its
// latest attempt (nil: never): the first backup hour (in loc) a week
// after one that ran, a day after one that failed, and after the daemon
// started for a target never checked, so an upgrade doesn't start
// reading data at once. At or before now means due now; zero that one is
// running.
func verifyDue(last *store.BackupCheck, hour int, started time.Time, loc *time.Location) time.Time {
	since := started
	if last != nil {
		switch apitypes.BackupStatus(last.Status) {
		case apitypes.BackupRunning:
			return time.Time{}
		case apitypes.BackupFailed:
			since = last.StartedAt
		default:
			since = last.StartedAt.Add(verifyEvery)
		}
	}
	s := since.In(loc)
	at := time.Date(s.Year(), s.Month(), s.Day(), hour, 0, 0, 0, loc)
	if !at.After(s) {
		at = at.AddDate(0, 0, 1)
	}
	return at
}

// toBackupCheck is a check or drill row as the API shows it; server and
// service are the names of what it ran on.
func toBackupCheck(r store.BackupCheck, server, service string) apitypes.BackupCheck {
	return apitypes.BackupCheck{
		ID: r.ID, Kind: r.Kind, Server: server, Service: service, Repository: repoLabel(r.Repository),
		Snapshot: r.SnapshotID, Trigger: r.Trigger, Status: apitypes.BackupStatus(r.Status),
		Error: r.Error, Detail: r.Detail, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
	}
}

// UpdateBackupChecks turns the weekly repository checks and restore
// drills on or off (operators).
func (c *Core) UpdateBackupChecks(ctx context.Context, a Actor, req apitypes.UpdateBackupChecksRequest) (apitypes.BackupSettings, error) {
	return apitypes.BackupSettings{}, invalid("backup checks aren't built yet")
}

// RunBackupChecks starts every repository's check now (operators), in
// the backup scheduler's next pass, which it wakes; the answer is the
// settings, whose Checks show them running and then finished.
func (c *Core) RunBackupChecks(ctx context.Context, a Actor) (apitypes.BackupSettings, error) {
	return apitypes.BackupSettings{}, invalid("backup checks aren't built yet")
}
