package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// baseline is scripts/perf/baseline.json.
type baseline struct {
	About string `json:"about"`
	Go    string `json:"go"`
	// BinaryBytes is the size of bin/dock with the UI embedded.
	BinaryBytes int64 `json:"binary_bytes"`
	// TempobaseBinaryBytes is the size of bin/tempobase with its UI
	// embedded; 0 until it is first recorded.
	TempobaseBinaryBytes int64 `json:"tempobase_binary_bytes,omitempty"`
	// CalibrationNs is a fixed CPU workload's ns/op on the machine that
	// recorded the baseline. Benchmarks are compared relative to it, so a
	// slower or faster machine doesn't read as a regression.
	CalibrationNs float64          `json:"calibration_ns"`
	Benchmarks    map[string]bench `json:"benchmarks"`
	// Waivers are checks known to be over budget, by the check name the
	// table prints, with the reason and what fixes it. They are reported
	// but don't fail. Edited by hand; keep the list short and remove an
	// entry with the fix.
	Waivers map[string]string `json:"waivers,omitempty"`
}

type bench struct {
	NsOp     float64 `json:"ns_op"`
	BytesOp  float64 `json:"bytes_op"`
	AllocsOp float64 `json:"allocs_op"`
}

func loadBaseline(path string) (baseline, error) {
	var b baseline
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	return b, json.Unmarshal(data, &b)
}

func updateBaseline(o options) error {
	old, err := loadBaseline(o.baseline)
	if err != nil {
		return err
	}
	b := baseline{
		About:   "Recorded by `make perf-baseline` (scripts/perf). Commit it with the change that moved the numbers; see docs/performance.md.",
		Go:      runtime.Version(),
		Waivers: old.Waivers,
		// Kept for the parts -checks leaves out.
		BinaryBytes: old.BinaryBytes, TempobaseBinaryBytes: old.TempobaseBinaryBytes,
		CalibrationNs: old.CalibrationNs, Benchmarks: old.Benchmarks,
	}
	if o.rerecord != "" {
		// A few rows: the rest, the toolchain's name included, stay.
		b.Go = old.Go
		if err := rerecordBenchmarks(o, &b); err != nil {
			return err
		}
		return writeBaseline(o, old, b)
	}
	if slices.Contains(o.checks, "binary") {
		st, err := os.Stat(o.dock)
		if err != nil {
			return fmt.Errorf("%w (run `make build` first)", err)
		}
		b.BinaryBytes = st.Size()
	}
	if slices.Contains(o.checks, "tempobase-binary") && tempobaseSourceExists() {
		st, err := os.Stat(o.tempobase)
		if err != nil {
			return fmt.Errorf("%w (run `make build-tempobase` first)", err)
		}
		b.TempobaseBinaryBytes = st.Size()
	}
	if slices.Contains(o.checks, "bench") {
		runs, calib, err := runBenchmarks(o, "", nil, ".")
		if err != nil {
			return err
		}
		b.Benchmarks, b.CalibrationNs = map[string]bench{}, calib
		for name, rs := range runs.byName {
			b.Benchmarks[name] = summarize(rs)
		}
	}
	return writeBaseline(o, old, b)
}

// writeBaseline writes b to o.baseline and prints how its budgets moved
// from old.
func writeBaseline(o options, old, b baseline) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(o.baseline, append(data, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s: binary %s, tempobase %s, %d benchmarks\n", o.baseline, mb(b.BinaryBytes), mb(b.TempobaseBinaryBytes), len(b.Benchmarks))
	for _, r := range budgetChanges(o, old, b) {
		fmt.Printf("%s: %s %s (%s)\n", r.result, r.check, r.measured, r.budget)
	}
	return nil
}

// allocsNoise is the rise in a benchmark's allocations (a fraction, plus
// 2 allocations) that counts as noise: map growth and the like move a
// median of 5 runs by under 2% (and most by under 0.5%). A rise past it,
// still within the gate, warns (checkBench).
const allocsNoise = 0.01

func checkBench(o options, t *table) error {
	base, err := loadBaseline(o.baseline)
	if err != nil {
		return err
	}
	runs, calib, err := runBenchmarks(o, "", nil, ".")
	if err != nil {
		return err
	}
	speed := 1.0 // how much slower this machine is than the baseline's
	if base.CalibrationNs > 0 {
		speed = calib / base.CalibrationNs
	}
	// atRev are the benchmarks over their timing budget, timed again at
	// o.baseRev on this machine (benchAtRev), and revName that revision.
	var atRev map[string]bench
	revName := ""
	// judge compares a benchmark with its baseline. Allocations are the
	// same on every machine, so allocsOK always gates; nsOK is a timing
	// budget, which -soft-timing reports as a warning. With -soft-timing a
	// time over its budget passes when the base revision, timed on this
	// machine, is as slow: the change didn't make it slower.
	judge := func(name string, r bench) (measured, budget string, allocsOK, nsOK bool) {
		b := base.Benchmarks[name]
		nsDelta := r.NsOp/speed/b.NsOp - 1
		allocsOK = r.AllocsOp <= b.AllocsOp*(1+o.allocsTol)+2
		measured = fmt.Sprintf("%s (%+.0f%%), %.0f allocs (%+.0f)", nsString(r.NsOp), nsDelta*100, r.AllocsOp, r.AllocsOp-b.AllocsOp)
		// Parallel benchmarks' time depends on the core count, and file
		// benchmarks' (io=) on the disk, kernel and other jobs' I/O, none
		// of which calibration's CPU workload captures: only their
		// allocations are gated. Their time still prints.
		if strings.Contains(name, "parallel=") || strings.Contains(name, "io=") {
			return measured, fmt.Sprintf("allocs +%.0f%%", o.allocsTol*100), allocsOK, true
		}
		budget = fmt.Sprintf("ns +%.0f%%, allocs +%.0f%%", o.nsTol*100, o.allocsTol*100)
		nsOK = nsDelta <= o.nsTol
		if p, ok := atRev[name]; ok && !nsOK {
			revDelta := r.NsOp/p.NsOp - 1
			measured += fmt.Sprintf("; %s at %s here (%+.0f%%)", nsString(p.NsOp), revName, revDelta*100)
			budget = fmt.Sprintf("ns +%.0f%% of %s's, allocs +%.0f%%", o.nsTol*100, revName, o.allocsTol*100)
			nsOK = revDelta <= o.nsTol
		}
		return measured, budget, allocsOK, nsOK
	}

	// Shared machines (CI runners, a busy laptop) have bursts of noise, and
	// noise only adds time: benchmarks with a regression run once more and
	// the fastest of all runs counts. Only those benchmarks (by top-level
	// function, in their packages) run again, not their whole packages.
	var again, funcs []string
	for name, rs := range runs.byName {
		if _, ok := base.Benchmarks[name]; !ok {
			continue
		}
		if _, _, allocsOK, nsOK := judge(name, summarize(rs)); !allocsOK || !nsOK {
			if !slices.Contains(again, runs.dir[name]) {
				again = append(again, runs.dir[name])
			}
			if f := topLevel(name); !slices.Contains(funcs, f) {
				funcs = append(funcs, f)
			}
		}
	}
	if len(again) > 0 {
		pattern := "^(" + strings.Join(funcs, "|") + ")$"
		// One calibration number can't stand for every kind of code: a
		// GitHub-hosted runner times the calibration about as fast as the
		// baseline's machine but tight, allocation-free loops 1.5-2.8x
		// slower (BenchmarkDrainEnqueue, BenchmarkScanBusy), so they read
		// as regressions on every pull request. On shared runners
		// (-soft-timing) the base revision, timed here, says whether this
		// change made them slower. A local run keeps to the baseline, so
		// a slowdown spread over several changes still shows.
		if o.softTiming && o.baseRev != "" {
			fmt.Fprintf(os.Stderr, "perf: timing benchmarks over budget at %s too: %s in %s\n", o.baseRev, pattern, strings.Join(again, " "))
			br, name, err := benchAtRev(o, o.baseRev, again, pattern)
			if err != nil {
				fmt.Fprintf(os.Stderr, "perf: timings not compared with %s: %v\n", o.baseRev, err)
			}
			atRev, revName = map[string]bench{}, name
			for n, rs := range br.byName {
				atRev[n] = summarize(rs)
			}
		}
		fmt.Fprintf(os.Stderr, "perf: re-running benchmarks over budget: %s in %s\n", pattern, strings.Join(again, " "))
		more, _, err := runBenchmarks(o, "", again, pattern)
		if err != nil {
			return err
		}
		for name, rs := range more.byName {
			runs.byName[name] = append(runs.byName[name], rs...)
		}
	}

	machine := fmt.Sprintf("%.2fx time per op", speed)
	if runs.cpu != "" {
		machine += " (" + runs.cpu + ")"
	}
	t.add("bench machine speed vs baseline", machine, "", info)
	for _, name := range slices.Sorted(maps.Keys(runs.byName)) {
		r := summarize(runs.byName[name])
		if _, ok := base.Benchmarks[name]; !ok {
			t.add("bench "+name, fmt.Sprintf("%s, %.0f allocs", nsString(r.NsOp), r.AllocsOp), "not in baseline", warn)
			continue
		}
		measured, budget, allocsOK, nsOK := judge(name, r)
		if !allocsOK {
			t.within("bench "+name, measured, budget, false)
			continue
		}
		// Allocations are all but deterministic, so any rise past noise is
		// a change's own: it warns, so the pull request that made it fixes
		// it or re-records it with the reason, instead of the next one
		// failing on the sum (#366 added 855 allocations to
		// BenchmarkCoreReads/Changes under a 15% gate, unnoticed).
		if b := base.Benchmarks[name].AllocsOp; r.AllocsOp > b*(1+allocsNoise)+2 {
			t.add("bench "+name, measured, "allocs above the baseline: fix it, or re-record it with the reason", warn)
			continue
		}
		t.withinTiming("bench "+name, measured, budget, nsOK)
	}
	for name := range base.Benchmarks {
		if _, ok := runs.byName[name]; !ok {
			t.add("bench "+name, "missing", "in baseline", warn)
		}
	}
	return nil
}

// nsString formats ns/op to three significant digits.
func nsString(ns float64) string {
	d, unit := time.Duration(ns), time.Duration(1)
	for d/unit >= 1000 {
		unit *= 10
	}
	return d.Round(unit).String()
}

// benchRuns are the results of each run of each benchmark, and the
// package directory each benchmark is in.
type benchRuns struct {
	byName map[string][]bench
	dir    map[string]string
	// cpu is the processor go test names, so a comment's timings can be
	// read against the machine they ran on.
	cpu string
}

// rerecordBenchmarks re-records the benchmarks o.rerecord matches into b,
// keeping every other row and the calibration. Allocations and bytes are
// taken as measured. ns/op is scaled by this machine's speed against the
// baseline's calibration, as checkBench compares it, and only replaces a
// row's time that moved past the ns tolerance: the scaling is rougher
// than the tolerance between machines (CI and a dev VM differ by ~15%),
// so rewriting every time would tighten budgets by noise. Rows edited by
// hand skip the scaling and drift (#386's BenchmarkCheckEnvShadowed sat
// 47% under its real time).
func rerecordBenchmarks(o options, b *baseline) error {
	if b.CalibrationNs <= 0 {
		return fmt.Errorf("the baseline has no calibration; record it whole with `make perf-baseline`")
	}
	runs, calib, err := runBenchmarks(o, "", nil, o.rerecord)
	if err != nil {
		return err
	}
	if len(runs.byName) == 0 {
		return fmt.Errorf("no benchmark matches %q", o.rerecord)
	}
	speed := calib / b.CalibrationNs
	b.Benchmarks = maps.Clone(b.Benchmarks)
	for name, rs := range runs.byName {
		s := summarize(rs)
		s.NsOp = math.Round(s.NsOp/speed*10) / 10
		if prev, ok := b.Benchmarks[name]; ok && math.Abs(s.NsOp/prev.NsOp-1) <= o.nsTol {
			s.NsOp = prev.NsOp
		}
		b.Benchmarks[name] = s
	}
	return nil
}

// summarize reduces a benchmark's runs to the fastest ns/op (noise only
// ever adds time, so the fastest run is the most repeatable) and the
// median bytes and allocations.
func summarize(rs []bench) bench {
	return bench{
		NsOp:     slices.Min(field(rs, func(b bench) float64 { return b.NsOp })),
		BytesOp:  median(field(rs, func(b bench) float64 { return b.BytesOp })),
		AllocsOp: median(field(rs, func(b bench) float64 { return b.AllocsOp })),
	}
}

// topLevel is the benchmark function of a benchmark's name
// (<package>.<Benchmark/sub>).
func topLevel(name string) string {
	_, f, _ := strings.Cut(name, ".Benchmark")
	f, _, _ = strings.Cut(f, "/")
	return "Benchmark" + f
}

// runBenchmarks runs the benchmarks matching pattern (go test -bench) in
// pkgs (default: every package with a *_bench_test.go file) o.benchCount
// times, in the module at dir ("" for the current directory), and returns
// their runs and the fastest calibration ns/op measured around them.
func runBenchmarks(o options, dir string, pkgs []string, pattern string) (benchRuns, float64, error) {
	if pkgs == nil {
		err := filepath.WalkDir(".", func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() && (e.Name() == "node_modules" || e.Name() == ".git" || e.Name() == "web") {
				return filepath.SkipDir
			}
			if pkg := "./" + filepath.ToSlash(filepath.Dir(p)); strings.HasSuffix(p, "_bench_test.go") && !slices.Contains(pkgs, pkg) {
				pkgs = append(pkgs, pkg)
			}
			return nil
		})
		if err != nil {
			return benchRuns{}, 0, err
		}
	}
	calib := []float64{calibrate(), calibrate()}
	args := append([]string{"test", "-run", "^$", "-bench", pattern, "-benchmem",
		"-benchtime", o.benchTime, "-count", strconv.Itoa(o.benchCount)}, pkgs...)
	fmt.Fprintf(os.Stderr, "perf: go %s\n", strings.Join(args, " "))
	var out bytes.Buffer
	cmd := exec.Command("go", args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, &out, os.Stderr
	if err := cmd.Run(); err != nil {
		return benchRuns{}, 0, fmt.Errorf("go test -bench: %w\n%s", err, out.String())
	}
	calib = append(calib, calibrate(), calibrate(), calibrate())
	runs, err := parseBench(out.String())
	return runs, slices.Min(calib), err
}

// benchAtRev runs the benchmarks pattern matches in pkgs, as
// runBenchmarks does, at git revision rev, checked out in a temporary
// worktree; packages rev doesn't have are left out. It returns their
// runs and rev's short name. checkBench compares a time over budget with
// it: both timed on one machine, minutes apart, which no calibration
// matches across processors.
func benchAtRev(o options, rev string, pkgs []string, pattern string) (benchRuns, string, error) {
	out, err := exec.Command("git", "rev-parse", "--short", rev).Output()
	if err != nil {
		return benchRuns{}, rev, fmt.Errorf("git rev-parse %s: %w", rev, err)
	}
	name := strings.TrimSpace(string(out))
	tmp, err := os.MkdirTemp("", "perf-base-")
	if err != nil {
		return benchRuns{}, name, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	dir := filepath.Join(tmp, "src")
	if out, err := exec.Command("git", "worktree", "add", "--detach", dir, rev).CombinedOutput(); err != nil {
		return benchRuns{}, name, fmt.Errorf("git worktree add: %w\n%s", err, out)
	}
	defer func() { _ = exec.Command("git", "worktree", "remove", "--force", dir).Run() }()
	pkgs = slices.DeleteFunc(slices.Clone(pkgs), func(p string) bool {
		_, err := os.Stat(filepath.Join(dir, p))
		return err != nil
	})
	if len(pkgs) == 0 {
		return benchRuns{}, name, nil
	}
	runs, _, err := runBenchmarks(o, dir, pkgs, pattern)
	return runs, name, err
}

var procsSuffix = regexp.MustCompile(`-\d+$`)

// parseBench reads `go test -bench -benchmem` output. Benchmarks are named
// <package path below internal/>.<Benchmark/sub>.
func parseBench(out string) (benchRuns, error) {
	runs := benchRuns{byName: map[string][]bench{}, dir: map[string]string{}}
	pkg, dir := "", ""
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if c, ok := strings.CutPrefix(line, "cpu: "); ok {
			runs.cpu = strings.TrimSpace(c)
			continue
		}
		if p, ok := strings.CutPrefix(line, "pkg: "); ok {
			_, rel, _ := strings.Cut(p, "tempoDock/")
			pkg, dir = strings.TrimPrefix(rel, "internal/"), "./"+rel
			continue
		}
		f := strings.Fields(line)
		if len(f) < 4 || !strings.HasPrefix(f[0], "Benchmark") {
			continue
		}
		var b bench
		for i := 2; i+1 < len(f); i += 2 {
			v, err := strconv.ParseFloat(f[i], 64)
			if err != nil {
				return runs, fmt.Errorf("parse %q: %w", line, err)
			}
			switch f[i+1] {
			case "ns/op":
				b.NsOp = v
			case "B/op":
				b.BytesOp = v
			case "allocs/op":
				b.AllocsOp = v
			}
		}
		name := pkg + "." + procsSuffix.ReplaceAllString(f[0], "")
		runs.byName[name] = append(runs.byName[name], b)
		runs.dir[name] = dir
	}
	return runs, sc.Err()
}

func field(rs []bench, f func(bench) float64) []float64 {
	out := make([]float64, len(rs))
	for i, r := range rs {
		out[i] = f(r)
	}
	return out
}

func median(vs []float64) float64 {
	vs = slices.Clone(vs)
	slices.Sort(vs)
	return vs[len(vs)/2]
}

// calibrate times a fixed, pure-Go mix of what the daemon's hot paths do
// (JSON encoding, map lookups, sorting, string building, allocation), so
// benchmark results can be compared across machines. It avoids anything
// with hardware acceleration (crypto, compression) that varies by CPU.
func calibrate() float64 {
	type item struct {
		ID      int64             `json:"id"`
		Name    string            `json:"name"`
		Status  string            `json:"status"`
		Labels  map[string]string `json:"labels"`
		Created time.Time         `json:"created"`
	}
	items := make([]item, 200)
	for i := range items {
		items[i] = item{ID: int64(i), Name: fmt.Sprintf("svc-%03d", i), Status: "active",
			Labels: map[string]string{"project": "big", "env": "production"}, Created: time.Unix(int64(i), 0).UTC()}
	}
	r := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			data, err := json.Marshal(items)
			if err != nil {
				b.Fatal(err)
			}
			var back []item
			if err := json.Unmarshal(data, &back); err != nil {
				b.Fatal(err)
			}
			byName := make(map[string]*item, len(back))
			var sb strings.Builder
			for i := range back {
				byName[back[i].Name] = &back[i]
				sb.WriteString(back[i].Name)
			}
			keys := slices.Sorted(maps.Keys(byName))
			slices.Reverse(keys)
			if len(keys) != len(items) || sb.Len() == 0 {
				b.Fatal("calibration mismatch")
			}
		}
	})
	return float64(r.T.Nanoseconds()) / float64(r.N)
}
