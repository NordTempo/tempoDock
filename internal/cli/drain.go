package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// Draining a server (ADR 0062): `dock server drain`.

// newServerDrainCmd is `dock server drain <server>`: plan, confirm, start
// and follow a drain, or --status, --resume [--skip] and --cancel.
func newServerDrainCmd(g *globals) *cobra.Command {
	var kind string
	var targets []string
	var hold int
	var dryRun, status, resume, skip, cancel, detach, yes bool
	cmd := &cobra.Command{
		Use:   "drain <server> [--kind non-production] [--to <project>=<server>]... [--dry-run] | --status | --resume [--skip] | --cancel",
		Short: "Move every environment off a server, without downtime (operator)",
		Long: `Move every environment on a server, or every one of a kind, to other
servers, one at a time: before maintenance, to take a kind off a server, or
before removing it. The server stops taking new environments (of the kind)
at once.

Each move copies volumes while their services keep serving, then holds
requests for a few seconds while every service stops on the old server,
the last changes copy and are checked, and the new server starts. No write
is lost, and callers see a slow answer rather than an error. The old
server forwards what still reaches it (DNS caches, records Dock doesn't
manage) for as long as it is there.

Targets come from placement: for each project and kind, the online server
that accepts the kind with the most free memory. --to <project>=<server>
picks one. The plan is shown first and checked like a move (memory, disk,
images, tunnels); a plan with problems doesn't start. --dry-run only shows
it.

A move that fails leaves its environment where it was and pauses the
drain: --resume tries it again, --resume --skip leaves it and goes on.
--cancel stops after the move that runs.`,
		Example: `  dock server drain homelab --kind non-production
  dock server drain hetzner-1 --to shop=hetzner-2 --yes
  dock server drain hetzner-1 --status`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			path := apiPrefix + "/servers/" + url.PathEscape(args[0]) + "/drain"
			switch {
			case status:
				d, err := get[apitypes.ServerDrain](ctx, s, path)
				if err != nil {
					return err
				}
				return g.io.Print(d, func() error { return g.printDrain(d) })
			case cancel:
				d, err := send[apitypes.ServerDrain](ctx, s, http.MethodPost, path+"/cancel", nil)
				if err != nil {
					return err
				}
				return g.io.Print(d, func() error {
					g.io.Successf("The drain of %s stops after the move that runs", d.Server)
					return nil
				})
			case resume:
				d, err := send[apitypes.ServerDrain](ctx, s, http.MethodPost, path+"/resume", apitypes.ResumeDrainRequest{Skip: skip})
				if err != nil {
					return err
				}
				return g.followDrainOrPrint(ctx, s, path, d, detach)
			}
			req := apitypes.DrainServerRequest{Kind: apitypes.EnvironmentKind(strings.ReplaceAll(kind, "-", "_")), MaxHold: hold, DryRun: true}
			for _, t := range targets {
				project, to, ok := strings.Cut(t, "=")
				if !ok || project == "" || to == "" {
					return usageError(fmt.Sprintf("--to %q isn't <project>=<server>", t), "For example --to shop=hetzner-2.")
				}
				req.Targets = append(req.Targets, apitypes.DrainTarget{Project: project, To: to})
			}
			plan, err := send[apitypes.ServerDrain](ctx, s, http.MethodPost, path, req)
			if err != nil {
				return err
			}
			if dryRun || len(plan.Problems) > 0 {
				if err := g.io.Print(plan, func() error { return g.printDrain(plan) }); err != nil {
					return err
				}
				if len(plan.Problems) > 0 {
					return &output.Error{Exit: output.ExitError, Message: fmt.Sprintf("server %s can't drain as planned", args[0]),
						Hint: "Fix the problems above, or pick other targets with --to <project>=<server>."}
				}
				return nil
			}
			if len(plan.Items) == 0 {
				g.io.Progressf("Nothing to move off %s.", args[0])
			} else if !yes {
				if err := g.printDrain(plan); err != nil {
					return err
				}
				q := fmt.Sprintf("Move these %d environments off %s? Each holds its requests for a few seconds while it switches.", len(plan.Items), args[0])
				if err := g.io.Confirm(q, "yes"); err != nil {
					return err
				}
			}
			req.DryRun = false
			d, err := send[apitypes.ServerDrain](ctx, s, http.MethodPost, path, req)
			if err != nil {
				return err
			}
			return g.followDrainOrPrint(ctx, s, path, d, detach)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "drain only production or non-production environments")
	cmd.Flags().StringArrayVar(&targets, "to", nil, "send a project's environments to a server: <project>=<server> (repeatable)")
	cmd.Flags().IntVar(&hold, "max-hold", 0, "longest each move may hold requests, in seconds (default 30)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the plan and its problems, change nothing")
	cmd.Flags().BoolVar(&status, "status", false, "show the running or latest drain and the server's forwards")
	cmd.Flags().BoolVar(&resume, "resume", false, "go on with a paused drain, trying the failed environment again")
	cmd.Flags().BoolVar(&skip, "skip", false, "with --resume: leave the failed environment where it is")
	cmd.Flags().BoolVar(&cancel, "cancel", false, "stop the drain after the move that runs")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	addDetachFlag(cmd, &detach)
	return cmd
}

// followDrainOrPrint follows a started drain until it stops running, or
// with detach prints it as it is.
func (g *globals) followDrainOrPrint(ctx context.Context, s *session, path string, d apitypes.ServerDrain, detach bool) error {
	if detach || d.Status != apitypes.DrainRunning {
		return g.io.Print(d, func() error {
			g.io.Successf("Draining %s: %d environments", d.Server, len(d.Items))
			g.io.Progressf("Follow it with `dock server drain %s --status`.", d.Server)
			return nil
		})
	}
	w, err := g.watch(ctx, s, target{})
	if err != nil {
		return err
	}
	defer w.close()
	// Read again now the stream is open, so nothing is missed between.
	if d, err = get[apitypes.ServerDrain](ctx, s, path); err != nil {
		return err
	}
	g.io.Progressf("Draining %s: %d environments", d.Server, len(d.Items))
	seen := map[string]string{}
	show := func(d apitypes.ServerDrain) {
		for _, it := range d.Items {
			key := it.Project + "/" + it.Environment
			line := string(it.Status)
			if it.Move != nil && it.Status == apitypes.DrainMoving {
				line += " (" + string(it.Move.Step) + ")"
			}
			if seen[key] == line {
				continue
			}
			seen[key] = line
			switch it.Status {
			case apitypes.DrainMoved:
				g.io.Successf("%s moved to %s", key, it.To)
			case apitypes.DrainFailed:
				g.io.Failf("%s: %s", key, it.Error)
			case apitypes.DrainMoving:
				g.io.Progressf("%s → %s: %s", key, it.To, line)
			}
		}
	}
	show(d)
	for d.Status == apitypes.DrainRunning {
		select {
		case <-ctx.Done():
			return &output.Error{Exit: output.ExitError, Message: "stopped following; the drain continues on the server",
				Hint: fmt.Sprintf("Check on it with `dock server drain %s --status`.", d.Server)}
		case ev, ok := <-w.events:
			if !ok {
				return &output.Error{Exit: output.ExitUnreachable, Message: "lost the live event stream; the drain continues on the server",
					Hint: fmt.Sprintf("Check on it with `dock server drain %s --status`.", d.Server)}
			}
			switch {
			case ev.Type == apitypes.EventServerDrain && ev.Drain != nil && ev.Drain.ID == d.ID:
				d = *ev.Drain
			case ev.Type == apitypes.EventLagged:
				if got, err := get[apitypes.ServerDrain](ctx, s, path); err == nil {
					d = got
				}
			default:
				continue
			}
			show(d)
		}
	}
	if err := g.io.Print(d, func() error { return g.printDrainEnd(d) }); err != nil {
		return err
	}
	if d.Status == apitypes.DrainPaused {
		return &output.Error{Exit: output.ExitError, Message: fmt.Sprintf("the drain of %s paused: %s", d.Server, d.Error),
			Hint: fmt.Sprintf("Retry with `dock server drain %s --resume`, or leave it with --resume --skip.", d.Server)}
	}
	return nil
}

// printDrainEnd is a followed drain's result.
func (g *globals) printDrainEnd(d apitypes.ServerDrain) error {
	if d.Status == apitypes.DrainSucceeded {
		g.io.Successf("Drained %s", d.Server)
		if len(d.Forwards) > 0 {
			g.io.Progressf("It forwards %d services that moved away while it is there; `dock server drain %s --status` lists them.", len(d.Forwards), d.Server)
		}
	}
	return nil
}

// printDrain is a drain or plan as a table, its problems and forwards.
func (g *globals) printDrain(d apitypes.ServerDrain) error {
	if d.ID != 0 {
		if err := g.io.Fields(
			"drain", itoa(d.ID),
			"server", d.Server,
			"status", string(d.Status),
			"started", agoPtr(d.StartedAt)+" by "+d.CreatedBy,
			"finished", agoPtr(d.FinishedAt),
			"error", d.Error,
		); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(g.io.Out)
	}
	if len(d.Items) > 0 && d.ID == 0 { // a plan
		t := g.io.Table("environment", "kind", "to", "data", "problem")
		for _, it := range d.Items {
			data := "-"
			if it.Stateful && it.DataBytes > 0 {
				data = bytesize(it.DataBytes)
			}
			t.Row(it.Org+"/"+it.Project+"/"+it.Environment, kindWords(it.Kind), it.To, data, it.Problem)
		}
		if err := t.Flush(); err != nil {
			return err
		}
	} else if len(d.Items) > 0 {
		t := g.io.Table("environment", "kind", "to", "data", "status", "hold", "problem")
		for _, it := range d.Items {
			data, held := "-", "-"
			if it.Stateful && it.DataBytes > 0 {
				data = bytesize(it.DataBytes)
			}
			st := string(it.Status)
			if it.Move != nil {
				if it.Status == apitypes.DrainMoving {
					st += " (" + string(it.Move.Step) + ")"
				}
				if it.Move.DowntimeFrom != nil {
					held = downtimeWords(*it.Move)
				}
			}
			why := it.Problem
			if why == "" {
				why = it.Error
			}
			t.Row(it.Org+"/"+it.Project+"/"+it.Environment, kindWords(it.Kind), it.To, data, st, held, why)
		}
		if err := t.Flush(); err != nil {
			return err
		}
	}
	if len(d.Forwards) > 0 {
		_, _ = fmt.Fprintln(g.io.Out, "\nForwards (requests that still reach this server):")
		t := g.io.Table("service", "port", "now on", "since")
		for _, f := range d.Forwards {
			t.Row(f.Project+"/"+f.Environment+"/"+f.Service, itoa(f.Port), f.NowOn, ago(f.Since))
		}
		if err := t.Flush(); err != nil {
			return err
		}
	}
	return nil
}
