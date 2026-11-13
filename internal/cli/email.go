package cli

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// Dock's own email (ADR 0079): the SMTP server every mail Dock sends goes
// through, set by operators. The console's Email page does the same.

func newEmailCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "email",
		Short: "Dock's email: the SMTP server invites and other mail go through (operator)",
		Long: `Show the SMTP server Dock sends invites and other mail through, and how
sending goes: the latest attempt and the mail waiting. Without one, Dock
sends no mail and invites show their link to copy instead.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			st, err := get[apitypes.EmailSettings](cmd.Context(), s, apiPrefix+"/platform/email")
			if err != nil {
				return err
			}
			return g.io.Print(st, func() error { return printEmail(g, st) })
		},
	}
	cmd.AddCommand(newEmailSetCmd(g), newEmailTestCmd(g), newEmailLogCmd(g), newEmailOffCmd(g))
	return cmd
}

func newEmailSetCmd(g *globals) *cobra.Command {
	var req apitypes.SetEmailRequest
	var security string
	var passwordStdin bool
	cmd := &cobra.Command{
		Use:   "set --host HOST --from ADDRESS [--port N] [--security starttls|tls] [--username U --password-stdin] [--from-name NAME] [--reply-to ADDRESS]",
		Short: "Set the SMTP server Dock sends mail through (operator, recently confirmed)",
		Long: `Set the SMTP server Dock sends invites and other mail through. Flags left
out keep their current value. The password is read from stdin, stored
encrypted and never shown. Port 465 is TLS from the start; on other ports
STARTTLS is used, and required before the password is sent. The server
may be on this server or its network (a local Postfix, a LAN relay).
Send a test with ` + "`dock email test`" + `.`,
		Example: `  printf %s "$SMTP_PASSWORD" | dock email set --host smtp.postmarkapp.com --username "$TOKEN" --password-stdin --from dock@example.com
  dock email set --host 127.0.0.1 --port 25 --from dock@example.com
  dock email set --reply-to ops@example.com`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			cur, err := get[apitypes.EmailSettings](cmd.Context(), s, apiPrefix+"/platform/email")
			if err != nil {
				return err
			}
			// Start from what is set, then the flags given.
			out := apitypes.SetEmailRequest{Host: cur.Host, Port: cur.Port, Security: cur.Security, Username: cur.Username, From: cur.From, FromName: cur.FromName, ReplyTo: cur.ReplyTo}
			f := cmd.Flags()
			if f.Changed("host") {
				out.Host = req.Host
			}
			if f.Changed("port") {
				out.Port = req.Port
				if !f.Changed("security") {
					out.Security = ""
				}
			}
			if f.Changed("security") {
				out.Security = apitypes.EmailSecurity(security)
			}
			if f.Changed("username") {
				out.Username = req.Username
			}
			if f.Changed("from") {
				out.From = req.From
			}
			if f.Changed("from-name") {
				out.FromName = req.FromName
			}
			if f.Changed("reply-to") {
				out.ReplyTo = req.ReplyTo
			}
			if passwordStdin {
				pw, err := readSecret(g, "dock email set --password-stdin")
				if err != nil {
					return err
				}
				out.Password = &pw
			}
			if out.Host == "" || out.From == "" {
				return usageError("email needs a server and a From address", "Pass --host and --from: `dock email set --host smtp.example.com --from dock@example.com`.")
			}
			st, err := send[apitypes.EmailSettings](cmd.Context(), s, http.MethodPut, apiPrefix+"/platform/email", out)
			if err != nil {
				return err
			}
			return g.io.Print(st, func() error {
				g.io.Successf("Dock sends mail through %s", net.JoinHostPort(st.Host, strconv.Itoa(st.Port)))
				g.io.Detail("Send a test with `dock email test`.")
				return nil
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&req.Host, "host", "", "the SMTP server's hostname or address")
	f.IntVar(&req.Port, "port", 0, "its port (default 587, or 465 with --security tls)")
	f.StringVar(&security, "security", "", "starttls or tls (default tls on port 465, else starttls)")
	f.StringVar(&req.Username, "username", "", "the login's username; empty for none")
	f.BoolVar(&passwordStdin, "password-stdin", false, "read the login's password from stdin")
	f.StringVar(&req.From, "from", "", "the From address")
	f.StringVar(&req.FromName, "from-name", "", "the From name (default Tempo Dock)")
	f.StringVar(&req.ReplyTo, "reply-to", "", "a Reply-To address; empty for none")
	return cmd
}

func newEmailTestCmd(g *globals) *cobra.Command {
	var to string
	cmd := &cobra.Command{
		Use:   "test [--to ADDRESS]",
		Short: "Send a test email now (operator)",
		Long:  `Send a test mail through Dock's SMTP server now, to your own address or --to, and say how it went.`,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			res, err := send[apitypes.EmailTest](cmd.Context(), s, http.MethodPost, apiPrefix+"/platform/email/test", apitypes.TestEmailRequest{To: to})
			if err != nil {
				return err
			}
			server := net.JoinHostPort(res.Settings.Host, strconv.Itoa(res.Settings.Port))
			return g.io.Print(res, func() error {
				if !res.OK {
					return &output.Error{Exit: output.ExitError, Message: fmt.Sprintf("the test email to %s failed: %s", res.To, res.Error), Hint: "Check the server's settings with `dock email`, fix them with `dock email set`, and test again."}
				}
				g.io.Successf("Sent a test email to %s through %s", res.To, server)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "who to send it to (default: you)")
	return cmd
}

func newEmailLogCmd(g *globals) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "log [--limit N]",
		Short: "The mail Dock sent, newest first (operator)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			l, err := get[apitypes.EmailLog](cmd.Context(), s, fmt.Sprintf("%s/platform/email/outbox?limit=%d", apiPrefix, limit))
			if err != nil {
				return err
			}
			return g.io.Print(l, func() error {
				if len(l.Deliveries) == 0 {
					g.io.Detail("Dock hasn't sent any mail.")
					return nil
				}
				t := g.io.Table("when", "what", "to", "org", "result")
				for _, d := range l.Deliveries {
					t.Row(ago(d.CreatedAt), string(d.Kind), d.To, dash(d.Org), deliveryResult(d))
				}
				return t.Flush()
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "how many (at most 200)")
	return cmd
}

func newEmailOffCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "off",
		Short: "Turn Dock's email off (operator, recently confirmed)",
		Long:  `Remove the SMTP server. Mail still waiting is given up on; invites show their link to copy instead.`,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			st, err := send[apitypes.EmailSettings](cmd.Context(), s, http.MethodDelete, apiPrefix+"/platform/email", nil)
			if err != nil {
				return err
			}
			return g.io.Print(st, func() error {
				g.io.Successf("Dock's email is off")
				return nil
			})
		},
	}
}

// printEmail prints the settings and how sending goes.
func printEmail(g *globals, st apitypes.EmailSettings) error {
	if !st.Configured {
		g.io.Detail("Email isn't set up: Dock sends no mail. Set a server with `dock email set`.")
		return nil
	}
	login := "none"
	if st.Username != "" {
		login = st.Username
		if st.PasswordSet {
			login += " (password set)"
		}
	}
	links := st.LinksURL
	if links == "" {
		links = "none: Dock has no public address, so mail with a link isn't sent (`dock address set`)"
	}
	return g.io.Fields(
		"server", fmt.Sprintf("%s, %s", net.JoinHostPort(st.Host, strconv.Itoa(st.Port)), securityName(st.Security)),
		"login", login,
		"from", fmt.Sprintf("%s <%s>", st.FromName, st.From),
		"reply-to", st.ReplyTo,
		"links", links,
		"status", emailStatus(st.Status),
		"queue", fmt.Sprintf("%d waiting, %d failed in the last day (dock email log)", st.Status.Queued, st.Status.FailedDay),
	)
}

// securityName is how a security mode reads.
func securityName(s apitypes.EmailSecurity) string {
	if s == apitypes.EmailTLS {
		return "TLS"
	}
	return "STARTTLS"
}

// emailStatus says whether sending works, from the latest attempt.
func emailStatus(s apitypes.EmailStatus) string {
	switch {
	case s.LastErrorAt != nil && (s.LastOKAt == nil || s.LastErrorAt.After(*s.LastOKAt)):
		return fmt.Sprintf("failing since %s: %s", s.LastErrorAt.Local().Format(time.Kitchen), s.LastError)
	case s.LastOKAt != nil:
		return "working: last mail sent " + ago(*s.LastOKAt)
	default:
		return "nothing sent yet (`dock email test`)"
	}
}

// deliveryResult is a mail's outcome, as a cell.
func deliveryResult(d apitypes.EmailDelivery) string {
	switch d.Status {
	case apitypes.EmailFailed:
		return "failed: " + d.Error
	case apitypes.EmailQueued:
		if d.Error != "" {
			return "retrying: " + d.Error
		}
		return "queued"
	}
	return "sent"
}
