package cli

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// Log drains (ADR 0046): where an environment's app output is sent.

func newLogDrainCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "drain",
		Short: "Send an environment's output to HTTP, syslog or Loki",
		Long: `A log drain sends the output of every service in an environment to an
HTTP endpoint (JSON lines), a syslog server (RFC 5424) or Loki, as the
services write it. Lines are queued per drain (10,000 at most); when a
drain can't keep up, new lines are dropped and counted.`,
	})
	cmd.AddCommand(newLogDrainListCmd(g), newLogDrainAddCmd(g), newLogDrainRemoveCmd(g), newLogDrainTestCmd(g))
	return cmd
}

// drainsPath is an environment's log drains in the API.
func drainsPath(t target) string { return t.envPath() + "/log-drains" }

func newLogDrainListCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the environment's log drains and how delivery goes",
		Long: `List the environment's log drains with the lines sent and dropped, when
each last delivered and its last error. Counters start at zero when the
daemon starts.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needEnvironment)
			if err != nil {
				return err
			}
			drains, err := get[[]apitypes.LogDrain](cmd.Context(), s, drainsPath(t))
			if err != nil {
				return err
			}
			return g.io.Print(drains, func() error {
				if len(drains) == 0 {
					g.io.Successf("No log drains in %s", t)
					return nil
				}
				tb := g.io.Table("id", "name", "kind", "target", "sent", "dropped", "last sent", "error")
				for _, d := range drains {
					st := d.Status
					tb.Row(itoa(d.ID), d.Name, string(d.Kind), d.Target, itoa(st.Sent), itoa(st.Dropped), agoPtr(st.LastSentAt), dash(st.LastError))
				}
				return tb.Flush()
			})
		},
	}
	f.add(cmd, needEnvironment)
	return cmd
}

func newLogDrainAddCmd(g *globals) *cobra.Command {
	var f targetFlags
	var headers []string
	var req apitypes.AddLogDrainRequest
	cmd := &cobra.Command{
		Use:   "add <http|syslog|loki> <url>",
		Short: "Send the environment's output somewhere (admin)",
		Long: `Add a log drain to the environment. From now on, every line its services
write is sent there:

  http    POST application/x-ndjson, one JSON object per line: time, stream,
          text, project, environment, service, release, server
  syslog  RFC 5424 to tcp://, tls:// or udp:// host[:port] (514, tls 6514);
          APP-NAME is the service, MSGID the stream, HOSTNAME the server
  loki    Loki's push API at the URL (/loki/api/v1/push is added), streams
          labelled project, environment, service and stream

A user and password in an http(s) URL are sent as basic auth; --token as a
bearer token. URLs and tokens often hold secrets: pass - to read the URL
from stdin. URLs, headers and tokens are stored encrypted and never shown.`,
		Example: `  dock logs drain add http https://logs.example.com/ingest --header X-Api-Key=...
  dock logs drain add syslog tls://logs.example.com:6514
  dock logs drain add loki https://loki.example.com --token - < token
  printf %s "$URL" | dock logs drain add http - --name archive`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			req.Kind = apitypes.LogDrainKind(args[0])
			switch req.Kind {
			case apitypes.DrainHTTP, apitypes.DrainSyslog, apitypes.DrainLoki:
			default:
				return usageError(fmt.Sprintf("unknown log drain kind %q", args[0]), "Pass http, syslog or loki.")
			}
			var err error
			if args[1] == "-" && req.Token == "-" {
				return usageError("only one of the URL and --token can be read from stdin", "Put the token in a --header instead, or the password in the URL.")
			}
			if req.URL, err = g.stdinIfDash(args[1]); err != nil {
				return err
			}
			if req.Token, err = g.stdinIfDash(req.Token); err != nil {
				return err
			}
			for _, h := range headers {
				k, v, ok := strings.Cut(h, "=")
				if !ok || k == "" {
					return usageError(fmt.Sprintf("--header %q is not Name=value", h), "Pass --header X-Api-Key=value.")
				}
				if req.Headers == nil {
					req.Headers = map[string]string{}
				}
				req.Headers[k] = v
			}
			s, t, err := g.open(f, needEnvironment)
			if err != nil {
				return err
			}
			d, err := send[apitypes.LogDrain](cmd.Context(), s, http.MethodPost, drainsPath(t), req)
			if err != nil {
				return err
			}
			return g.io.Print(d, func() error {
				g.io.Successf("Added %s log drain %s: %s's output goes to %s", d.Kind, d.Name, t, d.Target)
				_, _ = fmt.Fprintf(g.io.Out, "Send a test line: dock logs drain test %s\n", d.Name)
				return nil
			})
		},
	}
	f.add(cmd, needEnvironment)
	fl := cmd.Flags()
	fl.StringVar(&req.Name, "name", "", "name of the drain (default: the kind)")
	fl.StringArrayVar(&headers, "header", nil, "http and loki: send this header, Name=value (repeatable)")
	fl.StringVar(&req.Token, "token", "", "http and loki: send this bearer token (- reads it from stdin)")
	return cmd
}

// findDrain resolves a drain's ID or name in an environment.
func (g *globals) findDrain(ctx context.Context, s *session, t target, idOrName string) (apitypes.LogDrain, error) {
	drains, err := get[[]apitypes.LogDrain](ctx, s, drainsPath(t))
	if err != nil {
		return apitypes.LogDrain{}, err
	}
	id, _ := strconv.ParseInt(idOrName, 10, 64)
	for _, d := range drains {
		if d.Name == idOrName || d.ID == id {
			return d, nil
		}
	}
	return apitypes.LogDrain{}, &output.Error{Exit: output.ExitNotFound, Message: fmt.Sprintf("no log drain %q in %s", idOrName, t),
		Hint: "Run `dock logs drain list` to see the environment's drains."}
}

func newLogDrainRemoveCmd(g *globals) *cobra.Command {
	var f targetFlags
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove <id|name>",
		Short: "Stop sending the environment's output to a drain (admin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needEnvironment)
			if err != nil {
				return err
			}
			d, err := g.findDrain(cmd.Context(), s, t, args[0])
			if err != nil {
				return err
			}
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Remove log drain %s (%s)? %s's output stops going there.", d.Name, d.Target, t), "yes"); err != nil {
					return err
				}
			}
			if err := del(cmd.Context(), s, drainsPath(t)+"/"+itoa(d.ID)); err != nil {
				return err
			}
			return g.io.Print(map[string]any{"removed": d.Name, "id": d.ID}, func() error {
				g.io.Successf("Removed log drain %s", d.Name)
				return nil
			})
		},
	}
	f.add(cmd, needEnvironment)
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

func newLogDrainTestCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "test <id|name>",
		Short: "Send a test line to a drain (admin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needEnvironment)
			if err != nil {
				return err
			}
			d, err := g.findDrain(cmd.Context(), s, t, args[0])
			if err != nil {
				return err
			}
			res, err := send[apitypes.LogDrainTest](cmd.Context(), s, http.MethodPost, drainsPath(t)+"/"+itoa(d.ID)+"/test", nil)
			if err != nil {
				return err
			}
			if err := g.io.Print(res, func() error {
				if res.OK {
					g.io.Successf("Sent a test line to %s (%s)", res.Drain.Name, res.Drain.Target)
				}
				return nil
			}); err != nil {
				return err
			}
			if !res.OK {
				return &output.Error{Exit: output.ExitUnreachable, Message: "test line to " + d.Name + " failed: " + res.Error,
					Hint: "Check the drain's URL, headers or token; to change them, remove the drain and add it again."}
			}
			return nil
		},
	}
	f.add(cmd, needEnvironment)
	return cmd
}
