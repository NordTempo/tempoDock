package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/mcpkit"
)

// Volume snapshot tools (ADR 0047). Agents list, take and restore
// snapshots into a new service; an in-place restore replaces a service's
// data and is left to a human (CLI or UI), so no tool offers it.

// addSnapshotTools registers list_snapshots, create_snapshot and
// restore_snapshot.
func (h *handler) addSnapshotTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "list_snapshots", "List snapshots",
		"A stateful service's volume snapshots, newest first (id, trigger, status, size, how long the service was paused, note or error), its latest restores, "+
			"whether snapshots restore into a new service (template databases) and retention: the newest keep per service, none older than keep_days.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Limit int `json:"limit,omitempty" doc:"How many snapshots. Default 20, at most 200."`
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			n, err := mcpkit.Limit("limit", in.Limit, 20, 200)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			ss, err := h.core.ServiceSnapshots(ctx, c.Actor, ref)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			ss.Snapshots = ss.Snapshots[:min(n, len(ss.Snapshots))]
			lines := make([]string, len(ss.Snapshots))
			for i, sn := range ss.Snapshots {
				lines[i] = snapshotLine(sn)
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("%d snapshots of %s (keeps %d, none older than %d days): %s.", len(lines), ref, ss.Keep, ss.KeepDays, list(lines)), Data: ss}, nil
		})

	mcpkit.AddTool(s, "create_snapshot", "Snapshot volumes",
		"Snapshot a stateful service's volumes on its server now, e.g. before a risky migration, and wait for it. "+
			"Its running containers are paused (frozen, connections kept) while the volumes are copied, usually seconds; a database recovers from the copy as from a power cut. "+
			"Snapshots stay on the server, count against its disk and are pruned by count and age: they are not an off-server backup.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Note string `json:"note,omitempty" doc:"What the snapshot is for, e.g. before the 2.0 migration."`
			WaitInput
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			wait, err := in.duration()
			if err != nil {
				return mcpkit.Reply{}, err
			}
			snap, err := h.core.CreateSnapshot(ctx, c.Actor, ref, apitypes.CreateSnapshotRequest{Note: in.Note})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			ss, err := h.waitSnapshots(ctx, c, ref, wait, func(ss apitypes.ServiceSnapshots) bool {
				sn, ok := findSnapshot(ss, snap.ID)
				return !ok || sn.Status != apitypes.SnapshotRunning
			})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			if sn, ok := findSnapshot(ss, snap.ID); ok {
				snap = sn
			}
			summary := "Snapshot " + snapshotLine(snap) + " of " + ref.String() + "."
			if snap.Status == apitypes.SnapshotRunning {
				summary += " Still running; list_snapshots shows when it is ready."
			}
			return mcpkit.Reply{Summary: summary, Data: snap}, nil
		})

	mcpkit.AddTool(s, "restore_snapshot", "Restore a snapshot into a new service",
		"Restore a template database's snapshot (postgres, redis) into a NEW service with the snapshot's data and credentials, and wait until it has started. "+
			"The service the snapshot is of is never touched; once the new one checks out, point references at it (e.g. ${{<new>.DATABASE_URL}}) with set_variables. "+
			"In-place restores, which replace a service's own data, are not available here: a human does them with `dock volume snapshot restore --in-place` or in the UI. "+
			"Services that aren't template databases restore in place only, so not with this tool.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Snapshot int64  `json:"snapshot" doc:"The snapshot's id (list_snapshots shows them)."`
			As       string `json:"as,omitempty" doc:"Name of the new service. Default: <service>-snapshot-<id>."`
			WaitInput
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			if in.Snapshot <= 0 {
				return mcpkit.Reply{}, mcpkit.Invalid("pass snapshot: the id to restore (list_snapshots shows them)")
			}
			wait, err := in.duration()
			if err != nil {
				return mcpkit.Reply{}, err
			}
			res, err := h.core.RestoreSnapshot(ctx, c.Actor, ref, in.Snapshot, apitypes.RestoreSnapshotRequest{As: in.As})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			ss, err := h.waitSnapshots(ctx, c, ref, wait, func(ss apitypes.ServiceSnapshots) bool {
				r, ok := findRestore(ss, res.Restore.ID)
				return !ok || r.Status != apitypes.BackupRunning
			})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			if r, ok := findRestore(ss, res.Restore.ID); ok {
				res.Restore = r
			}
			r := res.Restore
			summary := fmt.Sprintf("Restoring snapshot %d into %s: %s.", in.Snapshot, r.To, r.Status)
			switch r.Status {
			case apitypes.BackupSucceeded:
				summary = fmt.Sprintf("Restored snapshot %d into %s; %s is untouched. Check %s, then point references at it.", in.Snapshot, r.To, ref.Service, r.To)
			case apitypes.BackupFailed:
				summary = fmt.Sprintf("Restoring snapshot %d into %s failed: %s. %s is kept for a look.", in.Snapshot, r.To, r.Error, r.To)
			case apitypes.BackupRunning:
				summary += " list_snapshots shows when it is done."
			}
			return mcpkit.Reply{Summary: summary, Data: res}, nil
		})
}

// findSnapshot returns snapshot id from a service's snapshots.
func findSnapshot(ss apitypes.ServiceSnapshots, id int64) (apitypes.Snapshot, bool) {
	for _, sn := range ss.Snapshots {
		if sn.ID == id {
			return sn, true
		}
	}
	return apitypes.Snapshot{}, false
}

// findRestore returns restore id from a service's latest restores.
func findRestore(ss apitypes.ServiceSnapshots, id int64) (apitypes.SnapshotRestore, bool) {
	for _, r := range ss.Restores {
		if r.ID == id {
			return r, true
		}
	}
	return apitypes.SnapshotRestore{}, false
}

// waitSnapshots waits until done reports that what a tool started (a
// snapshot or restore of ref) finished, or wait passes, then returns
// ref's snapshots as they are. Like waitReleases it follows the event
// bus instead of polling: the snapshot.* audit events that end snapshots
// and restores (or a lagged stream) make it read the snapshots again.
func (h *handler) waitSnapshots(ctx context.Context, c *call, ref core.ServiceRef, wait time.Duration, done func(apitypes.ServiceSnapshots) bool) (apitypes.ServiceSnapshots, error) {
	if wait > 0 {
		wctx, cancel := context.WithTimeout(ctx, wait)
		defer cancel()
		stream, err := h.core.Events(wctx, c.Actor, core.EventFilter{Project: ref.Project, Environment: ref.Environment})
		if err != nil {
			return apitypes.ServiceSnapshots{}, err
		}
		var readErr error
		// finished reads ref's snapshots and reports whether waiting ends.
		finished := func() bool {
			ss, err := h.core.ServiceSnapshots(wctx, c.Actor, ref)
			if err != nil {
				if wctx.Err() == nil {
					readErr = err
				}
				return true
			}
			return done(ss)
		}
		err = stream(wctx, func() {
			if finished() {
				cancel()
			}
		}, func(evs ...core.LiveEvent) error {
			for _, ev := range evs {
				snapshotEnd := ev.Audit != nil && ev.Audit.Target == ref.String() && strings.HasPrefix(ev.Audit.Action, "snapshot.")
				if (ev.Type == apitypes.EventLagged || snapshotEnd) && finished() {
					return errAllDone
				}
			}
			return nil
		})
		switch {
		case readErr != nil:
			return apitypes.ServiceSnapshots{}, readErr
		case err != nil && !errors.Is(err, errAllDone):
			return apitypes.ServiceSnapshots{}, err
		}
	}
	return h.core.ServiceSnapshots(ctx, c.Actor, ref)
}
