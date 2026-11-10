package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// `dock db health`, `queries`, `sessions` and `storage`: database
// insights (the Data panel's Health tab), read live from the database's
// own statistics. Each prints one view; --json prints the API's
// DataInsights for it.

// insightsOf reads the given views.
func insightsOf(ctx context.Context, s *session, data string, q url.Values) (apitypes.DataInsights, error) {
	return get[apitypes.DataInsights](ctx, s, data+"/insights?"+q.Encode())
}

func newDBHealthCmd(g *globals) *cobra.Command {
	return dbCmd(g, &cobra.Command{
		Use:   "health",
		Short: "How the database is doing: connections, cache, size, and what needs a look",
		Long: `Read the database's vital numbers and the hints from every view you may see:
idle transactions and blocked sessions, duplicate, invalid and unused indexes,
tables with dead rows or many full scans, statements that spill to disk.
Statements to copy (DROP INDEX CONCURRENTLY, VACUUM) are shown; Dock never
runs them. Counters count since their last reset or the server's start.`,
		Args: cobra.NoArgs,
	}, func(ctx context.Context, s *session, t target, data string, _ []string) error {
		ins, err := insightsOf(ctx, s, data, url.Values{})
		if err != nil {
			return err
		}
		return g.io.Print(ins, func() error {
			fields := []string{"service", t.String(), "counting since", sinceText(ins)}
			if ov := ins.Overview; ov != nil {
				fields = append(fields,
					"connections", pressureText(ov.Connections),
					"cache hit", ratioText(ov.CacheHit),
					"size", bytesize(ov.Bytes),
					"query stats", string(ov.QueryStats))
				if ov.TransactionIDAge != nil {
					fields = append(fields, "transaction ID age", fmt.Sprintf("%dM", *ov.TransactionIDAge/1_000_000))
				}
			}
			if err := g.io.Fields(fields...); err != nil {
				return err
			}
			printUnavailable(g, ins)
			if len(ins.Hints) == 0 {
				_, err := fmt.Fprintln(g.io.Out, "\nNothing needs a look.")
				return err
			}
			_, _ = fmt.Fprintln(g.io.Out)
			tb := g.io.Table("level", "what", "subject", "detail")
			for _, h := range ins.Hints {
				tb.Row(h.Level, h.Title, dash(h.Subject), h.Detail)
			}
			if err := tb.Flush(); err != nil {
				return err
			}
			for _, h := range ins.Hints {
				if h.Copy != "" {
					_, _ = fmt.Fprintf(g.io.Out, "  %s %s\n", output.GlyphPending, h.Copy)
				}
			}
			return nil
		})
	})
}

func newDBQueriesCmd(g *globals) *cobra.Command {
	var order string
	var limit int
	cmd := dbCmd(g, &cobra.Command{
		Use:   "queries [--order total|mean|calls|rows] [--limit N]",
		Short: "The statements that cost the most, from pg_stat_statements",
		Args:  cobra.NoArgs,
	}, func(ctx context.Context, s *session, _ target, data string, _ []string) error {
		q := url.Values{"view": {string(apitypes.InsightQueries)}, "order": {order}}
		if limit > 0 {
			q.Set("limit", itoa(int64(limit)))
		}
		ins, err := insightsOf(ctx, s, data, q)
		if err != nil {
			return err
		}
		return g.io.Print(ins, func() error {
			printUnavailable(g, ins)
			tb := g.io.Table("calls", "total", "mean", "max", "rows", "cache", "statement")
			for _, st := range ins.Queries {
				tb.Row(itoa(st.Calls), msText(st.TotalMs), msText(st.MeanMs), msText(st.MaxMs), itoa(st.Rows), ratioText(st.CacheHit), statementLine(st.Text, 100))
			}
			if err := tb.Flush(); err != nil {
				return err
			}
			if len(ins.Queries) > 0 {
				_, _ = fmt.Fprintf(g.io.Err, "%d of %d statements, counting since %s\n", len(ins.Queries), ins.QueriesTracked, sinceText(ins))
			}
			return nil
		})
	})
	cmd.Flags().StringVar(&order, "order", "total", "Sort by total or mean time, calls or rows")
	cmd.Flags().IntVar(&limit, "limit", 0, "Statements to show (default 50, at most 100)")
	cmd.AddCommand(newDBQueriesEnableCmd(g), newDBQueriesResetCmd(g))
	return cmd
}

func newDBQueriesEnableCmd(g *globals) *cobra.Command {
	return dbCmd(g, &cobra.Command{
		Use:   "enable",
		Short: "Turn on query stats (pg_stat_statements)",
		Long: `Turn on query stats. For a Postgres template database whose start command
doesn't load pg_stat_statements yet, this stages the change (deploy): it
waits to apply and goes live with the next deploy, which restarts the
database; Dock then creates the extension. Otherwise it creates the
extension as the owner (admin), which needs the server to load it.`,
		Args: cobra.NoArgs,
	}, func(ctx context.Context, s *session, t target, data string, _ []string) error {
		res, err := send[apitypes.DataQueryStatsResult](ctx, s, http.MethodPost, data+"/insights/queries/enable", nil)
		if err != nil {
			return err
		}
		return g.io.Print(res, func() error {
			g.io.Successf("%s", res.Detail)
			if res.State == "staged" {
				_, _ = fmt.Fprintf(g.io.Err, "Apply it with `dock apply -e %s` or the next deploy.\n", t.Environment)
			}
			return nil
		})
	})
}

func newDBQueriesResetCmd(g *globals) *cobra.Command {
	var yes bool
	cmd := dbCmd(g, &cobra.Command{
		Use:   "reset [--yes]",
		Short: "Set query stats back to zero (admin)",
		Args:  cobra.NoArgs,
	}, func(ctx context.Context, s *session, t target, data string, _ []string) error {
		if !yes {
			if err := g.io.Confirm(fmt.Sprintf("Reset the query stats of %s? Every statement counts from zero again.", t), "yes"); err != nil {
				return err
			}
		}
		if _, err := send[struct{}](ctx, s, http.MethodPost, data+"/insights/queries/reset", nil); err != nil {
			return err
		}
		return g.io.Print(map[string]bool{"reset": true}, func() error {
			g.io.Successf("Query stats of %s are back at zero", t)
			return nil
		})
	})
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

func newDBCancelCmd(g *globals) *cobra.Command {
	var terminate bool
	var confirm string
	cmd := dbCmd(g, &cobra.Command{
		Use:   "cancel SESSION [--terminate] [--confirm ENV]",
		Short: "Cancel a session's query, or end the session",
		Long: `Cancel the running query of a session from ` + "`dock db sessions`" + ` (the client gets
an error, its connection stays), or with --terminate end the session (its
connection drops and its open transaction rolls back). Needs data access
level write; ending a session on production needs --confirm <environment>.`,
		Args: cobra.ExactArgs(1),
	}, func(ctx context.Context, s *session, t target, data string, args []string) error {
		req := apitypes.DataCancelRequest{Terminate: terminate, Confirm: confirm}
		res, err := send[apitypes.DataCancelResult](ctx, s, http.MethodPost, data+"/insights/sessions/"+url.PathEscape(args[0])+"/cancel", req)
		if err != nil {
			return err
		}
		return g.io.Print(res, func() error {
			verb := "Cancelled the query of"
			if res.Terminated {
				verb = "Ended the session of"
			}
			g.io.Successf("%s %s (%s), running %s", verb, dash(res.Application), res.User, msText(float64(res.RunningMs)))
			return nil
		})
	})
	cmd.Flags().BoolVar(&terminate, "terminate", false, "End the session instead of cancelling its query")
	cmd.Flags().StringVar(&confirm, "confirm", "", "The environment's name, to end a session on production")
	return cmd
}

func newDBSessionsCmd(g *globals) *cobra.Command {
	var all bool
	cmd := dbCmd(g, &cobra.Command{
		Use:   "sessions [--all]",
		Short: "Sessions that are running, idle in a transaction or waiting, and who blocks them",
		Args:  cobra.NoArgs,
	}, func(ctx context.Context, s *session, _ target, data string, _ []string) error {
		ins, err := insightsOf(ctx, s, data, url.Values{"view": {string(apitypes.InsightSessions)}})
		if err != nil {
			return err
		}
		shown := ins.Sessions[:0:0]
		idle := 0
		for _, ss := range ins.Sessions {
			if ss.State == "idle" && !all {
				idle++
				continue
			}
			shown = append(shown, ss)
		}
		ins.Sessions = shown
		return g.io.Print(ins, func() error {
			printUnavailable(g, ins)
			tb := g.io.Table("session", "application", "user", "state", "for", "waiting on", "statement")
			for _, ss := range shown {
				tb.Row(ss.ID, dash(ss.Application), ss.User, stateText(ss), sinceTime(ss), dash(strings.Join(ss.BlockedBy, ", ")), dash(statementLine(ss.Text, 80)))
			}
			if err := tb.Flush(); err != nil {
				return err
			}
			if idle > 0 {
				_, _ = fmt.Fprintf(g.io.Err, "%d idle sessions hidden; --all shows them\n", idle)
			}
			return nil
		})
	})
	cmd.Flags().BoolVar(&all, "all", false, "Also show idle sessions")
	return cmd
}

func newDBStorageCmd(g *globals) *cobra.Command {
	var indexes bool
	cmd := dbCmd(g, &cobra.Command{
		Use:   "storage [--indexes]",
		Short: "Tables (or indexes) by size, with dead rows, scans and vacuums",
		Args:  cobra.NoArgs,
	}, func(ctx context.Context, s *session, _ target, data string, _ []string) error {
		view := apitypes.InsightTables
		if indexes {
			view = apitypes.InsightIndexes
		}
		ins, err := insightsOf(ctx, s, data, url.Values{"view": {string(view)}})
		if err != nil {
			return err
		}
		return g.io.Print(ins, func() error {
			printUnavailable(g, ins)
			if indexes {
				tb := g.io.Table("index", "table", "size", "scans", "notes")
				for _, x := range ins.Indexes {
					tb.Row(x.Schema+"."+x.Name, x.Table, sizePtr(x.Bytes, 0), itoa(x.Scans), dash(indexNotes(x)))
				}
				return tb.Flush()
			}
			tb := g.io.Table("table", "size", "indexes", "rows", "dead", "full scans", "index scans", "vacuumed")
			for _, x := range ins.Tables {
				tb.Row(x.Schema+"."+x.Name, sizePtr(x.TotalBytes, x.EstimatedBytes), sizePtr(x.IndexBytes, -1), "~"+itoa(x.EstimatedRows),
					itoa(x.DeadRows), itoa(x.SeqScans), itoa(x.IndexScans), agoPtr(x.VacuumedAt))
			}
			return tb.Flush()
		})
	})
	cmd.Flags().BoolVar(&indexes, "indexes", false, "List indexes instead of tables")
	return cmd
}

// printUnavailable says on stderr why views are missing or partial.
func printUnavailable(g *globals, ins apitypes.DataInsights) {
	for _, u := range ins.Unavailable {
		_, _ = fmt.Fprintf(g.io.Err, "%s: %s\n", u.View, u.Reason)
	}
}

// sinceText is when the counters started, "3d ago (start)".
func sinceText(ins apitypes.DataInsights) string {
	if ins.Since == nil {
		return "-"
	}
	return ago(*ins.Since) + " (" + map[string]string{"reset": "stats reset", "start": "server start"}[ins.SinceReason] + ")"
}

// pressureText is "41 of 47 usable (50 max), 1 idle in a transaction, 2
// waiting".
func pressureText(p apitypes.DataPressure) string {
	if p.Full {
		return "every connection is taken"
	}
	return fmt.Sprintf("%d of %d usable (%d max), %d idle in a transaction, %d waiting on locks",
		p.Used, p.Max-p.Reserved, p.Max, p.IdleInTransaction, p.Waiting)
}

// ratioText is a 0-1 ratio as "99.4%", "-" when unknown.
func ratioText(r *float64) string {
	if r == nil {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", *r*100)
}

// msText is a duration in milliseconds, as "0.32ms", "2.05s", "41m8s".
func msText(ms float64) string {
	d := time.Duration(ms * float64(time.Millisecond))
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%.2fms", ms)
	case d < time.Second:
		return fmt.Sprintf("%.0fms", ms)
	case d < time.Minute:
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}

// sizePtr is an exact size, else the estimate as "~1.2 MB" (none: -1).
func sizePtr(n *int64, estimate int64) string {
	switch {
	case n != nil:
		return bytesize(*n)
	case estimate >= 0:
		return "~" + bytesize(estimate) + " (locked)"
	}
	return "-"
}

// statementLine is a statement on one line, whitespace collapsed, cut
// to n characters.
func statementLine(s string, n int) string { return cut(strings.Join(strings.Fields(s), " "), n) }

// stateText is a session's state, "waiting" when it waits on a lock.
func stateText(s apitypes.DataSession) string {
	if len(s.BlockedBy) > 0 {
		return "waiting"
	}
	return strings.ReplaceAll(s.State, "_", " ")
}

// sinceTime is how long a session has been in its state.
func sinceTime(s apitypes.DataSession) string {
	at := s.StateAt
	if s.State == "active" && s.QueryAt != nil {
		at = s.QueryAt
	}
	if at == nil {
		return "-"
	}
	return strings.TrimSuffix(ago(*at), " ago")
}

// indexNotes lists what is special about an index.
func indexNotes(x apitypes.DataIndexStats) string {
	var n []string
	switch {
	case x.Primary:
		n = append(n, "primary key")
	case x.Constraint:
		n = append(n, "constraint")
	case x.Unique:
		n = append(n, "unique")
	}
	if !x.Valid {
		n = append(n, "invalid")
	}
	if x.Duplicate {
		n = append(n, "duplicate")
	}
	if x.Scans == 0 {
		n = append(n, "never scanned")
	}
	return strings.Join(n, ", ")
}
