package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Restore targets (ADR 0095, part 3). A restore into a new service makes
// the service at once and loads it in the background, so until its
// restore succeeded the service doesn't hold the data: nothing may switch
// to it. Whether a service is such a target is read from the restore row
// that names it (a backup's or to a time's restored_to, a snapshot
// restore's to_service_id), never stored on the service. A failed restore
// archives its target under another name, so the name is free for the
// next try, and raises an alert on the service restored from.

// restoreInto is the restore that made a service.
type restoreInto struct {
	id     int64
	status apitypes.BackupStatus
	// snapshot says the restore is a snapshot's (volume_snapshot_restores),
	// not a backup's or to a time (backups): the two count IDs apart.
	snapshot bool
}

// String names the restore as its list does.
func (r restoreInto) String() string {
	if r.snapshot {
		return fmt.Sprintf("snapshot restore %d", r.id)
	}
	return fmt.Sprintf("restore %d", r.id)
}

// watch is the command that shows the restore.
func (r restoreInto) watch() string {
	if r.snapshot {
		return "`dock volume snapshot list`"
	}
	return "`dock backup list`"
}

// restoreInto finds the newest restore that made the service; ok is
// false for a service no restore made. Two indexed lookups.
func (c *Core) restoreInto(ctx context.Context, serviceID int64) (r restoreInto, ok bool, err error) {
	b, berr := c.store.GetRestoreInto(ctx, &serviceID)
	if berr != nil && !store.IsNotFound(berr) {
		return restoreInto{}, false, berr
	}
	s, serr := c.store.GetSnapshotRestoreInto(ctx, serviceID)
	if serr != nil && !store.IsNotFound(serr) {
		return restoreInto{}, false, serr
	}
	switch {
	case berr != nil && serr != nil:
		return restoreInto{}, false, nil
	case serr != nil || (berr == nil && !b.StartedAt.Before(s.StartedAt)):
		return restoreInto{id: b.ID, status: apitypes.BackupStatus(b.Status)}, true, nil
	}
	return restoreInto{id: s.ID, status: apitypes.BackupStatus(s.Status), snapshot: true}, true, nil
}

// restoredTarget refuses a service whose restore is running or failed as
// what references switch to (SwitchReferences): it doesn't hold the data.
func (c *Core) restoredTarget(ctx context.Context, to svcScope) error {
	r, ok, err := c.restoreInto(ctx, to.svc.ID)
	if err != nil || !ok {
		return err
	}
	switch r.status {
	case apitypes.BackupRunning:
		return conflict("%s is still being restored (%s); switch once it finishes (%s shows it)", to, r, r.watch())
	case apitypes.BackupFailed:
		return invalid("%s's restore failed (%s), so it doesn't hold the data; restore again and switch to that one", to, r)
	}
	return nil
}

// failedTargetName is the name a failed restore's target is archived
// under: <name>-failed-<restore id> (a snapshot restore's ID with an "s",
// since the two kinds count apart), the name cut to fit a service name.
func failedTargetName(name string, r restoreInto) string {
	suffix := fmt.Sprintf("-failed-%d", r.id)
	if r.snapshot {
		suffix = fmt.Sprintf("-failed-s%d", r.id)
	}
	const maxName = 32 // nameRe
	if len(name)+len(suffix) > maxName {
		name = strings.TrimRight(name[:maxName-len(suffix)], "-")
	}
	return name + suffix
}

// restoreTargetFailed is what a failed restore into a new service does to
// its target, for all three kinds (a backup's, to a time, a snapshot's):
// the target toID is renamed and archived, which frees its name for the
// next try and stops its container (a second service started under the
// old name would otherwise share its private hostname with it), and an
// alert is raised on from, the service restored from. Its volume is kept
// for a look. Nothing references the target: SwitchReferences refuses it
// until its restore succeeded. The finish functions call it before they
// write the restore's row, so whoever reads "failed" finds the name free.
// It returns the name the target has now, for the audit text.
func (c *Core) restoreTargetFailed(ctx context.Context, from svcScope, toID int64, r restoreInto, cause error) string {
	to, err := c.scopeOf(ctx, toID)
	if err != nil {
		c.log.Error("load a failed restore's target", "service", toID, "err", err)
		return ""
	}
	// A restore drill's throwaway service is the drill's (ADR 0096): it
	// removes it for good and raises its own alert.
	if !r.snapshot && to.svc.Name == from.svc.Name+drillSuffix {
		if run, err := c.store.GetDrillRestore(ctx, store.GetDrillRestoreParams{ServiceID: from.svc.ID, RestoredTo: &toID}); err == nil && run.CreatedBy == System.Name {
			return to.svc.Name
		}
	}
	was, name := to.svc.Name, failedTargetName(to.svc.Name, r)
	var done []archivedService
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.RenameService(ctx, store.RenameServiceParams{Name: name, ID: toID}); err != nil {
			return err
		}
		as, ok, err := archiveTx(ctx, q, to)
		if CodeOf(err) == apitypes.CodeDeployInProgress {
			// Renamed all the same: the name is what the next try needs.
			c.log.Warn("a failed restore's target is deploying and stays unarchived", "service", to.String())
			return nil
		}
		if ok {
			done = append(done, as)
		}
		return err
	})
	if err != nil {
		c.log.Error("archive a failed restore's target", "service", to.String(), "err", err)
		name = was
	} else if len(done) > 0 {
		c.finishArchive(ctx, done)
	}
	s := restoreFailed(from, r, was, name, cause.Error())
	c.signalAlert(ctx, s)
	return name
}

// restoreAlertKey is the key of a service's failed-restore alert.
func restoreAlertKey(from svcScope) string {
	return "restore:" + alertServiceKey(from.svc.ID, from.project.Name, from.env.Name, from.svc.Name)
}

// restoreFailed is the alert for a restore of from into a new service
// failing: was is the name the target had, kept the one it is archived
// under. The next successful restore of from resolves it
// (restoreSucceeded).
func restoreFailed(from svcScope, r restoreInto, was, kept, reason string) alertSignal {
	p, e, s := from.project.Name, from.env.Name, from.svc.Name
	out := alertSignal{
		Key: restoreAlertKey(from), serviceID: from.svc.ID, projectID: from.project.ID,
		Kind: apitypes.AlertBackupFailed, Severity: apitypes.SeverityWarning,
		Title: "Restore of " + serviceKey(p, e, s) + " failed",
		Message: fmt.Sprintf("%s into %s failed: %s. %s doesn't hold the data and is archived as %s; restore again.",
			upperFirst(r.String()), was, strings.TrimRight(strings.TrimSpace(reason), ".:; "), was, kept),
		Command: "dock volume snapshot list " + svcFlags(p, e, s),
		Project: p, Environment: e, Service: s,
	}
	if !r.snapshot {
		out.Command = fmt.Sprintf("dock backup log %d %s", r.id, svcFlags(p, e, s))
		out.Target = &apitypes.AlertTarget{Backup: r.id}
	}
	return out
}

// restoreSucceeded closes from's failed-restore alert: a restore of it
// worked since.
func (c *Core) restoreSucceeded(ctx context.Context, from svcScope) {
	c.signalAlert(ctx, alertSignal{Key: restoreAlertKey(from), Resolve: true})
}

// upperFirst is s with its first letter in upper case.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// dataAccessAfterRestore sets up data access for the template database a
// restore made or filled, before its row says done, so `dock db query`
// works as soon as the restore reads as finished (as restore.finish does
// for a backup's). Failing is left to afterActive's retry.
func (c *Core) dataAccessAfterRestore(ctx context.Context, serviceID int64) {
	if to, err := c.scopeOf(ctx, serviceID); err == nil {
		_ = c.ensureDataAccess(ctx, to)
	}
}
