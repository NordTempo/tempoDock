package cli

import (
	"net/http"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// Followed Compose files (ADR 0108): `dock compose follow|status|sync|
// unfollow`. An environment follows a Compose file in a GitHub repository;
// each push that changes it is staged as changes to review.

func newComposeCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compose",
		Short: "Follow a Compose file in a GitHub repository",
		Long: `An environment can follow a docker-compose.yml in a GitHub repository,
read through the organization's GitHub App. Each push to its branch that
changes the file is converted, planned and staged in the environment, to
review with ` + "`dock changes`" + ` and apply with ` + "`dock apply`" + `, as
` + "`dock import compose --stage`" + ` would. Each service still builds and
deploys as before: the file shapes which services exist and their settings.

To update from a local file once instead, use ` + "`dock import compose`" + `.`,
	}
	cmd.AddCommand(newComposeFollowCmd(g), newComposeStatusCmd(g), newComposeSyncCmd(g), newComposeUnfollowCmd(g))
	return cmd
}

// newComposeFollowCmd is `dock compose follow OWNER/REPO`.
func newComposeFollowCmd(g *globals) *cobra.Command {
	var f targetFlags
	var req apitypes.ComposeSourceRequest
	cmd := &cobra.Command{
		Use:   "follow OWNER/REPO [--branch B] [--path P] [--prune] [--auto-apply]",
		Short: "Follow a Compose file and sync it now",
		Long: `Make the environment follow a Compose file in a GitHub repository, then sync
the branch's head at once and show the plan it staged. Following again
changes what it follows.

  --branch      the branch (default: the repository's default branch)
  --path        the file (default: docker-compose.yml, compose.yaml, ... at
                the root)
  --prune       stage an archive for each service the file drops (admin;
                applying it needs an admin too). Without it they are kept
  --auto-apply  apply a sync by itself when it removes nothing, nothing
                else is staged in the environment and no secret needs a
                value; every other sync waits for review`,
		Example: `  dock compose follow acme/shop -p shop -e production
  dock compose follow acme/shop --branch release --path deploy/compose.yaml --prune`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needEnvironment)
			if err != nil {
				return err
			}
			req.Repo = args[0]
			res, err := send[apitypes.ComposeSyncResult](cmd.Context(), s, http.MethodPut, t.envPath()+"/compose", req)
			if err != nil {
				return err
			}
			return g.io.Print(res, func() error {
				src := res.Source
				g.io.Successf("%s/%s follows %s on %s of %s", src.Project, src.Environment, src.Path, src.Branch, src.Repo)
				printComposeSync(g, res)
				return nil
			})
		},
	}
	f.add(cmd, needEnvironment)
	cmd.Flags().StringVar(&req.Branch, "branch", "", "the branch (default: the repository's default)")
	cmd.Flags().StringVar(&req.Path, "path", "", "the Compose file's path in the repository")
	cmd.Flags().BoolVar(&req.Prune, "prune", false, "stage archives for services the file drops (admin)")
	cmd.Flags().BoolVar(&req.AutoApply, "auto-apply", false, "apply a sync that removes nothing by itself")
	return cmd
}

// newComposeStatusCmd is `dock compose status`.
func newComposeStatusCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the Compose file an environment follows and its last sync",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needEnvironment)
			if err != nil {
				return err
			}
			st, err := send[apitypes.ComposeSourceStatus](cmd.Context(), s, http.MethodGet, t.envPath()+"/compose", nil)
			if err != nil {
				return err
			}
			return g.io.Print(st, func() error {
				src := st.Source
				if src == nil {
					g.io.Progressf("%s/%s follows no Compose file; follow one with `dock compose follow OWNER/REPO %s`", t.Project, t.Environment, t.flags())
					return nil
				}
				kv := []string{
					"Follows", src.Path + " on " + src.Branch + " of " + src.Repo,
					"Removals", map[bool]string{true: "staged as archives (an admin applies them)", false: "kept"}[src.Prune],
					"Auto-apply", map[bool]string{true: "when a sync removes nothing", false: "off"}[src.AutoApply],
				}
				if src.Pending != "" {
					kv = append(kv, "Queued", shortSHA(src.Pending))
				}
				if l := src.Last; l != nil {
					kv = append(kv, "Last sync", shortSHA(l.Commit.SHA)+" "+l.Commit.Message+", "+ago(l.At),
						"Outcome", string(l.Outcome)+": "+l.Summary)
				}
				return g.io.Fields(kv...)
			})
		},
	}
	f.add(cmd, needEnvironment)
	return cmd
}

// newComposeSyncCmd is `dock compose sync`.
func newComposeSyncCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync the followed Compose file at its branch's head now",
		Long: `Convert and plan the followed Compose file at its branch's head and stage the
result, as a push would, and show the plan. A sync that fails says why
and exits 1.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needEnvironment)
			if err != nil {
				return err
			}
			res, err := send[apitypes.ComposeSyncResult](cmd.Context(), s, http.MethodPost, t.envPath()+"/compose/sync", nil)
			if err != nil {
				return err
			}
			if err := g.io.Print(res, func() error {
				printComposeSync(g, res)
				return nil
			}); err != nil {
				return err
			}
			if l := res.Source.Last; l != nil && l.Outcome == apitypes.ComposeFailed {
				return &output.Error{Exit: output.ExitError, Message: "the sync failed: " + l.Summary, Hint: "Fix the Compose file in the repository and push, or run `dock compose sync` again."}
			}
			return nil
		},
	}
	f.add(cmd, needEnvironment)
	return cmd
}

// newComposeUnfollowCmd is `dock compose unfollow`.
func newComposeUnfollowCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "unfollow",
		Short: "Stop following the Compose file; staged changes stay",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needEnvironment)
			if err != nil {
				return err
			}
			if _, err := send[struct{}](cmd.Context(), s, http.MethodDelete, t.envPath()+"/compose", nil); err != nil {
				return err
			}
			return g.io.Print(struct{}{}, func() error {
				g.io.Successf("%s/%s no longer follows a Compose file", t.Project, t.Environment)
				return nil
			})
		},
	}
	f.add(cmd, needEnvironment)
	return cmd
}

// shortSHA is a commit's first seven characters.
func shortSHA(sha string) string { return sha[:min(7, len(sha))] }

// printComposeSync prints a sync: its commit and outcome, the warnings
// and the plan, and where to review what it staged.
func printComposeSync(g *globals, res apitypes.ComposeSyncResult) {
	src := res.Source
	l := src.Last
	if l == nil {
		return
	}
	for _, w := range res.Warnings {
		where := w.Service
		if where == "" {
			where = "compose"
		}
		g.io.Warnf("%s: %s", where, w.Message)
	}
	if l.Outcome == apitypes.ComposeFailed {
		g.io.Warnf("Sync of %s failed: %s", shortSHA(l.Commit.SHA), l.Summary)
		return
	}
	printPlan(g, res.Plan)
	switch l.Outcome {
	case apitypes.ComposeStaged:
		g.io.Successf("Staged %s at %s: %s", src.Path, shortSHA(l.Commit.SHA), l.Summary)
		g.io.Progressf("Review with `dock changes -p %s -e %s`, apply with `dock apply -p %s -e %s`.", src.Project, src.Environment, src.Project, src.Environment)
	case apitypes.ComposeApplied:
		g.io.Successf("Applied %s at %s: %s", src.Path, shortSHA(l.Commit.SHA), l.Summary)
	default:
		g.io.Successf("Nothing to stage: %s/%s matches %s at %s", src.Project, src.Environment, src.Path, shortSHA(l.Commit.SHA))
	}
}
