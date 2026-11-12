package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// Access requests (ADR 0044): `dock request list|show|wait`. Deciding is
// for admins in the UI only; a token never approves.

func newRequestCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "request",
		Short: "Follow access requests: changes a locked environment refused",
		Long: `A change a token (this CLI included) tries in a locked environment is
refused with exit code 5 and filed as an access request. An admin approves
or rejects it in the web UI, in the Inbox; approved, it runs as the
token that asked. Pass --reason to any command to tell them why.

Admins see their organization's requests; a token sees its own. There is
no approve command: tokens never decide.`,
	})
	cmd.AddCommand(newRequestListCmd(g), newRequestShowCmd(g), newRequestWaitCmd(g))
	return cmd
}

func newRequestListCmd(g *globals) *cobra.Command {
	var status string
	cmd := &cobra.Command{
		Use: "list [--status pending]", Aliases: []string{"ls"}, Short: "List access requests, newest first", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			path := apiPrefix + "/requests"
			if status != "" {
				path += "?status=" + status
			}
			rs, err := get[[]apitypes.AccessRequest](cmd.Context(), s, path)
			if err != nil {
				return err
			}
			return g.io.Print(rs, func() error {
				if len(rs) == 0 {
					_, _ = fmt.Fprintln(g.io.Out, "No access requests.")
					return nil
				}
				t := g.io.Table("id", "status", "request", "asked")
				for _, r := range rs {
					t.Row("#"+strconv.FormatInt(r.ID, 10), string(r.Status), r.Summary, ago(r.CreatedAt))
				}
				return t.Flush()
			})
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "only requests with this status: pending, approved, rejected, expired, done or failed")
	return cmd
}

func newRequestShowCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use: "show <id>", Short: "Show an access request: what it asks, its decision and its outcome", Args: cobra.ExactArgs(1),
		ValidArgsFunction: completeRequestArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requestID(args[0])
			if err != nil {
				return err
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			r, err := get[apitypes.AccessRequest](cmd.Context(), s, apiPrefix+"/requests/"+strconv.FormatInt(id, 10))
			if err != nil {
				return err
			}
			return g.io.Print(r, func() error { printRequest(g, r); return nil })
		},
	}
}

func newRequestWaitCmd(g *globals) *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "wait <id> [--timeout 30m]",
		Short: "Wait until an access request is decided and, if approved, has run",
		Long: `Wait for an admin to decide an access request and, if approved, for its
change to run. Exits 0 when it ran, 1 when it failed, 3 when it was
rejected, 4 when it expired, and 5 when --timeout passes first.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeRequestArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requestID(args[0])
			if err != nil {
				return err
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			deadline := time.Now().Add(timeout)
			g.io.Progressf("Waiting for access request #%d…", id)
			var r apitypes.AccessRequest
			for {
				wait := min(30, max(1, int(time.Until(deadline).Seconds())))
				if r, err = get[apitypes.AccessRequest](ctx, s, fmt.Sprintf("%s/requests/%d?wait=%d", apiPrefix, id, wait)); err != nil {
					return err
				}
				if r.Status.Decided() || !time.Now().Before(deadline) {
					break
				}
			}
			if err := g.io.Print(r, func() error { printRequest(g, r); return nil }); err != nil {
				return err
			}
			return requestExit(r)
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "give up after this long")
	return cmd
}

// requestExit is wait's exit for a request as it ended (nil: it ran).
func requestExit(r apitypes.AccessRequest) error {
	switch r.Status {
	case apitypes.RequestDone:
		return nil
	case apitypes.RequestFailed:
		return &output.Error{Exit: output.ExitError, Code: "request_failed", Message: fmt.Sprintf("access request #%d was approved but failed: %s", r.ID, r.Error)}
	case apitypes.RequestRejected:
		return &output.Error{Exit: output.ExitAuth, Code: "request_rejected", Message: fmt.Sprintf("access request #%d was rejected by %s%s", r.ID, r.DecidedBy, commentOf(r.Comment)),
			Hint: "Don't retry it: ask the admin what they want instead."}
	case apitypes.RequestExpired:
		if r.Comment != "" { // ended early, e.g. its environment was unlocked
			return &output.Error{Exit: output.ExitNotFound, Code: "request_expired", Message: fmt.Sprintf("access request #%d ended without a decision: %s", r.ID, r.Comment)}
		}
		return &output.Error{Exit: output.ExitNotFound, Code: "request_expired", Message: fmt.Sprintf("access request #%d expired without a decision", r.ID)}
	}
	return &output.Error{Exit: output.ExitConflict, Code: "request_pending", Message: fmt.Sprintf("access request #%d is still %s", r.ID, r.Status),
		Hint: fmt.Sprintf("Wait longer with `dock request wait %d --timeout 2h`.", r.ID)}
}

func requestID(s string) (int64, error) {
	id, err := strconv.ParseInt(trimHash(s), 10, 64)
	if err != nil || id < 1 {
		return 0, &output.Error{Exit: output.ExitUsage, Message: fmt.Sprintf("%q is not an access request number", s), Hint: "See `dock request list`."}
	}
	return id, nil
}

func trimHash(s string) string {
	if len(s) > 0 && s[0] == '#' {
		return s[1:]
	}
	return s
}

// printRequest prints one request for people.
func printRequest(g *globals, r apitypes.AccessRequest) {
	w := g.io.Out
	_, _ = fmt.Fprintf(w, "#%d %s: %s\n", r.ID, r.Status, r.Summary)
	_, _ = fmt.Fprintf(w, "  call      %s (%s, %s)\n", r.Title, r.Operation, r.Via)
	if r.Reason != "" {
		_, _ = fmt.Fprintf(w, "  reason    %s\n", r.Reason)
	}
	_, _ = fmt.Fprintf(w, "  asked     %s by %s, expires %s\n", ago(r.CreatedAt), r.RequestedBy, r.ExpiresAt.Local().Format("2006-01-02 15:04"))
	if r.DecidedBy != "" {
		_, _ = fmt.Fprintf(w, "  decided   by %s%s\n", r.DecidedBy, commentOf(r.Comment))
	}
	if r.Error != "" {
		_, _ = fmt.Fprintf(w, "  error     %s\n", r.Error)
	}
	if r.ChangesSince != nil && *r.ChangesSince > 0 {
		_, _ = fmt.Fprintf(w, "  changed   %d changes in %s/%s since it was asked\n", *r.ChangesSince, r.Project, r.Environment)
	}
	if len(r.Args) > 0 {
		var v any
		if json.Unmarshal(r.Args, &v) == nil {
			b, _ := json.MarshalIndent(v, "  ", "  ")
			_, _ = fmt.Fprintf(w, "  arguments %s\n", b)
		}
	}
}

func commentOf(c string) string {
	if c == "" {
		return ""
	}
	return ": " + c
}
