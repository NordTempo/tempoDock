package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// tempoBase's budgets (ADR 0024 section 5): smaller than Dock's, since it
// has no runtime, statistics tick or event stream. The binary measured
// 19.9 MB with its UI, then 20.2 MB with database insights (ADR 0069,
// +0.2 MB of code and API types), so the cap moved from 20 to 21 MB; the
// baseline +10% check still catches growth. The pure-Go SQLite is 2 MB of
// code, Huma 1.3 MB, pgx 1.1 MB, the UI 1 MB uncompressed. Embedding the
// UI pre-gzipped would win back about 0.7 MB.
const (
	tempobaseBinaryBudget   = 21 << 20
	tempobaseAnonBudget     = 20 << 20
	tempobaseTicksBudget    = 1
	tempobaseWakeupsBudget  = 0.1 // Go's forced GC every 2 min is ~0.01/s
	tempobaseP95Budget      = 10 * time.Millisecond
	tempobaseInitialJS      = 120 << 10 // gzipped
	tempobaseSeedConns      = 50
	tempobaseSeedUsers      = 20
	tempobaseIdleCloseAfter = 65 * time.Second // the engine closes idle connections after 60 s
)

// The seeded admin. A throwaway account in a temp data dir.
const (
	tempobaseAdmin    = "perf@example.com"
	tempobasePassword = "perf-password-not-a-secret"
)

// notBuilt is the measured text of a tempoBase check that has nothing to
// measure yet: the server (cmd/tempobase) or its UI (web/tempobase) isn't
// in the tree. Once they are, the checks run and fail without a build.
const notBuilt = "not built yet: no %s (ADR 0024)"

// tempobaseSourceExists reports whether cmd/tempobase is in the tree.
func tempobaseSourceExists() bool {
	st, err := os.Stat("cmd/tempobase")
	return err == nil && st.IsDir()
}

// checkTempobaseBinary checks bin/tempobase against its absolute budget
// and, once recorded, the baseline.
func checkTempobaseBinary(o options, t *table) error {
	const name = "tempobase binary size"
	if !tempobaseSourceExists() {
		t.add(name, fmt.Sprintf(notBuilt, "cmd/tempobase"), "", skipped)
		return nil
	}
	st, err := os.Stat(o.tempobase)
	if err != nil {
		return fmt.Errorf("%w (run `make build-tempobase` first)", err)
	}
	b, err := loadBaseline(o.baseline)
	if err != nil {
		return err
	}
	size, limit := st.Size(), int64(tempobaseBinaryBudget)
	measured, budget := mb(size), "≤ "+mb(limit)
	if b.TempobaseBinaryBytes > 0 {
		limit = min(limit, int64(float64(b.TempobaseBinaryBytes)*(1+binaryGrowthLimit)))
		measured += fmt.Sprintf(" (%+.1f%% vs baseline)", (float64(size)/float64(b.TempobaseBinaryBytes)-1)*100)
		budget = fmt.Sprintf("≤ %s (%s and baseline +%.0f%%)", mb(limit), mb(tempobaseBinaryBudget), binaryGrowthLimit*100)
	} else {
		budget += " (no baseline yet)"
	}
	t.within(name, measured, budget, size <= limit)
	return nil
}

// checkTempobaseUI checks what a first load of tempoBase's UI downloads.
// Its CSS is Dock's index.css, under the same budget.
func checkTempobaseUI(o options, t *table) error {
	const name = "tempobase UI initial JS (gzip)"
	if _, err := os.Stat(filepath.Join(filepath.Dir(o.tempobaseDist), "index.html")); errors.Is(err, fs.ErrNotExist) {
		t.add(name, fmt.Sprintf(notBuilt, "web/tempobase/index.html"), "", skipped)
		return nil
	}
	a, err := initialAssets(o.tempobaseDist)
	if err != nil {
		return err
	}
	t.within(name, fmt.Sprintf("%s (%d files)", kb(a.js), a.jsFiles), "≤ "+kb(tempobaseInitialJS), a.js <= tempobaseInitialJS)
	t.within("tempobase UI initial CSS (gzip)", kb(a.css), "≤ "+kb(cssBudget), a.css <= cssBudget)
	t.within("tempobase UI initial fonts", fmt.Sprintf("%s (%d files)", kb(a.fonts), a.fontFiles), "≤ "+kb(initialFontsBudget), a.fonts <= initialFontsBudget)
	return nil
}

// checkTempobaseDaemon measures an idle tempobase after start, a login and
// a connection list; then seeds it and measures read latency; then, with
// -tempobase-pg, that it closes its Postgres connections after use.
func checkTempobaseDaemon(o options, t *table) error {
	const name = "tempobase idle"
	if !tempobaseSourceExists() {
		t.add(name, fmt.Sprintf(notBuilt, "cmd/tempobase"), "", skipped)
		return nil
	}
	if runtime.GOOS != "linux" {
		t.add(name, "needs /proc (Linux)", "", warn)
		return nil
	}
	tb, err := startTempobase(o.tempobase)
	if err != nil {
		return err
	}
	defer tb.stop()

	// What the UI's first page loads.
	if err := tb.call(http.MethodGet, "/connections", nil, nil); err != nil {
		return err
	}
	time.Sleep(3 * time.Second) // start-up work and the first GC
	if err := o.gate.apart(func() error {
		return tb.idle(t, name, o.idle, idleBudgets{tempobaseAnonBudget, tempobaseTicksBudget, tempobaseWakeupsBudget})
	}); err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "perf: seeding tempobase...")
	if err := tb.seed(); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	budget, budgetText := scaledBudget(tempobaseP95Budget, o.speed)
	for _, path := range []string{"/me", "/connections"} {
		p50, p95, err := tb.load(path)
		if err != nil {
			return err
		}
		t.withinTiming("tempobase API p95 GET "+path, fmt.Sprintf("%s (p50 %s)", ms(p95), ms(p50)), budgetText, p95 <= budget)
	}
	if anon, err := tb.anon(); err == nil {
		t.add("tempobase memory after seeding and load", mb(anon)+" anon", "", info)
	}

	if o.tempobasePG == "" {
		t.add("tempobase Postgres connections after use", "no -tempobase-pg URL", "", skipped)
		return nil
	}
	return tb.idleConnections(t, o.gate, o.tempobasePG)
}

// tempobase is a running `tempobase serve`, signed in as the admin with a
// session cookie, plus what its admin commands need.
type tempobase struct {
	*daemon
	bin string
	env []string
}

// startTempobase starts tempobase on a free port with a temp data dir and
// the image's GOMEMLIMIT, creates the admin and signs in.
func startTempobase(bin string) (*tempobase, error) {
	dir, err := os.MkdirTemp("", "dock-perf-tempobase-")
	if err != nil {
		return nil, err
	}
	data := filepath.Join(dir, "data")
	if err := os.Mkdir(data, 0o700); err != nil {
		return nil, err
	}
	ports, err := freePorts(1)
	if err != nil {
		return nil, err
	}
	env := append(cleanEnv(),
		fmt.Sprintf("TEMPOBASE_LISTEN=127.0.0.1:%d", ports[0]),
		"TEMPOBASE_DATA_DIR="+data,
		"GOMEMLIMIT=48MiB", // as deploy/tempobase/Dockerfile and tempobase.service
		measuredProcs,
		"HOME="+dir,
	)
	logf, err := os.Create(filepath.Join(dir, "tempobase.log"))
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, "serve")
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	tb := &tempobase{
		daemon: &daemon{cmd: cmd, dir: dir, base: fmt.Sprintf("http://127.0.0.1:%d/api/v1", ports[0]), http: &http.Client{Timeout: 30 * time.Second}},
		bin:    bin,
		env:    env,
	}
	if err := tb.waitHealthy(logf.Name()); err != nil {
		return nil, err
	}
	if err := tb.admin(tempobasePassword+"\n", "create-user", "--email", tempobaseAdmin, "--name", "perf", "--role", "admin"); err != nil {
		tb.stop()
		return nil, err
	}
	if err := tb.login(); err != nil {
		tb.stop()
		return nil, err
	}
	return tb, nil
}

// admin runs `tempobase admin ARGS...` against the server's data dir with
// stdin as its input.
func (tb *tempobase) admin(stdin string, args ...string) error {
	cmd := exec.Command(tb.bin, append([]string{"admin"}, args...)...)
	cmd.Env = tb.env
	cmd.Stdin = strings.NewReader(stdin)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tempobase admin %s: %w: %s", args[0], err, out)
	}
	return nil
}

// login signs the admin in and keeps the session cookie.
func (tb *tempobase) login() error {
	body, _ := json.Marshal(map[string]string{"email": tempobaseAdmin, "password": tempobasePassword})
	req, _ := http.NewRequest(http.MethodPost, tb.base+"/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := tb.http.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("login: status %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "tempobase_session" {
			tb.cookie = c.Name + "=" + c.Value
			return nil
		}
	}
	return errors.New("login: no tempobase_session cookie")
}

// seed adds users (with set-password links, so no hashing) and saved
// connections to hosts that never resolve: listing them needs no database.
func (tb *tempobase) seed() error {
	for i := range tempobaseSeedUsers - 1 {
		role := []string{"viewer", "editor"}[i%2]
		if err := tb.admin("", "create-user", "--email", fmt.Sprintf("user-%02d@example.com", i), "--name", fmt.Sprintf("user %02d", i), "--role", role, "--link"); err != nil {
			return err
		}
	}
	for i := range tempobaseSeedConns {
		if err := tb.call(http.MethodPost, "/connections", map[string]any{
			"name":       fmt.Sprintf("db-%02d", i),
			"url":        fmt.Sprintf("postgres://app:app@db-%02d.invalid:5432/app?sslmode=disable", i),
			"production": i%5 == 0,
		}, nil); err != nil {
			return err
		}
	}
	return nil
}

// idleConnections saves pgURL as a connection, sets it up, reads its
// schema and runs a query, then checks that no socket to Postgres is left
// tempobaseIdleCloseAfter later: the engine's idle close works and nothing
// pools connections behind it.
func (tb *tempobase) idleConnections(t *table, g *gate, pgURL string) error {
	u, err := url.Parse(pgURL)
	if err != nil {
		return fmt.Errorf("-tempobase-pg: %w", err)
	}
	port, err := strconv.Atoi(cmp.Or(u.Port(), "5432"))
	if err != nil {
		return fmt.Errorf("-tempobase-pg: port: %w", err)
	}
	const conn = "/connections/perf-pg"
	steps := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/connections", map[string]any{"name": "perf-pg", "url": pgURL, "production": false}},
		{http.MethodPost, conn + "/data/setup", nil},
		{http.MethodGet, conn + "/data/schema", nil},
		{http.MethodPost, conn + "/data/query", map[string]string{"sql": "select 1"}},
	}
	for _, s := range steps {
		// Errors name the path, never the URL: it holds a password.
		if err := tb.call(s.method, s.path, s.body, nil); err != nil {
			return err
		}
	}
	open, err := socketsTo(tb.cmd.Process.Pid, port)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "perf: waiting %s for tempobase to close its Postgres connections\n", tempobaseIdleCloseAfter)
	g.pause(tempobaseIdleCloseAfter)
	left, err := socketsTo(tb.cmd.Process.Pid, port)
	if err != nil {
		return err
	}
	t.within(fmt.Sprintf("tempobase Postgres connections %s after use", tempobaseIdleCloseAfter),
		fmt.Sprintf("%d (%d right after use)", left, open), "0", left == 0)
	return nil
}

// socketsTo counts pid's TCP sockets whose remote port is port, by
// matching its socket file descriptors against /proc/PID/net/tcp{,6}.
func socketsTo(pid, port int) (int, error) {
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return 0, err
	}
	inodes := map[string]bool{}
	for _, fd := range fds {
		link, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, fd.Name()))
		if inode, ok := strings.CutPrefix(link, "socket:["); err == nil && ok {
			inodes[strings.TrimSuffix(inode, "]")] = true
		}
	}
	n := 0
	for _, table := range []string{"tcp", "tcp6"} {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/net/%s", pid, table))
		if errors.Is(err, fs.ErrNotExist) {
			continue // no IPv6
		}
		if err != nil {
			return 0, err
		}
		// sl local_address rem_address st tx:rx tr:when retrnsmt uid timeout inode
		for i, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if i == 0 || len(f) < 10 {
				continue
			}
			_, hexPort, _ := strings.Cut(f[2], ":")
			p, err := strconv.ParseUint(hexPort, 16, 16)
			if err == nil && int(p) == port && inodes[f[9]] {
				n++
			}
		}
	}
	return n, nil
}
