package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// Scheduled jobs (ADR 0054): `dock job` lists, sets, removes, turns on and
// off, runs now and shows the history of a service's cron jobs. A job's
// run is a `dock run` started by the clock, so its output is
// `dock runs show <id>`.

// newJobCmd is the `dock job` group.
func newJobCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:     "job",
		Aliases: []string{"jobs"},
		Short:   "Run commands on a schedule (cron) in a service's image",
		Long: `A service's scheduled jobs: each a cron expression in a timezone and a
command that runs like ` + "`dock run`" + ` in the service's active image, with its
variables and private network, without volumes. A job still running when it
comes due again is skipped unless its overlap is allow. Jobs can also come
from the service's dock.toml ([[jobs]]); those are changed there, and can
still be turned off and run now here.`,
	})
	cmd.AddCommand(newJobListCmd(g), newJobSetCmd(g), newJobRemoveCmd(g),
		newJobEnabledCmd(g, true), newJobEnabledCmd(g, false), newJobRunCmd(g), newJobHistoryCmd(g))
	return cmd
}

// jobPath is the API path of job name on t's service.
func jobPath(t target, name string) string {
	return t.servicePath() + "/jobs/" + url.PathEscape(name)
}

// newJobListCmd is `dock job list [service]`.
func newJobListCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:     "list [service]",
		Aliases: []string{"ls"},
		Short:   "List a service's jobs with their next and latest run",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			serviceArg(&f, args)
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			jobs, err := get[[]apitypes.Job](cmd.Context(), s, t.servicePath()+"/jobs")
			if err != nil {
				return err
			}
			return g.io.Print(jobs, func() error {
				if len(jobs) == 0 {
					g.io.Successf("%s has no jobs. Add one with `dock job set <name> --schedule '0 3 * * *' -- <command>`.", t)
					return nil
				}
				tb := g.io.Table("name", "schedule", "tz", "command", "next", "last", "source", "enabled")
				for _, j := range jobs {
					tb.Row(j.Name, j.Schedule, j.Timezone, cut(oneLine(j.Command), 40), jobNext(j.NextAt), jobLast(j.LastRun), jobSource(j.Source), yesNo(j.Enabled))
				}
				return tb.Flush()
			})
		},
	}
	f.add(cmd, needService)
	return cmd
}

// jobSetOptions are `dock job set`'s flags besides the target.
type jobSetOptions struct {
	schedule, timezone, overlap string
	timeout                     time.Duration
	disabled                    bool
}

// newJobSetCmd is `dock job set <name> --schedule ... -- <command...>`.
func newJobSetCmd(g *globals) *cobra.Command {
	var (
		f targetFlags
		o jobSetOptions
	)
	cmd := &cobra.Command{
		Use:   "set <name> --schedule CRON [flags] -- <command...>",
		Short: "Create a job or replace one by name",
		Long: `Create a scheduled job, or replace the one with this name (every setting:
a flag left out goes back to its default, and the job is on unless
--disabled). A changed schedule counts from now.

The schedule is five cron fields (minute hour day-of-month month
day-of-week) with ranges, lists, steps and names, or @hourly, @daily,
@weekly, @monthly or @yearly, read in --timezone (an IANA name, default
UTC). The command is everything after --, quoted as ` + "`dock run`" + ` does: one
argument as it is, several each quoted for the shell.

Jobs from dock.toml are changed there. Needs deploy scope; audited as
job.set.`,
		Example: `  dock job set nightly-cleanup -s web --schedule "0 3 * * *" --timezone Europe/Oslo -- bin/rails cleanup
  dock job set digest -s api --schedule @hourly --timeout 10m -- 'node digest.js && node notify.js'`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dash := cmd.ArgsLenAtDash()
			if dash < 0 || dash == len(args) {
				return usageError("put the command after --", "For example `dock job set nightly --schedule '0 3 * * *' -- bin/cleanup`.")
			}
			if dash != 1 {
				return usageError("give the job's name, and only it, before --", "For example `dock job set nightly --schedule '0 3 * * *' -- bin/cleanup`.")
			}
			if o.schedule == "" {
				return usageError("missing --schedule", "For example --schedule '0 3 * * *' (03:00 daily) or --schedule @hourly.")
			}
			if o.timeout < 0 || o.timeout > apitypes.MaxRunTimeoutSeconds*time.Second {
				return usageError("--timeout must be at most 24h", "Leave it out for an hour.")
			}
			if o.overlap != "" && o.overlap != string(apitypes.OverlapSkip) && o.overlap != string(apitypes.OverlapAllow) {
				return usageError(fmt.Sprintf("--overlap is skip or allow, not %q", o.overlap), "Leave it out to skip a run while the last one still runs.")
			}
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			req := apitypes.SetJobRequest{
				Schedule: o.schedule, Timezone: o.timezone, Command: shellJoin(args[dash:]),
				TimeoutSeconds: timeoutSeconds(o.timeout), Overlap: apitypes.JobOverlap(o.overlap),
			}
			if o.disabled {
				req.Enabled = new(false)
			}
			j, err := send[apitypes.Job](cmd.Context(), s, http.MethodPut, jobPath(t, args[0]), req)
			if err != nil {
				return err
			}
			return g.io.Print(j, func() error {
				g.io.Successf("Set job %s on %s", j.Name, t)
				return printJob(g, j)
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().StringVar(&o.schedule, "schedule", "", `cron expression, e.g. "0 3 * * *" or @daily (required)`)
	cmd.Flags().StringVar(&o.timezone, "timezone", "", "IANA timezone the schedule is read in, e.g. Europe/Oslo (default UTC)")
	cmd.Flags().DurationVar(&o.timeout, "timeout", 0, "stop a run after this long, at most 24h (default 1h)")
	cmd.Flags().StringVar(&o.overlap, "overlap", "", "when it comes due while still running: skip or allow (default skip)")
	cmd.Flags().BoolVar(&o.disabled, "disabled", false, "set it turned off (dock job enable turns it on)")
	return cmd
}

// printJob is a job's human output after set, enable and disable.
func printJob(g *globals, j apitypes.Job) error {
	return g.io.Fields(
		"schedule", j.Schedule+" ("+j.Timezone+")",
		"command", j.Command,
		"timeout", (time.Duration(j.TimeoutSeconds) * time.Second).String(),
		"overlap", string(j.Overlap),
		"enabled", yesNo(j.Enabled),
		"source", jobSource(j.Source),
		"next", jobNext(j.NextAt),
		"last", jobLast(j.LastRun),
	)
}

// newJobRemoveCmd is `dock job remove <name>`.
func newJobRemoveCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm"},
		Short:   "Remove a job and its history",
		Long:    `Remove a job and its runs, refused while it runs. Jobs from dock.toml go when the file drops them; ` + "`dock job disable`" + ` turns one off meanwhile.`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, jobPath(t, args[0])); err != nil {
				return err
			}
			return g.io.Print(map[string]string{"removed": args[0]}, func() error {
				g.io.Successf("Removed job %s from %s", args[0], t)
				return nil
			})
		},
	}
	f.add(cmd, needService)
	return cmd
}

// newJobEnabledCmd is `dock job enable <name>` (on) or `dock job disable
// <name>`.
func newJobEnabledCmd(g *globals, on bool) *cobra.Command {
	var f targetFlags
	use, short := "enable", "Turn a job on: it runs on its schedule again"
	if !on {
		use, short = "disable", "Turn a job off until enabled, dock.toml's too"
	}
	cmd := &cobra.Command{
		Use:   use + " <name>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			j, err := send[apitypes.Job](cmd.Context(), s, http.MethodPut, jobPath(t, args[0])+"/enabled", apitypes.SetJobEnabledRequest{Enabled: on})
			if err != nil {
				return err
			}
			return g.io.Print(j, func() error {
				if j.Enabled {
					g.io.Successf("Job %s is on, next run %s", j.Name, jobNext(j.NextAt))
				} else {
					g.io.Successf("Job %s is off; `dock job enable %s` turns it on", j.Name, j.Name)
				}
				return nil
			})
		},
	}
	f.add(cmd, needService)
	return cmd
}

// newJobRunCmd is `dock job run <name> [--detach]`.
func newJobRunCmd(g *globals) *cobra.Command {
	var (
		f      targetFlags
		detach bool
	)
	cmd := &cobra.Command{
		Use:   "run <name>",
		Short: "Run a job now, outside its schedule",
		Long: `Run a job now and wait for it: its output's tail goes to stdout once it ended,
and the job's exit code is dock's, as with ` + "`dock run`" + `. A job still running is
refused unless its overlap is allow. Ctrl-C stops waiting, not the run.

--detach prints the run as it started; ` + "`dock runs show <id>`" + ` shows it later.
With --json stdout is the run, with its output, once it ended. Needs deploy
scope; audited as job.run.`,
		Example: `  dock job run nightly-cleanup -s web
  dock job run digest -s api --detach`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			var w *watcher
			if !detach {
				// Before the run starts, so its end can't be missed.
				if w, err = g.watch(ctx, s, t); err != nil {
					return err
				}
				defer w.close()
			}
			r, err := send[apitypes.CommandRun](ctx, s, http.MethodPost, jobPath(t, name)+"/runs", nil)
			if err != nil {
				return err
			}
			if detach {
				return g.io.Print(r, func() error {
					g.io.Successf("Started run %d of job %s on %s", r.ID, name, t)
					g.io.Detail(fmt.Sprintf("Follow it with `dock runs show %d -s %s`.", r.ID, t.Service))
					return nil
				})
			}
			if r, err = w.waitCommandRun(ctx, t, r); err != nil {
				return err
			}
			if err := g.io.Print(r, func() error { return printJobRun(g, name, r) }); err != nil {
				return err
			}
			return jobRunExit(name, r)
		},
	}
	f.add(cmd, needService)
	cmd.Flags().BoolVarP(&detach, "detach", "d", false, "return once it started instead of waiting for it")
	return cmd
}

// waitCommandRun waits on w (opened before r started) until run r of t's
// service ended, and returns it read once more, with its output. Its end
// comes as a run event; after a lagged stream the run is read again, and
// a lost stream or Ctrl-C is an error pointing at `dock runs show`.
func (w *watcher) waitCommandRun(ctx context.Context, t target, r apitypes.CommandRun) (apitypes.CommandRun, error) {
	path := t.servicePath() + "/runs/" + itoa(r.ID)
	lost := func(msg string) error {
		return &output.Error{Exit: output.ExitError, Message: fmt.Sprintf("%s; run %d continues on the server", msg, r.ID),
			Hint: fmt.Sprintf("Check on it with `dock runs show %d -s %s`.", r.ID, t.Service)}
	}
	done := r.Status != apitypes.RunRunning
	for !done {
		select {
		case <-ctx.Done():
			return r, lost("stopped waiting")
		case ev, ok := <-w.events:
			switch {
			case !ok:
				cur, err := get[apitypes.CommandRun](ctx, w.s, path)
				if err != nil || cur.Status == apitypes.RunRunning {
					return r, lost("lost the live event stream")
				}
				return cur, nil
			case ev.Type == apitypes.EventRun && ev.Run != nil && ev.Run.ID == r.ID:
				done = ev.Run.Status != apitypes.RunRunning
			case ev.Type == apitypes.EventLagged:
				cur, err := get[apitypes.CommandRun](ctx, w.s, path)
				if err != nil {
					return r, err
				}
				done = cur.Status != apitypes.RunRunning
			}
		}
	}
	return get[apitypes.CommandRun](ctx, w.s, path)
}

// printJobRun is `dock job run`'s human output: the run's output tail on
// stdout, then how it ended on stderr. A run without an exit code says so
// through jobRunExit's error instead.
func printJobRun(g *globals, name string, r apitypes.CommandRun) error {
	if r.Output != "" {
		if _, err := io.WriteString(g.io.Out, strings.TrimSuffix(r.Output, "\n")+"\n"); err != nil {
			return err
		}
	}
	switch {
	case r.ExitCode == nil:
	case *r.ExitCode == 0:
		g.io.Progressf("Run %d of job %s succeeded in %s", r.ID, name, runDuration(r))
	default:
		g.io.Failf("Run %d of job %s exited with %d after %s", r.ID, name, *r.ExitCode, runDuration(r))
		if r.Cause != "" {
			g.io.Detail("Likely cause: " + r.Cause)
		}
	}
	return nil
}

// jobRunExit is runExit with hints for a job: its timeout is the job's.
func jobRunExit(name string, r apitypes.CommandRun) error {
	err := runExit(r)
	if e, ok := errors.AsType[*output.Error](err); ok {
		switch r.Status {
		case apitypes.RunTimedOut:
			e.Hint = fmt.Sprintf("Give it longer with `dock job set %s --timeout 2h ...` (at most 24h).", name)
		case apitypes.RunSkipped:
			e.Hint = "Check that the service has a release and runs: `dock status`."
		}
	}
	return err
}

// newJobHistoryCmd is `dock job history <name>`.
func newJobHistoryCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "history <name>",
		Short: "List a job's latest runs, newest first",
		Long: `List a job's latest runs (the newest 20), newest first: scheduled, run now
and skipped ones. ` + "`dock runs show <id>`" + ` shows one with its output.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			runs, err := get[[]apitypes.CommandRun](cmd.Context(), s, jobPath(t, args[0])+"/runs")
			if err != nil {
				return err
			}
			return g.io.Print(runs, func() error {
				if len(runs) == 0 {
					g.io.Successf("Job %s has not run yet. Run it now with `dock job run %s`.", args[0], args[0])
					return nil
				}
				tb := g.io.Table("id", "status", "exit", "trigger", "started", "duration", "error or likely cause")
				for _, r := range runs {
					tb.Row(itoa(r.ID), string(r.Status), runExitCode(r), string(r.Trigger), ago(r.StartedAt), runDuration(r), dash(cut(oneLine(cmp.Or(r.Error, r.Cause)), 60)))
				}
				if err := tb.Flush(); err != nil {
					return err
				}
				g.io.Detail(fmt.Sprintf("`dock runs show <id> -s %s` shows a run's output.", t.Service))
				return nil
			})
		},
	}
	f.add(cmd, needService)
	return cmd
}

// jobNext is a job's next time for humans, relative and in local time:
// "in 5h (Thu Nov 5 03:00)", "-" while it is off.
func jobNext(t *time.Time) string {
	if t == nil {
		return "-"
	}
	in := "now"
	switch d := time.Until(*t); {
	case d < time.Second:
	case d < time.Minute:
		in = fmt.Sprintf("in %ds", int(d.Seconds()))
	case d < time.Hour:
		in = fmt.Sprintf("in %dm", int(d.Minutes()))
	case d < 48*time.Hour:
		in = fmt.Sprintf("in %dh", int(d.Hours()))
	default:
		in = fmt.Sprintf("in %dd", int(d.Hours()/24))
	}
	return in + " (" + t.Local().Format("Mon Jan 2 15:04") + ")"
}

// jobLast is a job's latest run for tables: "succeeded 2h ago", "failed
// exit 3 2h ago", "-" before its first.
func jobLast(r *apitypes.CommandRun) string {
	if r == nil {
		return "-"
	}
	s := string(r.Status)
	if r.ExitCode != nil && *r.ExitCode != 0 {
		s += " exit " + runExitCode(*r)
	}
	return s + " " + ago(r.StartedAt)
}

// jobSource names where a job is defined: "api" or "dock.toml".
func jobSource(s apitypes.JobSource) string {
	if s == apitypes.JobFromRepo {
		return "dock.toml"
	}
	return string(s)
}

// yesNo is a boolean for tables.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
