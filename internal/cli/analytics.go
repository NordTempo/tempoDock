package cli

import (
	"cmp"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
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
	cmd.AddCommand(&cobra.Command{
		Use:   "server [<server>]",
		Short: "Choose the server the analytics store runs on, and turn analytics on (operator)",
		Long: `Run the analytics store on <server>, or on the control plane's own server
without one, and turn analytics on. Every server keeps its events until
the store has them, so a store that is off for a while loses nothing.

A store already running on another server stops; its data stays there,
and the new store starts empty.`,
		Example: "  dock analytics server homelab\n  dock analytics server",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			req := apitypes.SetAnalyticsServerRequest{}
			if len(args) == 1 {
				req.Server = args[0]
			}
			st, err := send[apitypes.AnalyticsStatus](cmd.Context(), s, http.MethodPut, apiPrefix+"/analytics/server", req)
			if err != nil {
				return err
			}
			return g.io.Print(st, func() error {
				g.io.Successf("The analytics store runs on %s", st.Server)
				return printAnalytics(g, st)
			})
		},
	})
	cmd.AddCommand(newAnalyticsEnableCmd(g), newAnalyticsDisableCmd(g), newAnalyticsSitesCmd(g),
		newAnalyticsStatsCmd(g), newAnalyticsSiteCmd(g), newAnalyticsDefaultsCmd(g))
	return cmd
}

// snippet is the line a site's pages need.
const snippet = `<script src="/_dock/a/s.js" defer></script>`

func newAnalyticsEnableCmd(g *globals) *cobra.Command {
	var site string
	cmd := &cobra.Command{
		Use:   "enable <hostname>",
		Short: "Count visits to a domain: it joins a site, or starts one",
		Long: `Turn analytics on for one of a service's domains. The hostname joins --site,
or the site of the service's other hostnames, or a new site named after it.
Its pages then need one line in their <head>:

  ` + snippet + `

No cookies or browser storage are used. The first site starts the
analytics store.`,
		Example: "  dock analytics enable shop.example.com",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			st, err := send[apitypes.Site](cmd.Context(), s, http.MethodPut, apiPrefix+"/analytics/hostnames/"+url.PathEscape(args[0]), apitypes.EnableAnalyticsRequest{Site: site})
			if err != nil {
				return err
			}
			return g.io.Print(st, func() error {
				g.io.Successf("%s counts for site %s", args[0], st.Name)
				g.io.Detail("Add this to the <head> of its pages: " + snippet)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&site, "site", "", "join this site (in the same environment)")
	return cmd
}

func newAnalyticsDisableCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "disable <hostname>",
		Short: "Stop counting visits to a domain; its site keeps its history",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, apiPrefix+"/analytics/hostnames/"+url.PathEscape(args[0])); err != nil {
				return err
			}
			return g.io.Print(map[string]string{"hostname": args[0]}, func() error {
				g.io.Successf("%s no longer counts visits", args[0])
				return nil
			})
		},
	}
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
					g.io.Successf("No sites yet. Count a domain's visits with `dock analytics enable <hostname>`.")
					return nil
				}
				tb := g.io.Table("site", "environment", "hostnames", "state")
				for _, st := range sites {
					hosts := make([]string, len(st.Hostnames))
					for i, h := range st.Hostnames {
						hosts[i] = h.Hostname
						if h.Service != "" {
							hosts[i] += " (" + h.Service + ")"
						}
					}
					state := "live"
					if st.Archived {
						state = "archived"
					}
					tb.Row(st.Name, st.Environment, dash(strings.Join(hosts, ", ")), state)
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
	cmd.AddCommand(newAnalyticsSiteSetCmd(g))
	return cmd
}

// siteSettingFlags set a site's (or the defaults') settings from flags.
type siteSettingFlags struct {
	replay, maskText, errors, public    string
	sample, events, replays, errorsDays int
	paths, ips, reset                   []string
	name                                string
}

func (sf *siteSettingFlags) add(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&sf.replay, "replay", "", "record sessions: on or off (phase A3)")
	fl.IntVar(&sf.sample, "replay-sample", 0, "percent of page loads recorded, 1 to 100")
	fl.StringVar(&sf.maskText, "mask-text", "", "mask all text in replays: on or off (inputs are always masked)")
	fl.StringVar(&sf.errors, "errors", "", "collect errors and Web Vitals: on or off (phase A2)")
	fl.IntVar(&sf.events, "event-retention-days", 0, "days raw events are kept (daily totals stay)")
	fl.IntVar(&sf.replays, "replay-retention-days", 0, "days replays are kept")
	fl.IntVar(&sf.errorsDays, "error-retention-days", 0, "days errors are kept")
	fl.StringArrayVar(&sf.paths, "exclude-path", nil, "a path not counted, * matches anything; repeat for more (replaces the list)")
	fl.StringArrayVar(&sf.ips, "ignore-ip", nil, "an address or CIDR range not counted; repeat for more (replaces the list)")
	fl.StringVar(&sf.public, "public", "", "public dashboard: on or off (phase A4)")
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
	}{"replay": {sf.replay, &set.Replay}, "mask-text": {sf.maskText, &set.MaskAllText}, "errors": {sf.errors, &set.ErrorsAndVitals}, "public": {sf.public, &set.Public}} {
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
	if err := g.io.Fields("site", st.Name, "environment", st.Environment, "hostnames", dash(strings.Join(hosts, ", "))); err != nil {
		return err
	}
	_, _ = io.WriteString(g.io.Out, "\n")
	return printSettings(g, st.Settings, st.Overrides)
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
	case st.Enabled:
		store = "starting, or not answering; see `docker logs dock-analytics` on " + st.Server
	}
	geo := ""
	if st.GeoIP != "" {
		geo = st.GeoIP + " (" + apitypes.GeoAttribution + ")"
	}
	return g.io.Fields("Analytics", state, "Server", st.Server, "Store", store, "Locations", geo)
}
