package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// Moving an environment to another server (ADR 0053): `dock environment
// move`, also registered under `dock env`.

// newEnvironmentMoveCmd is `dock environment move [<environment>] --to
// <server>`, or `--status` for the running or latest move.
func newEnvironmentMoveCmd(g *globals) *cobra.Command {
	var f targetFlags
	var to string
	var status, detach, yes bool
	cmd := &cobra.Command{
		Use:   "move [<environment>] --to <server> [--detach] [--yes] | --status",
		Short: "Move an environment to another server",
		Long: `Move an environment, with its services, volumes' data and domains, to
another server. Everything is checked first (the server accepts the
environment's kind, both servers are online, memory, disk, images and
Cloudflare tunnels) and a move that can't work is refused before anything
changes.

Stateless services keep serving on the old server until the switch.
Stateful services stop while their volumes copy, so they are down for the
copy plus a start: move at a quiet time. A failure before the switch rolls
back and leaves the environment where it was. Domains Dock routes through
Cloudflare follow by themselves; other domains need their DNS pointed at
the new server, and the move lists them when it ends. The old volumes stay
on the old server until you remove them.

Waits for the move, printing each step (--detach returns once it started).
--status shows the running or latest move. Operators only, and the
environment's lock applies.`,
		Example: `  dock env move staging --to homelab
  dock env move production --to hetzner-2 --yes --detach
  dock env move production --status`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if status == (to != "") {
				return usageError("pass --to <server> to move, or --status to see the latest move", "List servers with `dock server list`.")
			}
			t, s, err := lockTarget(g, f, args)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			path := t.envPath() + "/move"
			if status {
				m, err := get[apitypes.EnvironmentMove](ctx, s, path)
				if err != nil {
					return err
				}
				return g.io.Print(m, func() error { return g.printMove(m) })
			}
			if !yes {
				q := fmt.Sprintf("Move %s/%s to %s? Its stateful services stop while their volumes copy; domains not routed through Cloudflare need their DNS changed after.", t.Project, t.Environment, to)
				if err := g.io.Confirm(q, "yes"); err != nil {
					return err
				}
			}
			var w *watcher
			if !detach {
				if w, err = g.watch(ctx, s, target{Project: t.Project, Environment: t.Environment}); err != nil {
					return err
				}
				defer w.close()
			}
			m, err := send[apitypes.EnvironmentMove](ctx, s, http.MethodPost, path, apitypes.MoveEnvironmentRequest{To: to})
			if err != nil {
				return err
			}
			if detach {
				return g.io.Print(m, func() error {
					g.io.Successf("Moving %s/%s from %s to %s", m.Project, m.Environment, m.From, m.To)
					g.io.Progressf("Follow it with `dock env move %s --status`.", m.Environment)
					return nil
				})
			}
			g.io.Progressf("Moving %s/%s from %s to %s", m.Project, m.Environment, m.From, m.To)
			if m, err = w.followMove(ctx, m); err != nil {
				if errors.Is(err, context.Canceled) {
					return &output.Error{
						Exit: output.ExitError, Message: "stopped following; the move continues on the server",
						Hint: fmt.Sprintf("Check on it with `dock env move %s --status`.", m.Environment),
					}
				}
				return err
			}
			if err := g.io.Print(m, func() error { return g.printMoveEnd(m) }); err != nil {
				return err
			}
			return moveFailure(m)
		},
	}
	f.add(cmd, needEnvironment)
	cmd.Flags().StringVar(&to, "to", "", "the server to move to (see dock server list)")
	cmd.Flags().BoolVar(&status, "status", false, "show the running or latest move instead of starting one")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	addDetachFlag(cmd, &detach)
	return cmd
}

// moveEnded says whether a move with status s is over.
func moveEnded(s apitypes.MoveStatus) bool {
	return s == apitypes.MoveSucceeded || s == apitypes.MoveFailed
}

// followMove prints a move's progress from the event stream until it
// ends, and returns it as it ended. Each event carries the whole move;
// falling behind the server fetches it once. A lost stream ends following
// with a hint, since the move goes on regardless.
func (w *watcher) followMove(ctx context.Context, m apitypes.EnvironmentMove) (apitypes.EnvironmentMove, error) {
	p := moveProgress{io: w.g.io, step: m.Step, status: map[string]string{}}
	p.show(m)
	for !moveEnded(m.Status) {
		select {
		case <-ctx.Done():
			return m, ctx.Err()
		case ev, ok := <-w.events:
			if !ok {
				return m, &output.Error{
					Exit: output.ExitUnreachable, Message: "lost the live event stream; the move continues on the server",
					Hint: fmt.Sprintf("Check on it with `dock env move %s --status`.", m.Environment),
				}
			}
			switch ev.Type {
			case apitypes.EventEnvironmentMove:
				if ev.Move != nil && ev.Move.ID == m.ID {
					m = *ev.Move
					p.show(m)
				}
			case apitypes.EventLagged:
				t := target{Project: m.Project, Environment: m.Environment}
				if got, err := get[apitypes.EnvironmentMove](ctx, w.s, t.envPath()+"/move"); err == nil && got.ID == m.ID {
					m = got
					p.show(m)
				}
			}
		}
	}
	return m, nil
}

// moveProgress prints what changed in a move since it last saw it: each
// new step once, and each service's new status once.
type moveProgress struct {
	io          *output.IO
	step        apitypes.MoveStep
	status      map[string]string
	rollingBack bool
}

// show prints m's step and service changes.
func (p *moveProgress) show(m apitypes.EnvironmentMove) {
	if m.Step != p.step {
		p.step = m.Step
		if line := moveStepWords(m); line != "" {
			p.io.Progressf("%s: %s", m.Step, line)
		}
	}
	for _, sv := range m.Services {
		if p.status[sv.Service] == sv.Status {
			continue
		}
		p.status[sv.Service] = sv.Status
		switch sv.Status {
		case "waiting":
		case "failed":
			p.io.Failf("%s: %s failed: %s", m.Step, sv.Service, sv.Error)
		case "copied":
			p.io.Progressf("%s: %s %s", m.Step, sv.Service, bytesize(sv.CopiedBytes))
		default:
			p.io.Progressf("%s: %s %s", m.Step, sv.Service, sv.Status)
		}
	}
	if m.Status == apitypes.MoveRollingBack && !p.rollingBack {
		p.rollingBack = true
		p.io.Warnf("Rolling back: %s", m.Error)
	}
}

// moveStepWords says what a step does, for its progress line.
func moveStepWords(m apitypes.EnvironmentMove) string {
	switch m.Step {
	case apitypes.MovePrepare:
		return "images, network and volumes on " + m.To
	case apitypes.MoveStop:
		return "stateful services stop on " + m.From + "; the downtime starts"
	case apitypes.MoveCopy:
		return "volumes stream to " + m.To
	case apitypes.MoveStart:
		return "services start on " + m.To
	case apitypes.MoveSwitch:
		return "routes, domains and placement point at " + m.To
	case apitypes.MoveCleanup:
		return "old containers go from " + m.From
	}
	return ""
}

// downtimeWords is a move's downtime: "none", "since 14:02:11" while it
// lasts, or how long it was.
func downtimeWords(m apitypes.EnvironmentMove) string {
	switch {
	case m.DowntimeFrom == nil:
		return "none"
	case m.DowntimeTo == nil:
		return "since " + m.DowntimeFrom.Local().Format(time.TimeOnly)
	}
	d := m.DowntimeTo.Sub(*m.DowntimeFrom)
	if d < 10*time.Second {
		return d.Round(100 * time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

// printMoveEnd is a followed move's human output once it ended: the
// result, the downtime and what is left to do. A failure is printed by
// moveFailure.
func (g *globals) printMoveEnd(m apitypes.EnvironmentMove) error {
	if m.Status != apitypes.MoveSucceeded {
		return nil
	}
	g.io.Successf("Moved %s/%s from %s to %s; downtime %s", m.Project, m.Environment, m.From, m.To, downtimeWords(m))
	g.printMoveNotes(m)
	return nil
}

// printMove is `--status`'s human output: the move, each service's
// progress and what is left to do.
func (g *globals) printMove(m apitypes.EnvironmentMove) error {
	if err := g.io.Fields(
		"move", itoa(m.ID),
		"environment", m.Project+"/"+m.Environment,
		"from", m.From,
		"to", m.To,
		"status", string(m.Status),
		"step", string(m.Step),
		"downtime", downtimeWords(m),
		"started", ago(m.StartedAt)+" by "+m.CreatedBy,
		"finished", agoPtr(m.FinishedAt),
		"error", m.Error,
	); err != nil {
		return err
	}
	if len(m.Services) > 0 {
		_, _ = fmt.Fprintln(g.io.Out)
		t := g.io.Table("service", "stateful", "status", "copied", "error")
		for _, sv := range m.Services {
			stateful, copied := "no", "-"
			if sv.Stateful {
				stateful, copied = "yes", bytesize(sv.CopiedBytes)
			}
			t.Row(sv.Service, stateful, sv.Status, copied, sv.Error)
		}
		if err := t.Flush(); err != nil {
			return err
		}
	}
	g.printMoveNotes(m)
	return nil
}

// printMoveNotes lists what is left to do by hand after a move.
func (g *globals) printMoveNotes(m apitypes.EnvironmentMove) {
	if len(m.Notes) == 0 {
		return
	}
	_, _ = fmt.Fprintln(g.io.Out, "\nLeft to do:")
	for _, n := range m.Notes {
		_, _ = fmt.Fprintln(g.io.Out, "  - "+n)
	}
}

// moveFailure is the error for a failed move, or nil.
func moveFailure(m apitypes.EnvironmentMove) error {
	if m.Status != apitypes.MoveFailed {
		return nil
	}
	return &output.Error{
		Exit:    output.ExitError,
		Message: fmt.Sprintf("moving %s/%s to %s failed: %s", m.Project, m.Environment, m.To, m.Error),
		Hint:    fmt.Sprintf("It was rolled back: %s/%s is still on %s. See `dock env move %s --status`.", m.Project, m.Environment, m.From, m.Environment),
	}
}
