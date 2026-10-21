package cli

import (
	"context"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Staged changes, Railway style: variables, settings and volumes are staged
// next to the live values and go live with `dock apply` (or a deploy of the
// service, for its own changes).

// stageOpts are the options of a command that stages a change.
type stageOpts struct {
	// what describes the change for humans, e.g. "Set A, B on web".
	what string
	// deploy applies the environment's staged changes too (--deploy).
	deploy, detach bool
}

// stage runs a staging request and reports the result. With --deploy the
// environment's events are watched first and the resulting releases are
// followed.
func (g *globals) stage(ctx context.Context, s *session, t target, o stageOpts, call func() (apitypes.ChangeResult, error)) error {
	var w *watcher
	if o.deploy && !o.detach {
		var err error
		if w, err = g.watch(ctx, s, target{Project: t.Project, Environment: t.Environment}); err != nil {
			return err
		}
		defer w.close()
	}
	res, err := call()
	if err != nil {
		return err
	}
	if res.Applied == nil {
		return g.io.Print(res, func() error {
			if o.what != "" {
				g.io.Successf("%s (staged)", o.what)
			}
			g.io.Progressf("%s in %s/%s. Apply with `dock apply`%s.", plural(res.Pending.Count, "staged change"), t.Project, t.Environment, nextDeployOf(t, res.Pending))
			return nil
		})
	}
	if o.what != "" && !g.io.JSON {
		g.io.Successf("%s", o.what)
	}
	return g.finishApplied(ctx, s, w, *res.Applied, func(a apitypes.AppliedChanges) any {
		res.Applied = &a
		return res
	})
}

// nextDeployOf mentions that a service's own staged changes also go live
// with its next deploy.
func nextDeployOf(t target, p apitypes.PendingChanges) string {
	if t.Service == "" || p.CountFor(t.Service) == 0 {
		return ""
	}
	return fmt.Sprintf(" (%s's own changes also go live with its next `dock deploy`)", t.Service)
}

// finishApplied follows the releases an apply started (unless w is nil, for
// --detach) and prints the result; view builds the JSON value from the
// final AppliedChanges. Any failed release makes it a deploy_failed error.
func (g *globals) finishApplied(ctx context.Context, s *session, w *watcher, a apitypes.AppliedChanges, view func(apitypes.AppliedChanges) any) error {
	if w != nil && len(a.Releases) > 0 {
		rels, err := w.follow(ctx, a.Releases)
		if err != nil {
			return interrupted(err, rels)
		}
		a.Releases = rels
	}
	err := g.io.Print(view(a), func() error {
		if len(a.Releases) == 0 && len(a.Skipped) == 0 {
			g.io.Successf("Applied. No running service needed a new release.")
		}
		for _, r := range a.Releases {
			switch r.Status {
			case apitypes.ReleaseActive:
				g.io.Successf("%s: release %d is live", r.Service, r.Number)
			case apitypes.ReleaseFailed:
				g.io.Failf("%s: release %d failed: %s", r.Service, r.Number, r.Error)
			default:
				g.io.Successf("%s: release %d started (%s)", r.Service, r.Number, r.Status)
			}
		}
		for _, name := range a.Skipped {
			g.io.Warnf("%s was already deploying; it picks up the changes with its next release.", name)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, r := range a.Releases {
		if err := releaseFailure(ctx, s, r); err != nil {
			return err
		}
	}
	return nil
}

func newChangesCmd(g *globals) *cobra.Command {
	var f targetFlags
	var reveal bool
	cmd := &cobra.Command{
		Use:   "changes",
		Short: "Show the environment's staged changes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needEnvironment)
			if err != nil {
				return err
			}
			path := t.envPath() + "/changes"
			if reveal {
				path += "?reveal=true"
			}
			p, err := get[apitypes.PendingChanges](cmd.Context(), s, path)
			if err != nil {
				return err
			}
			return g.io.Print(p, func() error { return printChanges(g, p, reveal) })
		},
	}
	f.add(cmd, needEnvironment)
	cmd.Flags().BoolVar(&reveal, "reveal", false, "show variable values (admin)")
	return cmd
}

var changeGlyph = map[apitypes.ChangeAction]string{
	apitypes.ChangeAdd:    "+",
	apitypes.ChangeUpdate: "~",
	apitypes.ChangeRemove: "-",
}

func printChanges(g *globals, p apitypes.PendingChanges, reveal bool) error {
	if p.Count == 0 {
		g.io.Successf("No staged changes in %s/%s", p.Project, p.Environment)
		return nil
	}
	_, _ = fmt.Fprintf(g.io.Out, "%s in %s/%s:\n", plural(p.Count, "staged change"), p.Project, p.Environment)
	t := g.io.Rows()
	for _, v := range p.Variables {
		where := v.Service
		if v.Scope == apitypes.ScopeEnvironment {
			where = "(shared)"
		}
		diff := ""
		if reveal {
			diff = deref(v.Old) + " → " + deref(v.New)
			switch v.Action {
			case apitypes.ChangeAdd:
				diff = deref(v.New)
			case apitypes.ChangeRemove:
				diff = ""
			}
		}
		t.Row(changeGlyph[v.Action], where, "variable "+v.Key, diff)
	}
	for _, c := range p.Settings {
		t.Row("~", c.Service, c.Field, dash(c.Old)+" → "+dash(c.New))
	}
	for _, v := range p.Volumes {
		t.Row("+", v.Service, "volume "+v.Name, "at "+v.MountPath)
	}
	if err := t.Flush(); err != nil {
		return err
	}
	g.io.Progressf("Apply with `dock apply`, or drop them with `dock discard`.")
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func newApplyCmd(g *globals) *cobra.Command {
	var f targetFlags
	var detach bool
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply the environment's staged changes",
		Long: `Make every staged change in the environment live in one step, then redeploy
each affected running service with a config release, following progress
unless --detach.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needEnvironment)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			var w *watcher
			if !detach {
				if w, err = g.watch(ctx, s, target{Project: t.Project, Environment: t.Environment}); err != nil {
					return err
				}
				defer w.close()
			}
			a, err := send[apitypes.AppliedChanges](ctx, s, http.MethodPost, t.envPath()+"/changes/apply", nil)
			if err != nil {
				return err
			}
			return g.finishApplied(ctx, s, w, a, func(a apitypes.AppliedChanges) any { return a })
		},
	}
	f.add(cmd, needEnvironment)
	addDetachFlag(cmd, &detach)
	return cmd
}

func newDiscardCmd(g *globals) *cobra.Command {
	var f targetFlags
	var yes bool
	cmd := &cobra.Command{
		Use:   "discard",
		Short: "Drop the environment's staged changes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needEnvironment)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if !yes {
				p, err := get[apitypes.PendingChanges](ctx, s, t.envPath()+"/changes")
				if err != nil {
					return err
				}
				if p.Count == 0 {
					return g.io.Print(p, func() error { return printChanges(g, p, false) })
				}
				if err := g.io.Confirm(fmt.Sprintf("Discard %s in %s/%s?", plural(p.Count, "staged change"), t.Project, t.Environment), "yes"); err != nil {
					return err
				}
			}
			p, err := send[apitypes.PendingChanges](ctx, s, http.MethodPost, t.envPath()+"/changes/discard", nil)
			if err != nil {
				return err
			}
			return g.io.Print(p, func() error {
				g.io.Successf("Discarded staged changes in %s/%s", t.Project, t.Environment)
				return nil
			})
		},
	}
	f.add(cmd, needEnvironment)
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}
