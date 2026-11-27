package cli

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Host checks (ADR 0109): `dock server checks`, read-only security checks
// of a server's host, for operators and platform viewers.

// newServerChecksCmd is `dock server checks`.
func newServerChecksCmd(g *globals) *cobra.Command {
	var now bool
	cmd := &cobra.Command{
		Use:   "checks <name>",
		Short: "Show a server's host security checks, and what to do about each (admin)",
		Long: `Show the last read-only security check of a server's host: SSH that takes
passwords or root, ports open to the internet that Dock didn't open, Docker's
API on TCP, containers holding the Docker socket, security updates and
reboots waiting, automatic updates and firewalls. Servers check about once
a day; --now checks again first (a few seconds). Findings that don't need
fixing can be dismissed with a reason (` + "`dock server checks dismiss`" + `).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			path := apiPrefix + "/servers/" + url.PathEscape(args[0]) + "/host-checks"
			var v apitypes.HostChecks
			if now {
				v, err = send[apitypes.HostChecks](cmd.Context(), s, "POST", path, nil)
			} else {
				v, err = get[apitypes.HostChecks](cmd.Context(), s, path)
			}
			if err != nil {
				return err
			}
			return g.io.Print(v, func() error { return printHostChecks(g, v) })
		},
	}
	cmd.Flags().BoolVar(&now, "now", false, "check the host again first")
	cmd.AddCommand(newServerChecksDismissCmd(g), newServerChecksRestoreCmd(g))
	return cmd
}

// printHostChecks prints a server's checks: findings with what to do,
// then a line per check that passed or couldn't tell.
func printHostChecks(g *globals, v apitypes.HostChecks) error {
	if v.CheckedAt == nil {
		_, _ = fmt.Fprintf(g.io.Out, "Server %s hasn't checked its host yet; `dock server checks %s --now` checks it.\n", v.Server, v.Server)
		return nil
	}
	_, _ = fmt.Fprintf(g.io.Out, "checked %s: %d critical, %d warnings, %d info\n", ago(*v.CheckedAt), v.Counts.Critical, v.Counts.Warning, v.Counts.Info)
	if v.Error != "" {
		_, _ = fmt.Fprintf(g.io.Out, "the last check failed: %s\n", v.Error)
	}
	_, _ = fmt.Fprintln(g.io.Out)
	t := g.io.Table("severity", "finding", "what", "fix")
	rest := g.io.Table("check", "status", "what")
	for _, c := range v.Checks {
		switch {
		case c.Status == apitypes.HostCheckFinding && c.Dismissed != nil:
			rest.Row(c.Key, "dismissed", c.Title+" ("+c.Dismissed.Reason+", "+c.Dismissed.By+")")
		case c.Status == apitypes.HostCheckFinding:
			what := c.Title
			if c.Detail != "" {
				what += ". " + c.Detail
			}
			t.Row(string(c.Severity), c.Key, what, dash(c.Fix))
		default:
			what := c.Title
			if c.Detail != "" {
				what += ". " + c.Detail
			}
			rest.Row(c.Key, string(c.Status), what)
		}
	}
	if err := t.Flush(); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(g.io.Out)
	return rest.Flush()
}

// newServerChecksDismissCmd is `dock server checks dismiss`.
func newServerChecksDismissCmd(g *globals) *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "dismiss <server> <finding> --reason <why>",
		Short: "Dismiss a host check finding that doesn't need fixing (admin)",
		Long: `Dismiss a finding by its key (` + "`dock server checks`" + ` lists them), with why it
doesn't need fixing: it stops counting and alerting until restored.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			v, err := send[apitypes.HostChecks](cmd.Context(), s, "POST", apiPrefix+"/servers/"+url.PathEscape(args[0])+"/host-checks/dismiss",
				apitypes.DismissHostFindingRequest{Finding: args[1], Reason: reason})
			if err != nil {
				return err
			}
			return g.io.Print(v, func() error {
				_, err := fmt.Fprintf(g.io.Out, "Dismissed %s on server %s.\n", args[1], args[0])
				return err
			})
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why it doesn't need fixing; other operators see it")
	_ = cmd.MarkFlagRequired("reason")
	return cmd
}

// newServerChecksRestoreCmd is `dock server checks restore`.
func newServerChecksRestoreCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "restore <server> <finding>",
		Short: "Undo a host check finding's dismissal (admin)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			v, err := send[apitypes.HostChecks](cmd.Context(), s, "POST", apiPrefix+"/servers/"+url.PathEscape(args[0])+"/host-checks/restore",
				apitypes.RestoreHostFindingRequest{Finding: args[1]})
			if err != nil {
				return err
			}
			return g.io.Print(v, func() error {
				_, err := fmt.Fprintf(g.io.Out, "Restored %s on server %s: it counts and alerts again.\n", args[1], args[0])
				return err
			})
		},
	}
}
