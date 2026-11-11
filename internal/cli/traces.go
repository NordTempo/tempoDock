package cli

import (
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// newTracesCmd is `dock traces`: a service's traces, and one trace as a
// tree of its spans (tracing over OTLP).
func newTracesCmd(g *globals) *cobra.Command {
	var f targetFlags
	var q struct {
		rng, method, route string
		release, since     int64
		errors, slowest    bool
		slower             time.Duration
		limit              int
	}
	cmd := &cobra.Command{
		Use:   "traces",
		Short: "A service's traces: the requests and jobs that entered it, newest or slowest first",
		Long: `The traces that entered the service: each one's entry span (name, route,
status, duration, release) and why Dock kept it (sampled, error, slow).
Apps with a stock OpenTelemetry SDK send them once an operator turns
tracing on (` + "`dock tracing on`" + `); see the guide's variables page.
` + "`dock traces show TRACE`" + ` prints one as a tree of its spans.`,
		Example: "  dock traces --slowest --since-release 42\n  dock traces --errors --route /orders\n  dock traces show 4f2a9c1e0000000000000000000000ab",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			v := url.Values{"range": {q.rng}}
			for k, val := range map[string]string{"method": q.method, "route": q.route} {
				if val != "" {
					v.Set(k, val)
				}
			}
			for k, n := range map[string]int64{"release": q.release, "since_release": q.since, "slower_than_ms": q.slower.Milliseconds(), "limit": int64(q.limit)} {
				if n > 0 {
					v.Set(k, strconv.FormatInt(n, 10))
				}
			}
			if q.errors {
				v.Set("errors", "true")
			}
			if q.slowest {
				v.Set("slowest", "true")
			}
			list, err := get[apitypes.TraceList](cmd.Context(), s, t.servicePath()+"/traces?"+v.Encode())
			if err != nil {
				return err
			}
			return g.io.Print(list, func() error {
				if len(list.Traces) == 0 {
					g.io.Successf("No traces of %s in the last %s.", t, list.Range)
					return nil
				}
				tb := g.io.Table("started", "trace", "duration", "entry", "status", "release", "kept")
				for _, tr := range list.Traces {
					status := "ok"
					switch {
					case tr.HTTPStatus > 0:
						status = strconv.Itoa(tr.HTTPStatus)
					case tr.Error:
						status = "error"
					}
					tb.Row(tr.Start.Local().Format("Jan 2 15:04:05"), tr.Trace, msString(tr.DurationMS), entryName(tr), status, "r"+itoa(tr.Release), tr.Kept)
				}
				if err := tb.Flush(); err != nil {
					return err
				}
				if list.More {
					_, _ = fmt.Fprintf(g.io.Out, "\nMore traces match; narrow the range or the filters, or raise --limit (up to 200).\n")
				}
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().StringVar(&q.rng, "range", "24h", "1h, 24h or 7d")
	cmd.Flags().StringVar(&q.method, "method", "", "only this HTTP method")
	cmd.Flags().StringVar(&q.route, "route", "", "only this route, as the app names it, e.g. /orders/{id}")
	cmd.Flags().Int64Var(&q.release, "release", 0, "only this release (number)")
	cmd.Flags().Int64Var(&q.since, "since-release", 0, "only this release (number) and later ones")
	cmd.Flags().BoolVar(&q.errors, "errors", false, "only traces whose entry span failed (an error status or a 5xx)")
	cmd.Flags().DurationVar(&q.slower, "slower-than", 0, "only traces at least this long, e.g. 500ms")
	cmd.Flags().BoolVar(&q.slowest, "slowest", false, "slowest first instead of newest first")
	cmd.Flags().IntVar(&q.limit, "limit", 0, "at most this many (default 50, at most 200)")
	cmd.AddCommand(newTraceShowCmd(g))
	return cmd
}

// newTraceShowCmd is `dock traces show`: one trace as a tree of its spans.
func newTraceShowCmd(g *globals) *cobra.Command {
	var f targetFlags
	var at string
	cmd := &cobra.Command{
		Use:   "show <trace>",
		Short: "Show a trace as a tree of its spans: when each started, how long it took, where it failed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			path := t.servicePath() + "/traces/" + url.PathEscape(args[0])
			if at != "" {
				path += "?at=" + url.QueryEscape(at)
			}
			tr, err := get[apitypes.Trace](cmd.Context(), s, path)
			if err != nil {
				return err
			}
			return g.io.Print(tr, func() error { return printTrace(g.io.Out, tr) })
		},
	}
	f.add(cmd, needService)
	cmd.Flags().StringVar(&at, "at", "", "a time in the trace (RFC 3339), which makes finding it quick; a list's start")
	return cmd
}

// printTrace writes a trace as an indented tree: each span's start after
// the trace's, its duration, service and name, and what failed.
func printTrace(w io.Writer, tr apitypes.Trace) error {
	_, _ = fmt.Fprintf(w, "Trace %s, %s, %d spans, started %s\n\n", tr.Trace, msString(tr.DurationMS), len(tr.Spans), tr.Start.Local().Format("Jan 2 15:04:05.000"))
	ids := map[string]bool{}
	children := map[string][]apitypes.TraceSpan{}
	for _, sp := range tr.Spans {
		ids[sp.Span] = true
	}
	var roots []apitypes.TraceSpan
	for _, sp := range tr.Spans {
		if sp.Parent == "" || !ids[sp.Parent] {
			roots = append(roots, sp)
			continue
		}
		children[sp.Parent] = append(children[sp.Parent], sp)
	}
	var walk func(sp apitypes.TraceSpan, depth int)
	walk = func(sp apitypes.TraceSpan, depth int) {
		name := sp.Name
		who := sp.Service
		if sp.Release > 0 {
			who += " r" + itoa(sp.Release)
		}
		if sp.Hidden {
			name, who = "(a service you can't see)", ""
		}
		mark := ""
		switch {
		case sp.Status == "error" || sp.HTTPStatus >= 500:
			mark = "  ERROR"
			if sp.HTTPStatus > 0 {
				mark += " " + strconv.Itoa(sp.HTTPStatus)
			}
		case sp.HTTPStatus > 0:
			mark = "  " + strconv.Itoa(sp.HTTPStatus)
		}
		_, _ = fmt.Fprintf(w, "%9s %9s  %s%s  %s%s\n", "+"+msString(sp.OffsetMS), msString(sp.DurationMS), strings.Repeat("  ", depth), name, who, mark)
		for _, e := range sp.Events {
			if e.Name != "exception" {
				continue
			}
			msg := fmt.Sprint(e.Attrs["exception.type"])
			if m, ok := e.Attrs["exception.message"]; ok {
				msg += ": " + fmt.Sprint(m)
			}
			_, _ = fmt.Fprintf(w, "%9s %9s  %s  ! %s\n", "", "", strings.Repeat("  ", depth), msg)
		}
		for _, c := range children[sp.Span] {
			walk(c, depth+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	if tr.Truncated {
		_, _ = fmt.Fprintln(w, "\nThe trace has more spans than Dock shows (2,000).")
	}
	return nil
}

// entryName is a trace's entry as a list shows it: method and route when
// it has them, else its span's name.
func entryName(tr apitypes.TraceSummary) string {
	if tr.Route != "" {
		return strings.TrimSpace(tr.Method + " " + tr.Route)
	}
	return tr.Name
}

// msString formats milliseconds: "0.4 ms", "12 ms", "1,284 ms".
func msString(ms float64) string {
	if ms < 10 {
		return strconv.FormatFloat(ms, 'f', 1, 64) + " ms"
	}
	return commas(int64(ms+0.5)) + " ms"
}

// commas writes n with thousands separators.
func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
