package cli

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// newPromoteCmd is `dock promote <from> <to>` (ADR 0014 section 4): the
// images serving in one environment, deployed to another.
func newPromoteCmd(g *globals) *cobra.Command {
	var f targetFlags
	var services []string
	var detach bool
	cmd := &cobra.Command{
		Use:   "promote <from> <to> [-s service...]",
		Short: "Deploy the images serving in one environment to another",
		Long: `Deploy what is serving in <from> to <to>: for each service (all, or the ones
named with -s) a new release in <to> with the exact image of <from>'s
active release, by digest, and <to>'s own variables and settings. Nothing
is rebuilt. Services deploy one at a time and stop at the first failure;
the ones already promoted stay. Services that don't exist in <to>, and
the ones that already run that image there, are skipped and listed: an
unchanged service (a database, say) is never restarted.

Follows progress like deploy unless --detach. Exit 6 when a release fails,
exit 4 when nothing could be promoted (every service skipped, none of them
because it runs the image already; the result, with --json too, still
lists them). Needs deploy on <to>.`,
		Example: `  dock promote staging production
  dock promote staging production -s web -s worker`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeEnvironmentArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needProject)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			var w *watcher
			if !detach {
				if w, err = g.watch(ctx, s, target{Project: t.Project, Environment: args[1]}); err != nil {
					return err
				}
				defer w.close()
			}
			res, err := send[apitypes.PromoteResult](ctx, s, http.MethodPost, t.projectPath()+"/promote", apitypes.PromoteRequest{
				From: args[0], To: args[1], Services: services,
			})
			if err != nil {
				return err
			}
			if w != nil && len(res.Promoted) > 0 {
				rels := make([]apitypes.Release, len(res.Promoted))
				for i, p := range res.Promoted {
					rels[i] = p.Release
				}
				rels, err := w.follow(ctx, rels)
				if err != nil {
					return interrupted(err, rels)
				}
				for i := range res.Promoted {
					res.Promoted[i].Release = rels[i]
				}
			}
			return g.finishPromote(ctx, s, res)
		},
	}
	f.add(cmd, needProject)
	cmd.Flags().StringSliceVarP(&services, "service", "s", nil, "only these services (repeat, or comma-separated)")
	_ = cmd.RegisterFlagCompletionFunc("service", completion(needProject, 0, listFromServices))
	addDetachFlag(cmd, &detach)
	return cmd
}

// finishPromote prints what a promotion did: each release, then the
// skipped services. The first failed release makes it a deploy_failed
// error; the releases after it failed without starting. Nothing promoted
// at all is a not_found error, after the result, unless a service was
// skipped because it runs the image already: that is a promotion with
// nothing left to do.
func (g *globals) finishPromote(ctx context.Context, s *session, res apitypes.PromoteResult) error {
	err := g.io.Print(res, func() error {
		for _, p := range res.Promoted {
			r := p.Release
			switch r.Status {
			case apitypes.ReleaseActive:
				g.io.Successf("%s: release %d (from %s release %d) is live in %s", p.Service, r.Number, res.From, p.FromRelease, res.To)
			case apitypes.ReleaseFailed, apitypes.ReleaseCanceled:
				g.printNotLive(p.Service, r)
			default:
				g.io.Successf("%s: release %d (from %s release %d) queued in %s", p.Service, r.Number, res.From, p.FromRelease, res.To)
			}
		}
		for _, sk := range res.Skipped {
			if sk.Reason == apitypes.SkipSameImage {
				g.io.Successf("%s: %s", sk.Service, sk.Message)
				continue
			}
			g.io.Warnf("%s skipped: %s", sk.Service, sk.Message)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(res.Promoted) == 0 && slices.ContainsFunc(res.Skipped, func(sk apitypes.SkippedService) bool { return sk.Reason == apitypes.SkipSameImage }) {
		// Nothing to do: what could be promoted runs the image already.
		// The other skips were listed, as after a promotion that started.
		return promoteStopped(res.Skipped)
	}
	if len(res.Promoted) == 0 {
		return &output.Error{
			Exit:    output.ExitNotFound,
			Message: fmt.Sprintf("nothing was promoted from %s to %s", res.From, res.To),
			Hint:    nothingPromotedHint(res),
		}
	}
	for _, p := range res.Promoted {
		if err := releaseFailure(ctx, s, p.Release); err != nil {
			return err
		}
	}
	return promoteStopped(res.Skipped)
}

// nothingPromotedHint says what to do after a promotion that skipped
// every service. That promote never creates services is said only when
// one is missing in the target, not for a deploy in progress or a source
// without a release.
func nothingPromotedHint(res apitypes.PromoteResult) string {
	hint := "Each skipped service above says why."
	if slices.ContainsFunc(res.Skipped, func(sk apitypes.SkippedService) bool { return sk.Reason == apitypes.SkipMissingInTarget }) {
		hint += " Promote never creates services; `dock service create` makes one in " + res.To + "."
	}
	return hint
}

// promoteStopped is the deploy failure of a promotion (or a clone's
// deploy) that stopped at a service it couldn't start: the services
// before it stay live, the rest were skipped (apitypes.SkipFailed).
func promoteStopped(skipped []apitypes.SkippedService) error {
	for _, sk := range skipped {
		if sk.Reason == apitypes.SkipFailed {
			return &output.Error{
				Exit:    output.ExitDeployFailed,
				Message: fmt.Sprintf("%s wasn't promoted: %s", sk.Service, sk.Message),
				Hint:    "The services promoted before it stay live; the ones after it were skipped. Fix it and promote again.",
			}
		}
	}
	return nil
}
