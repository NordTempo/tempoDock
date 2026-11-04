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

func newReleaseCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{Use: "release", Short: "List, inspect and roll back releases"})
	cmd.AddCommand(newReleaseListCmd(g, "list"), newReleaseShowCmd(g), newRollbackCmd(g))
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
					g.io.Successf("%s has no releases yet. Deploy with `dock deploy`.", t)
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

func parseReleaseNumber(s string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "#"), 10, 64)
	if err != nil || n < 1 {
		return 0, usageError(fmt.Sprintf("%q is not a release number", s), "Release numbers are shown by `dock releases`.")
	}
	return n, nil
}

func newReleaseShowCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "show <number>",
		Short: "Show a release: status, image, config and progress events",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := parseReleaseNumber(args[0])
			if err != nil {
				return err
			}
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			r, err := get[apitypes.Release](cmd.Context(), s, t.servicePath()+"/releases/"+itoa(n))
			if err != nil {
				return err
			}
			return g.io.Print(r, func() error {
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
					return tb.Flush()
				}
				return nil
			})
		},
	}
	f.add(cmd, needService)
	return cmd
}

func newRollbackCmd(g *globals) *cobra.Command {
	var f targetFlags
	var detach bool
	cmd := &cobra.Command{
		Use:   "rollback [number]",
		Short: "Roll back to an earlier release (default: the one before the active one)",
		Long: `Start a new release with an earlier release's image and config, through the
same health-checked path as a deploy. Without a number, rolls back to the
release that was serving before the active one.`,
		Args: cobra.MaximumNArgs(1),
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
		Hint:    "Only releases that served traffic can be rolled back to. See `dock releases`.",
	}
}

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
