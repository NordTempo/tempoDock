// Command perf checks Tempo Dock against its performance budgets and prints
// a table: binary size, CLI start-up, daemon idle memory and CPU (also with
// SSE clients connected), API read latency on a seeded daemon, UI bundle
// sizes, and Go benchmarks compared with a checked-in baseline; and the same
// for tempoBase, the standalone Data panel server (tempobase.go), and Dock
// Analytics' store (analytics.go). It exits
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
	"sync"
	"time"
)

// measuredProcs is the GOMAXPROCS the measured daemons run with: the
// small servers Dock is for, and the same on any machine. The Go runtime's
// idle memory grows with it (24.6 MB at 4, 29.1 MB at 16 for Dock with 20
// SSE clients), so a 16-core CI runner would otherwise fail a memory
// budget that a 4-vCPU one passes.
const measuredProcs = "GOMAXPROCS=4"

// Budgets. Absolute ones are product targets (docs/experience.md) with
// headroom; relative ones compare against baseline.json.
const (
	cliBudget = 20 * time.Millisecond
	// Anonymous RSS of the idle daemon, plus each open SSE stream's share:
	// mostly its handler goroutine's stack, about 0.11 MB, which the
	// runtime keeps after the handshake. Measured on a 2-CPU, 8 GB slot
	// like a CI runner's, where GC timing moves the idle number by up to
	// 3 MB between runs (docs/performance.md).
	idleAnonBudget      = 32 << 20
	sseClientAnonBudget = 150 << 10
	idleTicksBudget     = 2 // clock ticks (usually 10 ms each) per idle window
	// Wake-ups (voluntary context switches) per second: what a forgotten
	// ticker shows up as long before it costs measurable CPU. Each SSE
	// client adds a heartbeat every 25 s.
	idleWakeupsBudget      = 0.2
	sseClientWakeupsBudget = 0.2
	apiP95Budget           = 20 * time.Millisecond
	initialJSBudget        = 200 << 10 // gzipped
	cssBudget              = 30 << 10  // gzipped
	fontsBudget            = 150 << 10 // woff2, already compressed
	// Fonts a first load can fetch: style A's Inter and JetBrains Mono,
	// Latin subsets (docs/design/README.md).
	initialFontsBudget = 100 << 10
	binaryGrowthLimit  = 0.10
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

	// markdown is a file the results are also written to as Markdown, for
	// CI's job summary and the perf comment on a pull request.
	markdown string

	// analytics is the analytics store's binary (ADR 0037).
	analytics string

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

	// gate is shared by checks running side by side; nil when a check
	// runs alone.
	gate *gate
}

func main() {
	var o options
	checks := "binary,cli,daemon,ui,bench,analytics,tempobase-binary,tempobase-daemon,tempobase-ui"
	flag.StringVar(&o.dock, "dock", "bin/dock", "dock binary to measure (built with the UI embedded)")
	flag.StringVar(&o.dist, "dist", "web/dist", "built UI")
	flag.StringVar(&o.analytics, "analytics", "bin/dock-analytics", "dock-analytics binary to measure (make build-analytics)")
	flag.StringVar(&o.tempobase, "tempobase", "bin/tempobase", "tempobase binary to measure (built with its UI embedded)")
	flag.StringVar(&o.tempobaseDist, "tempobase-dist", "web/tempobase/dist", "tempoBase's built UI")
	flag.StringVar(&o.tempobasePG, "tempobase-pg", "", "Postgres URL (owner with CREATEROLE) for tempoBase's idle connection check; skipped when empty")
	flag.StringVar(&o.baseline, "baseline", "scripts/perf/baseline.json", "baseline for binary sizes and benchmarks")
	flag.StringVar(&checks, "checks", checks, "comma-separated checks to run: binary, cli, daemon (idle and API latency), ui, bench, analytics, tempobase-binary, tempobase-daemon, tempobase-ui")
	// CI runs bench on its own (make perf-bench) and everything else with
	// -skip bench (make perf-artifacts), so a new check joins the second
	// job without listing it anywhere.
	skip := flag.String("skip", "", "comma-separated checks to leave out of -checks")
	flag.DurationVar(&o.idle, "idle", 30*time.Second, "length of each idle measurement window")
	flag.IntVar(&o.sseClients, "sse", 20, "SSE clients connected during the second idle window")
	flag.StringVar(&o.benchTime, "benchtime", "200ms", "go test -benchtime")
	flag.IntVar(&o.benchCount, "count", 5, "go test -count; the fastest run's ns/op and the median allocs/op are compared")
	flag.Float64Var(&o.nsTol, "ns-tolerance", 0.30, "allowed ns/op regression (fraction), after normalizing for machine speed")
	flag.Float64Var(&o.allocsTol, "allocs-tolerance", 0.15, "allowed allocs/op regression (fraction, plus 2 allocs)")
	flag.BoolVar(&o.softTiming, "soft-timing", false, "report timing budgets as warnings (shared CI runners)")
	flag.StringVar(&o.markdown, "markdown", "", "also write the results as Markdown to this file (CI's job summary and PR comment)")
	flag.BoolVar(&o.update, "update-baseline", false, "record binary size and benchmarks into -baseline instead of checking")
	flag.Parse()
	o.checks = slices.DeleteFunc(strings.Split(checks, ","), func(c string) bool {
		return slices.Contains(strings.Split(*skip, ","), c)
	})

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
	timesAPI := slices.Contains(o.checks, "daemon") || slices.Contains(o.checks, "tempobase-daemon") || slices.Contains(o.checks, "analytics")
	if timesAPI && base.CalibrationNs > 0 {
		o.speed = max(1, min(calibrate(), calibrate(), calibrate())/base.CalibrationNs)
	}
	t := &table{waivers: base.Waivers, softTiming: o.softTiming}
	runChecks(o, t)
	t.print(os.Stdout)
	if o.markdown != "" {
		if err := writeMarkdown(o.markdown, t); err != nil {
			fmt.Fprintln(os.Stderr, "perf:", err)
			os.Exit(1)
		}
	}
	if t.failed() {
		fmt.Println("\nOver budget. See docs/performance.md; if a change is intended, run `make perf-baseline` and commit scripts/perf/baseline.json.")
		os.Exit(1)
	}
}

// checkFuncs are the checks by name, and stages the order they run in. The
// groups of a stage run side by side under a gate: Dock's daemon, the
// analytics store and tempoBase are separate processes, and most of their
// checks' time is idle windows and waits that can overlap. The CLI's
// start-up and the benchmarks are timed alone, before and after them.
var (
	checkFuncs = map[string]func(options, *table) error{
		"binary": checkBinary, "cli": checkCLI, "daemon": checkDaemon, "ui": checkUI, "bench": checkBench,
		"analytics": checkAnalytics, "tempobase-binary": checkTempobaseBinary,
		"tempobase-daemon": checkTempobaseDaemon, "tempobase-ui": checkTempobaseUI,
	}
	stages = [][][]string{
		{{"binary", "cli"}},
		{{"daemon", "ui"}, {"analytics"}, {"tempobase-binary", "tempobase-daemon", "tempobase-ui"}},
		{{"bench"}},
	}
)

// runChecks runs the checks in o.checks stage by stage and adds their rows
// to t in stage and group order, whatever order they finish in.
func runChecks(o options, t *table) {
	for _, stage := range stages {
		var groups [][]string
		for _, g := range stage {
			if g = slices.DeleteFunc(slices.Clone(g), func(c string) bool { return !slices.Contains(o.checks, c) }); len(g) > 0 {
				groups = append(groups, g)
			}
		}
		so := o
		if len(groups) > 1 {
			so.gate = &gate{}
		}
		tables := make([]*table, len(groups))
		var wg sync.WaitGroup
		for i, g := range groups {
			tables[i] = &table{waivers: t.waivers, softTiming: t.softTiming}
			wg.Go(func() {
				so.gate.enter()
				defer so.gate.leave()
				for _, name := range g {
					fmt.Fprintf(os.Stderr, "perf: %s...\n", name)
					if err := checkFuncs[name](so, tables[i]); err != nil {
						tables[i].add(name, "error (below)", "", fail)
						tables[i].errs = append(tables[i].errs, fmt.Errorf("%s: %w", name, err))
					}
				}
			})
		}
		wg.Wait()
		for _, gt := range tables {
			t.rows, t.errs = append(t.rows, gt.rows...), append(t.errs, gt.errs...)
		}
	}
}

// gate keeps checks running side by side from moving each other's
// numbers. A group holds it by default, so start-up, seeding, load and
// anything timed run with nothing else busy. Idle windows and waits let go
// of it (apart): an idle window counts its own process's CPU ticks,
// voluntary context switches and memory, which another process's load
// doesn't change. A nil *gate, for a check running alone, only runs.
type gate struct{ mu sync.Mutex }

func (g *gate) enter() {
	if g != nil {
		g.mu.Lock()
	}
}

func (g *gate) leave() {
	if g != nil {
		g.mu.Unlock()
	}
}

// apart runs f, an idle window or a wait, without holding the gate.
func (g *gate) apart(f func() error) error {
	if g != nil {
		g.mu.Unlock()
		defer g.mu.Lock()
	}
	return f()
}

// pause sleeps for d without holding the gate.
func (g *gate) pause(d time.Duration) {
	_ = g.apart(func() error { time.Sleep(d); return nil })
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
