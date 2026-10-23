package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// daemon is a server under test with a temp data dir: `dock serve` on the
// fake runtime, or `tempobase serve` (tempobase.go).
type daemon struct {
	cmd  *exec.Cmd
	dir  string
	base string // http://host:port/api/v1
	// token is an API token; without one, requests carry the session
	// cookie the way a browser would.
	token, cookie string
	http          *http.Client
}

// authorize adds the daemon's credentials to req: the bearer token, or
// the session cookie with the same-origin header a browser sends.
func (d *daemon) authorize(req *http.Request) {
	if d.token != "" {
		req.Header.Set("Authorization", "Bearer "+d.token)
		return
	}
	if d.cookie != "" {
		req.Header.Set("Cookie", d.cookie)
	}
	req.Header.Set("Sec-Fetch-Site", "same-origin")
}

// checkDaemon measures the idle daemon, then the idle daemon with SSE
// clients connected, then seeds it through the API and measures read
// latency.
func checkDaemon(o options, t *table) error {
	if runtime.GOOS != "linux" {
		t.add("daemon", "needs /proc (Linux)", "", warn)
		return nil
	}
	d, err := startDaemon(o.dock)
	if err != nil {
		return err
	}
	defer d.stop()

	// Let start-up work (migrations, reconcile, first GC) finish.
	time.Sleep(3 * time.Second)
	betweenTicks(o.idle)
	if err := d.idle(t, "daemon idle", o.idle, dockIdle(0)); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := d.connectSSE(ctx, o.sseClients); err != nil {
		cancel()
		return err
	}
	time.Sleep(time.Second)
	betweenTicks(o.idle)
	err = d.idle(t, fmt.Sprintf("daemon idle, %d SSE clients", o.sseClients), o.idle, dockIdle(o.sseClients))
	cancel()
	if err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "perf: seeding the daemon through the API...")
	start := time.Now()
	if err := d.seed(); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	fmt.Fprintf(os.Stderr, "perf: seeded in %s; waiting for cutover drains to finish\n", time.Since(start).Round(time.Millisecond))
	// Replaced containers are stopped core.DefaultCutoverDrain (12 s) after
	// each cutover; let that settle so it doesn't land in the measurements.
	time.Sleep(15 * time.Second)
	if err := d.latency(t, o.speed); err != nil {
		return err
	}
	if anon, err := d.anon(); err == nil {
		t.add("daemon memory after seeding and load", mb(anon)+" anon", "", info)
	}
	return d.servedUI(t)
}

// servedUI checks what a browser downloads for the initial JS as the
// daemon serves it (compressed) and that it may cache it.
func (d *daemon) servedUI(t *table) error {
	// Go's transport would decompress transparently and hide the size.
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}, Timeout: 10 * time.Second}
	get := func(path string) (http.Header, []byte, error) {
		req, _ := http.NewRequest(http.MethodGet, strings.TrimSuffix(d.base, "/api/v1")+path, nil)
		req.Header.Set("Accept-Encoding", "gzip, br")
		resp, err := client.Do(req)
		if err != nil {
			return nil, nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		return resp.Header, b, err
	}
	h, index, err := get("/")
	if err != nil {
		return err
	}
	if h.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(bytes.NewReader(index))
		if err != nil {
			return err
		}
		if index, err = io.ReadAll(zr); err != nil {
			return err
		}
	}
	var wire int64
	cacheable := true
	for _, re := range []*regexp.Regexp{entryRe, preloadRe} {
		for _, m := range re.FindAllSubmatch(index, -1) {
			h, body, err := get(string(m[1]))
			if err != nil {
				return err
			}
			wire += int64(len(body))
			cc := h.Get("Cache-Control")
			cacheable = cacheable && (strings.Contains(cc, "max-age") || h.Get("ETag") != "" || h.Get("Last-Modified") != "")
		}
	}
	if wire == 0 {
		t.add("UI initial JS as served", "no UI in the binary", "", warn)
		return nil
	}
	cache := "cacheable"
	if !cacheable {
		cache = "no cache headers: downloaded on every load"
	}
	t.within("UI initial JS as served", fmt.Sprintf("%s on the wire, %s", kb(wire), cache),
		"≤ "+kb(initialJSBudget)+", cacheable", wire <= initialJSBudget && cacheable)
	return nil
}

// freePorts picks n distinct free loopback ports. Every listener stays
// open until all n are chosen, so no two picks can return the same port;
// they are closed on return, just before the caller starts its server.
func freePorts(n int) ([]int, error) {
	ls := make([]net.Listener, 0, n)
	defer func() {
		for _, l := range ls {
			_ = l.Close()
		}
	}()
	ports := make([]int, 0, n)
	for range n {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		ls = append(ls, l)
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
	}
	return ports, nil
}

func startDaemon(bin string) (*daemon, error) {
	dir, err := os.MkdirTemp("", "dock-perf-daemon-")
	if err != nil {
		return nil, err
	}
	ports, err := freePorts(3)
	if err != nil {
		return nil, err
	}
	api, internal, public := ports[0], ports[1], ports[2]
	data := filepath.Join(dir, "data")
	cfg := fmt.Sprintf(`data_dir = %q
listen = "127.0.0.1:%d"
internal_listen = "127.0.0.1:%d"
public_listen = "127.0.0.1:%d"
public_ip = "127.0.0.1"
base_domain = "localhost"

[caddy]
admin_socket = %q
http_port = 18080
https_port = 18443
disable_tls = true
`, data, api, internal, public, filepath.Join(data, "caddy-admin.sock"))
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		return nil, err
	}
	logf, err := os.Create(filepath.Join(dir, "daemon.log"))
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, "serve", "--config", cfgPath)
	// As the systemd unit runs it (deploy/systemd/dock.service).
	cmd.Env = append(cleanEnv(), "DOCK_FAKE_RUNTIME=1", "GOMEMLIMIT=64MiB", "HOME="+dir)
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	d := &daemon{cmd: cmd, dir: dir, base: fmt.Sprintf("http://127.0.0.1:%d/api/v1", api), http: &http.Client{Timeout: 30 * time.Second}}
	if err := d.waitHealthy(logf.Name()); err != nil {
		return nil, err
	}

	out, err := exec.Command(bin, "admin", "bootstrap", "--config", cfgPath).Output()
	if err != nil {
		d.stop()
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	d.token = strings.TrimSpace(string(out))
	return d, nil
}

// waitHealthy waits up to 10 s for /health to answer 200. If it doesn't,
// it stops the server and returns its log.
func (d *daemon) waitHealthy(logPath string) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := d.http.Get(d.base + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			log, _ := os.ReadFile(logPath) // before stop removes it
			d.stop()
			return fmt.Errorf("%s did not become healthy: %s", filepath.Base(d.cmd.Path), log)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (d *daemon) stop() {
	_ = d.cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { _ = d.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = d.cmd.Process.Kill()
		<-done
	}
	_ = os.RemoveAll(d.dir)
}

// procSample is the daemon's CPU time and wake-ups so far.
type procSample struct {
	ticks   int64 // utime + stime, in clock ticks
	wakeups int64 // voluntary context switches over all threads
}

func (d *daemon) sample() (procSample, error) {
	pid := d.cmd.Process.Pid
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return procSample{}, err
	}
	// Fields after the command name, which is in parentheses: state is
	// field 3, utime 14 and stime 15.
	f := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+1:]))
	utime, _ := strconv.ParseInt(f[11], 10, 64)
	stime, _ := strconv.ParseInt(f[12], 10, 64)
	s := procSample{ticks: utime + stime}
	tasks, err := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/status", pid))
	if err != nil {
		return s, err
	}
	for _, task := range tasks {
		if v, err := statusField(task, "voluntary_ctxt_switches"); err == nil {
			s.wakeups += v
		}
	}
	return s, nil
}

// statusField reads a numeric field of a /proc status file.
func statusField(path, name string) (int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for line := range strings.Lines(string(b)) {
		if v, ok := strings.CutPrefix(line, name+":"); ok {
			return strconv.ParseInt(strings.Fields(v)[0], 10, 64)
		}
	}
	return 0, fmt.Errorf("%s: no %s", path, name)
}

// anon is the daemon's anonymous resident memory in bytes: what it really
// costs, without the mapped binary.
func (d *daemon) anon() (int64, error) {
	kb, err := statusField(fmt.Sprintf("/proc/%d/status", d.cmd.Process.Pid), "RssAnon")
	return kb << 10, err
}

// idleBudgets are the limits of one idle window.
type idleBudgets struct {
	anon    int64   // anonymous RSS, bytes
	ticks   int64   // CPU clock ticks in the window
	wakeups float64 // voluntary context switches per second
}

// dockIdle is Dock's idle budget with clients SSE streams open.
func dockIdle(clients int) idleBudgets {
	return idleBudgets{
		anon:    idleAnonBudget,
		ticks:   idleTicksBudget,
		wakeups: idleWakeupsBudget + float64(clients)*sseClientWakeupsBudget,
	}
}

// idle measures CPU ticks and wake-ups over one window, and memory at its
// end, and adds a row for each against b. Callers pick when the window
// starts: Dock's fits between two statistics ticks (betweenTicks).
func (d *daemon) idle(t *table, name string, window time.Duration, b idleBudgets) error {
	before, err := d.sample()
	if err != nil {
		return err
	}
	time.Sleep(window)
	after, err := d.sample()
	if err != nil {
		return err
	}
	anon, err := d.anon()
	if err != nil {
		return err
	}
	ticks := after.ticks - before.ticks
	t.within(name+": memory", mb(anon)+" anon", "≤ "+mb(b.anon), anon <= b.anon)
	t.withinTiming(name+": CPU", fmt.Sprintf("%d ticks in %s", ticks, window), fmt.Sprintf("≤ %d ticks", b.ticks), ticks <= b.ticks)
	wakeups := float64(after.wakeups-before.wakeups) / window.Seconds()
	t.withinTiming(name+": wake-ups", fmt.Sprintf("%.2f/s", wakeups), fmt.Sprintf("≤ %.1f/s", b.wakeups), wakeups <= b.wakeups)
	return nil
}

// betweenTicks waits until an idle window fits between two of the
// statistics collector's minute ticks (internal/core/stats.go). The tick is
// the daemon's one always-on periodic job, budgeted on its own
// (BenchmarkStatsTick50); its 10-20 wake-ups alone would fail a 30 s
// window that happened to contain it, whatever else the daemon does.
func betweenTicks(window time.Duration) {
	const margin = 3 * time.Second // the tick's own work
	if window > time.Minute-2*margin {
		return
	}
	now := time.Now()
	last := now.Truncate(time.Minute)
	switch {
	case now.Before(last.Add(margin)):
		time.Sleep(last.Add(margin).Sub(now))
	case now.Add(window + margin).After(last.Add(time.Minute)):
		time.Sleep(last.Add(time.Minute + margin).Sub(now))
	}
}

// connectSSE opens n /events streams, waits until each is live and keeps
// reading them until ctx is done.
func (d *daemon) connectSSE(ctx context.Context, n int) error {
	ready := make(chan error, n)
	client := &http.Client{} // no timeout: streams stay open
	for range n {
		go func() {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.base+"/events", nil)
			req.Header.Set("Authorization", "Bearer "+d.token)
			resp, err := client.Do(req)
			if err != nil {
				ready <- err
				return
			}
			defer func() { _ = resp.Body.Close() }()
			sc := bufio.NewScanner(resp.Body)
			if !sc.Scan() || sc.Text() != "event: ready" {
				ready <- fmt.Errorf("events: status %d, first line %q", resp.StatusCode, sc.Text())
				return
			}
			ready <- nil
			for sc.Scan() {
			}
		}()
	}
	for range n {
		if err := <-ready; err != nil {
			return err
		}
	}
	return nil
}

// call makes an API request and decodes a JSON response into out (if not
// nil). Any status other than 2xx is an error.
func (d *daemon) call(method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, d.base+path, r)
	if err != nil {
		return err
	}
	d.authorize(req)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, b)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}
