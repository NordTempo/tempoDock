package core

import (
	"context"
	"crypto/subtle"
	"fmt"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/cpbackup"
	"github.com/NordTempo/tempoDock/internal/node"
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
// repository, as the repository itself lists it, or why it has none. It
// changes nothing.
func (c *Core) RecoveryPlan(ctx context.Context, a Actor) (apitypes.RecoveryPlan, error) {
	if err := a.requirePlatform(BackupsManage, "planning a recovery"); err != nil {
		return apitypes.RecoveryPlan{}, err
	}
	r := c.recovery.Load()
	if r == nil {
		return apitypes.RecoveryPlan{}, conflict("this control plane is not in recovery mode; restore one backup with `dock backup restore`")
	}
	plan, _, _, err := c.recoveryPlan(ctx, r)
	return plan, err
}

// recoveryPlan builds the plan of recovery r, and returns with it each
// planned service by its ID and the repository the backups are in, for
// the run.
func (c *Core) recoveryPlan(ctx context.Context, r *cpbackup.Recovery) (apitypes.RecoveryPlan, map[int64]apitypes.RecoveryService, string, error) {
	set, err := c.backupSettings(ctx)
	if err != nil {
		return apitypes.RecoveryPlan{}, nil, "", err
	}
	rows, err := c.store.ListRecoveryServices(ctx, store.ListRecoveryServicesParams{ServerID: c.localID, Repository: set.Repository})
	if err != nil {
		return apitypes.RecoveryPlan{}, nil, "", err
	}
	// A local repository was a volume on the server that is gone; a
	// bucket says itself what it holds now. The restored database only
	// knows the backups taken before its own.
	local := set.Repository == ""
	var newest map[int64]backup.Newest
	if !local {
		if newest, err = c.newestSnapshots(ctx, set); err != nil {
			return apitypes.RecoveryPlan{}, nil, "", err
		}
	}
	plan := apitypes.RecoveryPlan{Since: r.At, From: r.From, Repository: repoLabel(set.Repository), Services: make([]apitypes.RecoveryService, 0, len(rows))}
	byID := make(map[int64]apitypes.RecoveryService, len(rows))
	for _, row := range rows {
		s := apitypes.RecoveryService{
			Org: row.Org, Project: row.Project, Environment: row.Environment, Service: row.Name,
			Kind: apitypes.BackupVolume, State: apitypes.RecoveryNone,
		}
		if t, ok := templates[apitypes.Template(row.Template)]; ok {
			s.Kind = t.backup
		}
		n, ok := newest[row.ID]
		switch {
		case ok:
			s.State, s.Snapshot, s.BackedUpAt = apitypes.RecoveryRestore, n.Snapshot, &n.At
			if row.SnapshotID != nil && *row.SnapshotID == n.Snapshot {
				s.SizeBytes = *row.SizeBytes
			}
		case row.EverBackedUp:
			// In a local repository, in one the backups moved away from,
			// or removed from the bucket since.
			s.State = apitypes.RecoveryLost
		}
		plan.Services = append(plan.Services, s)
		byID[row.ID] = s
	}
	return plan, byID, set.Repository, nil
}

// newestSnapshots asks the backup repository for each service's newest
// snapshot, with one restic job on the local server.
func (c *Core) newestSnapshots(ctx context.Context, set store.BackupSetting) (map[int64]backup.Newest, error) {
	repo, err := c.repoOf(ctx, set)
	if err != nil {
		return nil, err
	}
	spec := node.JobSpec{
		Name: fmt.Sprintf("dock_recovery_list_%d", now().UnixNano()), Image: c.cfg.Backup.ResticImage,
		Labels: node.Labels{ControlPlane: c.install}, Env: repo.Env(), Script: backup.NewestScript,
	}
	out, err := c.runResticJob(ctx, c.localID, c.rt, repo, spec)
	if err != nil {
		return nil, fmt.Errorf("read the backup repository %s: %w", repoLabel(set.Repository), err)
	}
	newest, err := backup.ParseNewest(out)
	if err != nil {
		return nil, fmt.Errorf("read the backup repository %s: %w%s", repoLabel(set.Repository), err, withOutput(out))
	}
	return newest, nil
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
