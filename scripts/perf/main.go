// Command perf checks Tempo Dock against its performance budgets and prints
// a table: binary size, CLI start-up, daemon idle memory and CPU (also with
// SSE clients connected), API read latency on a seeded daemon, UI bundle
// sizes, and Go benchmarks compared with a checked-in baseline; and the same
// for tempoBase, the standalone Data panel server (tempobase.go). It exits
// non-zero when anything is over budget.
//
// Run it through make, which builds the UIs and binaries first:
//
//	make perf            # check everything
//	make perf-baseline   # re-record scripts/perf/baseline.json
//
// Budgets and how to read the results are in docs/performance.md.
package main

import (
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

// Budgets. Absolute ones are product targets (docs/experience.md) with
// headroom; relative ones compare against baseline.json.
const (
	cliBudget       = 20 * time.Millisecond
	idleAnonBudget  = 30 << 20
	idleTicksBudget = 2 // clock ticks (usually 10 ms each) per idle window
	// Wake-ups (voluntary context switches) per second: what a forgotten
	// ticker shows up as long before it costs measurable CPU. Each SSE
	// client adds a heartbeat every 25 s.
	idleWakeupsBudget      = 0.2
	sseClientWakeupsBudget = 0.2
	apiP95Budget           = 20 * time.Millisecond
	initialJSBudget        = 200 << 10 // gzipped
	cssBudget              = 30 << 10  // gzipped
	fontsBudget            = 150 << 10 // woff2, already compressed
	binaryGrowthLimit      = 0.10
)

type options struct {
	dock, dist, baseline string
	checks               []string
	idle                 time.Duration
	sseClients           int
	benchTime            string
	benchCount           int
	nsTol, allocsTol     float64
	update               bool

	// tempobase and tempobaseDist are tempoBase's binary and built UI;
	// tempobasePG is a Postgres URL for its connection check (optional).
	tempobase, tempobaseDist, tempobasePG string

	// speed is how much slower this machine is than the one that recorded
	// the baseline (1 if unknown or faster). API latency budgets scale with
	// it, so a slow CI runner gets proportionally more time.
	speed float64
	// softTiming reports timing budgets (start-up, latency, idle CPU and
	// wake-ups, benchmark ns/op) as warnings instead of failures. For shared
	// CI runners, whose noise and speed make timing unreliable; sizes,
	// memory and allocation budgets still fail.
	softTiming bool
}

func main() {
	var o options
	checks := "binary,cli,daemon,ui,bench,tempobase-binary,tempobase-daemon,tempobase-ui"
	flag.StringVar(&o.dock, "dock", "bin/dock", "dock binary to measure (built with the UI embedded)")
	flag.StringVar(&o.dist, "dist", "web/dist", "built UI")
	flag.StringVar(&o.tempobase, "tempobase", "bin/tempobase", "tempobase binary to measure (built with its UI embedded)")
	flag.StringVar(&o.tempobaseDist, "tempobase-dist", "web/tempobase/dist", "tempoBase's built UI")
	flag.StringVar(&o.tempobasePG, "tempobase-pg", "", "Postgres URL (owner with CREATEROLE) for tempoBase's idle connection check; skipped when empty")
	flag.StringVar(&o.baseline, "baseline", "scripts/perf/baseline.json", "baseline for binary sizes and benchmarks")
	flag.StringVar(&checks, "checks", checks, "comma-separated checks to run: binary, cli, daemon (idle and API latency), ui, bench, tempobase-binary, tempobase-daemon, tempobase-ui")
	flag.DurationVar(&o.idle, "idle", 30*time.Second, "length of each idle measurement window")
	flag.IntVar(&o.sseClients, "sse", 20, "SSE clients connected during the second idle window")
	flag.StringVar(&o.benchTime, "benchtime", "200ms", "go test -benchtime")
	flag.IntVar(&o.benchCount, "count", 5, "go test -count; the fastest run's ns/op and the median allocs/op are compared")
	flag.Float64Var(&o.nsTol, "ns-tolerance", 0.30, "allowed ns/op regression (fraction), after normalizing for machine speed")
	flag.Float64Var(&o.allocsTol, "allocs-tolerance", 0.15, "allowed allocs/op regression (fraction, plus 2 allocs)")
	flag.BoolVar(&o.softTiming, "soft-timing", false, "report timing budgets as warnings (shared CI runners)")
	flag.BoolVar(&o.update, "update-baseline", false, "record binary size and benchmarks into -baseline instead of checking")
	flag.Parse()
	o.checks = strings.Split(checks, ",")

	if o.update {
		if err := updateBaseline(o); err != nil {
			fmt.Fprintln(os.Stderr, "perf:", err)
			os.Exit(1)
		}
		return
	}

	base, err := loadBaseline(o.baseline)
	if err != nil {
		fmt.Fprintln(os.Stderr, "perf:", err)
		os.Exit(1)
	}
	o.speed = 1
	timesAPI := slices.Contains(o.checks, "daemon") || slices.Contains(o.checks, "tempobase-daemon")
	if timesAPI && base.CalibrationNs > 0 {
		o.speed = max(1, min(calibrate(), calibrate(), calibrate())/base.CalibrationNs)
	}
	t := &table{waivers: base.Waivers, softTiming: o.softTiming}
	steps := []struct {
		name string
		run  func(options, *table) error
	}{
		{"binary", checkBinary},
		{"cli", checkCLI},
		{"daemon", checkDaemon},
		{"ui", checkUI},
		{"bench", checkBench},
		{"tempobase-binary", checkTempobaseBinary},
		{"tempobase-daemon", checkTempobaseDaemon},
		{"tempobase-ui", checkTempobaseUI},
	}
	for _, s := range steps {
		if !slices.Contains(o.checks, s.name) {
			continue
		}
		fmt.Fprintf(os.Stderr, "perf: %s...\n", s.name)
		if err := s.run(o, t); err != nil {
			t.add(s.name, "error (below)", "", fail)
			t.errs = append(t.errs, fmt.Errorf("%s: %w", s.name, err))
		}
	}
	t.print(os.Stdout)
	if t.failed() {
		fmt.Println("\nOver budget. See docs/performance.md; if a change is intended, run `make perf-baseline` and commit scripts/perf/baseline.json.")
		os.Exit(1)
	}
}

type result string

const (
	ok   result = "ok"
	fail result = "FAIL"
	info result = "info"
	warn result = "warn"
	// waived is over budget, but listed in baseline.json's waivers.
	waived result = "waived"
	// skipped is a check whose subject isn't in the tree yet.
	skipped result = "skipped"
)

type row struct {
	check, measured, budget string
	result                  result
}

type table struct {
	rows []row
	errs []error
	// waivers are known over-budget checks (by name) with the reason.
	waivers    map[string]string
	softTiming bool
}

func (t *table) add(check, measured, budget string, r result) {
	t.rows = append(t.rows, row{check, measured, budget, r})
}

// withinTiming is within for timing budgets: with -soft-timing a miss is a
// warning, not a failure.
func (t *table) withinTiming(check, measured, budget string, pass bool) {
	if !pass && t.softTiming {
		if _, isWaived := t.waivers[check]; !isWaived {
			t.add(check, measured, budget, warn)
			return
		}
	}
	t.within(check, measured, budget, pass)
}

// within adds a row that passes when measured ≤ budget, or is waived.
func (t *table) within(check, measured, budget string, pass bool) {
	_, isWaived := t.waivers[check]
	switch {
	case pass && isWaived:
		t.add(check, measured, budget, "ok, drop its waiver")
	case pass:
		t.add(check, measured, budget, ok)
	case isWaived:
		t.add(check, measured, budget, waived)
	default:
		t.add(check, measured, budget, fail)
	}
}

func (t *table) failed() bool {
	return slices.ContainsFunc(t.rows, func(r row) bool { return r.result == fail })
}

func (t *table) print(w *os.File) {
	widths := [3]int{len("Check"), len("Measured"), len("Budget")}
	for _, r := range t.rows {
		widths[0] = max(widths[0], len(r.check))
		widths[1] = max(widths[1], len(r.measured))
		widths[2] = max(widths[2], len(r.budget))
	}
	line := func(a, b, c, d string) {
		_, _ = fmt.Fprintf(w, "%-*s  %-*s  %-*s  %s\n", widths[0], a, widths[1], b, widths[2], c, d)
	}
	_, _ = fmt.Fprintln(w)
	line("Check", "Measured", "Budget", "Result")
	line(strings.Repeat("-", widths[0]), strings.Repeat("-", widths[1]), strings.Repeat("-", widths[2]), "------")
	for _, r := range t.rows {
		line(r.check, r.measured, r.budget, string(r.result))
	}
	for _, r := range t.rows {
		if r.result == waived {
			_, _ = fmt.Fprintf(w, "\nwaived: %s: %s\n", r.check, t.waivers[r.check])
		}
	}
	for _, err := range t.errs {
		_, _ = fmt.Fprintf(w, "\nerror: %v\n", err)
	}
}

func kb(n int64) string { return fmt.Sprintf("%.1f KB", float64(n)/1024) }
func mb(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/(1<<20)) }
func ms(d time.Duration) string {
	return fmt.Sprintf("%.2f ms", float64(d)/float64(time.Millisecond))
}

func checkBinary(o options, t *table) error {
	st, err := os.Stat(o.dock)
	if err != nil {
		return fmt.Errorf("%w (run `make build` first)", err)
	}
	b, err := loadBaseline(o.baseline)
	if err != nil {
		return err
	}
	if b.BinaryBytes == 0 {
		t.add("binary size", mb(st.Size()), "no baseline", warn)
		return nil
	}
	growth := float64(st.Size())/float64(b.BinaryBytes) - 1
	limit := int64(float64(b.BinaryBytes) * (1 + binaryGrowthLimit))
	t.within("binary size", fmt.Sprintf("%s (%+.1f%% vs baseline)", mb(st.Size()), growth*100),
		fmt.Sprintf("≤ %s (baseline +%.0f%%)", mb(limit), binaryGrowthLimit*100), st.Size() <= limit)
	return nil
}
