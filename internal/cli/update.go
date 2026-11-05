package cli

import (
	"context"
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

// Updating Dock itself (ADR 0028): the console's Updates, from the CLI.

const (
	// updatePoll is how often `dock update apply` asks how the update is
	// going, while it waits.
	updatePoll = 2 * time.Second
	// updateWait bounds that wait: the control plane's update and every
	// node's, one at a time.
	updateWait = 30 * time.Minute
)

func newUpdateCmd(g *globals) *cobra.Command {
	var refresh bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Check for a newer Dock and update the platform (operator)",
		Long: `Show the version the platform runs, the releases newer than it with their
notes, and each server's version. The check is reused for 15 minutes;
--refresh checks now. Update with ` + "`dock update apply`" + `.

This updates the platform (the control plane and its nodes), not this CLI;
` + "`dock version`" + ` says how to get the CLI in step.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			path := apiPrefix + "/update"
			if refresh {
				path += "?" + url.Values{"refresh": {"true"}}.Encode()
			}
			st, err := get[apitypes.UpdateStatus](cmd.Context(), s, path)
			if err != nil {
				return err
			}
			return g.io.Print(st, func() error { return printUpdateStatus(g, st, true) })
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false, "check the release source now")
	cmd.AddCommand(newUpdateApplyCmd(g))
	return cmd
}

func newUpdateApplyCmd(g *globals) *cobra.Command {
	var detach bool
	cmd := &cobra.Command{
		Use:   "apply [VERSION]",
		Short: "Update the control plane, then every node behind it (operator)",
		Long: `Update the platform to VERSION, the newest release when left out. The
control plane downloads it and checks it against the release's SHA256SUMS,
backs itself up, swaps its binary and units and restarts; if the new version
isn't healthy within 90 seconds it rolls back, database included. Then it
updates every node behind it over its link, one at a time. Apps keep running
throughout; the API is down for a few seconds while the control plane
restarts.

When the control plane already runs VERSION, only the nodes behind it are
updated (those offline last time, for example).

It waits until everything is done; --detach returns once the update started,
and ` + "`dock update`" + ` shows how it went.`,
		Example: `  dock update apply
  dock update apply v0.4.0 --detach`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			var req apitypes.StartUpdateRequest
			if len(args) == 1 {
				req.Version = args[0]
			}
			st, err := send[apitypes.UpdateStatus](cmd.Context(), s, http.MethodPost, apiPrefix+"/update/apply", req)
			if err != nil {
				return err
			}
			if st.Running == nil {
				return g.io.Print(st, func() error {
					g.io.Successf("Up to date: every server runs %s", st.Version)
					return nil
				})
			}
			if detach {
				return g.io.Print(st, func() error {
					g.io.Successf("Updating to %s", runningVersion(st))
					g.io.Detail("It goes on in the background; `dock update` shows how it went.")
					return nil
				})
			}
			target := st.Running.Version
			g.io.Progressf("Updating to %s; the API restarts on the way...", target)
			st, err = waitUpdate(cmd.Context(), g, s, st)
			if err != nil {
				return err
			}
			if err := g.io.Print(st, func() error { return printUpdateStatus(g, st, false) }); err != nil {
				return err
			}
			return updateOutcome(st, target)
		},
	}
	cmd.Flags().BoolVar(&detach, "detach", false, "return once the update started")
	return cmd
}

func runningVersion(st apitypes.UpdateStatus) string {
	if st.Running != nil {
		return st.Running.Version
	}
	return st.Version
}

// waitUpdate polls the update status until no update runs. While the
// control plane restarts its API doesn't answer; those errors are waited
// out.
func waitUpdate(ctx context.Context, g *globals, s *session, st apitypes.UpdateStatus) (apitypes.UpdateStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, updateWait)
	defer cancel()
	tick := time.NewTicker(updatePoll)
	defer tick.Stop()
	stage := st.Running.Stage
	for st.Running != nil {
		select {
		case <-ctx.Done():
			return st, fmt.Errorf("the update hasn't finished after %s; `dock update` shows where it is: %w", updateWait, ctx.Err())
		case <-tick.C:
		}
		next, err := get[apitypes.UpdateStatus](ctx, s, apiPrefix+"/update")
		if err != nil {
			continue // restarting
		}
		st = next
		if st.Running != nil && st.Running.Stage != stage {
			stage = st.Running.Stage
			switch stage {
			case apitypes.UpdateApplying:
				g.io.Progressf("Verified; the control plane is updating itself...")
			case apitypes.UpdateServers:
				g.io.Progressf("The control plane runs %s; updating the nodes...", st.Version)
			}
		}
	}
	return st, nil
}

// updateOutcome is an error when the update to target didn't get
// everywhere it should have.
func updateOutcome(st apitypes.UpdateStatus, target string) error {
	if st.Version != target {
		msg := "the control plane didn't update"
		if st.Last != nil && st.Last.To == target {
			msg = fmt.Sprintf("updating the control plane to %s %s: %s", target, strings.ReplaceAll(st.Last.Status, "_", " "), st.Last.Error)
		}
		return &output.Error{Exit: output.ExitError, Message: msg}
	}
	var failed []string
	for _, s := range st.Servers {
		if s.State == "failed" {
			failed = append(failed, s.Name)
		}
	}
	if len(failed) > 0 {
		return &output.Error{Exit: output.ExitError, Message: "updating " + strings.Join(failed, ", ") + " failed; `dock update` shows why"}
	}
	return nil
}

// printUpdateStatus prints the versions, the update running or last
// finished, each server and, with notes, the newer releases' notes.
func printUpdateStatus(g *globals, st apitypes.UpdateStatus, notes bool) error {
	latest := st.Latest
	switch {
	case latest == "":
		latest = "unknown"
	case st.Available:
		latest += " (newer)"
	default:
		latest += " (up to date)"
	}
	checked := ""
	if st.CheckedAt != nil {
		checked = ago(*st.CheckedAt)
	}
	running := ""
	if r := st.Running; r != nil {
		running = fmt.Sprintf("to %s, %s, asked by %s %s", r.Version, r.Stage, r.RequestedBy, ago(r.StartedAt))
	}
	last := ""
	if l := st.Last; l != nil {
		last = fmt.Sprintf("%s to %s %s %s", l.From, l.To, strings.ReplaceAll(l.Status, "_", " "), ago(l.FinishedAt))
	}
	if err := g.io.Fields(
		"version", st.Version,
		"latest", latest,
		"checked", checked,
		"source", st.Source,
		"updating", running,
		"last update", last,
	); err != nil {
		return err
	}
	if st.CheckError != "" {
		g.io.Warnf("%s", st.CheckError)
	}
	if l := st.Last; l != nil && l.Error != "" {
		g.io.Warnf("The last update %s: %s", strings.ReplaceAll(l.Status, "_", " "), l.Error)
	}
	if !st.Updater {
		g.io.Warnf("This control plane has no updater (dock-update.path). Update it by hand once with install.sh from the new release (docs/guide/install.md#upgrading); later updates can run from here.")
	}
	t := g.io.Table("server", "status", "version", "update")
	for _, s := range st.Servers {
		name := s.Name
		if s.Local {
			name += " (control plane)"
		}
		state := s.State
		switch {
		case s.Error != "":
			state += ": " + s.Error
		case s.Manual != "":
			state = "by hand: " + s.Manual
		case state == "" && s.Behind:
			state = "behind"
		}
		t.Row(name, string(s.Status), s.Version, state)
	}
	if err := t.Flush(); err != nil {
		return err
	}
	if notes && len(st.Releases) > 0 {
		for _, r := range st.Releases {
			text := fmt.Sprintf("\n%s  %s\n", r.Version, r.PublishedAt.Format("2006-01-02"))
			if n := strings.TrimSpace(r.Notes); n != "" {
				text += indent(n, "  ") + "\n"
			}
			if _, err := io.WriteString(g.io.Out, text); err != nil {
				return err
			}
		}
	}
	if st.Available && st.Running == nil {
		g.io.Detail("Update with `dock update apply` (or " + "`dock update apply " + st.Latest + "`).")
	}
	return nil
}

func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}
