package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/runframe"
)

// One-off commands (ADR 0048): `dock run` streams a command in a job
// container from the service's active release, `dock runs` lists and
// shows past ones.

// exitStatus is an exit code to pass on without printing anything: the
// command `dock run` ran exited with it, or Ctrl-C stopped it (130).
type exitStatus int

// Error is for logs and wrapping only; run() prints nothing for it.
func (e exitStatus) Error() string { return "exit status " + strconv.Itoa(int(e)) }

// exitInterrupted is the conventional status of a command Ctrl-C stopped.
const exitInterrupted exitStatus = 130

// runOptions are `dock run`'s flags besides the target.
type runOptions struct {
	stdin, tty, detach bool
	timeout            time.Duration
	volumes            string
}

// newRunCmd is `dock run [service] -- <command...>`.
func newRunCmd(g *globals) *cobra.Command {
	var (
		f targetFlags
		o runOptions
	)
	cmd := &cobra.Command{
		Use:   "run [service] [flags] -- <command...>",
		Short: "Run a one-off command in a service's image (migrations, seeds, a console)",
		Long: `Run a command with /bin/sh -c in a new container from the service's active
release: its image, variables, CPU and memory limits and its environment's
private network. The running service is never touched, so it works with the
service stopped or crash looping. Its output streams here; the command's
exit code is dock's.

The command is everything after --. One argument is sent as it is, so shell
syntax in quotes works ('rake a && rake b'); several are quoted for the
shell each, so ` + "`psql -c 'select 1'`" + ` means what it says.

-i sends stdin to the command (without it stdin is never read). -t gives it
a terminal when stdin is one; -it is a console. Ctrl-C (unless -it, where
it goes to the command) stops the command. A stateful service's volumes
are mounted read-only; --volumes write mounts them read-write next to the
running service, only for apps whose storage takes a second writer.
Databases' volumes are never mounted.

--detach runs it in the background instead and prints the run right away;
` + "`dock runs show <id>`" + ` follows it. In a locked environment a run files an access
request; once an admin approves it, it runs in the background.

With --json the command's output goes to stderr and stdout is the run as
JSON once it ended (with --detach, as it started). Needs deploy scope;
every run is in the audit log (service.run) and in ` + "`dock runs`" + `.`,
		Example: `  dock run web -- bin/rails db:seed
  dock run -s api -- 'rake cache:clear && rake jobs:retry'
  dock run -it web -- bin/rails console
  dock run -i db-tools -- psql < fix.sql
  dock run web --timeout 2h --detach -- ./scripts/backfill`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dash := cmd.ArgsLenAtDash()
			if dash < 0 || dash == len(args) {
				return &output.Error{Exit: output.ExitUsage, Message: "put the command after --",
					Hint: "For example `dock run web -- bin/rails db:seed`."}
			}
			if dash > 1 {
				return &output.Error{Exit: output.ExitUsage, Message: fmt.Sprintf("only the service goes before --, got %s", strings.Join(args[:dash], " ")),
					Hint: "Put the whole command after --: `dock run web -- " + strings.Join(args[:dash], " ") + "`."}
			}
			if o.detach && (o.stdin || o.tty) {
				return &output.Error{Exit: output.ExitUsage, Message: "--detach runs without input or a terminal: leave out -i and -t"}
			}
			if o.timeout < 0 || o.timeout > apitypes.MaxRunTimeoutSeconds*time.Second {
				return &output.Error{Exit: output.ExitUsage, Message: "--timeout must be at most 24h", Hint: "Leave it out for an hour."}
			}
			serviceArg(&f, args[:dash])
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			command := shellJoin(args[dash:])
			if o.detach {
				return g.startRun(cmd.Context(), s, t, command, o)
			}
			r, err := g.streamRun(cmd.Context(), s, t, command, o)
			if err != nil {
				return lockedRunHint(err)
			}
			if err := g.io.Print(r, func() error { return nil }); err != nil {
				return err
			}
			return runExit(r)
		},
	}
	f.add(cmd, needService)
	cmd.Flags().BoolVarP(&o.stdin, "interactive", "i", false, "send stdin to the command")
	cmd.Flags().BoolVarP(&o.tty, "tty", "t", false, "give the command a terminal (when stdin is one); -it for a console")
	cmd.Flags().BoolVarP(&o.detach, "detach", "d", false, "run it in the background and return at once (no -i or -t)")
	cmd.Flags().DurationVar(&o.timeout, "timeout", 0, "stop the command after this long, at most 24h (default 1h)")
	cmd.Flags().StringVar(&o.volumes, "volumes", "", "a stateful service's volumes: none, read or write (default read)")
	return cmd
}

// startRun is `dock run --detach`: it starts command in the background
// and prints the run, running.
func (g *globals) startRun(ctx context.Context, s *session, t target, command string, o runOptions) error {
	r, err := send[apitypes.CommandRun](ctx, s, http.MethodPost, t.servicePath()+"/runs", apitypes.RunCommandRequest{
		Command: command, TimeoutSeconds: timeoutSeconds(o.timeout), Volumes: apitypes.RunVolumes(o.volumes),
	})
	if err != nil {
		return err
	}
	return g.io.Print(r, func() error {
		g.io.Successf("Started run %d in release %d of %s/%s/%s", r.ID, r.Release, r.Project, r.Environment, r.Service)
		g.io.Detail(fmt.Sprintf("Follow it with `dock runs show %d %s`.", r.ID, target{r.Project, r.Environment, r.Service}.flags()))
		return nil
	})
}

// lockedRunHint adds to a locked environment's hint what approving a
// streamed run's request does: it runs in the background, without this
// terminal.
func lockedRunHint(err error) error {
	if e, ok := errors.AsType[*output.Error](err); ok && e.Code == string(apitypes.CodeEnvironmentLocked) {
		e.Hint += " Approved, the command runs in the background, without a terminal: `dock runs` shows it. To use a terminal, ask an admin to open the environment for a while."
	}
	return err
}

// timeoutSeconds is --timeout in whole seconds, rounded up; 0 for the
// server's default.
func timeoutSeconds(d time.Duration) int { return int((d + time.Second - 1) / time.Second) }

// streamRun runs command on t's service over the run stream and returns
// the run as it ended. Output goes to stdout and stderr (stdout to stderr
// with --json); with o.stdin, stdin goes to the command; with o.tty and a
// terminal on stdin, the command gets a terminal of its size and the local
// one is in raw mode until it ends. Ctrl-C (ctx ending) closes the
// connection, which stops the command, and is exitInterrupted.
func (g *globals) streamRun(ctx context.Context, s *session, t target, command string, o runOptions) (apitypes.CommandRun, error) {
	fd, tty := -1, false
	if o.tty {
		if f, ok := g.io.In.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
			fd, tty = int(f.Fd()), true
		} else {
			g.io.Warnf("stdin is not a terminal, so the command runs without one")
		}
	}
	q := url.Values{"command": {command}, "tty": {strconv.FormatBool(tty)}, "stdin": {strconv.FormatBool(o.stdin)}}
	if o.timeout > 0 {
		q.Set("timeout_seconds", strconv.Itoa(timeoutSeconds(o.timeout)))
	}
	if o.volumes != "" {
		q.Set("volumes", o.volumes)
	}
	if tty {
		if cols, rows, err := term.GetSize(fd); err == nil {
			q.Set("cols", strconv.Itoa(cols))
			q.Set("rows", strconv.Itoa(rows))
		}
	}
	conn, err := s.api.Upgrade(ctx, t.servicePath()+"/run?"+q.Encode(), apitypes.RunProtocol)
	if err != nil {
		return apitypes.CommandRun{}, err
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	fw := runframe.NewWriter(conn)
	if tty {
		// Raw only with input: without it keys never reach the command,
		// and Ctrl-C has to stay a local interrupt.
		if o.stdin {
			if state, err := term.MakeRaw(fd); err == nil {
				defer func() { _ = term.Restore(fd, state) }()
			}
		}
		defer watchResize(fd, func(cols, rows uint16) { _ = fw.Resize(cols, rows) })()
	}
	if o.stdin {
		// Never joined: a terminal's read only ends with the process.
		go func() {
			_, _ = io.Copy(fw.Stream(runframe.Stdin), g.io.In)
			_ = fw.Frame(runframe.CloseStdin, nil)
		}()
	}

	stdout := g.io.Out
	if g.io.JSON {
		stdout = g.io.Err
	}
	fr := runframe.NewReader(conn)
	for {
		typ, p, err := fr.Next()
		if err != nil {
			if ctx.Err() != nil {
				return apitypes.CommandRun{}, exitInterrupted
			}
			return apitypes.CommandRun{}, &output.Error{Exit: output.ExitUnreachable, Message: "the connection to the server ended before the command did",
				Hint: "`dock runs` shows how it ended.", Err: err}
		}
		switch typ {
		case runframe.Stdout:
			_, _ = stdout.Write(p)
		case runframe.Stderr:
			_, _ = g.io.Err.Write(p)
		case runframe.Exit:
			var r apitypes.CommandRun
			if err := json.Unmarshal(p, &r); err != nil {
				return r, &output.Error{Exit: output.ExitIncompatible, Message: "the server sent a run this CLI can't read", Hint: "Install the CLI matching the server: `dock version`.", Err: err}
			}
			return r, nil
		}
	}
}

// runExit is how `dock run` exits for a run that ended: with the command's
// exit code when it exited, otherwise with an error saying how it ended.
func runExit(r apitypes.CommandRun) error {
	if r.ExitCode != nil {
		if *r.ExitCode == 0 {
			return nil
		}
		return exitStatus(*r.ExitCode)
	}
	hint := map[apitypes.RunStatus]string{
		apitypes.RunTimedOut:    "Give it longer with --timeout (at most 24h).",
		apitypes.RunCanceled:    "Run it again: a run stops when `dock run` ends.",
		apitypes.RunInterrupted: "Run it again once the server is back: `dock status`.",
	}[r.Status]
	if hint == "" {
		hint = "Check that the service has a release (`dock status`) and its server is online (`dock server list`)."
	}
	msg := strings.ReplaceAll(string(r.Status), "_", " ")
	if r.Error != "" {
		msg += ": " + r.Error
	}
	return &output.Error{Exit: output.ExitError, Code: string(r.Status), Message: fmt.Sprintf("run %d %s", r.ID, msg), Hint: hint}
}

// shellJoin is the command `dock run -- args...` sends: one argument as it
// is, several each quoted for the shell when needed and joined by spaces.
func shellJoin(args []string) string {
	if len(args) == 1 {
		return args[0]
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

// shellQuote leaves a word of only safe characters as it is and puts any
// other in single quotes, ending them around each embedded quote:
//
//	it's  ->  'it'\''s'
func shellQuote(s string) string {
	safe := func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:,+@%", r)
	}
	if s != "" && !strings.ContainsFunc(s, func(r rune) bool { return !safe(r) }) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// newRunsCmd is `dock runs [service]` and `dock runs show <id>`.
func newRunsCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "runs [service]",
		Short: "List a service's one-off commands, newest first",
		Long: `List the latest commands ` + "`dock run`" + `, the UI and agents ran on a service (the
newest 50), newest first. ` + "`dock runs show <id>`" + ` adds the tail of a run's output.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeServiceArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			serviceArg(&f, args)
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			runs, err := get[[]apitypes.CommandRun](cmd.Context(), s, t.servicePath()+"/runs")
			if err != nil {
				return err
			}
			return g.io.Print(runs, func() error {
				if len(runs) == 0 {
					g.io.Successf("%s has no runs yet. Run one with `dock run %s -- <command>`.", t, t.flags())
					return nil
				}
				tb := g.io.Table("id", "status", "exit", "command", "by", "started", "duration")
				for _, r := range runs {
					tb.Row(itoa(r.ID), string(r.Status), runExitCode(r), cut(oneLine(r.Command), 60), r.CreatedBy, ago(r.StartedAt), runDuration(r))
				}
				return tb.Flush()
			})
		},
	}
	f.add(cmd, needService)
	cmd.AddCommand(newRunsShowCmd(g))
	return cmd
}

// newRunsShowCmd is `dock runs show <id>`: one run with its output's tail.
func newRunsShowCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show a run with the tail of its output",
		Long: `Show one run: its command, how it ended and the last 16 KB of its output
(none for terminal sessions, whose output holds what was typed). Needs
deploy scope, as output can show the service's variables.`,
		Example:           `  dock runs show 12 -s web`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeRunArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || id <= 0 {
				return &output.Error{Exit: output.ExitUsage, Message: fmt.Sprintf("%q is not a run ID", args[0]), Hint: "`dock runs` lists them."}
			}
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			r, err := get[apitypes.CommandRun](cmd.Context(), s, t.servicePath()+"/runs/"+itoa(id))
			if err != nil {
				return err
			}
			return g.io.Print(r, func() error { return printRun(g, r) })
		},
	}
	f.add(cmd, needService)
	return cmd
}

// printRun is `dock runs show`'s human output: the run's fields, then its
// output as it was.
func printRun(g *globals, r apitypes.CommandRun) error {
	how := ""
	if r.TTY {
		how = "terminal"
	}
	if err := g.io.Fields(
		"run", fmt.Sprintf("%d of %s/%s/%s", r.ID, r.Project, r.Environment, r.Service),
		"status", string(r.Status),
		"exit code", runExitCode(r),
		"error", r.Error,
		"command", r.Command,
		"release", itoa(r.Release),
		"volumes", string(r.Volumes),
		"timeout", (time.Duration(r.TimeoutSeconds) * time.Second).String(),
		"session", how,
		"started", ago(r.StartedAt)+" by "+r.CreatedBy,
		"duration", runDuration(r),
	); err != nil {
		return err
	}
	switch {
	case r.Output != "":
		_, err := io.WriteString(g.io.Out, "\n"+strings.TrimSuffix(r.Output, "\n")+"\n")
		return err
	case r.TTY:
		g.io.Detail("Terminal sessions keep no output.")
	}
	return nil
}

// runExitCode is a run's exit code for tables, "-" when it didn't exit.
func runExitCode(r apitypes.CommandRun) string {
	if r.ExitCode == nil {
		return "-"
	}
	return strconv.Itoa(*r.ExitCode)
}

// runDuration is how long a run took, or has been running.
func runDuration(r apitypes.CommandRun) string {
	end := time.Now()
	if r.FinishedAt != nil {
		end = *r.FinishedAt
	}
	return end.Sub(r.StartedAt).Round(time.Second).String()
}
