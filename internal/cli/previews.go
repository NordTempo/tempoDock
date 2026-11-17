package cli

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// newPreviewsCmd is `dock env previews` (ADR 0056, 0077), also registered
// as `dock environment previews`: a project's previews (which services
// build pull requests, each base's numbers, the open previews), with set
// and close below it. A service's own switch is `dock service previews`.
func newPreviewsCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "previews",
		Short: "Preview environments per pull request: who builds them and the open previews",
		Long: `Show which services build pull requests in preview environments, the
numbers of each environment previews are cloned from, and the open previews
with their pull request, status and address.

A pull request to a repository a service with previews on deploys
(` + "`dock service previews on`" + `), into the branch it deploys, that changes its
directory, gets a non-production environment pr-<number> cloned from the
service's environment. It redeploys on new commits and is archived when the
pull request closes.`,
		Example: `  dock env previews
  dock service previews on -s web -e staging --copy PUBLIC_API_URL
  dock env previews set staging --max 5 --sleep-after 1h
  dock env previews refresh pr-12
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
	cmd.AddCommand(newPreviewsSetCmd(g), newPreviewsCloseCmd(g), newPreviewsRefreshCmd(g))
	return cmd
}

// newPreviewsSetCmd is `dock env previews set <environment>`: the numbers
// of previews cloned from an environment.
func newPreviewsSetCmd(g *globals) *cobra.Command {
	var f targetFlags
	var req apitypes.SetEnvironmentPreviewsRequest
	cmd := &cobra.Command{
		Use:   "set <environment> [--max N] [--sleep-after D] [--copy KEY ...]",
		Short: "Set how many previews cloned from an environment may be open, when they sleep, and the shared values they copy",
		Long: `Set the numbers of the previews cloned from <environment>: at most --max
open at once (default 3), and they sleep after --sleep-after without
requests (default 30m, or off): their services stop and the next request
wakes it. Open previews take the new sleep setting at once.

Previews run a pull request's code, so no literal value of a shared
variable is copied unless --copy names it (SENTRY_DSN). The settings are
replaced as a whole: pass every one again. Needs admin on the environment.`,
		Example: `  dock env previews set staging --max 5
  dock env previews set staging --sleep-after 1h --copy SENTRY_DSN`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needProject)
			if err != nil {
				return err
			}
			t.Environment = args[0]
			ep, err := send[apitypes.EnvironmentPreviews](cmd.Context(), s, http.MethodPut, t.envPath()+"/previews", req)
			if err != nil {
				return err
			}
			return g.io.Print(ep, func() error {
				g.io.Successf("Previews cloned from %s: %s", t, envPreviewRules(ep))
				return nil
			})
		},
	}
	f.add(cmd, needProject)
	cmd.Flags().IntVar(&req.MaxPreviews, "max", 0, "most open previews at once, 1-20 (default 3)")
	cmd.Flags().StringArrayVar(&req.CopyShared, "copy", nil, "a shared variable whose literal value previews copy, by key (repeatable)")
	cmd.Flags().Var(sleepFlag{&req.SleepAfterMinutes}, "sleep-after", "sleep a preview after this long without requests, 5m to 24h, or off (default 30m)")
	return cmd
}

// newServicePreviewsCmd is `dock service previews [on|off]` (ADR 0077):
// what a service does in previews. Without arguments it shows it; on and
// off switch whether pull requests build it, and the flags change the
// rest. What isn't passed stays as it is.
func newServicePreviewsCmd(g *globals) *cobra.Command {
	var f targetFlags
	var forks, dataForks bool
	var copies []string
	var data string
	cmd := &cobra.Command{
		Use:   "previews [on|off]",
		Short: "Whether pull requests build a service in a preview environment, and what previews copy from it",
		Long: `Show or change what a service does in preview environments (ADR 0077).

on: pull requests to the service's GitHub repository, into the branch it
deploys, that change its directory, get a preview environment pr-<number>
cloned from the service's environment, where it builds the pull request's
code; the environment's other services run their current images. off:
pull requests don't build it. One repository and branch previews from one
environment of a project.

Previews run a pull request's code, so none of the service's literal
variable values is copied unless --copy names it (repeatable; the list is
replaced). --forks also previews pull requests from forks.

--data says what the service's volumes start with in a new preview (ADR
0092): empty (the default), snapshot (its newest volume snapshot), copy (a
copy of this database as the preview is made) or masked (that copy with
the Data panel's sensitive columns replaced while it is read, so their
values never reach the preview). --data alone is masked. Copies are for
template databases; Redis masked copies start empty while it has
sensitive key patterns. Pull requests from forks start empty unless
--data-forks. An unmasked copy, a snapshot, or data for forks needs the
sensitive columns visible to you. Needs admin on the environment.`,
		Example: `  dock service previews -s web
  dock service previews on -s web --copy PUBLIC_API_URL
  dock service previews -s db --data
  dock service previews -s db --data snapshot
  dock service previews off -s web`,
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"on", "off"},
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			path := t.servicePath() + "/previews"
			cur, err := get[apitypes.ServicePreviews](cmd.Context(), s, path)
			if err != nil {
				return err
			}
			changed := len(args) > 0 || cmd.Flags().Changed("forks") || cmd.Flags().Changed("copy") || cmd.Flags().Changed("data") || cmd.Flags().Changed("data-forks")
			if !changed {
				return g.io.Print(cur, func() error { return g.printServicePreviews(t, cur) })
			}
			req := apitypes.SetServicePreviewsRequest{Build: cur.Build, AllowForks: cur.AllowForks, CopyVariables: cur.CopyVariables, Data: cur.Data, DataForks: cur.DataForks}
			if len(args) > 0 {
				switch args[0] {
				case "on":
					req.Build = true
				case "off":
					req.Build, req.AllowForks = false, false
				default:
					return usageError(fmt.Sprintf("%q is not on or off", args[0]), "`dock service previews on -s web` or `dock service previews off -s web`.")
				}
			}
			if cmd.Flags().Changed("forks") {
				req.AllowForks = forks
			}
			if cmd.Flags().Changed("copy") {
				req.CopyVariables = copies
			}
			if cmd.Flags().Changed("data") {
				mode := apitypes.PreviewData(data)
				if !slices.Contains([]apitypes.PreviewData{apitypes.PreviewDataEmpty, apitypes.PreviewDataSnapshot, apitypes.PreviewDataCopy, apitypes.PreviewDataMasked}, mode) {
					return usageError(fmt.Sprintf("--data is empty, snapshot, copy or masked, not %q", data), "`dock service previews -s db --data masked`.")
				}
				req.Data = mode
				if mode == apitypes.PreviewDataEmpty {
					req.DataForks = false
				}
			}
			if cmd.Flags().Changed("data-forks") {
				req.DataForks = dataForks
			}
			sp, err := send[apitypes.ServicePreviews](cmd.Context(), s, http.MethodPut, path, req)
			if err != nil {
				return err
			}
			return g.io.Print(sp, func() error {
				g.io.Successf("%s in previews: %s", t, servicePreviewRules(sp))
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().BoolVar(&forks, "forks", false, "also build pull requests from forks")
	cmd.Flags().StringArrayVar(&copies, "copy", nil, "a variable of the service whose literal value previews copy, by key (repeatable; replaces the list)")
	cmd.Flags().StringVar(&data, "data", "", "what the service's volumes start with in a new preview: empty, snapshot, copy or masked (--data alone)")
	cmd.Flags().Lookup("data").NoOptDefVal = string(apitypes.PreviewDataMasked)
	cmd.Flags().BoolVar(&dataForks, "data-forks", false, "give pull requests from forks the data too")
	return cmd
}

// printServicePreviews is `dock service previews`' human output.
func (g *globals) printServicePreviews(t target, sp apitypes.ServicePreviews) error {
	updated := ""
	if sp.UpdatedAt != nil {
		updated = ago(*sp.UpdatedAt) + " by " + sp.UpdatedBy
	}
	return g.io.Fields(
		"service", t.String(),
		"pull requests", map[bool]string{true: "build it in a preview", false: "don't build it (`dock service previews on`)"}[sp.Build],
		"forks", onOff(sp.AllowForks),
		"copied", dash(strings.Join(sp.CopyVariables, ", ")),
		"data", previewDataText(sp),
		"updated", dash(updated),
	)
}

// servicePreviewRules sums up a service's preview settings in one line.
func servicePreviewRules(sp apitypes.ServicePreviews) string {
	s := "pull requests don't build it"
	if sp.Build {
		s = "pull requests build it"
		if sp.AllowForks {
			s += ", forks too"
		}
	}
	if len(sp.CopyVariables) > 0 {
		s += ", copying " + strings.Join(sp.CopyVariables, ", ")
	}
	if sp.Data != "" && sp.Data != apitypes.PreviewDataEmpty {
		s += ", previews start with " + previewDataText(sp)
	}
	return s
}

// previewDataText says what a service's volumes start with in previews.
func previewDataText(sp apitypes.ServicePreviews) string {
	var s string
	switch sp.Data {
	case apitypes.PreviewDataSnapshot:
		s = "its newest snapshot"
	case apitypes.PreviewDataCopy:
		s = "a copy of it, sensitive columns included"
	case apitypes.PreviewDataMasked:
		s = "a masked copy of it"
		if sp.Masked != nil {
			s += fmt.Sprintf(" (%d sensitive columns marked)", sp.Masked.Columns)
		}
	default:
		return "empty"
	}
	if sp.DataForks {
		s += ", forks too"
	}
	return s
}

// envPreviewRules sums up an environment's preview numbers in one line.
func envPreviewRules(ep apitypes.EnvironmentPreviews) string {
	s := fmt.Sprintf("at most %d open", ep.MaxPreviews)
	if ep.SleepAfterMinutes > 0 {
		s += ", sleeping after " + minutesText(ep.SleepAfterMinutes) + " without requests"
	} else {
		s += ", never sleeping"
	}
	if len(ep.CopyShared) > 0 {
		s += ", copying shared " + strings.Join(ep.CopyShared, ", ")
	}
	return s
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

// printPreviews is `dock env previews`' human output: the services that
// build pull requests, each base's numbers, then the open previews.
func (g *globals) printPreviews(project string, set apitypes.PreviewSettings) error {
	if !set.Enabled {
		g.io.Successf("No service of %s builds pull requests. Turn previews on for one with `dock service previews on -s <service> -e <environment>`.", project)
	} else {
		tb := g.io.Table("service", "source", "pull requests", "copied", "data")
		for _, sp := range set.Services {
			source := ""
			if sp.Repo != "" {
				source = sp.Repo + "@" + sp.Branch
				if sp.Dir != "" {
					source += ":" + sp.Dir
				}
			}
			build := "-"
			if sp.Build {
				build = "build"
				if sp.AllowForks {
					build += ", forks too"
				}
			}
			data := "-"
			if sp.Data != "" && sp.Data != apitypes.PreviewDataEmpty {
				data = string(sp.Data)
			}
			tb.Row(sp.Environment+"/"+sp.Service, dash(source), build, dash(strings.Join(sp.CopyVariables, ", ")), data)
		}
		if err := tb.Flush(); err != nil {
			return err
		}
		for _, e := range set.Environments {
			g.io.Detail(fmt.Sprintf("Previews cloned from %s: %s", e.Environment, envPreviewRules(e.EnvironmentPreviews)))
		}
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
	if err := tb.Flush(); err != nil {
		return err
	}
	for _, pv := range set.Previews {
		for _, d := range pv.Databases {
			g.io.Detail(pv.Environment + "/" + previewDatabaseText(d))
		}
	}
	return nil
}

// previewDatabaseText is a preview database's copy in one line.
func previewDatabaseText(d apitypes.PreviewDatabase) string {
	switch d.Status {
	case apitypes.PreviewDataCopying:
		return fmt.Sprintf("%s: copying from %s, %s", d.Service, d.From, d.Message)
	case apitypes.PreviewDataCopied:
		return fmt.Sprintf("%s: %s, %s", d.Service, d.Message, ago(*cmpTime(d.FinishedAt, d.StartedAt)))
	}
	return d.Service + ": " + d.Message
}

// cmpTime is t, or def when t is nil.
func cmpTime(t *time.Time, def time.Time) *time.Time {
	if t == nil {
		return &def
	}
	return t
}

// newPreviewsRefreshCmd copies a preview's databases from its base again.
func newPreviewsRefreshCmd(g *globals) *cobra.Command {
	var f targetFlags
	var yes bool
	var services []string
	cmd := &cobra.Command{
		Use:   "refresh <environment> [-s service ...]",
		Short: "Copy a preview's databases from its base again",
		Long: `Copy a preview's databases from its base again, as when the preview was
made (ADR 0092): each database whose base service copies its data into
previews (` + "`dock service previews --data`" + `) is stopped, emptied and filled
with a new copy, masked if its setting says so. Then the preview's other
services restart, so the pull request's migrations run on the new data.
The base is only read. --service limits it to those databases. Needs deploy
on the preview.`,
		Example: `  dock env previews refresh pr-12
  dock env previews refresh pr-12 --service db`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needProject)
			if err != nil {
				return err
			}
			t.Environment = args[0]
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Refresh the data of preview %s? Its databases are emptied and copied again, and its services restart.", t), "yes"); err != nil {
					return err
				}
			}
			pv, err := send[apitypes.Preview](cmd.Context(), s, http.MethodPost, t.envPath()+"/preview/refresh-data", apitypes.RefreshPreviewDataRequest{Services: services})
			if err != nil {
				return err
			}
			return g.io.Print(pv, func() error {
				g.io.Successf("Refreshing the data of preview %s from its base. `dock env previews` shows how it goes.", t)
				return nil
			})
		},
	}
	f.add(cmd, needProject)
	cmd.Flags().StringArrayVar(&services, "service", nil, "a database of the preview to refresh (repeatable; default every one that copies its base)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

// pullRequestText names an environment's pull request in a table: "#12
// acme/shop", or "" for an environment that isn't a preview.
func pullRequestText(pr *apitypes.PullRequestRef) string {
	if pr == nil {
		return ""
	}
	return fmt.Sprintf("#%d %s", pr.Number, pr.Repo)
}
