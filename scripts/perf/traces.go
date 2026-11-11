package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"time"
)

// Budgets of the trace store (`dock-analytics traces`), its own process
// and container beside the analytics store, run as its container runs it:
// memory_limit 128 MB, one query at a time, GOMEMLIMIT 48 MiB.
const (
	// tracesAnonBudget leaves room for CI's runners, where an idle store
	// measures 27 MB (8 MB on an 8-core VM), as the analytics store's 46
	// MB there against 13 to 32 locally.
	tracesAnonBudget    = 40 << 20
	tracesTicksBudget   = 1
	tracesWakeupsBudget = 0.1
	// Views on 2M spans over a week (a service at about 3 spans a second,
	// all kept): a service's list at 1h, 24h and 7d, newest and slowest
	// and filtered; the org's errors over 18 services; a trace opened with
	// its time and without. At 10M spans the lists take up to 70 ms (a
	// service's) and 93 ms (the org's).
	tracesListBudget    = 60 * time.Millisecond
	tracesOrgListBudget = 120 * time.Millisecond
	tracesTraceBudget   = 30 * time.Millisecond
	// tracesIngestBudget is spans per second, in batches of 1,000.
	tracesIngestBudget = 15_000
	// tracesPeakBudget is the container's memory limit: writes at full
	// speed grow DuckDB past its memory_limit whatever the limit, and
	// CI's runs of this check peaked at 304 to 387 MB.
	tracesPeakBudget = 512 << 20
)

// traceCase is one line of `dock-analytics bench -traces -json`.
type traceCase struct {
	Name, Range string
	P95         time.Duration
}

// checkTraces seeds 2M spans into a temp trace store, then measures the
// idle server, the trace views over its HTTP API, ingest (two loads of
// 100k spans, a backlog) and the peak memory of all that.
func checkTraces(o options, t *table) error {
	const name = "traces idle"
	if runtime.GOOS != "linux" {
		t.add(name, "needs /proc (Linux)", "", warn)
		return nil
	}
	if _, err := os.Stat(o.analytics); err != nil {
		return fmt.Errorf("%w (run `make build-analytics` first)", err)
	}
	dir, err := os.MkdirTemp("", "dock-perf-traces-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	db := filepath.Join(dir, "traces.duckdb")
	fmt.Fprintln(os.Stderr, "perf: seeding the trace store with 2M spans...")
	if out, err := exec.Command(o.analytics, "seed-traces", "-db", db, "-spans", "2000000").CombinedOutput(); err != nil {
		return fmt.Errorf("seed-traces: %w: %s", err, out)
	}

	ports, err := freePorts(1)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", ports[0])
	key := make([]byte, 24)
	_, _ = rand.Read(key)
	env := append(cleanEnv(), "DOCK_ANALYTICS_TOKEN="+hex.EncodeToString(key), "GOMEMLIMIT=48MiB")
	logf, err := os.Create(filepath.Join(dir, "traces.log"))
	if err != nil {
		return err
	}
	cmd := exec.Command(o.analytics, "traces", "-db", db, "-listen", fmt.Sprintf("127.0.0.1:%d", ports[0]))
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		return err
	}
	d := &daemon{cmd: cmd, dir: dir}
	defer d.stop()
	if err := waitStore(url); err != nil {
		log, _ := os.ReadFile(logf.Name())
		return fmt.Errorf("%w: %s", err, log)
	}
	time.Sleep(3 * time.Second) // start-up work and the first GC
	if err := o.gate.apart(func() error {
		return d.idle(t, name, o.idle, idleBudgets{tracesAnonBudget, tracesTicksBudget, tracesWakeupsBudget})
	}); err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "perf: timing trace views...")
	var cases []traceCase
	if err := runJSON(&cases, env, o.analytics, "bench", "-url", url, "-traces", "-runs", "10", "-json"); err != nil {
		return err
	}
	for _, c := range []struct {
		check  string
		budget time.Duration
		names  []string
	}{
		{"traces: a service's list p95, 2M spans, ≤ 7 days", tracesListBudget, []string{"service list", "service list, filtered"}},
		{"traces: the org's errors p95, 2M spans, ≤ 7 days", tracesOrgListBudget, []string{"org list, filtered"}},
		{"traces: opening a trace p95, 2M spans", tracesTraceBudget, []string{"trace, with its time", "trace, without a time"}},
	} {
		var w traceCase
		for _, r := range cases {
			if slices.Contains(c.names, r.Name) && r.P95 >= w.P95 {
				w = r
			}
		}
		budget, text := scaledBudget(c.budget, o.speed)
		t.withinTiming(c.check, fmt.Sprintf("%s (%s, %s)", ms(w.P95), w.Name, w.Range), text, w.P95 <= budget)
	}

	var slowest float64
	var p95 time.Duration
	for range 2 {
		var load struct {
			Events   int
			Seconds  float64
			WriteP95 time.Duration
		}
		if err := runJSON(&load, env, o.analytics, "load", "-url", url, "-spans", "-events", "100000", "-json"); err != nil {
			return err
		}
		if rate := float64(load.Events) / load.Seconds; slowest == 0 || rate < slowest {
			slowest, p95 = rate, load.WriteP95
		}
	}
	t.withinTiming("traces: span ingest, batches of 1,000", fmt.Sprintf("%.0f spans/s (write p95 %s)", slowest, ms(p95)),
		fmt.Sprintf("≥ %d spans/s", tracesIngestBudget), slowest >= tracesIngestBudget)

	peak, err := statusField(fmt.Sprintf("/proc/%d/status", cmd.Process.Pid), "VmHWM")
	if err != nil {
		return err
	}
	t.within("traces peak memory", mb(peak<<10), "≤ "+mb(tracesPeakBudget)+" (the container's limit)", peak<<10 <= tracesPeakBudget)
	return nil
}
