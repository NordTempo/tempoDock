package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics/ingest"
)

// Budgets of Dock Analytics' store (ADR 0037), run as the container runs
// it: memory_limit 192 MB, one query at a time, GOMEMLIMIT 48 MiB.
const (
	analyticsAnonBudget    = 60 << 20
	analyticsTicksBudget   = 1
	analyticsWakeupsBudget = 0.1
	// Dashboards on 1M events (a busy site's year): unfiltered at every
	// range, filtered up to 30 days, and filtered over the year, which
	// reads raw events.
	analyticsDashboardBudget  = 100 * time.Millisecond
	analyticsFilterBudget     = 200 * time.Millisecond
	analyticsYearFilterBudget = 400 * time.Millisecond
	// analyticsErrorsViewBudget is the errors and Web Vitals views (ADR
	// 0037, phase A2): error groups and vitals by page and device, each
	// with its series; and the goals view (A4): five goals and a funnel.
	analyticsErrorsViewBudget = 200 * time.Millisecond
	// analyticsIngestBudget is events per second, in batches of 1,000.
	analyticsIngestBudget = 5000
	// analyticsPeakBudget is the container's memory limit.
	analyticsPeakBudget = 512 << 20
	// analyticsScriptBudget is the browser script's core, gzipped as
	// served, and analyticsErrorsBudget what errors and Web Vitals add.
	analyticsScriptBudget = 2 << 10
	analyticsErrorsBudget = 2 << 10
	// analyticsRecorderBudget is the session recorder (rrweb), which only
	// sampled page loads of sites that record load.
	analyticsRecorderBudget = 50 << 10
)

// errorsAndVitals and goalsAndFunnels are bench.ErrorsAndVitals and
// bench.GoalsAndFunnels: other views' timings (the bench is in the
// store's own module).
const (
	errorsAndVitals = "errors and vitals"
	goalsAndFunnels = "goals and funnels"
)

// filtered reports whether r is a filtered dashboard's timing.
func filtered(r analyticsResult) bool {
	return r.Filter != "none" && r.Filter != errorsAndVitals && r.Filter != goalsAndFunnels
}

// analyticsResult is one line of `dock-analytics bench -json`.
type analyticsResult struct {
	Range, Filter    string
	P50, P95         time.Duration
	SlowestQuery     time.Duration
	SlowestQueryName string
}

// checkAnalytics seeds 1M events into a temp store, then measures the
// idle server, the dashboards over its HTTP API, ingest and the peak
// memory of all that.
func checkAnalytics(o options, t *table) error {
	const name = "analytics idle"
	n, withErrors, recorder := ingest.ScriptSizes()
	t.within("analytics script (gzip)", kb(int64(n)), "≤ "+kb(analyticsScriptBudget), n <= analyticsScriptBudget)
	t.within("analytics session recorder (gzip)", kb(int64(recorder)), "≤ "+kb(analyticsRecorderBudget), recorder <= analyticsRecorderBudget)
	t.within("analytics script's errors and vitals (gzip)", kb(int64(withErrors-n)), "≤ "+kb(analyticsErrorsBudget), withErrors-n <= analyticsErrorsBudget)
	if runtime.GOOS != "linux" {
		t.add(name, "needs /proc (Linux)", "", warn)
		return nil
	}
	if _, err := os.Stat(o.analytics); err != nil {
		return fmt.Errorf("%w (run `make build-analytics` first)", err)
	}
	dir, err := os.MkdirTemp("", "dock-perf-analytics-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	db := filepath.Join(dir, "analytics.duckdb")
	fmt.Fprintln(os.Stderr, "perf: seeding the analytics store with 1M events...")
	if out, err := exec.Command(o.analytics, "seed", "-db", db, "-events", "1000000").CombinedOutput(); err != nil {
		return fmt.Errorf("seed: %w: %s", err, out)
	}

	ports, err := freePorts(1)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", ports[0])
	key := make([]byte, 24)
	_, _ = rand.Read(key)
	token := hex.EncodeToString(key)
	env := append(cleanEnv(), "DOCK_ANALYTICS_TOKEN="+token, "GOMEMLIMIT=48MiB") // as deploy/analytics/Dockerfile
	logf, err := os.Create(filepath.Join(dir, "analytics.log"))
	if err != nil {
		return err
	}
	cmd := exec.Command(o.analytics, "serve", "-db", db, "-listen", fmt.Sprintf("127.0.0.1:%d", ports[0]))
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
		return d.idle(t, name, o.idle, idleBudgets{analyticsAnonBudget, analyticsTicksBudget, analyticsWakeupsBudget})
	}); err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "perf: timing analytics dashboards...")
	var results []analyticsResult
	if err := runJSON(&results, env, o.analytics, "bench", "-url", url, "-runs", "10", "-ranges", "24h,30d,365d", "-json"); err != nil {
		return err
	}
	worst := func(keep func(analyticsResult) bool) analyticsResult {
		var w analyticsResult
		for _, r := range results {
			if keep(r) && r.P95 > w.P95 {
				w = r
			}
		}
		return w
	}
	for _, c := range []struct {
		check  string
		budget time.Duration
		keep   func(analyticsResult) bool
	}{
		{"analytics dashboard p95, 1M events", analyticsDashboardBudget, func(r analyticsResult) bool { return r.Filter == "none" }},
		{"analytics filtered dashboard p95, 1M events, ≤ 30 days", analyticsFilterBudget, func(r analyticsResult) bool { return filtered(r) && r.Range != "365d" }},
		{"analytics filtered dashboard p95, 1M events, a year", analyticsYearFilterBudget, func(r analyticsResult) bool { return filtered(r) && r.Range == "365d" }},
		{"analytics errors and vitals p95, 1M events, every range", analyticsErrorsViewBudget, func(r analyticsResult) bool { return r.Filter == errorsAndVitals }},
		{"analytics goals and funnels p95, 1M events, every range", analyticsErrorsViewBudget, func(r analyticsResult) bool { return r.Filter == goalsAndFunnels }},
	} {
		w := worst(c.keep)
		budget, text := scaledBudget(c.budget, o.speed)
		t.withinTiming(c.check, fmt.Sprintf("%s (%s, %s)", ms(w.P95), w.Range, w.Filter), text, w.P95 <= budget)
	}

	var load struct {
		Events   int
		Seconds  float64
		WriteP95 time.Duration
	}
	if err := runJSON(&load, env, o.analytics, "load", "-url", url, "-events", "100000", "-json"); err != nil {
		return err
	}
	rate := float64(load.Events) / load.Seconds
	t.withinTiming("analytics ingest, batches of 1,000", fmt.Sprintf("%.0f events/s (write p95 %s)", rate, ms(load.WriteP95)),
		fmt.Sprintf("≥ %d events/s", analyticsIngestBudget), rate >= analyticsIngestBudget)

	peak, err := statusField(fmt.Sprintf("/proc/%d/status", cmd.Process.Pid), "VmHWM")
	if err != nil {
		return err
	}
	t.within("analytics peak memory", mb(peak<<10), "≤ "+mb(analyticsPeakBudget)+" (the container's limit)", peak<<10 <= analyticsPeakBudget)
	return nil
}

// waitStore waits up to 30 s for the store's health endpoint.
func waitStore(url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/v1/health", nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("the analytics store didn't answer within 30 s")
}

// runJSON runs bin with args and env and decodes its standard output
// into out.
func runJSON(out any, env []string, bin string, args ...string) error {
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", filepath.Base(bin), strings.Join(slices.Clone(args[:1]), " "), err, stderr.String())
	}
	return json.Unmarshal(b, out)
}
