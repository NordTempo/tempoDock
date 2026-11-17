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

// Point-in-time restore (ADR 0093): agents restore a Postgres database
// as it was at a moment into a new service, as with snapshots. Turning
// it on, and restoring in place, are for people.

func (h *handler) addPITRTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "restore_to_time", "Restore a database as it was at a time",
		"Restore a Postgres database with point-in-time restore as it was at a moment, into a new service, and wait. A refusal names the times it can reach.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Time string `json:"time" doc:"RFC 3339, e.g. 2026-11-17T14:01:00Z."`
			As   string `json:"as,omitempty" doc:"Default: <service>-restored."`
			WaitInput
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			at, err := time.Parse(time.RFC3339, strings.TrimSpace(in.Time))
			if err != nil {
				return mcpkit.Reply{}, mcpkit.Invalid("time must be RFC 3339, like 2026-11-17T14:01:00Z")
			}
			wait, err := in.duration()
			if err != nil {
				return mcpkit.Reply{}, err
			}
			res, err := h.core.RestoreBackup(ctx, c.Actor, ref, apitypes.RestoreBackupRequest{ToTime: &at, As: in.As})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			run, err := h.waitBackupRun(ctx, c, ref, res.Restore.ID, wait)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			if run.ID != 0 {
				res.Restore = run
			}
			to, when := res.Service.Name, at.UTC().Format(time.RFC3339)
			summary := fmt.Sprintf("Restoring %s as of %s into %s: still running.", ref, when, to)
			switch res.Restore.Status {
			case apitypes.BackupSucceeded:
				summary = fmt.Sprintf("Restored %s as of %s into %s; %s is untouched. Check %s, then point references at it.", ref, when, to, ref.Service, to)
			case apitypes.BackupFailed:
				summary = fmt.Sprintf("Restoring %s as of %s into %s failed: %s. %s is kept for a look.", ref, when, to, res.Restore.Error, to)
			}
			return mcpkit.Reply{Summary: summary, Data: res}, nil
		})
}

// waitBackupRun waits up to wait for a backup or restore run of ref to
// finish, following its audit events, and returns it (zero if it isn't
// listed).
func (h *handler) waitBackupRun(ctx context.Context, c *call, ref core.ServiceRef, id int64, wait time.Duration) (apitypes.BackupRun, error) {
	find := func(ctx context.Context) (apitypes.BackupRun, bool, error) {
		b, err := h.core.ServiceBackups(ctx, c.Actor, ref)
		if err != nil {
			return apitypes.BackupRun{}, false, err
		}
		for _, r := range b.Runs {
			if r.ID == id {
				return r, r.Status != apitypes.BackupRunning, nil
			}
		}
		return apitypes.BackupRun{}, true, nil
	}
	if wait > 0 {
		wctx, cancel := context.WithTimeout(ctx, wait)
		defer cancel()
		stream, err := h.core.Events(wctx, c.Actor, core.EventFilter{Project: ref.Project, Environment: ref.Environment})
		if err != nil {
			return apitypes.BackupRun{}, err
		}
		var readErr error
		finished := func() bool {
			_, done, err := find(wctx)
			if err != nil && wctx.Err() == nil {
				readErr = err
			}
			return done || err != nil
		}
		err = stream(wctx, func() {
			if finished() {
				cancel()
			}
		}, func(evs ...core.LiveEvent) error {
			for _, ev := range evs {
				end := ev.Audit != nil && ev.Audit.Target == ref.String() && strings.HasPrefix(ev.Audit.Action, "backup.restore")
				if (ev.Type == apitypes.EventLagged || end) && finished() {
					return errAllDone
				}
			}
			return nil
		})
		switch {
		case readErr != nil:
			return apitypes.BackupRun{}, readErr
		case err != nil && !errors.Is(err, errAllDone):
			return apitypes.BackupRun{}, err
		}
	}
	r, _, err := find(ctx)
	return r, err
}
