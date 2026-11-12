package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Error issues (ADR 0062, "One place for errors"): the org's errors from
// its services' logs, browsers and 5xx spikes, grouped by fingerprint.

func newErrorsCmd(g *globals) *cobra.Command {
	var f targetFlags
	var state string
	var limit int
	cmd := &cobra.Command{
		Use:   "errors [-p P [-e E [-s S]]] [--state open|resolved|all]",
		Short: "Errors of the organization's services: from their logs, browsers and 5xx spikes, grouped",
		Long: `List the organization's error issues: errors grouped by fingerprint (their
type, their message without numbers and IDs, and their top stack frames)
per service. They come from the services' containers' logs (a stated
error level, a stack trace, a panic), from the frontend errors Dock
Analytics collects, and from minutes with many 5xx answers.

New issues come first (first seen in the service's running release, or
in the last 24 hours), then regressed ones (seen again after being
resolved), then ongoing ones. --project, --environment and --service
narrow the list; --service takes the linked project and environment.
Issues resolve by themselves after 7 days without an error.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{"state": {state}, "limit": {strconv.Itoa(limit)}}
			t := target{Project: f.project, Environment: f.environment}
			var s *session
			var err error
			if f.service != "" {
				s, t, err = g.open(f, needService)
				q.Set("service", t.Service)
			} else {
				s, err = g.connect()
			}
			if err != nil {
				return err
			}
			if t.Project != "" {
				q.Set("project", t.Project)
			}
			if t.Environment != "" {
				q.Set("environment", t.Environment)
			}
			list, err := get[apitypes.ErrorList](cmd.Context(), s, apiPrefix+"/errors?"+q.Encode())
			if err != nil {
				return err
			}
			return g.io.Print(list, func() error {
				if len(list.Issues) == 0 {
					if state == "all" {
						g.io.Successf("No errors")
					} else {
						g.io.Successf("No %s errors", state)
					}
					return nil
				}
				tb := g.io.Table("id", "state", "service", "errors", "last seen", "error")
				for _, i := range list.Issues {
					tb.Row(itoa(i.ID), string(i.State), i.Project+"/"+i.Environment+"/"+i.Service, itoa(i.Count), ago(i.LastSeen), i.Title)
				}
				if err := tb.Flush(); err != nil {
					return err
				}
				if list.Truncated {
					g.io.Detail(fmt.Sprintf("Only the %d most recently seen; narrow with --project, --environment or --service.", len(list.Issues)))
				}
				return nil
			})
		},
	}
	// Not f.add: without --service the list is the organization's, not
	// the linked project's.
	cmd.Flags().StringVarP(&f.project, "project", "p", "", "only this project's errors")
	cmd.Flags().StringVarP(&f.environment, "environment", "e", "", "only this environment's errors (of every project, without --project)")
	cmd.Flags().StringVarP(&f.service, "service", "s", "", "only this service's errors (in the linked project and environment by default)")
	cmd.Flags().StringVar(&state, "state", "open", "open, resolved or all")
	cmd.Flags().IntVar(&limit, "limit", 200, "how many at most (500 at most)")
	cmd.AddCommand(newErrorShowCmd(g), newErrorResolveCmd(g))
	return cmd
}

// errorID reads an error issue's ID argument.
func errorID(arg string) (int64, error) {
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		return 0, usageError(fmt.Sprintf("%q is not an error ID", arg), "Run `dock errors` to see the issues' IDs.")
	}
	return id, nil
}

func newErrorShowCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "One error issue: where, how often, since which release, and its latest sample",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := errorID(args[0])
			if err != nil {
				return err
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			d, err := get[apitypes.ErrorIssueDetail](cmd.Context(), s, apiPrefix+"/errors/"+itoa(id))
			if err != nil {
				return err
			}
			return g.io.Print(d, func() error {
				var day int64
				for _, n := range d.Trend {
					day += n
				}
				resolved := "-"
				if d.ResolvedAt != nil {
					resolved = ago(*d.ResolvedAt) + " by " + dash(d.ResolvedBy)
					if d.ResolvedBy == "" {
						resolved = ago(*d.ResolvedAt) + ", by itself after 7 days without one"
					}
				}
				err := g.io.Fields(
					"error", d.Title,
					"state", string(d.State),
					"service", d.Project+"/"+d.Environment+"/"+d.Service,
					"source", string(d.Source),
					"culprit", dash(d.Culprit),
					"errors", fmt.Sprintf("%s, %s in the last 24 hours", itoa(d.Count), itoa(day)),
					"first seen", ago(d.FirstSeen)+releaseNote(d.FirstRelease),
					"last seen", ago(d.LastSeen)+releaseNote(d.LastRelease),
					"resolved", resolved,
					"latest trace", latestTrace(d),
				)
				if err != nil || d.Sample == "" {
					return err
				}
				_, err = io.WriteString(g.io.Out, "\n"+d.Sample+"\n")
				return err
			})
		},
	}
}

// releaseNote is " (release N)", or nothing when it isn't known.
func releaseNote(n int64) string {
	if n == 0 {
		return ""
	}
	return " (release " + itoa(n) + ")"
}

func newErrorResolveCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "resolve <id>",
		Short: "Mark an error issue resolved (deploy); seen again, it regresses and alerts",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := errorID(args[0])
			if err != nil {
				return err
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			i, err := send[apitypes.ErrorIssue](cmd.Context(), s, http.MethodPost, apiPrefix+"/errors/"+itoa(id)+"/resolve", nil)
			if err != nil {
				return err
			}
			return g.io.Print(i, func() error {
				g.io.Successf("Resolved: %s", i.Title)
				return nil
			})
		},
	}
}

// latestTrace is the command that shows an issue's latest trace, or "".
func latestTrace(d apitypes.ErrorIssueDetail) string {
	t := d.LatestTrace
	if t == nil {
		return ""
	}
	return "dock traces show " + t.ID + " --at " + t.At.Format(time.RFC3339Nano) + " -p " + d.Project + " -e " + d.Environment + " -s " + d.Service
}
