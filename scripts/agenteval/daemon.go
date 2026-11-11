package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// daemon is one case's Dock: `dock serve` on the fake runtime with its own
// data dir and loopback ports, so cases never see each other's state and
// never touch another dock on the machine (a `make dev`, prod contexts).
type daemon struct {
	bin   string // the dock binary, also the CLI the checks and agents use
	dir   string
	cmd   *exec.Cmd
	url   string // http://127.0.0.1:port
	admin string // the bootstrap admin token: setup and checks only
	// password is the bootstrap admin user's, for what only a person may
	// do (approving access requests); cookie is its session once used.
	password, cookie string
	http             *http.Client
}

// startDaemon starts a daemon with extra environment (DOCK_FAKE_* hooks)
// and bootstraps its admin. stop removes everything it made.
func startDaemon(bin string, env map[string]string) (*daemon, error) {
	dir, err := os.MkdirTemp("", "dock-agenteval-")
	if err != nil {
		return nil, err
	}
	ports, err := freePorts(10)
	if err != nil {
		return nil, err
	}
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
traffic_listen = "127.0.0.1:%d"

# The fake runtime runs an in-memory store; it only needs a binary to name.
[analytics]
store_binary = %q
ingest_listen = "127.0.0.1:%d"
stats_listen = "127.0.0.1:%d"

[traces]
ingest_listen = "127.0.0.1:%d"

[status]
listen = "127.0.0.1:%d"

[hooks]
listen = "127.0.0.1:%d"

# Scans start a second after the last deploy instead of a minute.
[scan]
quiet_seconds = 1
`, data, ports[0], ports[1], ports[2], filepath.Join(data, "caddy-admin.sock"), ports[3],
		filepath.Join(dir, "dock-analytics"), ports[4], ports[5], ports[6], ports[7], ports[8])
	if err := os.WriteFile(filepath.Join(dir, "dock-analytics"), []byte("fake"), 0o755); err != nil {
		return nil, err
	}
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		return nil, err
	}
	logf, err := os.Create(filepath.Join(dir, "daemon.log"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = logf.Close() }()
	cmd := exec.Command(bin, "serve", "--config", cfgPath)
	cmd.Env = append(cleanEnv(), "DOCK_FAKE_RUNTIME=1", "HOME="+dir)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	d := &daemon{bin: bin, dir: dir, cmd: cmd, url: fmt.Sprintf("http://127.0.0.1:%d", ports[0]), http: &http.Client{Timeout: 60 * time.Second}}
	if err := d.waitHealthy(); err != nil {
		log, _ := os.ReadFile(logf.Name())
		d.stop()
		return nil, fmt.Errorf("%w: %s", err, log)
	}
	var stderr bytes.Buffer
	boot := exec.Command(bin, "admin", "bootstrap", "--config", cfgPath)
	boot.Stderr = &stderr
	out, err := boot.Output()
	if err != nil {
		d.stop()
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	d.admin = strings.TrimSpace(string(out))
	// "Created admin user admin@localhost with password X (shown once)"
	if _, rest, ok := strings.Cut(stderr.String(), " with password "); ok {
		d.password, _, _ = strings.Cut(rest, " ")
	}
	return d, nil
}

// waitHealthy waits up to 15 s for /health to answer 200.
func (d *daemon) waitHealthy() error {
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		resp, err := d.http.Get(d.url + "/api/v1/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
	}
	return errors.New("daemon did not become healthy")
}

// stop ends the daemon and removes its data dir.
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

// dock runs the CLI as the admin with --json and returns stdout. Setup
// and checks use it where the CLI says it shorter than the API.
func (d *daemon) dock(args ...string) ([]byte, error) {
	return d.dockAs(d.admin, args...)
}

// dockAs runs the CLI with token and --json.
func (d *daemon) dockAs(token string, args ...string) ([]byte, error) {
	cmd := exec.Command(d.bin, append(args, "--json")...)
	cmd.Env = append(cleanEnv(), "DOCK_URL="+d.url, "DOCK_TOKEN="+token, "HOME="+d.dir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("dock %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// call sends an API request as the admin and decodes the answer into out
// (nil: ignore it). path is under /api/v1.
func (d *daemon) call(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, d.url+"/api/v1"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+d.admin)
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
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(b))
	}
	if out == nil || len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

// decide approves or rejects an access request as the admin user, the way
// a person does in the UI: tokens never decide requests.
func (d *daemon) decide(id int64, approve bool, comment string) error {
	if d.cookie == "" {
		b, _ := json.Marshal(map[string]string{"email": "admin@localhost", "password": d.password})
		req, _ := http.NewRequest(http.MethodPost, d.url+"/api/v1/auth/login", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		resp, err := d.http.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 || len(resp.Cookies()) == 0 {
			return fmt.Errorf("log in as the admin: %s", resp.Status)
		}
		for _, c := range resp.Cookies() {
			d.cookie += c.Name + "=" + c.Value + "; "
		}
	}
	verb := "reject"
	if approve {
		verb = "approve"
	}
	b, _ := json.Marshal(map[string]string{"comment": comment})
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/v1/requests/%d/%s", d.url, id, verb), bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", d.cookie)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s request %d: %s: %s", verb, id, resp.Status, body)
	}
	return nil
}

// get is call's GET, typed by its result.
func get[T any](d *daemon, path string, args ...any) (T, error) {
	var v T
	err := d.call(http.MethodGet, fmt.Sprintf(path, escapeAll(args)...), nil, &v)
	return v, err
}

// escapeAll path-escapes string arguments for get's format.
func escapeAll(args []any) []any {
	out := slices.Clone(args)
	for i, a := range out {
		if s, ok := a.(string); ok {
			out[i] = url.PathEscape(s)
		}
	}
	return out
}

// freePorts picks n distinct free loopback ports.
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

// cleanEnv is this process's environment without Dock's variables, so a
// developer's DOCK_URL or DOCK_TOKEN (prod's) never reaches a case.
func cleanEnv() []string {
	return slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "DOCK_")
	})
}
