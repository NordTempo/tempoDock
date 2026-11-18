package cli

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// The outside heartbeat (ADR 0097): `dock alerts heartbeat`, for
// operators.

const heartbeatAPI = apiPrefix + "/alerts/heartbeat"

// heartbeatRow prints a heartbeat as a one-row table.
func heartbeatRow(g *globals, hb apitypes.Heartbeat) error {
	failing := "-"
	if hb.Failures > 0 {
		failing = fmt.Sprintf("%d: %s", hb.Failures, hb.LastError)
	}
	t := g.io.Table("target", "every", "last sent", "failing")
	t.Row(hb.Target, fmt.Sprintf("%d min", hb.Minutes), agoPtr(hb.LastSentAt), failing)
	return t.Flush()
}

func newAlertHeartbeatCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "heartbeat",
		Short: "The outside heartbeat: a URL Dock pings so silence means it is down (operators)",
		Long: `Show the outside heartbeat and how its last pings went.

Every alert is sent by the control plane, so a control plane that is down
sends none. With a heartbeat set, Dock sends a GET to one URL every few
minutes while it runs. Point it at a dead man's switch: a monitor outside
this server that raises the alarm when the pings stop.

The URL is a secret (it names the check): it is stored encrypted and only
its scheme and host are shown. Three failed pings in a row open a
platform alert; the next ping that works resolves it.`,
		Example: `  dock alerts heartbeat set - < heartbeat-url
  dock alerts heartbeat set https://monitor.example.com/ping/3f2a --every 2
  dock alerts heartbeat test`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			hb, err := get[apitypes.Heartbeat](cmd.Context(), s, heartbeatAPI)
			if err != nil {
				return err
			}
			return g.io.Print(hb, func() error { return heartbeatRow(g, hb) })
		},
	}
	cmd.AddCommand(newAlertHeartbeatSetCmd(g), newAlertHeartbeatRemoveCmd(g), newAlertHeartbeatTestCmd(g))
	return cmd
}

func newAlertHeartbeatSetCmd(g *globals) *cobra.Command {
	var every int
	cmd := &cobra.Command{
		Use:   "set <url|-> [--every 5]",
		Short: "Set the URL Dock pings, and how often (operators)",
		Long: `Set or replace the outside heartbeat. Dock sends a GET to the URL every
--every minutes, starting within a minute.

The URL holds the check's ID: pass - to read it from stdin instead of the
command line.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			u, err := g.stdinIfDash(args[0])
			if err != nil {
				return err
			}
			if every < 1 || every > 60 {
				return usageError("--every takes 1 to 60 minutes", "Run `dock alerts heartbeat set --help` for examples.")
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			hb, err := send[apitypes.Heartbeat](cmd.Context(), s, http.MethodPut, heartbeatAPI, apitypes.SetHeartbeatRequest{URL: u, Minutes: every})
			if err != nil {
				return err
			}
			return g.io.Print(hb, func() error {
				g.io.Successf("Heartbeat set: a ping goes to %s every %d min", hb.Target, hb.Minutes)
				_, _ = fmt.Fprintln(g.io.Out, "Send one now: dock alerts heartbeat test")
				return nil
			})
		},
	}
	cmd.Flags().IntVar(&every, "every", 5, "minutes between pings, 1 to 60")
	return cmd
}

func newAlertHeartbeatRemoveCmd(g *globals) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Stop pinging and forget the URL (operators)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes {
				if err := g.io.Confirm("Remove the outside heartbeat? Pings stop, and whoever watches them raises the alarm unless the check is removed there too.", "yes"); err != nil {
					return err
				}
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, heartbeatAPI); err != nil {
				return err
			}
			return g.io.Print(map[string]string{"removed": "heartbeat"}, func() error {
				g.io.Successf("Removed the outside heartbeat")
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

func newAlertHeartbeatTestCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "test",
		Short: "Send one ping now and say how it went (operators)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			res, err := send[apitypes.HeartbeatTest](cmd.Context(), s, http.MethodPost, heartbeatAPI+"/test", nil)
			if err != nil {
				return err
			}
			if err := g.io.Print(res, func() error {
				if res.OK {
					g.io.Successf("Sent a ping to %s", res.Heartbeat.Target)
				}
				return nil
			}); err != nil {
				return err
			}
			if !res.OK {
				return &output.Error{Exit: output.ExitUnreachable, Message: "the heartbeat ping failed: " + res.Error,
					Hint: "Check that the URL still exists at its receiver; `dock alerts heartbeat set <url>` replaces it."}
			}
			return nil
		},
	}
}
