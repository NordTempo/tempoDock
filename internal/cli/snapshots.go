package cli

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// newVolumeSnapshotCmd is `dock volume snapshot`: on its own it takes a
// snapshot of the service's volumes; list, restore and delete work with
// the snapshots taken (ADR 0047).
func newVolumeSnapshotCmd(g *globals) *cobra.Command {
	var f targetFlags
	var req apitypes.CreateSnapshotRequest
	var detach bool
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Snapshot a service's volumes, or list, restore and delete snapshots",
		Long: `Take a snapshot of a service's volumes now and wait for it (--detach returns
at once). The service's running containers are paused while its volumes are
copied on its server, so the copy is of one moment: a database recovers
from it as from a power cut. Snapshots stay on the server and count against
its disk; the newest few of each service are kept, none older than some days
(` + "`dock volume snapshot list`" + ` shows how many). For copies off the server,
use ` + "`dock backup`" + `.`,
		Example: `  dock volume snapshot -s postgres --note "before the 2.0 migration"
  dock volume snapshot list -s postgres
  dock volume snapshot restore 12 -s postgres`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			snap, err := send[apitypes.Snapshot](cmd.Context(), s, http.MethodPost, t.servicePath()+"/snapshots", req)
			if err != nil {
				return err
			}
			if !detach {
				g.io.Progressf("Taking snapshot %d of %s...", snap.ID, t.Service)
				if snap, err = waitSnapshot(cmd.Context(), s, t, snap.ID); err != nil {
					return err
				}
			}
			return g.io.Print(snap, func() error {
				if snap.Status == apitypes.SnapshotRunning {
					g.io.Successf("Taking snapshot %d of %s; follow it with `dock volume snapshot list`", snap.ID, t.Service)
					return nil
				}
				g.io.Successf("Took snapshot %d of %s: %s, paused %s", snap.ID, t.Service, bytesize(snap.SizeBytes), pausedText(snap.PausedMS))
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().StringVar(&req.Note, "note", "", `what the snapshot is for, e.g. "before the 2.0 migration"`)
	cmd.Flags().BoolVar(&detach, "detach", false, "return once the snapshot started")
	cmd.AddCommand(newSnapshotListCmd(g), newSnapshotRestoreCmd(g), newSnapshotDeleteCmd(g))
	return cmd
}

// newSnapshotListCmd is `dock volume snapshot list`.
func newSnapshotListCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List a service's snapshots and restores",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			ss, err := get[apitypes.ServiceSnapshots](cmd.Context(), s, t.servicePath()+"/snapshots")
			if err != nil {
				return err
			}
			return g.io.Print(ss, func() error { return printSnapshots(g, ss) })
		},
	}
	f.add(cmd, needService)
	return cmd
}

// printSnapshots is `dock volume snapshot list`'s human output.
func printSnapshots(g *globals, ss apitypes.ServiceSnapshots) error {
	restores := "in place"
	if ss.IntoNew {
		restores = "into a new service (--in-place replaces the service's data)"
	}
	if err := g.io.Fields("service", ss.Service, "restores", restores,
		"retention", fmt.Sprintf("keeps %d per service, %s", ss.Keep, plural(ss.KeepDays, "day"))); err != nil {
		return err
	}
	if len(ss.Snapshots) == 0 {
		g.io.Detail("No snapshots yet. Take one with `dock volume snapshot`.")
	} else {
		tb := g.io.Table("id", "taken", "trigger", "status", "size", "paused", "note")
		for _, sn := range ss.Snapshots {
			size, paused := "-", "-"
			if sn.Status == apitypes.SnapshotReady {
				size, paused = bytesize(sn.SizeBytes), pausedText(sn.PausedMS)
			}
			tb.Row(strconv.FormatInt(sn.ID, 10), ago(sn.StartedAt), string(sn.Trigger), string(sn.Status), size, paused,
				dash(cmp.Or(sn.Error, sn.Note)))
		}
		if err := tb.Flush(); err != nil {
			return err
		}
	}
	if len(ss.Restores) == 0 {
		return nil
	}
	g.io.Detail("Restores:")
	tb := g.io.Table("id", "started", "snapshot", "to", "status", "safety", "note")
	for _, r := range ss.Restores {
		to := r.To
		if r.InPlace {
			to += " (in place)"
		}
		safety := "-"
		if r.Safety != 0 {
			safety = strconv.FormatInt(r.Safety, 10)
		}
		tb.Row(strconv.FormatInt(r.ID, 10), ago(r.StartedAt), snapshotRef(r.Snapshot), to, string(r.Status), safety, dash(r.Error))
	}
	return tb.Flush()
}

// newSnapshotRestoreCmd is `dock volume snapshot restore`.
func newSnapshotRestoreCmd(g *globals) *cobra.Command {
	var f targetFlags
	var req apitypes.RestoreSnapshotRequest
	var yes, detach bool
	cmd := &cobra.Command{
		Use:   "restore <id>",
		Short: "Restore a snapshot into a new service, or in place",
		Long: `Restore a snapshot and wait for it (--detach returns at once).

A template database restores into a NEW service by default (--as names it,
default <service>-snapshot-<id>) with the snapshot's credentials; the
service itself is not touched. --in-place replaces the service's own
volumes instead: it stops, a safety snapshot keeps the data it holds now
(restore that one to undo), the snapshot is copied in and it starts again.
Other services restore in place only.`,
		Example: `  dock volume snapshot restore 12 -s postgres --as postgres-old
  dock volume snapshot restore 12 -s worker --in-place`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := snapshotArg(args[0])
			if err != nil {
				return err
			}
			if req.InPlace && req.As != "" {
				return usageError("--as names a new service, and an in-place restore keeps the service", "Leave out --as or --in-place.")
			}
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			if req.InPlace && !yes {
				q := fmt.Sprintf("Restore snapshot %d into %s in place? It stops and its volumes are replaced; a safety snapshot keeps what they hold now.", id, t)
				if err := g.io.Confirm(q, "yes"); err != nil {
					return err
				}
			}
			res, err := send[apitypes.SnapshotRestoreResult](cmd.Context(), s, http.MethodPost,
				fmt.Sprintf("%s/snapshots/%d/restore", t.servicePath(), id), req)
			if err != nil {
				return err
			}
			if !detach {
				g.io.Progressf("Restoring snapshot %d into %s...", id, res.Restore.To)
				if res.Restore, err = waitSnapshotRestore(cmd.Context(), s, t, res.Restore.ID); err != nil {
					return err
				}
			}
			return g.io.Print(res, func() error {
				r := res.Restore
				if r.Status == apitypes.BackupRunning {
					g.io.Successf("Restoring snapshot %d into %s; follow it with `dock volume snapshot list`", id, r.To)
				} else {
					g.io.Successf("Restored snapshot %d into %s", id, r.To)
				}
				if r.InPlace {
					g.io.Detail(fmt.Sprintf("Snapshot %d holds the data it replaced; restore it to undo: `dock volume snapshot restore %d --in-place`.", r.Safety, r.Safety))
				} else {
					g.io.Detail(fmt.Sprintf("%s is untouched. Check %s, then point references at it.", t.Service, r.To))
				}
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().StringVar(&req.As, "as", "", "name of the new service (default <service>-snapshot-<id>)")
	cmd.Flags().BoolVar(&req.InPlace, "in-place", false, "replace the service's own volumes, after a safety snapshot")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	cmd.Flags().BoolVar(&detach, "detach", false, "return once the restore started")
	return cmd
}

// newSnapshotDeleteCmd is `dock volume snapshot delete`.
func newSnapshotDeleteCmd(g *globals) *cobra.Command {
	var f targetFlags
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <id>",
		Aliases: []string{"rm"},
		Short:   "Delete a snapshot and free its disk on the server",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := snapshotArg(args[0])
			if err != nil {
				return err
			}
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Delete snapshot %d of %s? Its files on the server are removed.", id, t), "yes"); err != nil {
					return err
				}
			}
			if err := del(cmd.Context(), s, fmt.Sprintf("%s/snapshots/%d", t.servicePath(), id)); err != nil {
				return err
			}
			v := struct {
				ID      int64 `json:"id"`
				Deleted bool  `json:"deleted"`
			}{id, true}
			return g.io.Print(v, func() error {
				g.io.Successf("Deleted snapshot %d of %s", id, t)
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

// snapshotArg parses a snapshot ID argument.
func snapshotArg(arg string) (int64, error) {
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil || id <= 0 {
		return 0, usageError(fmt.Sprintf("%q is not a snapshot ID", arg), "Pick one from `dock volume snapshot list`.")
	}
	return id, nil
}

// snapshotRef is a restore's snapshot in a table: its ID, or "deleted".
func snapshotRef(id int64) string {
	if id == 0 {
		return "deleted"
	}
	return strconv.FormatInt(id, 10)
}

// pausedText is how long a snapshot paused the service, e.g. "180ms".
func pausedText(ms int64) string {
	if ms == 0 {
		return "not running"
	}
	return (time.Duration(ms) * time.Millisecond).String()
}

// waitSnapshot polls a service's snapshots until snapshot id finished.
// A failed snapshot is an error.
func waitSnapshot(ctx context.Context, s *session, t target, id int64) (apitypes.Snapshot, error) {
	return waitSnapshots(ctx, s, t, func(ss apitypes.ServiceSnapshots) (apitypes.Snapshot, bool, error) {
		for _, sn := range ss.Snapshots {
			switch {
			case sn.ID != id || sn.Status == apitypes.SnapshotRunning:
			case sn.Status == apitypes.SnapshotFailed:
				return sn, true, &output.Error{Exit: output.ExitError, Message: fmt.Sprintf("snapshot %d of %s failed: %s", id, t.Service, sn.Error)}
			default:
				return sn, true, nil
			}
		}
		return apitypes.Snapshot{}, false, nil
	})
}

// waitSnapshotRestore polls a service's snapshots until restore id
// finished. A failed restore is an error.
func waitSnapshotRestore(ctx context.Context, s *session, t target, id int64) (apitypes.SnapshotRestore, error) {
	return waitSnapshots(ctx, s, t, func(ss apitypes.ServiceSnapshots) (apitypes.SnapshotRestore, bool, error) {
		for _, r := range ss.Restores {
			switch {
			case r.ID != id || r.Status == apitypes.BackupRunning:
			case r.Status == apitypes.BackupFailed:
				msg := fmt.Sprintf("restoring snapshot %d into %s failed: %s", r.Snapshot, r.To, r.Error)
				return r, true, &output.Error{Exit: output.ExitError, Message: msg, Hint: restoreFailedHint(r)}
			default:
				return r, true, nil
			}
		}
		return apitypes.SnapshotRestore{}, false, nil
	})
}

// restoreFailedHint says what to do after a failed restore.
func restoreFailedHint(r apitypes.SnapshotRestore) string {
	if r.InPlace {
		return fmt.Sprintf("Snapshot %d holds the data the service had; restore it with `dock volume snapshot restore %d --in-place`.", r.Safety, r.Safety)
	}
	return fmt.Sprintf("%s is kept for a look; archive it when done (`dock service archive %s`).", r.To, r.To)
}

// waitSnapshots fetches a service's snapshots every backupPoll until
// done finds what it waits for finished, and returns what it found.
func waitSnapshots[T any](ctx context.Context, s *session, t target, done func(apitypes.ServiceSnapshots) (T, bool, error)) (T, error) {
	tick := time.NewTicker(backupPoll)
	defer tick.Stop()
	for {
		ss, err := get[apitypes.ServiceSnapshots](ctx, s, t.servicePath()+"/snapshots")
		if err != nil {
			var zero T
			return zero, err
		}
		if v, ok, err := done(ss); ok {
			return v, err
		}
		select {
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		case <-tick.C:
		}
	}
}
