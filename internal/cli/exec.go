package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// newExecCmd is `dock exec [service] [-- <command...>]` (ADR 0111): a
// shell or a command inside the service's running container.
func newExecCmd(g *globals) *cobra.Command {
	var (
		f          targetFlags
		stdin, tty bool
	)
	cmd := &cobra.Command{
		Use:   "exec [service] [flags] [-- <command...>]",
		Short: "Open a shell in a service's running container",
		Long: `Open a shell, or run a command, inside the container that serves the
service's active release: its files, processes and variables as they are
now. Without a command it is a shell (bash where the image has one, sh
otherwise) with a terminal and stdin. ` + "`dock run`" + ` starts a new container instead,
which leaves what serves traffic alone and works with the service stopped.

The command is everything after --, run as it is, without a shell (as
` + "`docker exec`" + ` does): ` + "`dock exec web -- sh -c 'ls | wc -l'`" + ` for shell syntax. -i sends
stdin to it and -t gives it a terminal when stdin is one; a command gets
neither by default. Ctrl-C (unless -it, where it goes to the command)
ends the session. When a session ends, Dock stops what it started in the
container where it can (the image needs sh, tr and grep).

Needs the shell permission on the environment (Full access, or the Deploy
area's shell level; admin with a plain scope). Every session is
in the audit log (service.exec); nothing of what it shows is kept. A
locked environment refuses tokens, ` + "`dock login`" + `'s included: ask an approver
there to open it for a while. The exit code is the command's.`,
		Example: `  dock exec web
  dock exec -s api -- ls -la /app
  dock exec -it web -- bin/rails console
  dock exec web -- sh -c 'du -sh /app/storage/*'`,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeServiceArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			before, command := args, []string(nil)
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				before, command = args[:dash], args[dash:]
			}
			if len(before) > 1 {
				return &output.Error{Exit: output.ExitUsage, Message: fmt.Sprintf("only the service goes before --, got %s", strings.Join(before, " ")),
					Hint: "Put the command after --: `dock exec web -- " + strings.Join(before[1:], " ") + "`."}
			}
			if len(command) == 0 {
				// A shell is interactive.
				stdin, tty = true, true
			}
			serviceArg(&f, before)
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			fd, hasTTY := g.terminal(tty)
			q := url.Values{"cmd": command, "tty": {strconv.FormatBool(hasTTY)}, "stdin": {strconv.FormatBool(stdin)}}
			raw, err := g.streamFrames(cmd.Context(), s, t.servicePath()+"/exec", apitypes.ExecProtocol, q, fd, hasTTY, stdin,
				"Open the session again; `dock status` shows whether the service and its server are up.")
			if err != nil {
				return err
			}
			var end apitypes.ExecEnd
			if err := json.Unmarshal(raw, &end); err != nil {
				return &output.Error{Exit: output.ExitIncompatible, Message: "the server sent a session end this CLI can't read", Hint: "Run `dock self-update` to install the CLI that matches the server.", Err: err}
			}
			if err := g.io.Print(end, func() error { return nil }); err != nil {
				return err
			}
			return execExit(end)
		},
	}
	f.add(cmd, needService)
	cmd.Flags().BoolVarP(&stdin, "interactive", "i", false, "send stdin to the command (always for a shell)")
	cmd.Flags().BoolVarP(&tty, "tty", "t", false, "give the command a terminal when stdin is one (always for a shell)")
	return cmd
}

// execExit is how `dock exec` exits: with the command's exit code, or an
// error saying why the session ended without one.
func execExit(end apitypes.ExecEnd) error {
	if end.ExitCode != nil {
		if *end.ExitCode == 0 {
			return nil
		}
		return exitStatus(*end.ExitCode)
	}
	e := &output.Error{Exit: output.ExitError, Code: string(end.Code), Message: "the session ended: " + end.Error}
	switch end.Code {
	case apitypes.CodeConflict:
		e.Exit, e.Hint = output.ExitConflict, "`dock status` shows the service's state; `dock run` works with it stopped."
	case apitypes.CodeServerOffline:
		e.Exit, e.Hint = output.ExitUnreachable, "`dock server list` shows whether its server is online."
	case apitypes.CodeForbidden:
		e.Exit = output.ExitAuth
	case apitypes.CodeEnvironmentLocked:
		// Locked after the session was opened, before it started.
		e.Exit, e.Hint = output.ExitConflict, "Ask an approver there to open the environment for a while."
	}
	return e
}
