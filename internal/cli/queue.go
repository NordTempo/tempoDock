package cli

import (
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// newQueueCmd is `dock queue`: the org's deploy queue (ADR 0086, amended
// 2026-11-27), as the top bar's Deploys panel shows it.
func newQueueCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "queue",
		Short: "What each builder builds and what waits for it",
		Long: `Show the deploy queue: for each builder, the release it builds and the
ones waiting behind it in order, then the releases deploying that aren't
at a builder (pulling an image, starting, health checking, or queued
behind a promotion step). A builder builds one release at a time. Another
organization's release, or one you can't read, shows only as its place in
line. Cancel one with ` + "`dock release cancel`" + `. Needs read scope.`,
		Example: `  dock queue
  dock queue --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			q, err := get[apitypes.DeployQueue](cmd.Context(), s, apiPrefix+"/deploy-queue")
			if err != nil {
				return err
			}
			return g.io.Print(q, func() error {
				if len(q.Builders) == 0 && len(q.Deploying) == 0 {
					g.io.Successf("Nothing is building or waiting")
					return nil
				}
				tb := g.io.Table("where", "#", "release", "step", "since", "by")
				for _, b := range q.Builders {
					if it := b.Building; it != nil {
						tb.Row(queueRow(b.Label, "building", *it, it.Since)...)
					}
					for i, it := range b.Waiting {
						tb.Row(queueRow(b.Label, strconv.Itoa(i+1), it, it.QueuedAt)...)
					}
				}
				for _, it := range q.Deploying {
					tb.Row(queueRow("deploying", "", it, it.Since)...)
				}
				return tb.Flush()
			})
		},
	}
}

// queueRow is one release of the queue as a table row: where it is, its
// place, the release, its step, since when (from), who started it.
func queueRow(where, place string, it apitypes.QueueItem, from time.Time) []string {
	if it.Hidden {
		return []string{where, place, "another's release", "", ago(from), ""}
	}
	step := string(it.Status)
	if place != "building" && place != "" {
		step = "waiting"
	}
	return []string{where, place, fmt.Sprintf("%s/%s/%s r%d", it.Project, it.Environment, it.Service, it.Release), step, ago(from), it.CreatedBy}
}
