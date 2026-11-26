package cli

import (
	"cmp"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// Dock Analytics (ADR 0037): the platform side, for operators. Sites and
// their numbers come with `dock analytics` subcommands of their own.

func newAnalyticsCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "analytics",
		Short: "Dock Analytics: cookieless web analytics for your sites",
		Long: `Show Dock Analytics' state: whether it is on, which server runs the
analytics store, and whether the store answers (operator).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			st, err := get[apitypes.AnalyticsStatus](cmd.Context(), s, apiPrefix+"/analytics")
			if err != nil {
				return err
			}
			return g.io.Print(st, func() error { return printAnalytics(g, st) })
		},
	}
	server := &cobra.Command{
		Use:   "server [<server>]",
		Short: "Choose the server the analytics store runs on, and turn analytics on (operator)",
		Long: `Run the analytics store on <server>, or on the control plane's own server
without one, and turn analytics on. Every server keeps its events until
the store has them, so a store that is off for a while loses nothing.

A store already running on another server moves with its data, in the
background: events wait in each server's spool, the data is copied and
checked, then the store and its data are removed from the old server.
Follow it with ` + "`dock analytics`" + `. When the old server is offline its data
can't be copied: --start-empty starts the store empty instead, and the
old store is removed when that server is back.`,
		Example: "  dock analytics server homelab\n  dock analytics server",
		Args:    cobra.MaximumNArgs(1),
	}
	startEmpty := server.Flags().Bool("start-empty", false, "move although the old server is offline: the new store starts empty")
	server.RunE = func(cmd *cobra.Command, args []string) error {
		s, err := g.connect()
		if err != nil {
			return err
		}
		req := apitypes.SetAnalyticsServerRequest{StartEmpty: *startEmpty}
		if len(args) == 1 {
			req.Server = args[0]
		}
		st, err := send[apitypes.AnalyticsStatus](cmd.Context(), s, http.MethodPut, apiPrefix+"/analytics/server", req)
		if err != nil {
			return err
		}
		return g.io.Print(st, func() error {
			if st.MovingTo != "" {
				g.io.Successf("Moving the analytics store from %s to %s; see `dock analytics`", st.Server, st.MovingTo)
			} else {
				g.io.Successf("The analytics store runs on %s", st.Server)
			}
			return printAnalytics(g, st)
		})
	}
	cmd.AddCommand(server, newAnalyticsSwitchCmd(g, true), newAnalyticsSwitchCmd(g, false))
	cmd.AddCommand(newAnalyticsEnableCmd(g), newAnalyticsDisableCmd(g), newAnalyticsMoveCmd(g), newAnalyticsSitesCmd(g),
		newAnalyticsStatsCmd(g), newAnalyticsCheckCmd(g), newAnalyticsErrorsCmd(g), newAnalyticsVitalsCmd(g), newAnalyticsReplaysCmd(g), newAnalyticsGoalsCmd(g), newAnalyticsFunnelsCmd(g), newAnalyticsSiteCmd(g), newAnalyticsDefaultsCmd(g))
	return cmd
}

// newAnalyticsSwitchCmd is `dock analytics on` and `dock analytics off`:
// Dock Analytics for the whole platform (ADR 0101). Per domain it is
// `dock analytics enable|disable <hostname>`.
func newAnalyticsSwitchCmd(g *globals, on bool) *cobra.Command {
	var removeData, yes bool
	cmd := &cobra.Command{
		Use:   "on",
		Args:  cobra.NoArgs,
		Short: "Turn Dock Analytics on for the platform (operator)",
		Long: `Run the analytics store again on the analytics server, on the data it
kept, and have the servers' ingests take events again (ADR 0101).`,
	}
	if !on {
		cmd.Use, cmd.Short = "off", "Turn Dock Analytics off for the platform, keeping or removing its data (operator)"
		cmd.Long = `Stop the analytics store, which frees its memory, and have the servers'
ingests keep nothing; sites' pages still load the script, which then
counts nothing. A domain can't turn analytics on again until an operator
does (` + "`dock analytics on`" + `). The store's data stays for that, unless
--remove-data removes it too: every site's events, replays and errors.`
		cmd.Flags().BoolVar(&removeData, "remove-data", false, "also remove the store's data: every site's events, replays and errors")
		cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if removeData && !yes {
			if err := g.io.Confirm("Turn analytics off and remove every site's events, replays and errors for good?", "yes"); err != nil {
				return err
			}
		}
		s, err := g.connect()
		if err != nil {
			return err
		}
		st, err := send[apitypes.AnalyticsStatus](cmd.Context(), s, http.MethodPut, apiPrefix+"/analytics/enabled",
			apitypes.AnalyticsSwitch{Enabled: on, RemoveData: removeData})
		if err != nil {
			return err
		}
		return g.io.Print(st, func() error {
			switch {
			case on:
				g.io.Successf("Dock Analytics is on; the store runs on %s", st.Server)
			case removeData:
				g.io.Successf("Dock Analytics is off; its store and data are removed")
			default:
				g.io.Successf("Dock Analytics is off; its data stays on %s for `dock analytics on`", st.Server)
			}
			return nil
		})
	}
	return cmd
}

// snippet is the line a site's pages need.
const snippet = `<script src="/_dock/a/s.js" defer></script>`

func newAnalyticsEnableCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Count a service's visits, on every domain of it",
		Long: `Turn analytics on for a service. Its domains that don't redirect count for
its site: the one it had before, with that history, else a new one named
after it. Domains added later count too. Its pages then need one line in
their <head>:

  ` + snippet + `

No cookies or browser storage are used. The first site starts the
analytics store.`,
		Example: "  dock analytics enable -s web -e production",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			st, err := send[apitypes.Site](cmd.Context(), s, http.MethodPut, t.servicePath()+"/analytics", nil)
			if err != nil {
				return err
			}
			return g.io.Print(st, func() error {
				g.io.Successf("%s counts visits for site %s", t.Service, st.Name)
				g.io.Detail("Add this to the <head> of its pages: " + snippet)
				return nil
			})
		},
	}
	f.add(cmd, needService)
	return cmd
}

func newAnalyticsDisableCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "disable",
		Short: "Stop counting a service's visits; its site keeps its history",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, t.servicePath()+"/analytics"); err != nil {
				return err
			}
			return g.io.Print(map[string]string{"service": t.Service}, func() error {
				g.io.Successf("%s no longer counts visits", t.Service)
				return nil
			})
		},
	}
	f.add(cmd, needService)
	return cmd
}

func newAnalyticsMoveCmd(g *globals) *cobra.Command {
	var f targetFlags
	var to string
	cmd := &cobra.Command{
		Use:   "move <site> --to <service>",
		Short: "Hand a site and its history to another service",
		Long: `Move a site, with its history, settings, goals and funnels, to another
service, such as one that replaced the site's own. The new service's
domains count for it from now on, and the old one stops counting. The new
service must not count visits yet. -e and -p name the new service's
environment and project (default: the site's own project and the linked
environment).`,
		Example: "  dock analytics move shop --to web-v2 -e production",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" {
				return &output.Error{Exit: output.ExitUsage, Message: "name the service the site moves to", Hint: "Add --to <service>, e.g. `dock analytics move " + args[0] + " --to web-v2`."}
			}
			s, t, err := g.open(f, needEnvironment)
			if err != nil {
				return err
			}
			req := apitypes.MoveSiteRequest{Project: t.Project, Environment: t.Environment, Service: to}
			st, err := send[apitypes.Site](cmd.Context(), s, http.MethodPost, t.projectPath()+"/sites/"+url.PathEscape(args[0])+"/move", req)
			if err != nil {
				return err
			}
			return g.io.Print(st, func() error {
				g.io.Successf("Site %s is %s's now", st.Name, to)
				return nil
			})
		},
	}
	f.add(cmd, needEnvironment)
	cmd.Flags().StringVar(&to, "to", "", "the service to move the site to")
	return cmd
}

func newAnalyticsSitesCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "sites",
		Short: "List the project's analytics sites",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needProject)
			if err != nil {
				return err
			}
			sites, err := get[[]apitypes.Site](cmd.Context(), s, t.projectPath()+"/sites")
			if err != nil {
				return err
			}
			return g.io.Print(sites, func() error {
				if len(sites) == 0 {
					g.io.Successf("No sites yet. Count a service's visits with `dock analytics enable -s <service>`.")
					return nil
				}
				tb := g.io.Table("site", "environment", "service", "hostnames", "state")
				for _, st := range sites {
					hosts := make([]string, len(st.Hostnames))
					for i, h := range st.Hostnames {
						hosts[i] = h.Hostname
					}
					state := "live"
					if st.Archived {
						state = "archived"
					}
					tb.Row(st.Name, st.Environment, dash(st.Service), dash(strings.Join(hosts, ", ")), state)
				}
				return tb.Flush()
			})
		},
	}
	f.add(cmd, needProject)
	return cmd
}

func newAnalyticsStatsCmd(g *globals) *cobra.Command {
	var f targetFlags
	var r string
	var filters []string
	cmd := &cobra.Command{
		Use:   "stats <site>",
		Short: "A site's visitors, page views, sources and more over a range",
		Example: `  dock analytics stats shop.example.com --range 7d
  dock analytics stats shop.example.com --filter country:is:NO --filter page:contains:/blog`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needProject)
			if err != nil {
				return err
			}
			q := url.Values{"range": {r}}
			for _, fl := range filters {
				q.Add("filter", fl)
			}
			st, err := get[apitypes.SiteStats](cmd.Context(), s, t.projectPath()+"/sites/"+url.PathEscape(args[0])+"/stats?"+q.Encode())
			if err != nil {
				return err
			}
			return g.io.Print(st, func() error { return printSiteStats(g, st) })
		},
	}
	f.add(cmd, needProject)
	cmd.Flags().StringVar(&r, "range", "30d", "24h, 7d, 30d, 90d or 12m")
	cmd.Flags().StringArrayVar(&filters, "filter", nil, "dimension:op:value (op is, is_not or contains); repeat for more")
	return cmd
}

// statsQuery is the range and filters flags of a site's reads.
type statsQuery struct {
	r       string
	filters []string
}

// add registers the flags.
func (sq *statsQuery) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&sq.r, "range", "30d", "24h, 7d, 30d, 90d or 12m")
	cmd.Flags().StringArrayVar(&sq.filters, "filter", nil, "dimension:op:value (op is, is_not or contains); repeat for more")
}

// values are the query's parameters.
func (sq *statsQuery) values() url.Values {
	q := url.Values{"range": {sq.r}}
	for _, fl := range sq.filters {
		q.Add("filter", fl)
	}
	return q
}

func newAnalyticsErrorsCmd(g *globals) *cobra.Command {
	var f targetFlags
	var sq statsQuery
	cmd := &cobra.Command{
		Use:   "errors <site> [<group>]",
		Short: "A site's errors, grouped; or one group's stack, pages and occurrences",
		Long: `List a site's uncaught errors and unhandled rejections over a range,
grouped by type, message and top stack frame, the most frequent first.
Given a group (its 16 hex digits from the list), show its latest stack
trace, the pages, browsers and releases it happened on and its latest
occurrences. Filters take page, hostname, country, browser,
browser_version, os, device, service and release.`,
		Example: `  dock analytics errors shop.example.com --range 7d
  dock analytics errors shop.example.com 9f3c0a1b2c3d4e5f
  dock analytics errors shop.example.com --filter release:is:web`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needProject)
			if err != nil {
				return err
			}
			path := t.projectPath() + "/sites/" + url.PathEscape(args[0]) + "/errors"
			if len(args) == 2 {
				d, err := get[apitypes.SiteError](cmd.Context(), s, path+"/"+url.PathEscape(args[1])+"?"+sq.values().Encode())
				if err != nil {
					return err
				}
				return g.io.Print(d, func() error { return printSiteError(g, d) })
			}
			st, err := get[apitypes.SiteErrors](cmd.Context(), s, path+"?"+sq.values().Encode())
			if err != nil {
				return err
			}
			return g.io.Print(st, func() error { return printSiteErrors(g, st) })
		},
	}
	f.add(cmd, needProject)
	sq.add(cmd)
	return cmd
}

// printSiteErrors prints error groups as a table.
func printSiteErrors(g *globals, st apitypes.SiteErrors) error {
	if !st.StoreOnline {
		g.io.Warnf("%s", st.StoreError)
		return nil
	}
	if !st.Collecting {
		g.io.Warnf("this site doesn't collect errors; turn them on with `dock analytics site set %s --errors on`", st.Site)
	}
	if len(st.Groups) == 0 {
		g.io.Detail("No errors in this range.")
		return nil
	}
	tb := g.io.Table("GROUP", "COUNT", "SESSIONS", "LAST SEEN", "ERROR")
	for _, e := range st.Groups {
		tb.Row(e.Group, itoa(e.Count), itoa(e.Sessions), e.LastSeen.Local().Format("Jan 2 15:04"), cut(oneLine(e.Type+": "+e.Message), 80))
	}
	return tb.Flush()
}

// printSiteError prints one error group.
func printSiteError(g *globals, d apitypes.SiteError) error {
	err := g.io.Fields(
		"error", d.Type+": "+d.Message,
		"frame", dash(d.Frame),
		"count", itoa(d.Count),
		"sessions", itoa(d.Sessions),
		"first seen", d.FirstSeen.Local().Format(time.DateTime)+" ("+dash(d.FirstRelease)+")",
		"last seen", d.LastSeen.Local().Format(time.DateTime),
	)
	if err != nil {
		return err
	}
	if d.Stack != "" {
		_, _ = io.WriteString(g.io.Out, "\n"+d.Stack+"\n")
	}
	for _, b := range []struct {
		name string
		rows []apitypes.AnalyticsRow
	}{{"page", d.Pages}, {"browser", d.Browsers}, {"release", d.Releases}} {
		if len(b.rows) == 0 {
			continue
		}
		_, _ = io.WriteString(g.io.Out, "\n")
		tb := g.io.Table(b.name, "visitors", "count")
		for _, r := range b.rows[:min(len(b.rows), 5)] {
			tb.Row(dash(cmp.Or(r.Label, r.Value)), itoa(r.Visitors), itoa(r.Count))
		}
		if err := tb.Flush(); err != nil {
			return err
		}
	}
	if len(d.Recent) == 0 {
		return nil
	}
	_, _ = io.WriteString(g.io.Out, "\n")
	tb := g.io.Table("AT", "PAGE", "BROWSER", "RELEASE", "SESSION")
	for _, o := range d.Recent[:min(len(d.Recent), 10)] {
		tb.Row(o.At.Local().Format("Jan 2 15:04:05"), o.Path, dash(strings.TrimSpace(o.Browser+" "+o.BrowserVersion)), dash(o.Release), dash(o.Session))
	}
	return tb.Flush()
}

func newAnalyticsReplaysCmd(g *globals) *cobra.Command {
	var f targetFlags
	var sq statsQuery
	var errorsOnly bool
	var limit int
	cmd := &cobra.Command{
		Use:   "replays <site> [<replay>]",
		Short: "A site's session replays; or one replay's page loads and timeline",
		Long: `List a site's recorded visits over a range, newest first: a visitor's
recorded page loads with no gap over 30 minutes. Given a replay, show its
page loads and its timeline of page views, custom events and errors.
Watch replays in the UI, which records every replay watched in the audit
log. Sites record when their replay setting is on:
dock analytics site set SITE --replay on.`,
		Example: `  dock analytics replays shop.example.com --errors
  dock analytics replays shop.example.com 9f3c0a1b2c3d4e5f`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needProject)
			if err != nil {
				return err
			}
			path := t.projectPath() + "/sites/" + url.PathEscape(args[0]) + "/replays"
			if len(args) == 2 {
				d, err := get[apitypes.ReplayDetail](cmd.Context(), s, path+"/"+url.PathEscape(args[1]))
				if err != nil {
					return err
				}
				return g.io.Print(d, func() error { return printReplay(g, d) })
			}
			q := sq.values()
			q.Set("limit", strconv.Itoa(limit))
			if errorsOnly {
				q.Set("errors", "true")
			}
			list, err := get[apitypes.SiteReplays](cmd.Context(), s, path+"?"+q.Encode())
			if err != nil {
				return err
			}
			return g.io.Print(list, func() error { return printReplays(g, list) })
		},
	}
	f.add(cmd, needProject)
	sq.add(cmd)
	cmd.Flags().BoolVar(&errorsOnly, "errors", false, "only replays with an error")
	cmd.Flags().IntVar(&limit, "limit", 50, "at most this many replays")
	return cmd
}

// printReplays prints replays as a table.
func printReplays(g *globals, l apitypes.SiteReplays) error {
	if !l.StoreOnline {
		g.io.Warnf("%s", l.StoreError)
		return nil
	}
	if !l.Recording {
		g.io.Warnf("this site doesn't record replays; turn them on with `dock analytics site set %s --replay on`", l.Site)
	} else if l.WaitsForConsent {
		g.io.Detail("Replay waits for each visitor's consent: it records only on pages that call dock.consent(true). " +
			"To record at load (only if the script loads after your banner's consent): dock analytics site set " + l.Site + " --consent none")
	}
	if len(l.Replays) == 0 {
		g.io.Detail("No replays in this range.")
		return nil
	}
	tb := g.io.Table("REPLAY", "STARTED", "LENGTH", "PAGES", "ENTRY", "DEVICE", "ERRORS")
	for _, r := range l.Replays {
		tb.Row(r.ID, r.Start.Local().Format("Jan 2 15:04"), r.End.Sub(r.Start).Round(time.Second).String(), strconv.Itoa(r.Segments),
			r.EntryPath, dash(strings.TrimSpace(r.Device+" "+r.Browser)), itoa(r.Errors))
	}
	return tb.Flush()
}

// printReplay prints a replay's page loads and timeline.
func printReplay(g *globals, d apitypes.ReplayDetail) error {
	err := g.io.Fields(
		"replay", d.ID,
		"started", d.Start.Local().Format(time.DateTime),
		"length", d.End.Sub(d.Start).Round(time.Second).String(),
		"visitor", dash(strings.TrimSpace(strings.Join([]string{d.Country, d.Device, d.Browser, d.OS}, " "))),
		"release", dash(d.Release),
		"errors", itoa(d.Errors),
	)
	if err != nil || len(d.Timeline) == 0 {
		return err
	}
	_, _ = io.WriteString(g.io.Out, "\n")
	tb := g.io.Table("AT", "WHAT", "PAGE", "DETAIL")
	for _, it := range d.Timeline {
		tb.Row("+"+it.At.Sub(d.Start).Round(time.Second).String(), it.Kind, it.Path, dash(cut(oneLine(it.Name), 80)))
	}
	return tb.Flush()
}

// goalStepFlag reads "page:/pricing" or "event:signup".
func goalStepFlag(v string) (apitypes.GoalStep, error) {
	kind, match, ok := strings.Cut(v, ":")
	if !ok || (kind != "page" && kind != "event") || match == "" {
		return apitypes.GoalStep{}, usageError(fmt.Sprintf("%q isn't a goal", v), "a goal or step looks like page:/pricing (* matches anything) or event:signup")
	}
	return apitypes.GoalStep{Kind: kind, Match: match}, nil
}

func newAnalyticsGoalsCmd(g *globals) *cobra.Command {
	var f targetFlags
	var sq statsQuery
	cmd := &cobra.Command{
		Use:   "goals <site>",
		Short: "A site's goals and their conversions over a range",
		Long: `Show a site's goals with the visitors and sessions that reached them
over a range, how often, and their share of the range's visitors. A goal
is a page (a path, * matching anything) or a custom event (its name).`,
		Example: `  dock analytics goals shop.example.com --range 7d
  dock analytics goals add shop.example.com Signup event:signup
  dock analytics goals add shop.example.com Pricing page:/pricing`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needProject)
			if err != nil {
				return err
			}
			st, err := get[apitypes.SiteGoals](cmd.Context(), s, t.projectPath()+"/sites/"+url.PathEscape(args[0])+"/goals?"+sq.values().Encode())
			if err != nil {
				return err
			}
			return g.io.Print(st, func() error {
				if !st.StoreOnline {
					g.io.Warnf("%s", st.StoreError)
				}
				if len(st.Goals) == 0 {
					g.io.Detail("No goals; add one with `dock analytics goals add`.")
					return nil
				}
				tb := g.io.Table("GOAL", "MATCHES", "VISITORS", "RATE", "COUNT")
				for _, gl := range st.Goals {
					tb.Row(gl.Name, gl.Kind+":"+gl.Match, itoa(gl.Visitors), fmt.Sprintf("%.1f%%", gl.Rate*100), itoa(gl.Count))
				}
				return tb.Flush()
			})
		},
	}
	f.add(cmd, needProject)
	sq.add(cmd)
	var af targetFlags
	add := &cobra.Command{
		Use:   "add <site> <name> <page:/path | event:name>",
		Short: "Add a goal to a site",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			step, err := goalStepFlag(args[2])
			if err != nil {
				return err
			}
			s, t, err := g.open(af, needProject)
			if err != nil {
				return err
			}
			gl, err := send[apitypes.Goal](cmd.Context(), s, http.MethodPost, t.projectPath()+"/sites/"+url.PathEscape(args[0])+"/goals",
				apitypes.CreateGoalRequest{Name: args[1], GoalStep: step})
			if err != nil {
				return err
			}
			return g.io.Print(gl, func() error { g.io.Successf("Goal %s counts %s:%s", gl.Name, gl.Kind, gl.Match); return nil })
		},
	}
	af.add(add, needProject)
	var rf targetFlags
	remove := &cobra.Command{
		Use:   "remove <site> <name>",
		Short: "Remove a site's goal",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(rf, needProject)
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, t.projectPath()+"/sites/"+url.PathEscape(args[0])+"/goals/"+url.PathEscape(args[1])); err != nil {
				return err
			}
			return g.io.Print(map[string]string{"removed": args[1]}, func() error { g.io.Successf("Removed goal %s", args[1]); return nil })
		},
	}
	rf.add(remove, needProject)
	cmd.AddCommand(add, remove)
	return cmd
}

func newAnalyticsFunnelsCmd(g *globals) *cobra.Command {
	var f targetFlags
	var sq statsQuery
	cmd := &cobra.Command{
		Use:   "funnels <site> [<funnel>]",
		Short: "A site's funnels; or how many sessions took one's steps",
		Long: `List a site's funnels, or show one over a range: how many sessions
reached each of its steps after the ones before it. Steps are pages (a
path, * matching anything) or custom events (their names).`,
		Example: `  dock analytics funnels shop.example.com
  dock analytics funnels shop.example.com Checkout --range 7d
  dock analytics funnels add shop.example.com Checkout page:/pricing page:/checkout event:paid`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needProject)
			if err != nil {
				return err
			}
			path := t.projectPath() + "/sites/" + url.PathEscape(args[0]) + "/funnels"
			if len(args) == 1 {
				list, err := get[[]apitypes.Funnel](cmd.Context(), s, path)
				if err != nil {
					return err
				}
				return g.io.Print(list, func() error {
					if len(list) == 0 {
						g.io.Detail("No funnels; add one with `dock analytics funnels add`.")
						return nil
					}
					tb := g.io.Table("FUNNEL", "STEPS")
					for _, fn := range list {
						steps := make([]string, len(fn.Steps))
						for i, st := range fn.Steps {
							steps[i] = st.Kind + ":" + st.Match
						}
						tb.Row(fn.Name, strings.Join(steps, " > "))
					}
					return tb.Flush()
				})
			}
			fn, err := get[apitypes.SiteFunnel](cmd.Context(), s, path+"/"+url.PathEscape(args[1])+"?"+sq.values().Encode())
			if err != nil {
				return err
			}
			return g.io.Print(fn, func() error {
				if !fn.StoreOnline {
					g.io.Warnf("%s", fn.StoreError)
					return nil
				}
				tb := g.io.Table("STEP", "SESSIONS", "VISITORS", "OF FIRST")
				for _, st := range fn.Steps {
					tb.Row(st.Kind+":"+st.Match, itoa(st.Sessions), itoa(st.Visitors), fmt.Sprintf("%.1f%%", st.Conversion*100))
				}
				return tb.Flush()
			})
		},
	}
	f.add(cmd, needProject)
	sq.add(cmd)
	var af targetFlags
	add := &cobra.Command{
		Use:   "add <site> <name> <step> <step> [<step>...]",
		Short: "Add a funnel to a site: 2 to 8 steps, each page:/path or event:name",
		Args:  cobra.RangeArgs(4, 10),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := apitypes.CreateFunnelRequest{Name: args[1]}
			for _, a := range args[2:] {
				st, err := goalStepFlag(a)
				if err != nil {
					return err
				}
				req.Steps = append(req.Steps, st)
			}
			s, t, err := g.open(af, needProject)
			if err != nil {
				return err
			}
			fn, err := send[apitypes.Funnel](cmd.Context(), s, http.MethodPost, t.projectPath()+"/sites/"+url.PathEscape(args[0])+"/funnels", req)
			if err != nil {
				return err
			}
			return g.io.Print(fn, func() error { g.io.Successf("Funnel %s has %d steps", fn.Name, len(fn.Steps)); return nil })
		},
	}
	af.add(add, needProject)
	var rf targetFlags
	remove := &cobra.Command{
		Use:   "remove <site> <name>",
		Short: "Remove a site's funnel",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(rf, needProject)
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, t.projectPath()+"/sites/"+url.PathEscape(args[0])+"/funnels/"+url.PathEscape(args[1])); err != nil {
				return err
			}
			return g.io.Print(map[string]string{"removed": args[1]}, func() error { g.io.Successf("Removed funnel %s", args[1]); return nil })
		},
	}
	rf.add(remove, needProject)
	cmd.AddCommand(add, remove)
	return cmd
}

func newAnalyticsVitalsCmd(g *globals) *cobra.Command {
	var f targetFlags
	var sq statsQuery
	var by []string
	cmd := &cobra.Command{
		Use:   "vitals <site>",
		Short: "A site's Web Vitals: LCP, INP, CLS, FCP and TTFB at the 75th percentile",
		Long: `Show a site's Web Vitals over a range: the 75th percentile of every
page load's LCP, INP, CLS, FCP and TTFB, rated good, needs improvement or
poor by Google's thresholds, overall and with --by for each page, device,
browser, country, service or release.`,
		Example: `  dock analytics vitals shop.example.com
  dock analytics vitals shop.example.com --by page --range 7d`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needProject)
			if err != nil {
				return err
			}
			q := sq.values()
			q["dimension"] = by
			v, err := get[apitypes.SiteVitals](cmd.Context(), s, t.projectPath()+"/sites/"+url.PathEscape(args[0])+"/vitals?"+q.Encode())
			if err != nil {
				return err
			}
			return g.io.Print(v, func() error { return printSiteVitals(g, v) })
		},
	}
	f.add(cmd, needProject)
	sq.add(cmd)
	cmd.Flags().StringArrayVar(&by, "by", nil, "break down by page, hostname, country, browser, browser_version, os, device, service or release; repeat for more")
	return cmd
}

// printSiteVitals prints vitals as a table: all page loads, then each
// value's.
func printSiteVitals(g *globals, v apitypes.SiteVitals) error {
	if !v.StoreOnline {
		g.io.Warnf("%s", v.StoreError)
		return nil
	}
	if !v.Collecting {
		g.io.Warnf("this site doesn't collect Web Vitals; turn them on with `dock analytics site set %s --errors on`", v.Site)
	}
	table := func(title string, rows []apitypes.VitalsRow) error {
		tb := g.io.Table(title, "LOADS", "LCP", "INP", "CLS", "FCP", "TTFB")
		for _, r := range rows {
			tb.Row(dash(cmp.Or(r.Label, r.Value)), itoa(r.Loads), vital(v, "lcp", r.LCP), vital(v, "inp", r.INP), vital(v, "cls", r.CLS), vital(v, "fcp", r.FCP), vital(v, "ttfb", r.TTFB))
		}
		return tb.Flush()
	}
	all := v.All
	all.Value = "all"
	if err := table("PAGE LOADS", []apitypes.VitalsRow{all}); err != nil {
		return err
	}
	dims := slices.Sorted(maps.Keys(v.Breakdowns))
	for _, d := range dims {
		_, _ = io.WriteString(g.io.Out, "\n")
		if err := table(strings.ToUpper(d), v.Breakdowns[d]); err != nil {
			return err
		}
	}
	return nil
}

// vital is a 75th percentile with its rating: "1.8 s good".
func vital(v apitypes.SiteVitals, name string, st apitypes.VitalStat) string {
	if st.P75 < 0 {
		return "-"
	}
	val := fmt.Sprintf("%.0f ms", st.P75)
	switch {
	case name == "cls":
		val = fmt.Sprintf("%.2f", st.P75)
	case st.P75 >= 1000:
		val = fmt.Sprintf("%.1f s", st.P75/1000)
	}
	t := v.Thresholds[name]
	switch {
	case st.P75 <= t.Good:
		return val + " good"
	case st.P75 > t.Poor:
		return val + " poor"
	}
	return val + " fair"
}

// printSiteStats prints a dashboard as text.
func printSiteStats(g *globals, st apitypes.SiteStats) error {
	if !st.StoreOnline {
		g.io.Warnf("%s", st.StoreError)
		return nil
	}
	if st.Notice != "" {
		g.io.Warnf("%s", st.Notice)
	}
	o, p := st.Overview, st.Previous
	err := g.io.Fields(
		"visitors", itoa(o.Visitors)+change(o.Visitors, p.Visitors),
		"page views", itoa(o.Pageviews)+change(o.Pageviews, p.Pageviews),
		"sessions", itoa(o.Sessions)+change(o.Sessions, p.Sessions),
		"bounce rate", fmt.Sprintf("%.0f%%", o.BounceRate*100),
		"session length", (time.Duration(o.DurationMS) * time.Millisecond).Round(time.Second).String(),
		"events", itoa(o.Events),
	)
	if err != nil {
		return err
	}
	defer g.io.Detail("Locations: " + apitypes.GeoAttribution)
	for _, d := range []string{"page", "referrer", "country", "browser", "device", "event", "service"} {
		rows := st.Breakdowns[d]
		if len(rows) == 0 {
			continue
		}
		_, _ = io.WriteString(g.io.Out, "\n")
		tb := g.io.Table(d, "visitors", "count")
		for _, r := range rows[:min(len(rows), 5)] {
			tb.Row(dash(cmp.Or(r.Label, r.Value)), itoa(r.Visitors), itoa(r.Count))
		}
		if err := tb.Flush(); err != nil {
			return err
		}
	}
	return nil
}

// change is " (+12%)" against the previous period, or nothing.
func change(now, before int64) string {
	if before == 0 {
		return ""
	}
	return fmt.Sprintf(" (%+d%%)", (now-before)*100/before)
}

func newAnalyticsSiteCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "site <site>",
		Short: "Show a site and its settings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needProject)
			if err != nil {
				return err
			}
			st, err := get[apitypes.Site](cmd.Context(), s, t.projectPath()+"/sites/"+url.PathEscape(args[0]))
			if err != nil {
				return err
			}
			return g.io.Print(st, func() error { return printSite(g, st) })
		},
	}
	f.add(cmd, needProject)
	cmd.AddCommand(newAnalyticsSiteSetCmd(g), newAnalyticsSitePublicCmd(g))
	return cmd
}

// newAnalyticsSitePublicCmd shows where a site's public dashboard is and
// whether each address works yet.
func newAnalyticsSitePublicCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "public <site>",
		Short: "Show a site's public dashboard addresses and their DNS",
		Long: `Show where a site's public dashboard is, checking each address now: DNS,
then its certificate. Turn the dashboard on with
` + "`dock analytics site set SITE --public on`" + `, and add a domain of your own with
` + "`--public-domain stats.example.com`" + `.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needProject)
			if err != nil {
				return err
			}
			p, err := get[apitypes.SitePublic](cmd.Context(), s, t.projectPath()+"/sites/"+url.PathEscape(args[0])+"/public")
			if err != nil {
				return err
			}
			return g.io.Print(p, func() error {
				if !p.Public {
					_, _ = fmt.Fprintf(g.io.Out, "Site %s has no public dashboard; turn it on with `dock analytics site set %s --public on`.\n", args[0], args[0])
					return nil
				}
				for _, a := range p.Addresses {
					status := strings.ReplaceAll(string(a.Status), "_", " ")
					if a.StatusReason != "" {
						status += ": " + a.StatusReason
					}
					_, _ = fmt.Fprintf(g.io.Out, "%s  %s\n", a.URL, status)
					if a.Status == apitypes.DomainActive {
						continue
					}
					for i, r := range a.DNSRecords {
						label := "  Point DNS:"
						if i > 0 {
							label = "         or:"
						}
						_, _ = fmt.Fprintf(g.io.Out, "%s  %s  %s  →  %s\n", label, r.Type, r.Name, r.Value)
					}
				}
				if len(p.Addresses) == 0 {
					_, _ = io.WriteString(g.io.Out, "No addresses: the control plane has no base domain and the site no --public-domain (or [analytics] stats_listen is off).\n")
				}
				return nil
			})
		},
	}
	f.add(cmd, needProject)
	return cmd
}

// siteSettingFlags set a site's (or the defaults') settings from flags.
type siteSettingFlags struct {
	replay, maskText, errors, public    string
	publicDomain, publicErrors, consent string
	sample, events, replays, errorsDays int
	paths, ips, reset                   []string
	name                                string
}

func (sf *siteSettingFlags) add(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&sf.consent, "consent", "", "what waits for the visitor's consent (dock.consent(true) in the page): replay, all, or none when the script loads only after consent")
	fl.StringVar(&sf.replay, "replay", "", "record sessions: on or off (phase A3)")
	fl.IntVar(&sf.sample, "replay-sample", 0, "percent of page loads recorded, 1 to 100")
	fl.StringVar(&sf.maskText, "mask-text", "", "mask all text in replays: on or off (field values are always masked)")
	fl.StringVar(&sf.errors, "errors", "", "collect errors and Web Vitals: on or off (phase A2)")
	fl.IntVar(&sf.events, "event-retention-days", 0, "days raw events are kept (daily totals stay)")
	fl.IntVar(&sf.replays, "replay-retention-days", 0, "days replays are kept")
	fl.IntVar(&sf.errorsDays, "error-retention-days", 0, "days errors are kept")
	fl.StringArrayVar(&sf.paths, "exclude-path", nil, "a path not counted, * matches anything; repeat for more (replaces the list)")
	fl.StringArrayVar(&sf.ips, "ignore-ip", nil, "an address or CIDR range not counted; repeat for more (replaces the list)")
	fl.StringVar(&sf.public, "public", "", "a public, read-only dashboard at stats-<site>-<random>.<base domain>: on or off")
	fl.StringVar(&sf.publicDomain, "public-domain", "", "also show the public dashboard at this domain of yours, e.g. stats.example.com (a site's own; \"\" for none)")
	fl.StringVar(&sf.publicErrors, "public-errors", "", "show error groups on the public dashboard: on or off")
	fl.StringArrayVar(&sf.reset, "reset", nil, "set a setting back to the default, by its JSON name (e.g. replay_sample_rate); repeat for more")
}

// request turns the flags that were given into an update.
func (sf *siteSettingFlags) request(cmd *cobra.Command) (apitypes.UpdateSiteRequest, error) {
	req := apitypes.UpdateSiteRequest{Name: sf.name, Reset: sf.reset}
	set := &req.Settings
	onOff := func(flag, v string, dst **bool) error {
		if !cmd.Flags().Changed(flag) {
			return nil
		}
		switch v {
		case "on", "true":
			b := true
			*dst = &b
		case "off", "false":
			b := false
			*dst = &b
		default:
			return usageError(fmt.Sprintf("--%s is on or off, not %q", flag, v), "")
		}
		return nil
	}
	num := func(flag string, v int, dst **int) {
		if cmd.Flags().Changed(flag) {
			*dst = &v
		}
	}
	for flag, p := range map[string]struct {
		v   string
		dst **bool
	}{"replay": {sf.replay, &set.Replay}, "mask-text": {sf.maskText, &set.MaskAllText}, "errors": {sf.errors, &set.ErrorsAndVitals}, "public": {sf.public, &set.Public}, "public-errors": {sf.publicErrors, &set.PublicErrors}} {
		if err := onOff(flag, p.v, p.dst); err != nil {
			return req, err
		}
	}
	num("replay-sample", sf.sample, &set.ReplaySampleRate)
	num("event-retention-days", sf.events, &set.EventRetentionDays)
	num("replay-retention-days", sf.replays, &set.ReplayRetentionDays)
	num("error-retention-days", sf.errorsDays, &set.ErrorRetentionDays)
	if cmd.Flags().Changed("exclude-path") {
		set.ExcludedPaths = &sf.paths
	}
	if cmd.Flags().Changed("consent") {
		set.Consent = &sf.consent
	}
	if cmd.Flags().Changed("public-domain") {
		set.PublicDomain = &sf.publicDomain
	}
	if cmd.Flags().Changed("ignore-ip") {
		set.IgnoredIPs = &sf.ips
	}
	return req, nil
}

func newAnalyticsSiteSetCmd(g *globals) *cobra.Command {
	var f targetFlags
	var sf siteSettingFlags
	cmd := &cobra.Command{
		Use:   "set <site>",
		Short: "Rename a site or change its settings",
		Long: `Change a site's settings. Flags given are set on the site; --reset sets a
setting back to the install's default (` + "`dock analytics defaults`" + `).`,
		Example: "  dock analytics site set shop.example.com --exclude-path '/admin/*' --ignore-ip 203.0.113.0/24\n" +
			"  dock analytics site set shop.example.com --reset excluded_paths",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req, err := sf.request(cmd)
			if err != nil {
				return err
			}
			s, t, err := g.open(f, needProject)
			if err != nil {
				return err
			}
			st, err := send[apitypes.Site](cmd.Context(), s, http.MethodPatch, t.projectPath()+"/sites/"+url.PathEscape(args[0]), req)
			if err != nil {
				return err
			}
			return g.io.Print(st, func() error {
				g.io.Successf("Site %s updated", st.Name)
				return printSite(g, st)
			})
		},
	}
	f.add(cmd, needProject)
	sf.add(cmd)
	cmd.Flags().StringVar(&sf.name, "name", "", "rename the site")
	return cmd
}

func newAnalyticsDefaultsCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "defaults",
		Short: "Every site's default settings (operator)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			d, err := get[apitypes.SiteSettings](cmd.Context(), s, apiPrefix+"/analytics/defaults")
			if err != nil {
				return err
			}
			return g.io.Print(d, func() error { return printSettings(g, d, apitypes.SiteSettings{}) })
		},
	}
	var sf siteSettingFlags
	set := &cobra.Command{
		Use:   "set",
		Short: "Change the defaults sites follow where they set nothing (operator)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req, err := sf.request(cmd)
			if err != nil {
				return err
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			d, err := send[apitypes.SiteSettings](cmd.Context(), s, http.MethodPatch, apiPrefix+"/analytics/defaults", req)
			if err != nil {
				return err
			}
			return g.io.Print(d, func() error {
				g.io.Successf("Defaults updated")
				return printSettings(g, d, apitypes.SiteSettings{})
			})
		},
	}
	sf.add(set)
	cmd.AddCommand(set)
	return cmd
}

// printSite prints a site with its hostnames and settings.
func printSite(g *globals, st apitypes.Site) error {
	hosts := make([]string, len(st.Hostnames))
	for i, h := range st.Hostnames {
		hosts[i] = h.Hostname
		if h.Service != "" {
			hosts[i] += " (" + h.Service + ")"
		}
	}
	if err := g.io.Fields("site", st.Name, "environment", st.Environment, "hostnames", dash(strings.Join(hosts, ", ")),
		"public dashboard", dash(strings.Join(st.PublicURLs, ", "))); err != nil {
		return err
	}
	_, _ = io.WriteString(g.io.Out, "\n")
	if err := printPrivacy(g, st.Privacy); err != nil {
		return err
	}
	_, _ = io.WriteString(g.io.Out, "\n")
	return printSettings(g, st.Settings, st.Overrides)
}

// printPrivacy prints what a site's settings mean for consent: its status
// and what it collects, each with its label (ADR 0085).
func printPrivacy(g *globals, p apitypes.SitePrivacy) error {
	if p.NeedsConsent {
		g.io.Warnf("%s %s", p.Summary, p.Detail)
	} else {
		g.io.Detail(strings.TrimSpace(p.Summary + " " + p.Detail))
	}
	words := map[string]string{apitypes.PrivacyNotNeeded: "no consent", apitypes.PrivacyAfterConsent: "after consent",
		apitypes.PrivacyNeedsConsent: "needs consent"}
	tb := g.io.Table("COLLECTS", "CONSENT", "WHY")
	for _, f := range p.Features {
		tb.Row(f.Name, words[f.Consent], f.Why)
	}
	if err := tb.Flush(); err != nil {
		return err
	}
	g.io.Detail("Mention all of these in your privacy policy. Not legal advice.")
	return nil
}

// printSettings prints settings, marking those overrides sets.
func printSettings(g *globals, s, overrides apitypes.SiteSettings) error {
	b := func(v *bool) string {
		if v != nil && *v {
			return "on"
		}
		return "off"
	}
	n := func(v *int) string {
		if v == nil {
			return ""
		}
		return strconv.Itoa(*v)
	}
	l := func(v *[]string) string {
		if v == nil || len(*v) == 0 {
			return "none"
		}
		return strings.Join(*v, ", ")
	}
	mark := func(set bool, v string) string {
		if set {
			return v + " (this site)"
		}
		return v
	}
	o := overrides
	return g.io.Fields(
		"waits for consent", mark(o.Consent != nil, deref(s.Consent)),
		"replay", mark(o.Replay != nil, b(s.Replay)),
		"replay sample", mark(o.ReplaySampleRate != nil, n(s.ReplaySampleRate)+"%"),
		"mask all text", mark(o.MaskAllText != nil, b(s.MaskAllText)),
		"errors and vitals", mark(o.ErrorsAndVitals != nil, b(s.ErrorsAndVitals)),
		"events kept", mark(o.EventRetentionDays != nil, n(s.EventRetentionDays)+" days"),
		"replays kept", mark(o.ReplayRetentionDays != nil, n(s.ReplayRetentionDays)+" days"),
		"errors kept", mark(o.ErrorRetentionDays != nil, n(s.ErrorRetentionDays)+" days"),
		"excluded paths", mark(o.ExcludedPaths != nil, l(s.ExcludedPaths)),
		"ignored addresses", mark(o.IgnoredIPs != nil, l(s.IgnoredIPs)),
		"public dashboard", mark(o.Public != nil, b(s.Public)),
		"public domain", mark(o.PublicDomain != nil, dash(deref(s.PublicDomain))),
		"public errors", mark(o.PublicErrors != nil, b(s.PublicErrors)),
	)
}

// printAnalytics prints the state as fields.
func printAnalytics(g *globals, st apitypes.AnalyticsStatus) error {
	state := "off"
	if st.Enabled {
		state = "on"
	}
	store := "not running"
	switch {
	case st.StoreRunning:
		store = fmt.Sprintf("running (%s), %s events, %s", st.StoreVersion, strconv.FormatInt(st.Events, 10), bytesize(st.StoreBytes))
	case st.Enabled && !st.ServerOnline:
		store = "unreachable: " + st.Server + " is offline; events wait in each server's spool"
	case st.Starting:
		store = "starting (the first start downloads it)"
	case st.Enabled:
		store = "not answering; see `docker logs dock-analytics` on " + st.Server
	}
	geo := ""
	if st.GeoIP != "" {
		geo = st.GeoIP + " (" + apitypes.GeoAttribution + ")"
	}
	moving := ""
	if st.MovingTo != "" {
		moving = "to " + st.MovingTo + ", with its data; events wait in each server's spool"
	}
	return g.io.Fields("Analytics", state, "Server", st.Server, "Store", store, "Moving", moving, "Problem", st.Error, "Locations", geo)
}
