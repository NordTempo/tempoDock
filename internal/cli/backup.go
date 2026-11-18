package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// backupPoll is how often `dock backup run`, `restore` and the volume
// snapshot commands check on a run they wait for. A variable so tests can
// wait less.
var backupPoll = 2 * time.Second

func newBackupCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "backup",
		Short: "Back up and restore stateful services",
		Long: `Every service with a volume is backed up daily with restic: template databases
with their own tool (pg_dump, Redis snapshots), other volumes file by file.
Until they are moved to a bucket (dock backup move) backups stay on each
server, not off-box. Restores create a new service; the live one is never
touched. Once a week the repositories are checked and every template database
is restored once as a drill (dock backup check, dock backup drill).`,
	})
	cmd.AddCommand(newBackupListCmd(g), newBackupLogCmd(g), newBackupRunCmd(g), newBackupRestoreCmd(g),
		newBackupScheduleCmd(g), newBackupPITRCmd(g), newBackupStatusCmd(g), newBackupConfigureCmd(g), newBackupMoveCmd(g),
		newBackupCheckCmd(g), newBackupDrillCmd(g))
	return cmd
}

func newBackupListCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:               "list [service]",
		Aliases:           []string{"ls"},
		Short:             "List a service's backups and restores",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeServiceArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			serviceArg(&f, args)
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			b, err := get[apitypes.ServiceBackups](cmd.Context(), s, t.servicePath()+"/backups")
			if err != nil {
				return err
			}
			return g.io.Print(b, func() error { return printBackups(g, b) })
		},
	}
	f.add(cmd, needService)
	return cmd
}

// newBackupLogCmd prints what a backup or restore run's job printed.
func newBackupLogCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "log <id> -s <service>",
		Short: "Show a backup or restore run's log",
		Long: `Print what a backup or restore run's job printed: its last 16 KB, with the
repository's and database's secrets masked. The ids are in ` + "`dock backup list`" + `;
the service's newest 20 runs keep their log. Needs deploy access.`,
		Example: `  dock backup log 42 -s db`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return usageError(fmt.Sprintf("%q isn't a run id", args[0]), "Use an id from `dock backup list`.")
			}
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			l, err := get[apitypes.BackupLog](cmd.Context(), s, t.servicePath()+"/backups/"+args[0]+"/log")
			if err != nil {
				return err
			}
			return g.io.Print(l, func() error {
				if l.Output == "" {
					g.io.Detail(fmt.Sprintf("Run %d kept no log: its job never ran, or it is older than the service's newest 20 runs.", id))
					return nil
				}
				_, err := io.WriteString(g.io.Out, strings.TrimRight(l.Output, "\n")+"\n")
				return err
			})
		},
	}
	f.add(cmd, needService)
	return cmd
}

// backupMethod names how a kind is backed up.
var backupMethod = map[apitypes.BackupKind]string{
	apitypes.BackupPostgres: "pg_dump",
	apitypes.BackupRedis:    "Redis snapshot",
	apitypes.BackupMySQL:    "mysqldump",
	apitypes.BackupVolume:   "volume files",
}

func printBackups(g *globals, b apitypes.ServiceBackups) error {
	where := "local, not off-box"
	if b.OffBox {
		where = "off-box"
	}
	drillNext := b.NextCheckAt
	if !b.Restorable {
		drillNext = nil // volume backups have no drill
	}
	if err := g.io.Fields("service", b.Service, "method", backupMethod[b.Kind], "to", where, "schedule", scheduleText(b.Schedule),
		"repository check", checkText(b.Check, b.NextCheckAt, "check"), "restore drill", checkText(b.Drill, drillNext, "drill")); err != nil {
		return err
	}
	if b.Warning != "" {
		g.io.Warnf("%s", b.Warning)
	}
	if !b.OffBox {
		g.io.Warnf("Backups stay on the service's server. Add a bucket with `dock storage add`, then `dock backup move --storage <name>`.")
	}
	printPITR(g, b.PITR)
	if len(b.Runs) == 0 {
		g.io.Detail("No backups yet. Run one with `dock backup run`.")
		return nil
	}
	tb := g.io.Table("id", "started", "op", "trigger", "status", "snapshot", "size", "repository", "note")
	for _, r := range b.Runs {
		tb.Row(strconv.FormatInt(r.ID, 10), ago(r.StartedAt), r.Operation, r.Trigger, runStatus(r), dash(snapshotID(r.Snapshot)),
			sizeText(r), r.Repository, runNote(r))
	}
	return tb.Flush()
}

// checkText is a repository check's or restore drill's line in `dock
// backup list` (ADR 0096): how the latest one went and when, or, when
// none ran yet, when the first one (what: "check" or "drill") is due.
// Empty when there is neither, which leaves the line out.
func checkText(c *apitypes.BackupCheck, next *time.Time, what string) string {
	if c == nil {
		if next == nil {
			return ""
		}
		return "first " + what + " " + next.Format("Mon 15:04 MST") // the control plane's zone, as its hour
	}
	when, note := c.StartedAt, c.Detail
	if c.FinishedAt != nil {
		when = *c.FinishedAt
	}
	var out string
	switch c.Status {
	case apitypes.BackupRunning:
		return "running since " + ago(c.StartedAt)
	case apitypes.BackupSucceeded:
		out = "passed " + ago(when)
	case apitypes.BackupFailed:
		out, note = "failed "+ago(when), c.Error
	default:
		out = string(c.Status) + " " + ago(when)
	}
	if note != "" {
		out += ": " + note
	}
	return out
}

// checkTarget names what a repository check read: the repository, and
// the server holding a local one.
func checkTarget(c apitypes.BackupCheck) string {
	if c.Server == "" {
		return c.Repository
	}
	return c.Repository + " on " + c.Server
}

func scheduleText(s apitypes.BackupSchedule) string {
	if !s.Enabled {
		return "off"
	}
	// The hour is the control plane's, and NextAt comes in its zone too:
	// both print in it, never one converted to this machine's.
	zone := ""
	if s.NextAt != nil {
		zone = " " + s.NextAt.Format("MST")
	}
	out := fmt.Sprintf("daily at %02d:00%s", s.Hour, zone)
	if s.DefaultHour {
		out += " (install default)"
	}
	if s.NextAt != nil {
		out += ", next " + s.NextAt.Format("Mon 15:04")
	}
	return out
}

func runStatus(r apitypes.BackupRun) string {
	if r.Pruned {
		return "pruned"
	}
	return string(r.Status)
}

func runNote(r apitypes.BackupRun) string {
	switch {
	case r.Error != "":
		return r.Error
	case r.ToTime != nil && r.RestoredTo != "":
		return "to " + r.ToTime.UTC().Format(time.RFC3339) + " into " + r.RestoredTo
	case r.RestoredTo != "":
		return "into " + r.RestoredTo
	case r.ConsistentAt != nil:
		return "base backup, restores from " + r.ConsistentAt.UTC().Format(time.RFC3339)
	}
	return ""
}

// printPITR prints a database's point-in-time restore: its window and
// how archiving does, or how to turn it on.
func printPITR(g *globals, p *apitypes.PITR) {
	if p == nil {
		return
	}
	if !p.Enabled {
		if p.Unavailable != "" {
			g.io.Detail("Point-in-time restore is off: " + p.Unavailable + ".")
		} else {
			g.io.Detail("Point-in-time restore is off. Turn it on with `dock backup pitr on` to restore any moment of the last days.")
		}
		return
	}
	window := "nothing yet: the first base backup is on its way"
	if p.From != nil {
		window = "from " + p.From.UTC().Format(time.RFC3339)
		if p.Through != nil {
			window += " to " + p.Through.UTC().Format(time.RFC3339)
		}
	}
	_ = g.io.Fields("point in time", fmt.Sprintf("on, %d days", p.Days), "restorable", window)
	if a := p.Archiver; a != nil && !a.Healthy {
		g.io.Warnf("Archiving is behind: %s", a.Problem)
	}
}

func sizeText(r apitypes.BackupRun) string {
	if r.Operation != "backup" || r.Status != apitypes.BackupSucceeded {
		return "-"
	}
	return bytesize(r.SizeBytes)
}

// snapshotID is a snapshot's short form, as restic prints it.
func snapshotID(id string) string { return id[:min(8, len(id))] }

func newBackupRunCmd(g *globals) *cobra.Command {
	var f targetFlags
	var detach, controlPlane bool
	cmd := &cobra.Command{
		Use:   "run [service]",
		Short: "Back up a service, or the control plane, now",
		Long: `Back up a service now and wait for it (--detach returns at once).

--control-plane backs up the control plane itself (its database, keys and
config) into the repository instead, as it does daily (admin). For a file
to move the control plane with, run ` + "`dock admin backup`" + ` on its server.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeServiceArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			if controlPlane {
				if len(args) > 0 || f.service != "" {
					return usageError("--control-plane backs up the control plane, not a service", "Leave out the service.")
				}
				return runControlPlaneBackup(cmd.Context(), g, detach)
			}
			serviceArg(&f, args)
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			run, err := send[apitypes.BackupRun](cmd.Context(), s, http.MethodPost, t.servicePath()+"/backups", nil)
			if err != nil {
				return err
			}
			if !detach {
				g.io.Progressf("Backing up %s...", t.Service)
				if run, err = waitRun(cmd.Context(), s, t, run.ID); err != nil {
					return err
				}
			}
			return g.io.Print(run, func() error {
				switch run.Status {
				case apitypes.BackupRunning:
					g.io.Successf("Started backup %d of %s; follow it with `dock backup list %s`", run.ID, t.Service, t.flags())
				case apitypes.BackupSucceeded:
					g.io.Successf("Backed up %s: snapshot %s, %s, to %s", t.Service, snapshotID(run.Snapshot), bytesize(run.SizeBytes), run.Repository)
				}
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().BoolVar(&detach, "detach", false, "return once the backup started")
	cmd.Flags().BoolVar(&controlPlane, "control-plane", false, "back up the control plane instead of a service (admin)")
	return cmd
}

// runControlPlaneBackup is `dock backup run --control-plane`: start the
// control plane's backup and, unless detach, poll the backup settings
// until it finished. A failed run is an error.
func runControlPlaneBackup(ctx context.Context, g *globals, detach bool) error {
	s, err := g.connect()
	if err != nil {
		return err
	}
	run, err := send[apitypes.ControlPlaneBackupRun](ctx, s, http.MethodPost, apiPrefix+"/backup/control-plane", nil)
	if err != nil {
		return err
	}
	if !detach {
		g.io.Progressf("Backing up the control plane...")
		tick := time.NewTicker(backupPoll)
		defer tick.Stop()
		for run.Status == apitypes.BackupRunning {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-tick.C:
			}
			set, err := get[apitypes.BackupSettings](ctx, s, apiPrefix+"/backup")
			if err != nil {
				return err
			}
			if l := set.ControlPlane.Latest; l != nil && l.StartedAt.Equal(run.StartedAt) {
				run = *l
			}
		}
		if run.Status == apitypes.BackupFailed {
			return &output.Error{Exit: output.ExitError, Message: "the control plane backup failed: " + run.Error}
		}
	}
	return g.io.Print(run, func() error {
		if run.Status == apitypes.BackupRunning {
			g.io.Successf("Started a control plane backup; see it with `dock backup status`")
			return nil
		}
		g.io.Successf("Backed up the control plane: snapshot %s, %s, to %s", snapshotID(run.Snapshot), bytesize(run.SizeBytes), run.Repository)
		return nil
	})
}

// waitRun polls a service's backups until run id finished. A failed run
// is an error.
func waitRun(ctx context.Context, s *session, t target, id int64) (apitypes.BackupRun, error) {
	tick := time.NewTicker(backupPoll)
	defer tick.Stop()
	for {
		b, err := get[apitypes.ServiceBackups](ctx, s, t.servicePath()+"/backups")
		if err != nil {
			return apitypes.BackupRun{}, err
		}
		for _, r := range b.Runs {
			if r.ID != id || r.Status == apitypes.BackupRunning {
				continue
			}
			if r.Status == apitypes.BackupFailed {
				return r, &output.Error{Exit: output.ExitError, Message: fmt.Sprintf("%s %d of %s failed: %s", r.Operation, r.ID, t.Service, r.Error)}
			}
			return r, nil
		}
		select {
		case <-ctx.Done():
			return apitypes.BackupRun{}, ctx.Err()
		case <-tick.C:
		}
	}
}

func newBackupRestoreCmd(g *globals) *cobra.Command {
	var f targetFlags
	var snapshot, as, toTime string
	var detach, inPlace, yes bool
	cmd := &cobra.Command{
		Use:   "restore [service] --snapshot ID | --to-time TIME",
		Short: "Restore a backup, or a moment, into a new service",
		Long: `Restore a template database's snapshot into a NEW service from the same
template, with new credentials. The live service is never touched: when the
new one checks out, point references at it (e.g. ${{postgres-restored.DATABASE_URL}}).

With point-in-time restore on (dock backup pitr on), --to-time restores a
Postgres database as it was at that moment instead, with the credentials it
had. --in-place restores the moment into the database itself: it stops, a
safety snapshot keeps what it holds now (restore that one to undo), and it
starts on the restored data.`,
		Example: `  dock backup restore postgres --snapshot 4cf53263 --as postgres-old
  dock backup restore postgres --to-time 2026-11-17T14:01Z
  dock backup restore postgres --to-time 2026-11-17T15:01+01:00 --in-place`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeServiceArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			serviceArg(&f, args)
			req := apitypes.RestoreBackupRequest{Snapshot: snapshot, As: as, InPlace: inPlace}
			switch {
			case snapshot == "" && toTime == "":
				return usageError("--snapshot or --to-time is required", "Pick a snapshot from `dock backup list`, or a time in its restorable window.")
			case snapshot != "" && toTime != "":
				return usageError("pass --snapshot or --to-time, not both", "")
			case inPlace && toTime == "":
				return usageError("--in-place restores a time; a backup restores into a new service", "Add --to-time, or leave out --in-place.")
			case inPlace && as != "":
				return usageError("--as names a new service, and an in-place restore keeps the service", "Leave out --as or --in-place.")
			}
			if toTime != "" {
				at, err := parseWhen("to-time", toTime)
				if err != nil {
					return err
				}
				req.ToTime = &at
			}
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			if inPlace && !yes {
				q := fmt.Sprintf("Restore %s in place to %s? It stops and its data is replaced; a safety snapshot keeps what it holds now.", t, req.ToTime.Format(time.RFC3339))
				if err := g.io.Confirm(q, "yes"); err != nil {
					return err
				}
			}
			res, err := send[apitypes.RestoreResult](cmd.Context(), s, http.MethodPost, t.servicePath()+"/backups/restore", req)
			if err != nil {
				return deployInProgress(err, t)
			}
			what := "snapshot " + snapshotID(res.Restore.Snapshot)
			if req.ToTime != nil {
				what = t.Service + " as of " + req.ToTime.Format(time.RFC3339)
			}
			if !detach {
				g.io.Progressf("Restoring %s into %s...", what, res.Service.Name)
				if res.Restore, err = waitRun(cmd.Context(), s, t, res.Restore.ID); err != nil {
					return err
				}
			}
			return g.io.Print(res, func() error {
				if res.Restore.Status == apitypes.BackupRunning {
					g.io.Successf("Restoring %s into %s; it continues in the background (`dock backup list %s`)", what, res.Service.Name, t.flags())
				} else {
					g.io.Successf("Restored %s into %s", what, res.Service.Name)
				}
				if inPlace {
					g.io.Detail("A safety snapshot holds the data it replaced; `dock volume snapshot list " + t.flags() + "` shows it.")
				} else {
					g.io.Detail(fmt.Sprintf("%s is untouched. Check %s, then point references at it.", t.Service, res.Service.Name))
				}
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().StringVar(&snapshot, "snapshot", "", "snapshot ID, or its first 8 or more characters")
	cmd.Flags().StringVar(&toTime, "to-time", "", "restore the database as it was at this time (point-in-time restore): 2026-11-17T14:01Z, UTC unless it has an offset")
	cmd.Flags().StringVar(&as, "as", "", "name of the new service (default <service>-restored)")
	cmd.Flags().BoolVar(&inPlace, "in-place", false, "restore --to-time into the database itself, after a safety snapshot")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	cmd.Flags().BoolVar(&detach, "detach", false, "return once the restore started")
	return cmd
}

// newBackupPITRCmd turns a database's point-in-time restore on or off.
func newBackupPITRCmd(g *globals) *cobra.Command {
	var f targetFlags
	var days int
	cmd := &cobra.Command{
		Use:   "pitr on|off [service] [--days N]",
		Short: "Turn a Postgres database's point-in-time restore on or off",
		Long: `Point-in-time restore archives a Postgres template database's WAL next to its
backups and takes a daily base backup, so it restores to any moment of the
last --days days (dock backup restore --to-time). It needs backups on a
storage connection. Turning it on or off restarts the database once.

If archiving stalls (the storage is down), Postgres keeps the WAL it couldn't
archive and its disk fills; an alert says so first. Turning it off restarts
the database without archiving, and Postgres frees that WAL at its next
checkpoint. Operators only.`,
		Example: `  dock backup pitr on postgres --days 14
  dock backup pitr off postgres`,
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: cobra.FixedCompletions([]string{"on", "off"}, cobra.ShellCompDirectiveNoFileComp),
		RunE: func(cmd *cobra.Command, args []string) error {
			var req apitypes.SetPITRRequest
			switch args[0] {
			case "on", "off":
				on := args[0] == "on"
				req.Enabled = &on
			default:
				return usageError(fmt.Sprintf("%q is not on or off", args[0]), "dock backup pitr on|off [service]")
			}
			serviceArg(&f, args[1:])
			if cmd.Flags().Changed("days") {
				req.Days = &days
			}
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			// Off when it is off already changes nothing and restarts
			// nothing, so it is read first and said so. A platform token
			// can't read an org's backups: then what it was isn't known.
			wasOff, known := false, false
			if !*req.Enabled {
				if cur, err := get[apitypes.ServiceBackups](cmd.Context(), s, t.servicePath()+"/backups"); err == nil {
					wasOff, known = cur.PITR == nil || !cur.PITR.Enabled, true
				}
			}
			b, err := send[apitypes.ServiceBackups](cmd.Context(), s, http.MethodPut, t.servicePath()+"/backups/pitr", req)
			if err != nil {
				return deployInProgress(err, t)
			}
			return g.io.Print(b, func() error {
				switch {
				case *req.Enabled:
					g.io.Successf("Point-in-time restore of %s is on, %d days; it restarts once and takes a base backup", t.Service, b.PITR.Days)
				case wasOff:
					g.io.Successf("Point-in-time restore of %s was off already; nothing changed and it was not restarted", t.Service)
				case !known:
					g.io.Successf("Point-in-time restore of %s is off; if it was on, it restarts without archiving, and its window ages out", t.Service)
				default:
					g.io.Successf("Point-in-time restore of %s is off; it restarts without archiving, and its window ages out", t.Service)
				}
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().IntVar(&days, "days", 7, "days of the restorable window to keep, 1-35")
	return cmd
}

func newBackupScheduleCmd(g *globals) *cobra.Command {
	var f targetFlags
	var hour int
	var on, off, defaultHour bool
	cmd := &cobra.Command{
		Use:   "schedule [service]",
		Short: "Turn a service's daily backups on or off, or move them",
		Example: `  dock backup schedule postgres --hour 4
  dock backup schedule worker --off`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeServiceArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			serviceArg(&f, args)
			var req apitypes.UpdateBackupScheduleRequest
			switch {
			case on && off:
				return usageError("pass --on or --off, not both", "")
			case on, off:
				req.Enabled = &on
			}
			if cmd.Flags().Changed("hour") {
				req.Hour = &hour
			}
			req.DefaultHour = defaultHour
			if req.Enabled == nil && req.Hour == nil && !req.DefaultHour {
				return usageError("nothing to change", "Pass --hour, --default-hour, --on or --off.")
			}
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			b, err := send[apitypes.ServiceBackups](cmd.Context(), s, http.MethodPut, t.servicePath()+"/backups/schedule", req)
			if err != nil {
				return err
			}
			return g.io.Print(b, func() error {
				g.io.Successf("Backups of %s: %s", t.Service, scheduleText(b.Schedule))
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().IntVar(&hour, "hour", 0, "hour of the daily backup, 0-23 (the server's local time)")
	cmd.Flags().BoolVar(&defaultHour, "default-hour", false, "follow the install's hour (dock backup configure --hour)")
	cmd.Flags().BoolVar(&on, "on", false, "turn daily backups on")
	cmd.Flags().BoolVar(&off, "off", false, "turn daily backups off")
	return cmd
}

func newBackupStatusCmd(g *globals) *cobra.Command {
	var reveal bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show where backups go, retention and schedule",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			path := apiPrefix + "/backup"
			if reveal {
				path += "?reveal=true"
			}
			set, err := get[apitypes.BackupSettings](cmd.Context(), s, path)
			if err != nil {
				return err
			}
			return g.io.Print(set, func() error { return printBackupSettings(g, set) })
		},
	}
	cmd.Flags().BoolVar(&reveal, "reveal", false, "show the repository password (admin)")
	return cmd
}

func printBackupSettings(g *globals, set apitypes.BackupSettings) error {
	repo := "local repository on each server (not off-box)"
	if set.Storage != "" {
		repo = fmt.Sprintf("storage %s, folder %s (%s)", set.Storage, set.Path, set.Repository)
	} else if set.OffBox {
		repo = set.Repository
	}
	kv := []string{
		"repository", repo,
		"retention", fmt.Sprintf("%d daily, %d weekly, %d monthly", set.KeepDaily, set.KeepWeekly, set.KeepMonthly),
		"schedule", fmt.Sprintf("daily at %02d:00 %s", set.Hour, set.TimeZone),
		"restic", set.ResticImage,
	}
	kv = append(kv, "control plane", controlPlaneText(set.ControlPlane), "weekly checks", weeklyChecksText(set))
	if set.Password != "" {
		kv = append(kv, "password", set.Password)
	}
	if err := g.io.Fields(kv...); err != nil {
		return err
	}
	if l := set.ControlPlane.Latest; l != nil && l.Status == apitypes.BackupFailed {
		g.io.Warnf("The latest control plane backup failed: %s", l.Error)
	}
	if m := set.Move; m != nil {
		switch {
		case m.Running:
			g.io.Warnf("Moving the backups to %s since %s; backups and restores wait for it.", m.To, ago(m.StartedAt))
		case m.Error != "":
			g.io.Warnf("Moving the backups to %s failed: %s", m.To, m.Error)
		default:
			g.io.Detail(fmt.Sprintf("Moved %s to %s %s.", plural(m.Moved, "backup"), m.To, ago(*m.FinishedAt)))
		}
		for _, s := range m.Sources {
			if s.Error != "" {
				g.io.Warnf("%s on %s: %s", s.Repository, s.Server, s.Error)
			}
		}
	}
	if len(set.Repositories) > 0 {
		t := g.io.Table("repository", "server", "snapshots", "size", "new backups")
		for _, r := range set.Repositories {
			cur := "-"
			if r.Current {
				cur = "here"
			}
			t.Row(r.Repository, dash(r.Server), strconv.Itoa(r.Snapshots), bytesize(r.Bytes), cur)
		}
		if err := t.Flush(); err != nil {
			return err
		}
	}
	if len(set.Checks) > 0 {
		t := g.io.Table("checked", "server", "trigger", "result")
		for _, c := range set.Checks {
			t.Row(c.Repository, dash(c.Server), c.Trigger, checkText(&c, nil, "check"))
		}
		if err := t.Flush(); err != nil {
			return err
		}
	}
	if !set.OffBox {
		g.io.Warnf("Backups are not off-box: losing a server loses its backups. Add a bucket with `dock storage add`, then `dock backup move --storage <name>`.")
	}
	return nil
}

// weeklyChecksText is the weekly checks' line in `dock backup status`:
// on with the next one, or off.
func weeklyChecksText(set apitypes.BackupSettings) string {
	switch {
	case !set.Verify:
		return "off (turn them on with `dock backup check --on`)"
	case set.NextCheckAt != nil:
		return "on, next " + set.NextCheckAt.Format("Mon 15:04 MST") // the control plane's zone, as its hour
	}
	return "on"
}

// newBackupCheckCmd is `dock backup check`: check every repository now,
// or turn the weekly checks and drills on or off.
func newBackupCheckCmd(g *globals) *cobra.Command {
	var on, off, detach bool
	cmd := &cobra.Command{
		Use:   "check [--on|--off]",
		Short: "Check that the backups can be read back, now (admin)",
		Long: `Once a week, in the backup hour, Dock checks every repository holding backups
(restic check, with 1 GB of its data read back) and restores every template
database's newest snapshot once as a drill. A failure raises an alert.

This runs the repository checks now and waits for them (--detach returns at
once); ` + "`dock backup status`" + ` shows each repository's latest check. --off turns
the weekly checks and drills off, --on back on. Operators only.`,
		Example: `  dock backup check
  dock backup check --off`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if on && off {
				return usageError("pass --on or --off, not both", "")
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if on || off {
				set, err := send[apitypes.BackupSettings](ctx, s, http.MethodPut, apiPrefix+"/backup/checks", apitypes.UpdateBackupChecksRequest{Enabled: &on})
				if err != nil {
					return err
				}
				return g.io.Print(set, func() error {
					g.io.Successf("Weekly backup checks and restore drills: %s", weeklyChecksText(set))
					return nil
				})
			}
			set, err := send[apitypes.BackupSettings](ctx, s, http.MethodPost, apiPrefix+"/backup/checks/run", nil)
			if err != nil {
				return err
			}
			if !detach {
				g.io.Progressf("Checking the backup repositories...")
				if set, err = waitChecks(ctx, s, set); err != nil {
					return err
				}
			}
			failed := 0
			err = g.io.Print(set, func() error {
				if len(set.Checks) == 0 {
					g.io.Detail("No repository holds backups yet, so there is nothing to check. Run a backup with `dock backup run`.")
				}
				for _, c := range set.Checks {
					switch c.Status {
					case apitypes.BackupRunning:
						g.io.Successf("Checking %s; see the result with `dock backup status`", checkTarget(c))
					case apitypes.BackupFailed:
						g.io.Failf("%s: check %s", checkTarget(c), checkText(&c, nil, "check"))
					default:
						g.io.Successf("%s: check %s", checkTarget(c), checkText(&c, nil, "check"))
					}
				}
				return nil
			})
			for _, c := range set.Checks {
				if c.Status == apitypes.BackupFailed {
					failed++
				}
			}
			if err == nil && failed > 0 {
				return &output.Error{Exit: output.ExitError, Message: plural(failed, "repository check") + " failed",
					Hint: "Backups in that repository may not restore. Fix what the error names, then run `dock backup check` again."}
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&on, "on", false, "turn the weekly checks and restore drills on")
	cmd.Flags().BoolVar(&off, "off", false, "turn the weekly checks and restore drills off")
	cmd.Flags().BoolVar(&detach, "detach", false, "return once the checks started")
	return cmd
}

// waitChecks polls the backup settings until no repository check is
// running, and answers with them.
func waitChecks(ctx context.Context, s *session, set apitypes.BackupSettings) (apitypes.BackupSettings, error) {
	running := func() bool {
		for _, c := range set.Checks {
			if c.Status == apitypes.BackupRunning {
				return true
			}
		}
		return false
	}
	tick := time.NewTicker(backupPoll)
	defer tick.Stop()
	for running() {
		select {
		case <-ctx.Done():
			return set, ctx.Err()
		case <-tick.C:
		}
		var err error
		if set, err = get[apitypes.BackupSettings](ctx, s, apiPrefix+"/backup"); err != nil {
			return set, err
		}
	}
	return set, nil
}

// newBackupDrillCmd is `dock backup drill`: start one database's restore
// drill now.
func newBackupDrillCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "drill [service]",
		Short: "Run a database's restore drill now (admin)",
		Long: `Restore a template database's newest snapshot into a throwaway service
(<service>-drill), query it, and remove it again: proof that the backup
restores. The service itself is never touched. Dock does this weekly for
snapshots up to 2 GB; run by hand, a larger one is restored too, and needs
that much free disk for a moment.

It runs in the background; ` + "`dock backup list`" + ` shows the result. Operators only.`,
		Example:           `  dock backup drill postgres`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeServiceArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			serviceArg(&f, args)
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			d, err := send[apitypes.BackupCheck](cmd.Context(), s, http.MethodPost, t.servicePath()+"/backups/drill", nil)
			if err != nil {
				return deployInProgress(err, t)
			}
			return g.io.Print(d, func() error {
				if d.Status != apitypes.BackupRunning {
					g.io.Successf("Restore drill of %s: %s", t.Service, checkText(&d, nil, "drill"))
					return nil
				}
				g.io.Successf("Started restore drill %d of %s from snapshot %s", d.ID, t.Service, snapshotID(d.Snapshot))
				g.io.Detail(fmt.Sprintf("It finishes in the background; `dock backup list %s` shows the result.", t.flags()))
				return nil
			})
		},
	}
	f.add(cmd, needService)
	return cmd
}

// newBackupMoveCmd is `dock backup move`.
func newBackupMoveCmd(g *globals) *cobra.Command {
	var req apitypes.MoveBackupsRequest
	var yes bool
	cmd := &cobra.Command{
		Use:   "move --storage <name> [--path <folder>]",
		Short: "Move the backups to a storage connection's bucket (admin)",
		Long: `Copy every repository holding backups (each server's local one, or the
current bucket's) into a folder of the storage connection's bucket, check the
copy, point every backup at its copy so it stays restorable, make the bucket
where new backups go, and empty the old repositories. It runs in the
background; ` + "`dock backup status`" + ` shows how it goes. Backups and restores
started meanwhile are refused; scheduled ones wait. A failure before the
switch changes nothing.

From one bucket to another, both connections need the same endpoint and
keys: restic copies with one set.`,
		Example: `  dock storage add hetzner-backups ...     # once
  dock backup move --storage hetzner-backups
  dock backup move --storage hetzner-backups --path dock/backups`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if req.Storage == "" {
				return usageError("--storage is required", "Name the storage connection: `dock storage` lists them.")
			}
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Move the backups to %s? The old repositories are emptied once the copy is checked.", req.Storage), "yes"); err != nil {
					return err
				}
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			set, err := send[apitypes.BackupSettings](cmd.Context(), s, http.MethodPost, apiPrefix+"/backup/move", req)
			if err != nil {
				return err
			}
			return g.io.Print(set, func() error {
				g.io.Successf("Moving the backups to %s", req.Storage)
				g.io.Detail("It finishes in the background; `dock backup status` shows the result.")
				return nil
			})
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&req.Storage, "storage", "", "the storage connection")
	fs.StringVar(&req.Path, "path", "", "folder in the bucket (default dock-backups)")
	fs.BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

// controlPlaneText is the control plane backup's line in `dock backup
// status`: its latest run and the next.
func controlPlaneText(cp apitypes.ControlPlaneBackup) string {
	out := "no backup yet"
	if l := cp.Latest; l != nil {
		out = fmt.Sprintf("%s %s", l.Status, ago(l.StartedAt))
		if l.Status == apitypes.BackupSucceeded {
			out += fmt.Sprintf(" (snapshot %s, %s)", snapshotID(l.Snapshot), bytesize(l.SizeBytes))
		}
	}
	if cp.NextAt != nil {
		out += ", next " + cp.NextAt.Format("Mon 15:04 MST") // the control plane's zone, as its hour
	}
	return out
}

func newBackupConfigureCmd(g *globals) *cobra.Command {
	var repository, accessKey, region string
	var secretStdin bool
	var keepDaily, keepWeekly, keepMonthly, hour int
	cmd := &cobra.Command{
		Use:   "configure",
		Short: "Set backup retention and the default hour (admin)",
		Long: `Set the install's backup retention and the hour of daily backups. Flags left
out stay as they are. The first configure prints the repository password:
store it safely, since without it the backups can't be read after losing
the server. Where backups go is a storage connection: ` + "`dock backup move`" + `.`,
		Example: `  dock backup configure --keep-daily 14 --hour 2`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var req apitypes.ConfigureBackupRequest
			fs := cmd.Flags()
			str := func(name string, v *string, dst **string) {
				if fs.Changed(name) {
					*dst = v
				}
			}
			num := func(name string, v *int, dst **int) {
				if fs.Changed(name) {
					*dst = v
				}
			}
			str("repository", &repository, &req.Repository)
			str("access-key-id", &accessKey, &req.AccessKeyID)
			str("region", &region, &req.Region)
			num("keep-daily", &keepDaily, &req.KeepDaily)
			num("keep-weekly", &keepWeekly, &req.KeepWeekly)
			num("keep-monthly", &keepMonthly, &req.KeepMonthly)
			num("hour", &hour, &req.Hour)
			if secretStdin {
				b, err := io.ReadAll(io.LimitReader(g.io.In, 4<<10))
				if err != nil {
					return err
				}
				secret := strings.TrimSpace(string(b))
				if secret == "" {
					return usageError("--secret-access-key-stdin read nothing", "Pipe the secret in: `printf %s \"$SECRET\" | dock backup configure --secret-access-key-stdin`.")
				}
				req.SecretAccessKey = &secret
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			set, err := send[apitypes.BackupSettings](cmd.Context(), s, http.MethodPut, apiPrefix+"/backup", req)
			if err != nil {
				return err
			}
			return g.io.Print(set, func() error {
				g.io.Successf("Backup settings saved")
				if err := printBackupSettings(g, set); err != nil {
					return err
				}
				if set.Password != "" {
					g.io.Warnf("Store the repository password now (password manager); without it the backups can't be read. Admins can show it again with `dock backup status --reveal`.")
				}
				return nil
			})
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&repository, "repository", "", `S3 repository: s3:https://<endpoint>/<bucket>[/path]; "" for local repositories`)
	fs.StringVar(&accessKey, "access-key-id", "", "S3 access key ID")
	fs.BoolVar(&secretStdin, "secret-access-key-stdin", false, "read the S3 secret access key from stdin")
	fs.StringVar(&region, "region", "", "S3 region, if the store needs one")
	fs.IntVar(&keepDaily, "keep-daily", 0, "daily snapshots to keep per service")
	fs.IntVar(&keepWeekly, "keep-weekly", 0, "weekly snapshots to keep per service")
	fs.IntVar(&keepMonthly, "keep-monthly", 0, "monthly snapshots to keep per service")
	fs.IntVar(&hour, "hour", 0, "hour of daily backups, 0-23 (the control plane's local time)")
	for _, f := range []string{"repository", "access-key-id", "secret-access-key-stdin", "region"} {
		_ = fs.MarkDeprecated(f, "buckets are storage connections: `dock storage add`, then `dock backup move --storage <name>`")
	}
	return cmd
}
