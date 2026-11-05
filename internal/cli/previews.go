package cli

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// newPreviewsCmd is `dock env previews` (ADR 0056), also registered as
// `dock environment previews`: a project's preview settings and open
// previews, with enable, disable and close below it.
func newPreviewsCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "previews",
		Short: "Preview environments per pull request: settings and open previews",
		Long: `Show whether pull requests get preview environments, cloned from which
environment, the limits, and the open previews with their pull request,
status and address.

A pull request to a repository the base environment's services deploy, into
the branch they deploy, gets a non-production environment pr-<number>
cloned from the base. It redeploys on new commits and is archived when the
pull request closes.`,
		Example: `  dock env previews
  dock env previews enable staging --copy web.PUBLIC_API_URL
  dock env previews close pr-12`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needProject)
			if err != nil {
				return err
			}
			set, err := get[apitypes.PreviewSettings](cmd.Context(), s, t.projectPath()+"/previews")
			if err != nil {
				return err
			}
			return g.io.Print(set, func() error { return g.printPreviews(t.Project, set) })
		},
	}
	f.add(cmd, needProject)
	cmd.AddCommand(newPreviewsEnableCmd(g), newPreviewsDisableCmd(g), newPreviewsCloseCmd(g))
	return cmd
}

// newPreviewsEnableCmd turns previews on, or replaces their settings.
func newPreviewsEnableCmd(g *globals) *cobra.Command {
	var f targetFlags
	var req apitypes.SetPreviewsRequest
	cmd := &cobra.Command{
		Use:   "enable <base-environment> [--max N] [--copy service.KEY ...] [--forks]",
		Short: "Turn previews on, cloned from an environment, or change their settings",
		Long: `Give pull requests preview environments cloned from <base-environment>
(e.g. staging). The settings are replaced as a whole: when changing them,
pass every setting again (--max, each --copy, --forks); one left out goes
back to its default.

Previews run a pull request's code, so no literal variable value of the
base is copied unless --copy names it (web.PUBLIC_API_URL, or
shared.SENTRY_DSN for a shared one). References are always copied and
database templates get new credentials. Pull requests from forks get no
preview unless --forks. Needs admin on the whole project.`,
		Example: `  dock env previews enable staging
  dock env previews enable staging --max 5 --copy web.PUBLIC_API_URL --copy shared.SENTRY_DSN`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needProject)
			if err != nil {
				return err
			}
			req.Base = args[0]
			set, err := send[apitypes.PreviewSettings](cmd.Context(), s, http.MethodPut, t.projectPath()+"/previews", req)
			if err != nil {
				return err
			}
			return g.io.Print(set, func() error {
				g.io.Successf("Previews of pull requests are on for %s: %s", t.Project, previewRules(set))
				if len(set.CopyVariables) == 0 {
					g.io.Progressf("No literal values are copied; name the ones previews need with --copy service.KEY.")
				}
				return nil
			})
		},
	}
	f.add(cmd, needProject)
	cmd.Flags().IntVar(&req.MaxPreviews, "max", 0, "most open previews at once, 1-20 (default 3)")
	cmd.Flags().StringArrayVar(&req.CopyVariables, "copy", nil, "a base variable whose literal value previews copy, as service.KEY or shared.KEY (repeatable)")
	cmd.Flags().BoolVar(&req.AllowForks, "forks", false, "also preview pull requests from forks")
	return cmd
}

// newPreviewsDisableCmd turns previews off; open ones stay.
func newPreviewsDisableCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "disable",
		Short: "Turn previews off; open previews stay until their pull requests close",
		Long: `New pull requests get no preview. Open previews stay until their pull
requests close; close one now with ` + "`dock env previews close <environment>`" + `.
Needs admin on the whole project.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needProject)
			if err != nil {
				return err
			}
			set, err := send[apitypes.PreviewSettings](cmd.Context(), s, http.MethodDelete, t.projectPath()+"/previews", nil)
			if err != nil {
				return err
			}
			return g.io.Print(set, func() error {
				g.io.Successf("Previews of pull requests are off for %s", t.Project)
				if n := len(set.Previews); n > 0 {
					g.io.Progressf("%s stay open until their pull requests close; close one with `dock env previews close <environment>`.", plural(n, "preview"))
				}
				return nil
			})
		},
	}
	f.add(cmd, needProject)
	return cmd
}

// newPreviewsCloseCmd closes a preview before its pull request does.
func newPreviewsCloseCmd(g *globals) *cobra.Command {
	var f targetFlags
	var yes bool
	cmd := &cobra.Command{
		Use:   "close <environment>",
		Short: "Close a preview: archive its environment and free its containers, volumes and addresses",
		Long: `Close a preview as its pull request closing would: the environment is
archived, its containers, network and volumes are removed and its
addresses are freed. The pull request's next event (a new commit,
reopening) makes a new preview with new, empty volumes. Needs deploy on the
whole project.`,
		Example: `  dock env previews close pr-12`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needProject)
			if err != nil {
				return err
			}
			t.Environment = args[0]
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Close preview %s? Its containers, volumes and addresses are removed.", t), "yes"); err != nil {
					return err
				}
			}
			pv, err := send[apitypes.Preview](cmd.Context(), s, http.MethodPost, t.envPath()+"/preview/close", nil)
			if err != nil {
				return err
			}
			return g.io.Print(pv, func() error {
				g.io.Successf("Closed preview %s of %s#%d. Its containers, volumes and addresses are being removed.", t, pv.Repo, pv.Number)
				return nil
			})
		},
	}
	f.add(cmd, needProject)
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

// printPreviews is `dock env previews`' human output: the settings, then
// the open previews.
func (g *globals) printPreviews(project string, set apitypes.PreviewSettings) error {
	if set.Enabled {
		copied := "none"
		if len(set.CopyVariables) > 0 {
			copied = strings.Join(set.CopyVariables, ", ")
		}
		updated := ""
		if set.UpdatedAt != nil {
			updated = ago(*set.UpdatedAt) + " by " + set.UpdatedBy
		}
		if err := g.io.Fields(
			"previews", "on, cloned from "+set.Base,
			"max open", fmt.Sprint(set.MaxPreviews),
			"copied", copied,
			"forks", onOff(set.AllowForks),
			"updated", updated,
		); err != nil {
			return err
		}
	} else {
		g.io.Successf("Previews of pull requests are off for %s. Turn them on with `dock env previews enable <base-environment>`.", project)
	}
	if len(set.Previews) == 0 {
		g.io.Progressf("No open previews.")
		return nil
	}
	if _, err := fmt.Fprintln(g.io.Out); err != nil {
		return err
	}
	tb := g.io.Table("environment", "pull request", "status", "address", "commit")
	for _, pv := range set.Previews {
		addr := ""
		if len(pv.URLs) > 0 {
			addr = pv.URLs[0].URL
		}
		tb.Row(pv.Environment, cut(fmt.Sprintf("#%d %s", pv.Number, pv.Title), 40), string(pv.Status), dash(addr), pv.Commit[:min(7, len(pv.Commit))])
	}
	return tb.Flush()
}

// previewRules sums up preview settings in one line.
func previewRules(set apitypes.PreviewSettings) string {
	s := fmt.Sprintf("cloned from %s, at most %d open", set.Base, set.MaxPreviews)
	if len(set.CopyVariables) > 0 {
		s += ", copying " + strings.Join(set.CopyVariables, ", ")
	}
	if set.AllowForks {
		s += ", forks too"
	}
	return s
}

// pullRequestText names an environment's pull request in a table: "#12
// acme/shop", or "" for an environment that isn't a preview.
func pullRequestText(pr *apitypes.PullRequestRef) string {
	if pr == nil {
		return ""
	}
	return fmt.Sprintf("#%d %s", pr.Number, pr.Repo)
}
