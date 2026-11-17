package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// Alerts and where they are sent (ADR 0020). An org's alerts by default;
// --platform for the platform's (ADR 0062).

// withPlatform adds platform=true to an alerts API path when the command
// runs with --platform (a persistent flag of `dock alerts`).
func withPlatform(cmd *cobra.Command, path string) string {
	if on, _ := cmd.Flags().GetBool("platform"); on {
		if strings.Contains(path, "?") {
			return path + "&platform=true"
		}
		return path + "?platform=true"
	}
	return path
}

func newAlertsCmd(g *globals) *cobra.Command {
	var all bool
	var limit int
	cmd := &cobra.Command{
		Use:   "alerts [--all] [--platform]",
		Short: "Open alerts: crash loops, failed deploys, unhealthy services; --platform for servers",
		Long: `List the organization's open alerts, critical first: failed deploys, crash
loops, unhealthy services, failed backups and jobs, uptime, domains and
analytics. Alerts open and resolve by themselves as conditions come and
go; --all also lists resolved ones, newest first.

--platform lists the platform's alerts instead (full disks, memory and
CPU, offline servers, failed Dock updates, the control plane's backup),
for operators. It works with every subcommand: ` + "`dock alerts channel add --platform`" + `
adds a channel for the platform's alerts.

Alerts are sent to the channels set up with ` + "`dock alerts channel add`" + `:
an organization's channels get its alerts, platform channels the
platform's. Thresholds are in the control plane's config.toml ([alerts]).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			path := apiPrefix + "/alerts"
			if all {
				path += "?all=true&limit=" + strconv.Itoa(limit)
			}
			path = withPlatform(cmd, path)
			alerts, err := get[[]apitypes.Alert](cmd.Context(), s, path)
			if err != nil {
				return err
			}
			return g.io.Print(alerts, func() error {
				if len(alerts) == 0 {
					g.io.Successf("No open alerts")
					return nil
				}
				t := g.io.Table("id", "severity", "alert", "opened", "resolved")
				for _, a := range alerts {
					t.Row(itoa(a.ID), string(a.Severity), a.Title, ago(a.OpenedAt), agoPtr(a.ResolvedAt))
				}
				return t.Flush()
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "also list resolved alerts")
	cmd.PersistentFlags().Bool("platform", false, "the platform's alerts and channels instead of the organization's (operators)")
	cmd.Flags().IntVar(&limit, "limit", 100, "with --all: how many (at most 500)")
	cmd.AddCommand(newAlertResolveCmd(g), newAlertChannelCmd(g), newVulnAlertsCmd(g))
	return cmd
}

func newAlertResolveCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "resolve <id>",
		Short: "Resolve an alert by hand (deploy; operators for platform alerts)",
		Long: `Resolve an open alert whose condition won't clear by itself. If the
condition is still there, a new alert opens.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return usageError(fmt.Sprintf("%q is not an alert ID", args[0]), "Run `dock alerts` to see the open alerts' IDs.")
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			a, err := send[apitypes.Alert](cmd.Context(), s, http.MethodPost, apiPrefix+"/alerts/"+itoa(id)+"/resolve", nil)
			if err != nil {
				return err
			}
			return g.io.Print(a, func() error {
				g.io.Successf("Resolved: %s", a.Title)
				return nil
			})
		},
	}
}

func newAlertChannelCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "channel",
		Short: "Where alerts are sent: webhooks, Slack, email (admin; --platform: operators)",
	})
	cmd.AddCommand(newAlertChannelListCmd(g), newAlertChannelAddCmd(g), newAlertChannelRemoveCmd(g), newAlertChannelTestCmd(g))
	return cmd
}

func newAlertChannelListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List alert channels and how their last deliveries went",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			chs, err := get[[]apitypes.AlertChannel](cmd.Context(), s, withPlatform(cmd, apiPrefix+"/alerts/channels"))
			if err != nil {
				return err
			}
			return g.io.Print(chs, func() error {
				t := g.io.Table("name", "kind", "target", "severity", "remind", "last sent", "failing")
				for _, ch := range chs {
					t.Row(ch.Name, string(ch.Kind), ch.Target, string(ch.MinSeverity)+"+", remindEvery(ch.RenotifyMinutes),
						agoPtr(ch.LastSentAt), channelFailing(ch))
				}
				return t.Flush()
			})
		},
	}
}

// remindEvery is a channel's renotify interval in words.
func remindEvery(minutes int) string {
	if minutes == 0 {
		return "never"
	}
	return "every " + (time.Duration(minutes) * time.Minute).String()
}

// channelFailing says whether a channel's deliveries fail, and why.
func channelFailing(ch apitypes.AlertChannel) string {
	if ch.Failures == 0 {
		return "-"
	}
	return fmt.Sprintf("%d: %s", ch.Failures, ch.LastError)
}

// maxSecretInput bounds what a channel reads from stdin.
const maxSecretInput = 4 << 10

func newAlertChannelAddCmd(g *globals) *cobra.Command {
	var webhook, slack, email, minSeverity, oldSMTP string
	var oldPasswordStdin bool
	var renotify time.Duration
	var req apitypes.CreateAlertChannelRequest
	cmd := &cobra.Command{
		Use:   "add <name> (--webhook <url> | --slack <url> | --email <to,...>)",
		Short: "Add an alert channel (admin)",
		Long: `Add a channel alerts are sent to: when they open, when their severity goes
up, as reminders while they stay open (--renotify), and when they resolve.

--webhook POSTs JSON signed with HMAC-SHA256: the Dock-Signature header is
t=<unix time>,v1=<hex HMAC of "<t>.<body>">. Without --secret a signing
secret is generated and printed once.
--slack POSTs {"text": ...} to a Slack-compatible incoming webhook.
--email sends mail through Dock's email server, which an operator sets
up with ` + "`dock email set`" + ` (ADR 0079).

URLs often hold tokens: pass - to read the URL from stdin instead of the
command line. URLs, secrets and passwords are stored encrypted and never
shown again.`,
		Example: `  dock alerts channel add ops --slack - < slack-webhook-url
  dock alerts channel add pager --webhook https://hooks.example.com/dock --min-severity critical
  dock alerts channel add mail --email ops@example.com,oncall@example.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req.Name = args[0]
			set := 0
			for kind, v := range map[apitypes.AlertChannelKind]string{apitypes.ChannelWebhook: webhook, apitypes.ChannelSlack: slack, apitypes.ChannelEmail: email} {
				if v != "" {
					req.Kind = kind
					set++
				}
			}
			if set != 1 {
				return usageError("pass exactly one of --webhook, --slack or --email", "Run `dock alerts channel add --help` for examples.")
			}
			var err error
			switch req.Kind {
			case apitypes.ChannelWebhook, apitypes.ChannelSlack:
				if req.URL, err = g.stdinIfDash(webhook + slack); err != nil {
					return err
				}
			case apitypes.ChannelEmail:
				if oldSMTP != "" || oldPasswordStdin {
					return usageError("email channels send through Dock's email server now; they take no SMTP settings", "Leave out the --smtp-* and --from flags; an operator sets the server with `dock email set`.")
				}
				for _, to := range strings.Split(email, ",") {
					if to = strings.TrimSpace(to); to != "" {
						req.To = append(req.To, to)
					}
				}
			}
			req.MinSeverity = apitypes.AlertSeverity(minSeverity)
			if cmd.Flags().Changed("renotify") {
				minutes := int(renotify / time.Minute)
				req.RenotifyMinutes = &minutes
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			created, err := send[apitypes.CreatedAlertChannel](cmd.Context(), s, http.MethodPost, withPlatform(cmd, apiPrefix+"/alerts/channels"), req)
			if err != nil {
				return err
			}
			return g.io.Print(created, func() error {
				ch := created.Channel
				g.io.Successf("Added %s channel %s: %s alerts and up go to %s", ch.Kind, ch.Name, ch.MinSeverity, ch.Target)
				if created.Warning != "" {
					g.io.Warnf("%s", created.Warning)
				}
				if created.Secret != "" {
					_, _ = fmt.Fprintf(g.io.Out, "\nSigning secret (shown once; check the Dock-Signature header with it):\n\n  %s\n\n", created.Secret)
				}
				platform := ""
				if on, _ := cmd.Flags().GetBool("platform"); on {
					platform = " --platform"
				}
				_, _ = fmt.Fprintf(g.io.Out, "Send a test alert: dock alerts channel test %s%s\n", ch.Name, platform)
				return nil
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&webhook, "webhook", "", "POST signed JSON to this URL (- reads it from stdin)")
	f.StringVar(&slack, "slack", "", "POST to this Slack-compatible incoming webhook (- reads it from stdin)")
	f.StringVar(&req.Secret, "secret", "", "webhook signing secret (generated when not given)")
	f.StringVar(&email, "email", "", "send email to these addresses, comma separated")
	// The SMTP flags from before ADR 0079, hidden, to say where they went.
	for _, name := range []string{"smtp-host", "smtp-port", "smtp-user", "from"} {
		f.StringVar(&oldSMTP, name, "", "gone: Dock's email server sends (dock email)")
		_ = f.MarkHidden(name)
	}
	f.BoolVar(&oldPasswordStdin, "smtp-password-stdin", false, "gone: Dock's email server sends (dock email)")
	_ = f.MarkHidden("smtp-password-stdin")
	f.StringVar(&minSeverity, "min-severity", "warning", "send only alerts this severe: warning or critical")
	f.DurationVar(&renotify, "renotify", 4*time.Hour, "remind about alerts still open this often; 0 never")
	return cmd
}

// stdinIfDash returns v, or a line read from stdin when v is "-".
func (g *globals) stdinIfDash(v string) (string, error) {
	if v != "-" {
		return v, nil
	}
	b, err := io.ReadAll(io.LimitReader(g.io.In, maxSecretInput+1))
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if s == "" || len(b) > maxSecretInput || strings.Contains(s, "\n") {
		return "", &output.Error{Exit: output.ExitUsage, Message: "expected exactly one line on stdin", Hint: "Pipe just the value, e.g. `printf %s \"$URL\" | dock alerts channel add ops --slack -`."}
	}
	return s, nil
}

func newAlertChannelRemoveCmd(g *globals) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an alert channel (admin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Remove alert channel %s? Alerts stop going there.", args[0]), "yes"); err != nil {
					return err
				}
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, withPlatform(cmd, apiPrefix+"/alerts/channels/"+url.PathEscape(args[0]))); err != nil {
				return err
			}
			return g.io.Print(map[string]string{"removed": args[0]}, func() error {
				g.io.Successf("Removed alert channel %s", args[0])
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

func newAlertChannelTestCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "test <name>",
		Short: "Send a test alert to a channel (admin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			res, err := send[apitypes.AlertChannelTest](cmd.Context(), s, http.MethodPost, withPlatform(cmd, apiPrefix+"/alerts/channels/"+url.PathEscape(args[0])+"/test"), nil)
			if err != nil {
				return err
			}
			if err := g.io.Print(res, func() error {
				if res.OK {
					g.io.Successf("Sent a test alert to %s (%s)", res.Channel.Name, res.Channel.Target)
				}
				return nil
			}); err != nil {
				return err
			}
			if !res.OK {
				hint := "Check the channel's URL; to change it, remove the channel and add it again."
				if res.Channel.Kind == apitypes.ChannelEmail {
					hint = "Email channels send through Dock's email server: an operator sets it up with `dock email set` and checks it with `dock email test`. The channel itself needs no change."
				}
				return &output.Error{Exit: output.ExitUnreachable, Message: "test alert to " + args[0] + " failed: " + res.Error, Hint: hint}
			}
			return nil
		},
	}
}
