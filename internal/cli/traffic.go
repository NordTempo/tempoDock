package cli

import (
	"fmt"
	"io"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// newTrafficCmd is `dock traffic`: what the service's domains served over a
// range (ADR 0033).
func newTrafficCmd(g *globals) *cobra.Command {
	var f targetFlags
	var r string
	cmd := &cobra.Command{
		Use:   "traffic",
		Short: "Requests, errors and the most requested paths of the service's domains",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			tr, err := get[apitypes.ServiceTraffic](cmd.Context(), s, t.servicePath()+"/traffic?range="+url.QueryEscape(r))
			if err != nil {
				return err
			}
			return g.io.Print(tr, func() error {
				tot := tr.Totals
				if tot.Requests == 0 {
					g.io.Successf("%s served no requests in the last %s. Traffic is counted for the service's domains (`dock domain list`).", t, tr.Range)
					return nil
				}
				errs := fmt.Sprintf("%s 4xx, %s 5xx", itoa(tot.Status4xx), itoa(tot.Status5xx))
				err := g.io.Fields(
					"range", fmt.Sprintf("last %s, since %s", tr.Range, tr.Since.Local().Format("Jan 2 15:04")),
					"requests", itoa(tot.Requests),
					"errors", errs,
					"avg time", fmt.Sprintf("%.0f ms", tot.AvgDurationMS),
					"sent", bytesize(tot.Bytes),
				)
				if err != nil || len(tr.Paths) == 0 {
					return err
				}
				_, _ = io.WriteString(g.io.Out, "\n")
				tb := g.io.Table("method", "path", "requests", "share")
				for _, p := range tr.Paths {
					tb.Row(p.Method, p.Path, itoa(p.Requests), share(p.Requests, tot.Requests))
				}
				return tb.Flush()
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().StringVar(&r, "range", "24h", "24h, 7d or 30d")
	return cmd
}

// share is n as a percentage of total, "12%" or "<1%".
func share(n, total int64) string {
	switch {
	case n == 0 || total == 0:
		return "0%"
	case n*100 < total:
		return "<1%"
	}
	return fmt.Sprintf("%d%%", n*100/total)
}
