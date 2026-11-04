package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// newReleaseCmd is `dock release` and its subcommands.
func newReleaseCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{Use: "release", Short: "List, inspect, wait for and roll back releases"})
	cmd.AddCommand(newReleaseListCmd(g, "list"), newReleaseShowCmd(g), newReleaseWaitCmd(g), newRollbackCmd(g))
	return cmd
}

// newReleaseListCmd is `dock release list`, and `dock releases` when use
// is "releases".
func newReleaseListCmd(g *globals, use string) *cobra.Command {
	var f targetFlags
	var limit int
	cmd := &cobra.Command{
		Use:   use,
		Short: "List a service's releases, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			rels, err := get[[]apitypes.Release](cmd.Context(), s, t.servicePath()+"/releases?limit="+strconv.Itoa(limit))
			if err != nil {
				return err
			}
			return g.io.Print(rels, func() error {
				if len(rels) == 0 {
					g.io.Successf("%s has no releases yet. Deploy with `dock deploy %s`.", t, t.flags())
					return nil
				}
				tb := g.io.Table("#", "status", "source", "image", "by", "created")
				for _, r := range rels {
					tb.Row(itoa(r.Number), string(r.Status), describeSource(r), r.ImageRef, r.CreatedBy, ago(r.CreatedAt))
				}
				return tb.Flush()
			})
		},
	}
	if use == "list" {
		cmd.Aliases = []string{"ls"}
	}
	f.add(cmd, needService)
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "how many to show (max 100)")
	return cmd
}

// describeSource is "upload", "rollback of 12" for copies, or "git
// abc1234 fix checkout" for releases built from a push.
func describeSource(r apitypes.Release) string {
	switch {
	case r.FromRelease != 0:
		return fmt.Sprintf("%s of %d", r.Source, r.FromRelease)
	case r.Source == apitypes.SourceUpload && r.Commit != nil:
		msg := r.Commit.Message
		if len([]rune(msg)) > 50 {
			msg = string([]rune(msg)[:49]) + "…"
		}
		return strings.TrimSpace("git " + r.Commit.SHA[:min(7, len(r.Commit.SHA))] + " " + msg)
	}
	return string(r.Source)
}

// parseReleaseNumber parses a release number argument ("12" or "#12").
func parseReleaseNumber(s string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "#"), 10, 64)
	if err != nil || n < 1 {
		return 0, usageError(fmt.Sprintf("%q is not a release number", s), "Release numbers are shown by `dock releases`.")
	}
	return n, nil
}

// newReleaseShowCmd is `dock release show N`: a release with its events
// and, when it failed, the end of its build log and output.
func newReleaseShowCmd(g *globals) *cobra.Command {
	var f targetFlags
	var logLines int
	cmd := &cobra.Command{
		Use:   "show <number>",
		Short: "Show a release: status, image, config and progress events",
		Long: `Show a release: status, image, config and progress events. For a failed
release, also the last lines of its build log and of what its container
wrote (--log-lines, 0 for none); with --json they are its "logs" field.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeReleaseArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := parseReleaseNumber(args[0])
			if err != nil {
				return err
			}
			if logLines < 0 {
				return usageError("--log-lines can't be negative", "Pass 0 to leave out the logs.")
			}
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			r, err := get[apitypes.Release](cmd.Context(), s, t.servicePath()+"/releases/"+itoa(n))
			if err != nil {
				return err
			}
			v := g.viewRelease(cmd.Context(), s, r, logLines)
			return g.io.Print(v, func() error { return printRelease(g, v) })
		},
	}
	f.add(cmd, needService)
	cmd.Flags().IntVar(&logLines, "log-lines", defaultLogLines, "lines of a failed release's build log and output to show, 0 for none")
	return cmd
}

// printRelease prints a release for humans: its fields, events and logs.
func printRelease(g *globals, v releaseView) error {
	r := v.Release
	finished := ""
	if r.FinishedAt != nil {
		finished = ago(*r.FinishedAt)
	}
	errText := r.Error
	if r.ErrorCode != "" {
		errText = string(r.ErrorCode) + ": " + errText
	}
	mounts := make([]string, len(r.Config.Mounts))
	for i, m := range r.Config.Mounts {
		mounts[i] = m.Volume + " at " + m.Path
	}
	commit := ""
	if c := r.Commit; c != nil {
		commit = c.SHA + " " + c.Message
		if c.Author != "" {
			commit += " (" + c.Author + ")"
		}
	}
	if err := g.io.Fields(
		"release", fmt.Sprintf("%d of %s/%s/%s", r.Number, r.Project, r.Environment, r.Service),
		"status", string(r.Status),
		"error", errText,
		"source", describeSource(r),
		"commit", commit,
		"image", r.ImageRef,
		"image id", shortID(r.ImageID),
		"created", ago(r.CreatedAt)+" by "+r.CreatedBy,
		"finished", finished,
		"port", strconv.Itoa(r.Config.Port),
		"health check", r.Config.HealthCheckPath,
		"start command", r.Config.StartCommand,
		"pre-deploy", r.Config.PreDeployCommand,
		"variables", strings.Join(r.Config.VariableKeys, " "),
		"volumes", strings.Join(mounts, ", "),
		"from dock.toml", strings.Join(r.Config.FromRepoFile, ", "),
	); err != nil {
		return err
	}
	if len(r.Events) > 0 {
		_, _ = fmt.Fprintln(g.io.Out)
		tb := g.io.Table("time", "event")
		for _, e := range r.Events {
			tb.Row(e.CreatedAt.Local().Format("15:04:05"), e.Message)
		}
		if err := tb.Flush(); err != nil {
			return err
		}
	}
	if v.Logs == nil {
		return nil
	}
	for _, part := range [...]struct {
		title string
		lines []string
	}{{"Build log", v.Logs.Build}, {"Output", v.Logs.Container}} {
		if len(part.lines) == 0 {
			continue
		}
		_, _ = fmt.Fprintf(g.io.Out, "\n%s (last %s):\n", part.title, plural(len(part.lines), "line"))
		for _, l := range part.lines {
			_, _ = fmt.Fprintln(g.io.Out, "  "+l)
		}
	}
	return nil
}

// newReleaseWaitCmd is `dock release wait [N]`: follow a release someone
// else started (a --detach deploy, a git push) like `dock deploy` does.
func newReleaseWaitCmd(g *globals) *cobra.Command {
	var f targetFlags
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "wait [number]",
		Short: "Follow a release until it is live or failed (default: the newest)",
		Long: `Follow a release's progress until it is live or failed, like ` + "`dock deploy`" + `
does, and exit the same way: 0 once it is live, 6 (deploy_failed) when it
failed. Without a number, the service's newest release. A release that is
already done is printed right away, with the same exit code.

Ctrl-C (or --timeout) stops following; the deploy continues on the server.`,
		Example: `  dock deploy --detach && dock release wait
  dock release wait 12 -p shop -e production -s web --timeout 10m`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeReleaseArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			var n int64
			if len(args) == 1 {
				var err error
				if n, err = parseReleaseNumber(args[0]); err != nil {
					return err
				}
			}
			if timeout < 0 {
				return usageError("--timeout can't be negative", "Leave it out to wait as long as it takes.")
			}
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			// Watch before fetching, so no step between the two is missed.
			w, err := g.watch(ctx, s, t)
			if err != nil {
				return err
			}
			defer w.close()
			r, err := releaseToWait(ctx, s, t, n)
			if err != nil {
				return err
			}
			if !r.Status.Done() {
				g.io.Progressf("Following release %d of %s (%s)", r.Number, t, r.Status)
				following := ctx
				if timeout > 0 {
					var cancel context.CancelFunc
					following, cancel = context.WithTimeout(ctx, timeout)
					defer cancel()
				}
				rels, err := w.follow(following, []apitypes.Release{r})
				if errors.Is(err, context.DeadlineExceeded) {
					return &output.Error{
						Exit:    output.ExitError,
						Message: fmt.Sprintf("release %d of %s is still %s after %s; the deploy continues on the server", r.Number, t, rels[0].Status, timeout),
						Hint:    fmt.Sprintf("Wait again with `dock release wait %d %s`.", r.Number, t.flags()),
					}
				}
				if err != nil {
					return interrupted(err, rels)
				}
				r = rels[0]
			}
			return g.finishRelease(ctx, s, r)
		},
	}
	f.add(cmd, needService)
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "stop following after this long, like 10m (default: no limit)")
	return cmd
}

// releaseToWait is release n of t, or its newest release when n is 0.
func releaseToWait(ctx context.Context, s *session, t target, n int64) (apitypes.Release, error) {
	if n != 0 {
		return get[apitypes.Release](ctx, s, t.servicePath()+"/releases/"+itoa(n))
	}
	rels, err := get[[]apitypes.Release](ctx, s, t.servicePath()+"/releases?limit=1")
	if err != nil {
		return apitypes.Release{}, err
	}
	if len(rels) == 0 {
		return apitypes.Release{}, &output.Error{
			Exit:    output.ExitNotFound,
			Message: t.String() + " has no releases yet",
			Hint:    fmt.Sprintf("Deploy with `dock deploy %s`.", t.flags()),
		}
	}
	return rels[0], nil
}

// newRollbackCmd is `dock rollback [N]` (also `dock release rollback`).
func newRollbackCmd(g *globals) *cobra.Command {
	var f targetFlags
	var detach bool
	cmd := &cobra.Command{
		Use:   "rollback [number]",
		Short: "Roll back to an earlier release (default: the one before the active one)",
		Long: `Start a new release with an earlier release's image and config, through the
same health-checked path as a deploy. Without a number, rolls back to the
release that was serving before the active one.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeReleaseArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			var n int64
			if len(args) == 1 {
				if n, err = parseReleaseNumber(args[0]); err != nil {
					return err
				}
			} else if n, err = previousRelease(ctx, s, t); err != nil {
				return err
			}
			return g.startAndFollow(ctx, s, t, detach, func() (apitypes.Release, error) {
				r, err := send[apitypes.Release](ctx, s, http.MethodPost, t.servicePath()+"/releases/"+itoa(n)+"/rollback", nil)
				if err == nil {
					g.io.Progressf("Rolling %s back: release %d copies release %d, %s", t, r.Number, r.FromRelease, describeCopy(r))
				}
				return r, err
			})
		},
	}
	f.add(cmd, needService)
	addDetachFlag(cmd, &detach)
	return cmd
}

// describeCopy names the image and config a rollback or restart copied:
// "image app:1 (4be1c0ffee12), port 8080, 3 variables".
func describeCopy(r apitypes.Release) string {
	image := "image " + r.ImageRef
	if r.ImageID != "" {
		image += " (" + shortID(r.ImageID) + ")"
	}
	parts := []string{image, "port " + strconv.Itoa(r.Config.Port), plural(len(r.Config.VariableKeys), "variable")}
	if len(r.Config.Mounts) > 0 {
		parts = append(parts, plural(len(r.Config.Mounts), "volume"))
	}
	return strings.Join(parts, ", ")
}

// previousRelease is the newest release that served traffic before the
// active one.
func previousRelease(ctx context.Context, s *session, t target) (int64, error) {
	rels, err := get[[]apitypes.Release](ctx, s, t.servicePath()+"/releases?limit=100")
	if err != nil {
		return 0, err
	}
	active := int64(-1)
	for _, r := range rels {
		if r.Status == apitypes.ReleaseActive {
			active = r.Number
		}
	}
	for _, r := range rels { // newest first
		if r.Status == apitypes.ReleaseSuperseded && (active < 0 || r.Number < active) {
			return r.Number, nil
		}
	}
	return 0, &output.Error{
		Exit:    output.ExitNotFound,
		Message: "no earlier release of " + t.String() + " to roll back to",
		Hint:    fmt.Sprintf("Only releases that served traffic can be rolled back to. See `dock releases %s`.", t.flags()),
	}
}

// newRestartCmd is `dock restart`.
func newRestartCmd(g *globals) *cobra.Command {
	var f targetFlags
	var detach bool
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart a service with a copy of its active release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			return g.startAndFollow(ctx, s, t, detach, func() (apitypes.Release, error) {
				return send[apitypes.Release](ctx, s, http.MethodPost, t.servicePath()+"/restart", nil)
			})
		},
	}
	f.add(cmd, needService)
	addDetachFlag(cmd, &detach)
	return cmd
}
