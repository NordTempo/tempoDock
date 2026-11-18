package core

import (
	"context"
	"crypto/subtle"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cpbackup"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Recovery mode (ADR 0098): the control plane was restored from the
// bucket onto a server whose apps and volumes are not back yet
// (install.sh --restore-from-bucket). While it lasts the backup
// scheduler runs nothing, so an empty volume is never backed up over a
// good one and retention removes nothing the recovery needs, and missing
// containers are not reported as drift. `dock admin recover` reads the
// plan and ends the mode through the loopback listener, proving itself
// with the token in the data dir's recovery file.

// Recovering reports whether the control plane is in recovery mode.
func (c *Core) Recovering() bool { return c.recovery.Load() != nil }

// RecoveryActor returns the system actor for a caller that holds the
// recovery file's token, and false for anyone else or outside recovery
// mode.
func (c *Core) RecoveryActor(token string) (Actor, bool) {
	r := c.recovery.Load()
	if r == nil || subtle.ConstantTimeCompare([]byte(token), []byte(r.Token)) != 1 {
		return Actor{}, false
	}
	return System, true
}

// RecoveryPlan lists what a recovery restores: every stateful service on
// the control plane's own server with its newest backup in the current
// repository, or why it has none. It changes nothing.
func (c *Core) RecoveryPlan(ctx context.Context, a Actor) (apitypes.RecoveryPlan, error) {
	if err := a.requirePlatform(BackupsManage, "planning a recovery"); err != nil {
		return apitypes.RecoveryPlan{}, err
	}
	r := c.recovery.Load()
	if r == nil {
		return apitypes.RecoveryPlan{}, conflict("this control plane is not in recovery mode; restore one backup with `dock backup restore`")
	}
	set, err := c.backupSettings(ctx)
	if err != nil {
		return apitypes.RecoveryPlan{}, err
	}
	rows, err := c.store.ListRecoveryServices(ctx, store.ListRecoveryServicesParams{ServerID: c.localID, Repository: set.Repository})
	if err != nil {
		return apitypes.RecoveryPlan{}, err
	}
	plan := apitypes.RecoveryPlan{Since: r.At, From: r.From, Repository: repoLabel(set.Repository), Services: make([]apitypes.RecoveryService, 0, len(rows))}
	// A local repository was a volume on the server that is gone.
	local := set.Repository == ""
	for _, row := range rows {
		s := apitypes.RecoveryService{
			Org: row.Org, Project: row.Project, Environment: row.Environment, Service: row.Name,
			Kind: apitypes.BackupVolume, State: apitypes.RecoveryNone,
		}
		if t, ok := templates[apitypes.Template(row.Template)]; ok {
			s.Kind = t.backup
		}
		switch {
		case row.BackupID != nil && !local:
			s.State, s.Snapshot, s.SizeBytes, s.BackedUpAt = apitypes.RecoveryRestore, *row.SnapshotID, *row.SizeBytes, row.FinishedAt
		case row.EverBackedUp:
			// In a local repository, or in one the backups moved away from.
			s.State = apitypes.RecoveryLost
		}
		plan.Services = append(plan.Services, s)
	}
	return plan, nil
}

// EndRecovery takes the control plane out of recovery mode: the backup
// scheduler runs again and drift is reported again.
func (c *Core) EndRecovery(ctx context.Context, a Actor) error {
	if err := a.requirePlatform(BackupsManage, "ending a recovery"); err != nil {
		return err
	}
	if !c.Recovering() {
		return nil
	}
	if err := cpbackup.EndRecovery(c.cfg.DataDir); err != nil {
		return err
	}
	c.recovery.Store(nil)
	c.audit(ctx, a.platform(), "control_plane.recovery_ended", nil, nil, "control plane", "ended recovery mode; scheduled backups run again")
	c.kickBackups()
	return nil
}
