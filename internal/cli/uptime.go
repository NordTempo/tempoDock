package cli

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// newUptimeCmd is `dock uptime`: a service's uptime checks (ADR 0055).
func newUptimeCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{Use: "uptime", Short: "Check a service's URLs from another server"})
	cmd.AddCommand(newUptimeListCmd(g), newUptimeAddCmd(g), newUptimeDomainsCmd(g), newUptimeUpdateCmd(g), newUptimeRemoveCmd(g))
	return cmd
}

func newUptimeListCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List uptime checks with their state and uptime",
		Long: `List the service's uptime checks: state, the share of probes that were up
over 24 hours, 7 and 30 days, the last probe and the server that probes.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			checks, err := get[[]apitypes.UptimeCheck](cmd.Context(), s, t.servicePath()+"/uptime-checks")
			if err != nil {
				return err
			}
			return g.io.Print(checks, func() error {
				if len(checks) == 0 {
					g.io.Successf("%s has no uptime checks. Add one with `dock uptime add <url>`.", t)
					return nil
				}
				tb := g.io.Table("id", "url", "state", "24h", "7d", "30d", "last probe", "from", "every")
				for _, c := range checks {
					url := c.URL
					if c.Auto {
						url += " (auto)"
					}
					tb.Row(strconv.FormatInt(c.ID, 10), url, string(c.State), pct(c.Uptime24h), pct(c.Uptime7d), pct(c.Uptime30d),
						lastProbe(c.LastProbe), c.Server, (time.Duration(c.IntervalSeconds) * time.Second).String())
				}
				if err := tb.Flush(); err != nil {
					return err
				}
				for _, c := range checks {
					for _, in := range c.Incidents {
						if in.ResolvedAt == nil {
							_, _ = fmt.Fprintf(g.io.Out, "\n%s is down since %s: %s\n", c.URL, ago(in.OpenedAt), in.Message)
						}
					}
				}
				return nil
			})
		},
	}
	f.add(cmd, needService)
	return cmd
}

// pct formats an uptime percentage, "-" without probes.
func pct(p *float64) string {
	if p == nil {
		return "-"
	}
	return uptimePercent(*p)
}

// uptimePercent shows 100% only for a perfect record and otherwise at
// most 99.99%, so a single failed probe never rounds away.
func uptimePercent(p float64) string {
	if p >= 100 {
		return "100%"
	}
	return strconv.FormatFloat(min(p, 99.99), 'f', 2, 64) + "%"
}

// lastProbe is a probe as "200 in 41 ms, 20s ago" or "502: <error>, 20s ago".
func lastProbe(p *apitypes.UptimeProbe) string {
	if p == nil {
		return "-"
	}
	s := fmt.Sprintf("%d in %d ms", p.Status, p.LatencyMS)
	if !p.Up {
		s = p.Error
	}
	return s + ", " + ago(p.At)
}

func newUptimeAddCmd(g *globals) *cobra.Command {
	var f targetFlags
	var status string
	var interval, timeout time.Duration
	cmd := &cobra.Command{
		Use:   "add <url>",
		Short: "Probe a URL on one of the service's domains",
		Long: `Add an uptime check: Dock sends GET to the URL every --interval from another
server (the control plane when there is no other) and expects --status
within --timeout. Redirects aren't followed. Two failures in a row open an
uptime_down alert; the next success resolves it. The URL's host must be one
of the service's domains. Project admins only; at most 10 per service.`,
		Example: `  dock uptime add https://shop.example.com/health -s web
  dock uptime add https://shop.example.com/ --status 3xx --interval 5m`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			if interval%time.Second != 0 || timeout%time.Second != 0 {
				return &output.Error{Exit: output.ExitUsage, Message: "--interval and --timeout are whole seconds", Hint: "For example --interval 90s --timeout 5s."}
			}
			req := apitypes.AddUptimeCheckRequest{
				URL: args[0], ExpectStatus: status,
				IntervalSeconds: int(interval / time.Second), TimeoutSeconds: int(timeout / time.Second),
			}
			c, err := send[apitypes.UptimeCheck](cmd.Context(), s, "POST", t.servicePath()+"/uptime-checks", req)
			if err != nil {
				return err
			}
			return g.io.Print(c, func() error {
				g.io.Successf("Added uptime check %d: %s every %ds from %s, expecting %s", c.ID, c.URL, c.IntervalSeconds, c.Server, c.ExpectStatus)
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().StringVar(&status, "status", "200", "the status that means up: a code, 2xx or 3xx")
	cmd.Flags().DurationVar(&interval, "interval", time.Minute, "how often to probe, 30s to 1h")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "how long a probe may take, 1s to 30s and below the interval")
	return cmd
}

// newUptimeDomainsCmd adds a check of each verified domain that has none
// (ADR 0059): what Dock does by itself for domains verified from now on.
func newUptimeDomainsCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "domains",
		Short: "Check every verified domain that has no uptime check",
		Long: `Add an uptime check of https://<domain>/ for each of the service's verified
domains that has none. Dock adds these by itself when a domain is verified
and removes them with the domain; this catches up domains from before.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			checks, err := send[[]apitypes.UptimeCheck](cmd.Context(), s, http.MethodPost, t.servicePath()+"/uptime-checks/domains", nil)
			if err != nil {
				return err
			}
			return g.io.Print(checks, func() error {
				g.io.Successf("%s has %s; `dock uptime list` shows them", t, plural(len(checks), "uptime check"))
				return nil
			})
		},
	}
	f.add(cmd, needService)
	return cmd
}

// newUptimeUpdateCmd is `dock uptime update`: a check's interval or
// expected status, changed in place with its history kept.
func newUptimeUpdateCmd(g *globals) *cobra.Command {
	var f targetFlags
	var status string
	var interval time.Duration
	cmd := &cobra.Command{
		Use:   "update <id> [--interval 5m] [--status 2xx]",
		Short: "Change an uptime check's interval or expected status",
		Long: `Change a check in place: its history stays and the new interval applies at
once. Project admins only.`,
		Example: `  dock uptime update 3 --interval 5m
  dock uptime update 3 --status 3xx -s web`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(strings.TrimSpace(args[0]), 10, 64)
			if err != nil {
				return &output.Error{Exit: output.ExitUsage, Message: fmt.Sprintf("%q is not a check ID", args[0]), Hint: "`dock uptime list` shows the IDs."}
			}
			var req apitypes.UpdateUptimeCheckRequest
			if cmd.Flags().Changed("interval") {
				if interval%time.Second != 0 {
					return &output.Error{Exit: output.ExitUsage, Message: "--interval is whole seconds", Hint: "For example --interval 90s."}
				}
				secs := int(interval / time.Second)
				req.IntervalSeconds = &secs
			}
			if cmd.Flags().Changed("status") {
				req.ExpectStatus = &status
			}
			if req.IntervalSeconds == nil && req.ExpectStatus == nil {
				return &output.Error{Exit: output.ExitUsage, Message: "nothing to change", Hint: "Give --interval, --status or both."}
			}
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			c, err := send[apitypes.UptimeCheck](cmd.Context(), s, http.MethodPatch, t.servicePath()+"/uptime-checks/"+strconv.FormatInt(id, 10), req)
			if err != nil {
				return err
			}
			return g.io.Print(c, func() error {
				g.io.Successf("Uptime check %d: %s every %ds, expecting %s", c.ID, c.URL, c.IntervalSeconds, c.ExpectStatus)
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().StringVar(&status, "status", "", "the status that means up: a code, 2xx or 3xx")
	cmd.Flags().DurationVar(&interval, "interval", 0, "how often to probe, 30s to 1h")
	return cmd
}

func newUptimeRemoveCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:     "remove <id>",
		Aliases: []string{"rm"},
		Short:   "Remove an uptime check and its history",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(strings.TrimSpace(args[0]), 10, 64)
			if err != nil {
				return &output.Error{Exit: output.ExitUsage, Message: fmt.Sprintf("%q is not a check ID", args[0]), Hint: "`dock uptime list` shows the IDs."}
			}
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, t.servicePath()+"/uptime-checks/"+strconv.FormatInt(id, 10)); err != nil {
				return err
			}
			v := struct {
				ID      int64 `json:"id"`
				Removed bool  `json:"removed"`
			}{id, true}
			return g.io.Print(v, func() error {
				g.io.Successf("Removed uptime check %d from %s", id, t)
				return nil
			})
		},
	}
	f.add(cmd, needService)
	return cmd
}
