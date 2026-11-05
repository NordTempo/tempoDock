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

// backupPoll is how often `dock backup run` and `restore` check on a run
// they wait for.
const backupPoll = 2 * time.Second

func newBackupCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "backup",
		Short: "Back up and restore stateful services",
		Long: `Every service with a volume is backed up daily with restic: template databases
with their own tool (pg_dump, Redis snapshots), other volumes file by file.
Until they are moved to a bucket (dock backup move) backups stay on each
server, not off-box. Restores create a new service; the live one is never
touched.`,
	})
	cmd.AddCommand(newBackupListCmd(g), newBackupLogCmd(g), newBackupRunCmd(g), newBackupRestoreCmd(g),
		newBackupScheduleCmd(g), newBackupStatusCmd(g), newBackupConfigureCmd(g), newBackupMoveCmd(g))
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
	apitypes.BackupVolume:   "volume files",
}

func printBackups(g *globals, b apitypes.ServiceBackups) error {
	where := "local, not off-box"
	if b.OffBox {
		where = "off-box"
	}
	if err := g.io.Fields("service", b.Service, "method", backupMethod[b.Kind], "to", where, "schedule", scheduleText(b.Schedule)); err != nil {
		return err
	}
	if b.Warning != "" {
		g.io.Warnf("%s", b.Warning)
	}
	if !b.OffBox {
		g.io.Warnf("Backups stay on the service's server. Send them off-box with `dock backup configure --repository s3:...`.")
	}
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

func scheduleText(s apitypes.BackupSchedule) string {
	if !s.Enabled {
		return "off"
	}
	out := fmt.Sprintf("daily at %02d:00", s.Hour)
	if s.DefaultHour {
		out += " (install default)"
	}
	if s.NextAt != nil {
		out += ", next " + s.NextAt.Local().Format("Mon 15:04")
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
	case r.RestoredTo != "":
		return "into " + r.RestoredTo
	}
	return ""
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
	var snapshot, as string
	var detach bool
	cmd := &cobra.Command{
		Use:   "restore [service] --snapshot ID",
		Short: "Restore a backup into a new service",
		Long: `Restore a template database's snapshot into a NEW service from the same
template, with new credentials. The live service is never touched: when the
new one checks out, point references at it (e.g. ${{postgres-restored.DATABASE_URL}}).`,
		Example:           "  dock backup restore postgres --snapshot 4cf53263 --as postgres-old",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeServiceArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			serviceArg(&f, args)
			if snapshot == "" {
				return usageError("--snapshot is required", "Pick one from `dock backup list`.")
			}
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			res, err := send[apitypes.RestoreResult](cmd.Context(), s, http.MethodPost, t.servicePath()+"/backups/restore",
				apitypes.RestoreBackupRequest{Snapshot: snapshot, As: as})
			if err != nil {
				return err
			}
			if !detach {
				g.io.Progressf("Restoring snapshot %s into %s...", snapshotID(res.Restore.Snapshot), res.Service.Name)
				if res.Restore, err = waitRun(cmd.Context(), s, t, res.Restore.ID); err != nil {
					return err
				}
			}
			return g.io.Print(res, func() error {
				if res.Restore.Status == apitypes.BackupRunning {
					g.io.Successf("Created %s; the restore continues in the background (`dock backup list %s`)", res.Service.Name, target{t.Project, t.Environment, res.Service.Name}.flags())
				} else {
					g.io.Successf("Restored snapshot %s into %s", snapshotID(res.Restore.Snapshot), res.Service.Name)
				}
				g.io.Detail(fmt.Sprintf("%s is untouched. Check %s, then point references at it.", t.Service, res.Service.Name))
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().StringVar(&snapshot, "snapshot", "", "snapshot ID, or its first 8 or more characters")
	cmd.Flags().StringVar(&as, "as", "", "name of the new service (default <service>-restored)")
	cmd.Flags().BoolVar(&detach, "detach", false, "return once the new service is created")
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
	kv = append(kv, "control plane", controlPlaneText(set.ControlPlane))
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
	if !set.OffBox {
		g.io.Warnf("Backups are not off-box: losing a server loses its backups. Add a bucket with `dock storage add`, then `dock backup move --storage <name>`.")
	}
	return nil
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
		out += ", next " + cp.NextAt.Local().Format("Mon 15:04")
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
