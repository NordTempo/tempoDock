package main

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

// writeMarkdown writes t to path as Markdown (-markdown). CI appends it to
// the job summary and posts it on the pull request, where reviewers and
// agents see a regression without opening the job's log.
func writeMarkdown(path string, t *table) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	t.markdown(f)
	return f.Close()
}

// markdown writes a one-line verdict, the rows that need a look (failures,
// warnings, waivers) and errors, then every row folded away.
func (t *table) markdown(w io.Writer) {
	attention := slices.DeleteFunc(slices.Clone(t.rows), func(r row) bool {
		return r.result == ok || r.result == info || r.result == skipped
	})
	var failed, warned int
	for _, r := range attention {
		switch r.result {
		case fail:
			failed++
		case warn:
			warned++
		}
	}

	switch {
	case failed > 0 || warned > 0:
		_, _ = fmt.Fprintf(w, "**%d failed, %d warnings** in %d checks.\n\n", failed, warned, len(t.rows))
	default:
		_, _ = fmt.Fprintf(w, "All %d checks within budget.\n\n", len(t.rows))
	}
	if len(attention) > 0 {
		mdRows(w, attention)
		_, _ = fmt.Fprintln(w)
	}
	for _, r := range t.rows {
		if r.result == waived {
			_, _ = fmt.Fprintf(w, "- waived: %s: %s\n", mdCell(r.check), mdCell(t.waivers[r.check]))
		}
	}
	for _, err := range t.errs {
		_, _ = fmt.Fprintf(w, "\n```\n%s\n```\n", mdError(err))
	}
	if len(attention) < len(t.rows) {
		_, _ = fmt.Fprintf(w, "\n<details><summary>All %d checks</summary>\n\n", len(t.rows))
		mdRows(w, t.rows)
		_, _ = fmt.Fprint(w, "\n</details>\n")
	}
}

// mdRows writes rows as a Markdown table, failures and warnings in bold.
func mdRows(w io.Writer, rows []row) {
	_, _ = fmt.Fprint(w, "| Check | Measured | Budget | Result |\n|---|---|---|---|\n")
	for _, r := range rows {
		res := string(r.result)
		if r.result == fail || r.result == warn {
			res = "**" + res + "**"
		}
		_, _ = fmt.Fprintf(w, "| %s | %s | %s | %s |\n", mdCell(r.check), mdCell(r.measured), mdCell(r.budget), res)
	}
}

// mdErrorMax caps an error in the Markdown: a comment on GitHub holds at
// most 65,536 characters, and a failed go test's output can be most of that.
const mdErrorMax = 4000

// mdError is err without go test's passing output (benchmark results,
// package headers, PASS and ok lines), which leaves the failures, cut to
// mdErrorMax bytes. The job's log has all of it.
func mdError(err error) string {
	noise := []string{"Benchmark", "goos:", "goarch:", "pkg:", "cpu:", "PASS", "ok "}
	lines := slices.DeleteFunc(strings.Split(err.Error(), "\n"), func(l string) bool {
		return slices.ContainsFunc(noise, func(p string) bool { return strings.HasPrefix(l, p) })
	})
	s := strings.Join(lines, "\n")
	if len(s) > mdErrorMax {
		s = s[:mdErrorMax] + "\n... cut; the job's log has the rest"
	}
	return s
}

// mdCell keeps a value inside one table cell.
func mdCell(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ").Replace(s)
}
