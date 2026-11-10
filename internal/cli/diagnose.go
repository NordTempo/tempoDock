package cli

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// newDiagnoseCmd is `dock diagnose [-r N]`: a failed release's diagnosis
// (ADR 0070), the service's newest without -r.
func newDiagnoseCmd(g *globals) *cobra.Command {
	var f targetFlags
	var release int64
	cmd := &cobra.Command{
		Use:   "diagnose",
		Short: "Why a release failed: the likely cause and the evidence",
		Long: `Why a release failed, from what Dock kept when it failed: the likely cause
and what was ruled out, the health check's answer, what changed since the
last good release, the pre-deploy command, and the last lines of the
container and the build. The service's newest diagnosis, or release N's
with -r. With --json, the whole bundle.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			list, err := get[[]apitypes.DiagnosisSummary](ctx, s, t.servicePath()+"/diagnoses?limit=1"+releaseQuery(release))
			if err != nil {
				return err
			}
			if len(list) == 0 {
				what := t.Service + " has no diagnoses"
				if release > 0 {
					what = fmt.Sprintf("release %d of %s has no diagnosis", release, t.Service)
				}
				return &output.Error{Exit: output.ExitNotFound, Code: string(apitypes.CodeNotFound), Message: what,
					Hint: fmt.Sprintf("Dock keeps one for each failed release, for 30 days. See the releases with `dock releases %s`.", t.flags())}
			}
			d, err := get[apitypes.Diagnosis](ctx, s, t.servicePath()+"/diagnoses/"+strconv.FormatInt(list[0].ID, 10))
			if err != nil {
				return err
			}
			return g.io.Print(d, func() error { return printDiagnosis(g, d, t.flags()) })
		},
	}
	f.add(cmd, needService)
	cmd.Flags().Int64VarP(&release, "release", "r", 0, "release number (default: the service's newest diagnosis)")
	return cmd
}

// releaseQuery is the list's release filter.
func releaseQuery(n int64) string {
	if n <= 0 {
		return ""
	}
	return "&release=" + strconv.FormatInt(n, 10)
}

// printDiagnosis prints a diagnosis the way the release page shows it.
func printDiagnosis(g *globals, d apitypes.Diagnosis, flags string) error {
	out := g.io.Out
	_, _ = fmt.Fprintf(out, "r%d of %s/%s/%s failed in %s after %s.\n\n", d.Release, d.Project, d.Environment, d.Service,
		strings.ReplaceAll(string(d.Failure.Step), "_", " "), humanDuration(d.Failure.DurationMS))
	cause, matched := d.Cause.Summary, d.Cause.Detail
	if d.Cause.Rule == "" {
		cause, matched = cause+": "+d.Failure.Error, ""
	}
	fix := ""
	if f := d.Cause.Fix; f != nil && f.CLI != "" {
		fix = f.CLI
	}
	var ruledOut []string
	for _, c := range d.Checked {
		if !c.Matched {
			ruledOut = append(ruledOut, strings.ReplaceAll(c.Rule, "_", " ")+" ("+c.Why+")")
		}
	}
	if err := g.io.Fields(
		"likely cause", cause,
		"matched", matched,
		"fix", fix,
		"ruled out", strings.Join(ruledOut, ", "),
		"health check", healthText(d.Health),
		"container", exitText(d.Failure.Exit),
		"changed", changesText(d),
		"pre-deploy", preDeployText(d.PreDeploy),
		"errors", errorsText(d.Errors),
		"resources", resourcesText(d.Resources),
	); err != nil {
		return err
	}
	for _, part := range [...]struct {
		title string
		lines []apitypes.DiagnosisLine
	}{{"Output", d.ContainerTail}, {"Build log", d.BuildTail}} {
		if len(part.lines) == 0 {
			continue
		}
		_, _ = fmt.Fprintf(out, "\n%s (last %s):\n", part.title, plural(len(part.lines), "line"))
		for _, l := range part.lines {
			mark := "  "
			if l.Matched {
				mark = "> "
			}
			_, _ = fmt.Fprintln(out, mark+l.Text)
		}
	}
	_, _ = fmt.Fprintf(out, "\nFull output: `dock logs -r %d %s`, build: `dock logs --build -r %d %s`\n", d.Release, flags, d.Release, flags)
	return nil
}

// humanDuration says milliseconds as people read them.
func humanDuration(ms int64) string {
	if ms < 60_000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	return fmt.Sprintf("%dm%02ds", ms/60_000, ms/1000%60)
}

func healthText(h *apitypes.DiagnosisHealth) string {
	if h == nil {
		return ""
	}
	what := "the container had to keep running"
	switch h.Kind {
	case "http":
		what = fmt.Sprintf("GET %s on :%d", h.Path, h.Port)
	case "tcp":
		what = fmt.Sprintf("TCP on :%d", h.Port)
	}
	got := strings.ReplaceAll(h.Result, "_", " ")
	if h.Status != 0 {
		got = fmt.Sprintf("answered %d", h.Status)
	}
	return fmt.Sprintf("%s for %ds: %s", what, h.TimeoutSeconds, cmp.Or(got, "failed"))
}

func exitText(e *apitypes.DiagnosisExit) string {
	switch {
	case e == nil:
		return ""
	case e.Running:
		return fmt.Sprintf("running when stopped, %s", plural(e.Restarts, "restart"))
	case e.OOMKilled:
		return fmt.Sprintf("killed for memory (limit %d MB)", e.MemoryLimitMB)
	case e.ExitCode != nil:
		return fmt.Sprintf("exited with code %d, %s", *e.ExitCode, plural(e.Restarts, "restart"))
	}
	return ""
}

func changesText(d apitypes.Diagnosis) string {
	c := d.Changes
	if c == nil {
		return ""
	}
	var parts []string
	if c.Image != nil {
		img := c.Image.From + " → " + c.Image.To
		if c.Image.Commit != nil {
			img += " (" + shortID(c.Image.Commit.SHA) + " " + c.Image.Commit.Message + ")"
		}
		parts = append(parts, "image "+img)
	}
	for _, v := range [...]struct {
		names []string
		what  string
	}{{c.Variables.Added, "added"}, {c.Variables.Removed, "removed"}, {c.Variables.Changed, "changed"}} {
		if len(v.names) > 0 {
			parts = append(parts, strings.Join(v.names, ", ")+" "+v.what)
		}
	}
	for _, s := range c.Settings {
		parts = append(parts, strings.ReplaceAll(s.Name, "_", " ")+" "+s.From+" → "+s.To)
	}
	if len(parts) == 0 {
		return fmt.Sprintf("nothing since r%d", d.LastGoodRelease)
	}
	return strings.Join(parts, "; ")
}

func preDeployText(p *apitypes.DiagnosisPreDeploy) string {
	switch {
	case p == nil:
		return ""
	case p.TimedOut:
		return fmt.Sprintf("%s: timed out after %s", p.Command, humanDuration(p.DurationMS))
	case p.ExitCode == nil:
		return p.Command + ": couldn't run"
	}
	return fmt.Sprintf("%s: exit %d, %s", p.Command, *p.ExitCode, humanDuration(p.DurationMS))
}

func errorsText(es []apitypes.DiagnosisError) string {
	if len(es) == 0 {
		return "none around the failure"
	}
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = fmt.Sprintf("%s (%d, :%d)", e.Title, e.Count, e.ID)
	}
	return strings.Join(parts, "; ")
}

func resourcesText(r *apitypes.DiagnosisResources) string {
	if r == nil || len(r.MemoryMB) == 0 {
		return ""
	}
	last := len(r.MemoryMB) - 1
	s := fmt.Sprintf("memory %d MB (peak %d)", r.MemoryMB[last], r.PeakMemoryMB)
	if r.MemoryLimitMB > 0 {
		s += fmt.Sprintf(" of %d MB", r.MemoryLimitMB)
	}
	s += fmt.Sprintf(", CPU %.0f%%", r.CPUPercent[last])
	if r.OfRelease > 0 {
		s = fmt.Sprintf("r%d: %s", r.OfRelease, s)
	}
	return s
}
