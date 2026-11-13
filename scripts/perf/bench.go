package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
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
		runs, calib, err := runBenchmarks(o, nil, ".")
		if err != nil {
			return err
		}
		b.Benchmarks, b.CalibrationNs = map[string]bench{}, calib
		for name, rs := range runs.byName {
			b.Benchmarks[name] = summarize(rs)
		}
	}
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

// nearGate is the share of a benchmark's allocations gate past which it
// warns (checkBench).
const nearGate = 0.98

func checkBench(o options, t *table) error {
	base, err := loadBaseline(o.baseline)
	if err != nil {
		return err
	}
	runs, calib, err := runBenchmarks(o, nil, ".")
	if err != nil {
		return err
	}
	speed := 1.0 // how much slower this machine is than the baseline's
	if base.CalibrationNs > 0 {
		speed = calib / base.CalibrationNs
	}
	// judge compares a benchmark with its baseline. Allocations are the
	// same on every machine, so allocsOK always gates; nsOK is a timing
	// budget, which -soft-timing reports as a warning.
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
		return measured, fmt.Sprintf("ns +%.0f%%, allocs +%.0f%%", o.nsTol*100, o.allocsTol*100), allocsOK, nsDelta <= o.nsTol
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
		fmt.Fprintf(os.Stderr, "perf: re-running benchmarks over budget: %s in %s\n", pattern, strings.Join(again, " "))
		more, _, err := runBenchmarks(o, again, pattern)
		if err != nil {
			return err
		}
		for name, rs := range more.byName {
			runs.byName[name] = append(runs.byName[name], rs...)
		}
	}

	t.add("bench machine speed vs baseline", fmt.Sprintf("%.2fx time per op", speed), "", info)
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
		// Allocations are deterministic, so a count just under its gate
		// fails the next change that adds a little, often a parallel PR
		// that passed on its own (#307 and #306 on BenchmarkNewAPI). Warn
		// while the change that got it there can still re-record it.
		if limit := base.Benchmarks[name].AllocsOp*(1+o.allocsTol) + 2; r.AllocsOp > nearGate*limit && r.AllocsOp >= 100 {
			t.add("bench "+name, measured, fmt.Sprintf("allocs within %.0f%% of their gate: re-record if intended", (1-nearGate)*100), warn)
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
// times, and returns their runs and the fastest calibration ns/op
// measured around them.
func runBenchmarks(o options, pkgs []string, pattern string) (benchRuns, float64, error) {
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
	cmd.Stdout, cmd.Stderr = &out, os.Stderr
	if err := cmd.Run(); err != nil {
		return benchRuns{}, 0, fmt.Errorf("go test -bench: %w\n%s", err, out.String())
	}
	calib = append(calib, calibrate(), calibrate(), calibrate())
	runs, err := parseBench(out.String())
	return runs, slices.Min(calib), err
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
