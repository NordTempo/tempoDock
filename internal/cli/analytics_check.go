package cli

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// newAnalyticsCheckCmd is `dock analytics check <site>`: whether the
// site's tracking script works, checking its pages now.
func newAnalyticsCheckCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "check <site>",
		Short: "Check that a site's tracking script works: its pages, the script tag and the last event",
		Long: `Check a site's tracking now. Dock fetches each of its hostnames' page,
looks for the script tag (<script src="/_dock/a/s.js" defer></script>) and the
page's Content-Security-Policy, and fetches /_dock/a/s.js to see that it is
Dock's. With the last event it says whether tracking is active, waiting for
the first visit, quiet, stopped, or what to fix. A site is checked at most
once a minute.`,
		Example: `  dock analytics check homepage`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needProject)
			if err != nil {
				return err
			}
			tr, err := send[apitypes.SiteTracking](cmd.Context(), s, http.MethodPost, t.projectPath()+"/sites/"+url.PathEscape(args[0])+"/check", nil)
			if err != nil {
				return err
			}
			return g.io.Print(tr, func() error { return printTracking(g, tr) })
		},
	}
	f.add(cmd, needProject)
	return cmd
}

// printTracking is the human output of a site's tracking.
func printTracking(g *globals, tr apitypes.SiteTracking) error {
	state := string(tr.State)
	if tr.LastEventAt != nil {
		state += ", last event " + ago(*tr.LastEventAt)
	}
	if err := g.io.Fields("tracking", state); err != nil {
		return err
	}
	if tr.StoreError != "" {
		g.io.Warnf("%s", tr.StoreError)
	}
	if tr.Check == nil {
		return nil
	}
	mark := map[apitypes.CheckResult]string{apitypes.CheckOK: "✓", apitypes.CheckFail: "✗", apitypes.CheckSkip: "-"}
	for _, h := range tr.Check.Hosts {
		if _, err := fmt.Fprintln(g.io.Out, "\n"+h.Hostname); err != nil {
			return err
		}
		for _, st := range h.Steps {
			line := fmt.Sprintf("  %s %s", mark[st.Result], st.Text)
			if st.Fix != "" {
				line += "\n      " + st.Fix
			}
			if _, err := fmt.Fprintln(g.io.Out, line); err != nil {
				return err
			}
		}
	}
	return nil
}
