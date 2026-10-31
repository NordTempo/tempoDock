//go:build integration

package caddy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/NordTempo/tempoDock/internal/access"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/traffic"
)

// These tests run a real `caddy run` (from PATH) with a temp config, socket
// and data dir. Certificates come from Caddy's internal CA; no real ACME CA
// is ever contacted.

type testCaddy struct {
	proxy   *Proxy
	pid     int
	dataDir string
	opts    Options
	// client sends every request to the local Caddy, whatever the URL host,
	// and trusts Caddy's internal CA.
	client *http.Client
}

// testGracePeriod replaces defaultGracePeriod so Caddy exits quickly.
const testGracePeriod = 2 * time.Second

func startCaddy(t *testing.T, askURL string, disableTLS bool) *testCaddy {
	t.Helper()
	return startCaddyWith(t, func(o *Options) { o.AskURL, o.DisableTLS = askURL, disableTLS })
}

// startCaddyWith starts Caddy with the test defaults changed by mod.
func startCaddyWith(t *testing.T, mod func(*Options)) *testCaddy {
	t.Helper()
	bin, err := exec.LookPath("caddy")
	if err != nil {
		t.Skip("caddy binary not on PATH")
	}
	// Unix socket paths are limited to ~108 bytes; t.TempDir can be long.
	dir, err := os.MkdirTemp("", "caddy")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// Another process can take a picked port before Caddy binds it (the
	// suite runs next to other tests); Caddy then exits on its initial
	// config, and we start it again with new ports.
	var opts Options
	var cmd *exec.Cmd
	for attempt := 1; ; attempt++ {
		ports := freePorts(t, 2)
		opts = Options{
			AdminSocket: filepath.Join(dir, "admin.sock"),
			HTTPPort:    ports[0],
			HTTPSPort:   ports[1],
			PublicIPs:   []string{"127.0.0.1"},
			internalCA:  true,
			gracePeriod: testGracePeriod,
		}
		mod(&opts)
		var portTaken bool
		cmd, portTaken = runCaddy(t, bin, dir, opts)
		if !portTaken {
			break
		}
		if attempt == 3 {
			t.Fatal("caddy: its ports were taken three times")
		}
	}

	p, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	tc := &testCaddy{proxy: p, pid: cmd.Process.Pid, dataDir: filepath.Join(dir, "data"), opts: opts}
	var d net.Dialer
	tc.client = &http.Client{
		Timeout: 10 * time.Second,
		// Show redirects to the test instead of following them.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				_, port, _ := net.SplitHostPort(addr)
				return d.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
			},
			TLSClientConfig:     &tls.Config{RootCAs: x509.NewCertPool()},
			MaxIdleConnsPerHost: 64,
			// Like browsers, use HTTP/2 when the server offers it.
			ForceAttemptHTTP2: true,
		},
	}
	return tc
}

// trustInternalCA makes the test client and CheckDomain trust Caddy's
// internal root. The root exists once a config with the tls app is loaded.
func (tc *testCaddy) trustInternalCA(t *testing.T) {
	t.Helper()
	pem, err := os.ReadFile(filepath.Join(tc.dataDir, "caddy", "pki", "authorities", "local", "root.crt"))
	if err != nil {
		t.Fatalf("read internal CA root: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	tc.client.Transport.(*http.Transport).TLSClientConfig.RootCAs = pool
	tc.proxy.rootCAs = pool
}

func (tc *testCaddy) url(scheme, host, path string) string {
	port := tc.opts.HTTPPort
	if scheme == "https" {
		port = tc.opts.HTTPSPort
	}
	return fmt.Sprintf("%s://%s:%d%s", scheme, host, port, path)
}

// get retries transport errors for a few seconds: right after a config
// load, Caddy may still be closing the old listener (EOF, reset).
func (tc *testCaddy) get(t *testing.T, url string) (int, http.Header, string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	resp, err := tc.client.Get(url)
	for err != nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		resp, err = tc.client.Get(url)
	}
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

// freePorts returns n distinct free TCP ports. It holds every listener
// until all are picked: closed one at a time, the kernel can hand out the
// same port twice, and Caddy then fails its initial config ("listener
// address repeated") and exits under the test's first load.
func freePorts(t *testing.T, n int) []int {
	t.Helper()
	ports := make([]int, n)
	for i := range ports {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = l.Close() }()
		ports[i] = l.Addr().(*net.TCPAddr).Port
	}
	return ports
}

// backend is an upstream that answers with its name.
func backend(t *testing.T, name string) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, name)
	}))
	t.Cleanup(s.Close)
	return strings.TrimPrefix(s.URL, "http://")
}

// askServer approves the given hostnames and records every question.
type askServer struct {
	url   string
	mu    sync.Mutex
	asked []string
}

func newAskServer(t *testing.T, allowed ...string) *askServer {
	t.Helper()
	a := &askServer{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d := r.URL.Query().Get("domain")
		a.mu.Lock()
		a.asked = append(a.asked, d)
		a.mu.Unlock()
		if !slices.Contains(allowed, d) {
			http.Error(w, "unknown hostname", http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	a.url = s.URL + "/internal/caddy/ask"
	return a
}

func (a *askServer) questions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.asked)
}

func TestIntegrationRouting(t *testing.T) {
	ask := newAskServer(t, "a.test", "b.test", "www.a.test")
	tc := startCaddy(t, ask.url, false)
	ctx := context.Background()

	err := tc.proxy.ApplyRoutes(ctx, []node.Route{
		{Hostnames: []string{"a.test"}, Upstream: backend(t, "A")},
		{Hostnames: []string{"b.test"}, Upstream: backend(t, "B")},
		{Hostnames: []string{"www.a.test"}, RedirectTo: "a.test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tc.trustInternalCA(t)

	for host, want := range map[string]string{"a.test": "A", "b.test": "B"} {
		if code, _, body := tc.get(t, tc.url("https", host, "/")); code != 200 || body != want {
			t.Errorf("https %s: got %d %q, want 200 %q", host, code, body, want)
		}
	}

	code, h, _ := tc.get(t, tc.url("https", "www.a.test", "/x?y=1"))
	if want := tc.url("https", "a.test", "/x?y=1"); code != 308 || h.Get("Location") != want {
		t.Errorf("www redirect: got %d %q, want 308 %q", code, h.Get("Location"), want)
	}

	code, h, _ = tc.get(t, tc.url("http", "b.test", "/p"))
	if want := tc.url("https", "b.test", "/p"); code != 308 || h.Get("Location") != want {
		t.Errorf("http->https: got %d %q, want 308 %q", code, h.Get("Location"), want)
	}

	code, h, body := tc.get(t, tc.url("http", "unknown.test", "/"))
	if code != 404 || !strings.Contains(body, "Nothing is deployed") || !strings.HasPrefix(h.Get("Content-Type"), "text/html") {
		t.Errorf("unknown host: got %d %q %q", code, h.Get("Content-Type"), body)
	}
}

func TestIntegrationRoutingWithoutTLS(t *testing.T) {
	tc := startCaddy(t, "", true)
	err := tc.proxy.ApplyRoutes(context.Background(), []node.Route{
		{Hostnames: []string{"a.test"}, Upstream: backend(t, "A")},
		{Hostnames: []string{"www.a.test"}, RedirectTo: "a.test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, _, body := tc.get(t, tc.url("http", "a.test", "/")); code != 200 || body != "A" {
		t.Errorf("a.test: got %d %q", code, body)
	}
	code, h, _ := tc.get(t, tc.url("http", "www.a.test", "/q"))
	if want := tc.url("http", "a.test", "/q"); code != 308 || h.Get("Location") != want {
		t.Errorf("www redirect: got %d %q, want %q", code, h.Get("Location"), want)
	}
	if code, _, _ := tc.get(t, tc.url("http", "nope.test", "/")); code != 404 {
		t.Errorf("unknown host: got %d, want 404", code)
	}
}

// TestIntegrationSwapUnderTraffic flips a route between two upstreams while
// concurrent clients hammer it, then swaps to a final upstream and checks
// that new requests stop reaching the old ones.
//
// Reloads are not perfectly lossless, for two upstream reasons:
//   - On Linux each Caddy config binds its own SO_REUSEPORT socket and the
//     old server shuts down on reload. Go's HTTP/1 server drops the first
//     request of a connection the old socket accepted just before shutdown
//     (net/http: "if c.server.shuttingDown() { return }"), and the kernel
//     resets connections still queued on the closed socket unless
//     net.ipv4.tcp_migrate_req=1. Clients see EOF or a reset on a fresh
//     connection.
//   - Go's HTTP/2 server only sends GOAWAY to connections registered when
//     shutdown starts. A connection whose HTTP/2 setup finishes a moment
//     later keeps serving the old config until it goes away by itself.
//
// So the test allows rare connection drops, never a bad response, and only
// requires HTTP/1.1 to stop reaching old upstreams.
func TestIntegrationSwapUnderTraffic(t *testing.T) {
	for _, proto := range []string{"HTTP/1.1", "HTTP/2.0"} {
		t.Run(proto, func(t *testing.T) {
			ask := newAskServer(t, "app.test")
			tc := startCaddy(t, ask.url, false)
			tc.client.Transport.(*http.Transport).ForceAttemptHTTP2 = proto == "HTTP/2.0"
			ctx := context.Background()
			backends := []string{backend(t, "A"), backend(t, "B")}
			routes := func(up string) []node.Route {
				return []node.Route{{Hostnames: []string{"app.test"}, Upstream: up}}
			}
			if err := tc.proxy.ApplyRoutes(ctx, routes(backends[0])); err != nil {
				t.Fatal(err)
			}
			tc.trustInternalCA(t)
			tc.get(t, tc.url("https", "app.test", "/")) // issue the certificate up front

			const workers, swaps = 32, 50
			var (
				total, dropped, bad atomic.Int64
				finalSwap           atomic.Pointer[time.Time]
				mu                  sync.Mutex
				seen                = map[string]int{}
				// lastStale is the latest start of a request that still
				// reached an old upstream after the final swap.
				lastStale time.Time
			)
			stop := make(chan struct{})
			var wg sync.WaitGroup
			for range workers {
				wg.Go(func() {
					for {
						select {
						case <-stop:
							return
						default:
						}
						start := time.Now()
						total.Add(1)
						resp, err := tc.client.Get(tc.url("https", "app.test", "/"))
						if err != nil {
							dropped.Add(1)
							continue
						}
						b, _ := io.ReadAll(resp.Body)
						_ = resp.Body.Close()
						if resp.StatusCode != 200 || resp.Proto != proto {
							bad.Add(1)
							t.Logf("bad response: %s %d %q", resp.Proto, resp.StatusCode, b)
							continue
						}
						mu.Lock()
						seen[string(b)]++
						if fs := finalSwap.Load(); fs != nil && string(b) != "C" && start.After(*fs) && start.After(lastStale) {
							lastStale = start
						}
						mu.Unlock()
					}
				})
			}

			var latencies []time.Duration
			for i := 1; i <= swaps; i++ {
				time.Sleep(50 * time.Millisecond)
				start := time.Now()
				if err := tc.proxy.ApplyRoutes(ctx, routes(backends[i%2])); err != nil {
					t.Fatalf("swap %d: %v", i, err)
				}
				latencies = append(latencies, time.Since(start))
			}
			if err := tc.proxy.ApplyRoutes(ctx, routes(backend(t, "C"))); err != nil {
				t.Fatal(err)
			}
			swapped := time.Now()
			finalSwap.Store(&swapped)
			time.Sleep(time.Second)
			close(stop)
			wg.Wait()

			slices.Sort(latencies)
			pct := func(p int) time.Duration { return latencies[len(latencies)*p/100] }
			var stale time.Duration
			if !lastStale.IsZero() {
				stale = lastStale.Sub(swapped).Round(time.Millisecond)
			}
			t.Logf("%s: %d workers, %d swaps, %d requests %v, %d connection drops, %d bad responses; reload latency p50=%v p95=%v max=%v; old upstreams still reached %v after the final swap",
				proto, workers, swaps, total.Load(), seen, dropped.Load(), bad.Load(), pct(50), pct(95), latencies[len(latencies)-1], stale)

			if bad.Load() != 0 {
				t.Errorf("%d requests got a bad response", bad.Load())
			}
			// Drops depend on machine speed (reloads take 2 ms here, 50+ ms
			// on shared CI runners). Strict mode (DOCK_STRICT_PERF=1) holds
			// the measured bound; otherwise only gross regressions fail.
			limit := total.Load() / 100
			if os.Getenv("DOCK_STRICT_PERF") == "" {
				limit = total.Load() / 5
			}
			if dropped.Load() > limit {
				t.Errorf("%d of %d requests were dropped, want at most %d", dropped.Load(), total.Load(), limit)
			}
			if seen["A"] == 0 || seen["B"] == 0 {
				t.Errorf("traffic did not reach both upstreams: %v", seen)
			}
			if proto == "HTTP/1.1" && stale > 0 {
				t.Errorf("old upstreams got new requests %v after the final swap", stale)
			}
		})
	}
}

// TestIntegrationInFlightRequestSurvivesReload checks that a slow request
// finishes on the old config and how long the reload waits for it.
func TestIntegrationInFlightRequestSurvivesReload(t *testing.T) {
	tc := startCaddy(t, "", true)
	ctx := context.Background()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = io.WriteString(w, "slow")
	}))
	t.Cleanup(slow.Close)
	if err := tc.proxy.ApplyRoutes(ctx, []node.Route{{Hostnames: []string{"slow.test"}, Upstream: strings.TrimPrefix(slow.URL, "http://")}}); err != nil {
		t.Fatal(err)
	}

	done := make(chan string, 1)
	go func() {
		resp, err := tc.client.Get(tc.url("http", "slow.test", "/"))
		if err != nil {
			done <- err.Error()
			return
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		done <- string(b)
	}()
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	if err := tc.proxy.ApplyRoutes(ctx, []node.Route{{Hostnames: []string{"other.test"}, Upstream: backend(t, "O")}}); err != nil {
		t.Fatal(err)
	}
	t.Logf("reload with a slow request in flight took %v", time.Since(start))
	if body := <-done; body != "slow" {
		t.Errorf("in-flight request got %q, want %q", body, "slow")
	}
}

func TestIntegrationInvalidConfigKeepsOldRoutes(t *testing.T) {
	tc := startCaddy(t, "", true)
	ctx := context.Background()
	if err := tc.proxy.ApplyRoutes(ctx, []node.Route{{Hostnames: []string{"a.test"}, Upstream: backend(t, "A")}}); err != nil {
		t.Fatal(err)
	}

	bad := []byte(`{"apps":{"http":{"servers":{"x":{"listen":[":1"],"routes":[{"handle":[{"handler":"no_such_handler"}]}]}}}}}`)
	err := tc.proxy.load(ctx, bad)
	if err == nil || !strings.Contains(err.Error(), "caddy rejected the config") {
		t.Fatalf("want rejection, got %v", err)
	}
	t.Logf("rejection: %v", err)

	if code, _, body := tc.get(t, tc.url("http", "a.test", "/")); code != 200 || body != "A" {
		t.Errorf("old route after rejected load: got %d %q", code, body)
	}
}

func TestIntegrationOnDemandAsk(t *testing.T) {
	ask := newAskServer(t, "ok.test")
	tc := startCaddy(t, ask.url, false)
	ctx := context.Background()
	// refused.test has a route, but the ask endpoint says no.
	err := tc.proxy.ApplyRoutes(ctx, []node.Route{
		{Hostnames: []string{"ok.test"}, Upstream: backend(t, "OK")},
		{Hostnames: []string{"refused.test"}, Upstream: backend(t, "NO")},
	})
	if err != nil {
		t.Fatal(err)
	}
	tc.trustInternalCA(t)
	tc.proxy.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.IPv4(127, 0, 0, 1)}}, nil
	}

	if code, _, body := tc.get(t, tc.url("https", "ok.test", "/")); code != 200 || body != "OK" {
		t.Errorf("ok.test: got %d %q", code, body)
	}
	if resp, err := tc.client.Get(tc.url("https", "refused.test", "/")); err == nil {
		_ = resp.Body.Close()
		t.Error("refused.test: TLS handshake succeeded, want failure")
	}
	if q := ask.questions(); !slices.Contains(q, "ok.test") || !slices.Contains(q, "refused.test") {
		t.Errorf("ask endpoint saw %v, want ok.test and refused.test", q)
	}

	dc, err := tc.proxy.CheckDomain(ctx, "ok.test")
	if err != nil || !dc.DNSOK || !dc.CertificateOK || dc.Error != "" {
		t.Errorf("CheckDomain(ok.test) = %+v, %v", dc, err)
	}
	// No certificate served looks like a pending issuance to the node; core
	// turns it into an error if it stays that way (certPatience).
	dc, err = tc.proxy.CheckDomain(ctx, "refused.test")
	if err != nil || !dc.DNSOK || dc.CertificateOK || dc.Error != "" {
		t.Errorf("CheckDomain(refused.test) = %+v, %v", dc, err)
	}
}

func TestIntegrationIdleRSS(t *testing.T) {
	ask := newAskServer(t)
	tc := startCaddy(t, ask.url, false)
	up := backend(t, "X")
	var routes []node.Route
	for i := range 50 {
		routes = append(routes, node.Route{Hostnames: []string{"app" + strconv.Itoa(i) + ".test"}, Upstream: up})
	}
	if err := tc.proxy.ApplyRoutes(context.Background(), routes); err != nil {
		t.Fatal(err)
	}
	for _, r := range routes {
		tc.get(t, tc.url("http", r.Hostnames[0], "/"))
	}
	time.Sleep(2 * time.Second)
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", tc.pid))
	if err != nil {
		t.Skipf("no /proc: %v", err)
	}
	for line := range strings.Lines(string(status)) {
		if strings.HasPrefix(line, "VmRSS:") {
			t.Logf("caddy idle RSS with %d routes: %s", len(routes), strings.Join(strings.Fields(line)[1:], " "))
		}
	}
}

// runCaddy writes opts as Caddy's config in dir and runs Caddy until its
// admin socket and HTTP port answer. portTaken reports that Caddy exited
// because one of its ports was in use; the caller retries with new ones.
func runCaddy(t *testing.T, bin, dir string, opts Options) (cmd *exec.Cmd, portTaken bool) {
	t.Helper()
	boot, err := BootstrapConfig(opts)
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "caddy.json")
	if err := os.WriteFile(cfgPath, boot, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(bin, "run", "--config", cfgPath)
	cmd.Env = append(os.Environ(),
		"HOME="+dir,
		"XDG_DATA_HOME="+filepath.Join(dir, "data"),
		"XDG_CONFIG_HOME="+filepath.Join(dir, "config"),
	)
	logPath := filepath.Join(dir, "caddy.log")
	logs, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logs.Close() }()
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()

	up := func() bool {
		for _, dial := range [][2]string{{"unix", opts.AdminSocket}, {"tcp", fmt.Sprintf("127.0.0.1:%d", opts.HTTPPort)}} {
			c, err := net.Dial(dial[0], dial[1])
			if err != nil {
				return false
			}
			_ = c.Close()
		}
		return true
	}
	deadline := time.Now().Add(10 * time.Second)
	for !up() {
		select {
		case <-exited:
			b, _ := os.ReadFile(logPath)
			if strings.Contains(string(b), "address already in use") {
				return nil, true
			}
			t.Fatalf("caddy exited at start:\n%s", b)
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			<-exited
			b, _ := os.ReadFile(logPath)
			t.Fatalf("caddy never came up:\n%s", b)
		}
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-exited:
		case <-time.After(testGracePeriod + 5*time.Second):
			t.Errorf("caddy did not exit within the grace period")
			_ = cmd.Process.Kill()
			<-exited
		}
		if t.Failed() {
			b, _ := os.ReadFile(logPath)
			t.Logf("caddy log:\n%s", b)
		}
	})
	return cmd, false
}

// TestIntegrationClientIPHeader checks that Caddy accepts the header edit
// and replaces a client's own value with its address.
func TestIntegrationClientIPHeader(t *testing.T) {
	tc := startCaddy(t, "", true)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Header.Get("X-Dock-Client-Ip"))
	}))
	t.Cleanup(s.Close)
	err := tc.proxy.ApplyRoutes(context.Background(), []node.Route{
		{Hostnames: []string{"dock.test"}, Upstream: strings.TrimPrefix(s.URL, "http://"), ClientIPHeader: "X-Dock-Client-Ip"},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, tc.url("http", "dock.test", "/"), nil)
	req.Header.Set("X-Dock-Client-Ip", "203.0.113.66")
	var resp *http.Response
	for deadline := time.Now().Add(5 * time.Second); ; {
		if resp, err = tc.client.Do(req); err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if b, _ := io.ReadAll(resp.Body); string(b) != "127.0.0.1" {
		t.Fatalf("upstream saw %q, want the client's 127.0.0.1", b)
	}
}

// Both kinds of access protection through a real Caddy: a password asks
// for basic auth; a login goes through the access check, which sends page
// loads to sign in, trades a grant for a cookie, and lets the request on
// with its body and the visitor's name, without the check's headers.
func TestIntegrationAccess(t *testing.T) {
	check := httptest.NewServer(AccessHandler())
	t.Cleanup(check.Close)
	tc := startCaddyWith(t, func(o *Options) { o.DisableTLS, o.AccessURL = true, check.URL+"/internal/caddy/access" })
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_, _ = fmt.Fprintf(w, "%s %s user=%s body=%s key=%s", r.Method, r.URL.RequestURI(), r.Header.Get(access.UserHeader), b, r.Header.Get(accessKeyHeader))
	}))
	t.Cleanup(app.Close)
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct horse battery"), 4)
	key := access.NewKey()
	upstream := strings.TrimPrefix(app.URL, "http://")
	err := tc.proxy.ApplyRoutes(context.Background(), []node.Route{
		{Hostnames: []string{"pw.test"}, Upstream: upstream, Access: &node.Access{Username: "team", PasswordHash: string(hash)}},
		{Hostnames: []string{"login.test"}, Upstream: upstream, Access: &node.Access{Key: key, LoginURL: "https://dock.test/access"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, url, body string, edit func(*http.Request)) (int, http.Header, string) {
		t.Helper()
		req, _ := http.NewRequest(method, url, strings.NewReader(body))
		if edit != nil {
			edit(req)
		}
		var resp *http.Response
		for deadline := time.Now().Add(5 * time.Second); ; {
			if resp, err = tc.client.Do(req); err == nil || time.Now().After(deadline) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, string(b)
	}

	// Password.
	if code, h, _ := do("GET", tc.url("http", "pw.test", "/"), "", nil); code != 401 || !strings.HasPrefix(h.Get("WWW-Authenticate"), "Basic") {
		t.Fatalf("no password: %d %v", code, h)
	}
	if code, _, _ := do("GET", tc.url("http", "pw.test", "/"), "", func(r *http.Request) { r.SetBasicAuth("team", "wrong password!!") }); code != 401 {
		t.Fatalf("wrong password: %d", code)
	}
	code, _, body := do("GET", tc.url("http", "pw.test", "/x"), "", func(r *http.Request) {
		r.SetBasicAuth("team", "correct horse battery")
		r.Header.Set(access.UserHeader, "eve")
	})
	if code != 200 || body != "GET /x user=team body= key=" {
		t.Fatalf("right password: %d %q", code, body)
	}

	// Login: a page load goes to sign in, a script gets a 401.
	nav := func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "navigate") }
	if code, h, _ := do("GET", tc.url("http", "login.test", "/orders"), "", nav); code != 302 || h.Get("Location") != "https://dock.test/access?host=login.test&to=%2Forders" {
		t.Fatalf("page load: %d %q", code, h.Get("Location"))
	}
	if code, _, _ := do("POST", tc.url("http", "login.test", "/api"), "x", nil); code != 401 {
		t.Fatalf("script: %d", code)
	}
	grant := access.Sign(key, access.Grant, "login.test", "ada@example.com", time.Now().Add(access.GrantTTL))
	code, h, _ := do("GET", tc.url("http", "login.test", access.CallbackPath+"?grant="+grant+"&to=%2Forders"), "", nav)
	cookie := h.Get("Set-Cookie")
	if code != 302 || h.Get("Location") != "/orders" || !strings.HasPrefix(cookie, accessCookie+"=") {
		t.Fatalf("callback: %d %v", code, h)
	}
	code, _, body = do("POST", tc.url("http", "login.test", "/api?q=1"), `{"n":1}`, func(r *http.Request) {
		r.Header.Set("Cookie", strings.SplitN(cookie, ";", 2)[0])
		r.Header.Set(access.UserHeader, "eve")
		r.Header.Set(accessKeyHeader, "forged")
	})
	if code != 200 || body != `POST /api?q=1 user=ada@example.com body={"n":1} key=` {
		t.Fatalf("signed in: %d %q", code, body)
	}
}

// Caddy accepts the traffic log config and sends a line per request of a
// counted route to the counter, and none for other hostnames.
func TestIntegrationTraffic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	port := freePorts(t, 1)[0]
	counter, err := traffic.Listen(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	tc := startCaddyWith(t, func(o *Options) {
		o.DisableTLS, o.TrafficAddr = true, net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	})
	up := backend(t, "app")
	err = tc.proxy.ApplyRoutes(ctx, []node.Route{
		{Hostnames: []string{"counted.test"}, Upstream: up, Traffic: true},
		{Hostnames: []string{"quiet.test"}, Upstream: up},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		req, _ := http.NewRequest(http.MethodGet, tc.url("http", "counted.test", "/items/7?x=1"), nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 test")
		resp, err := tc.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	tc.get(t, tc.url("http", "quiet.test", "/"))

	var got []node.HostTraffic
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		d, _ := counter.DrainTraffic(ctx)
		got = append(got, d...)
		if len(got) > 0 && got[0].Requests == 3 {
			break
		}
	}
	if len(got) != 1 || got[0].Host != "counted.test" || got[0].Requests != 3 || got[0].Status[0] != 3 {
		t.Fatalf("counted %+v; want 3 requests to counted.test", got)
	}
	if p := got[0].Paths; len(p) != 1 || p[0].Path != "/items/:id" || p[0].Method != "GET" {
		t.Errorf("paths = %+v", p)
	}
	var s traffic.Sketch
	if err := s.UnmarshalBinary(got[0].Visitors); err != nil || s.Estimate() != 1 {
		t.Errorf("visitors = %d, %v; want 1 (the user agent reached the counter)", s.Estimate(), err)
	}
}

// TestIntegrationTunnelListener runs a direct Caddy with a tunnel listener
// (ADR 0036): the listener serves every route over plain HTTP, without
// redirecting to HTTPS, while a tunnel-only route isn't on the public
// listeners at all.
func TestIntegrationTunnelListener(t *testing.T) {
	ask := newAskServer(t, "a.test")
	tunnelPort := freePorts(t, 1)[0]
	tc := startCaddyWith(t, func(o *Options) {
		o.AskURL, o.TunnelPort, o.ServerMarker = ask.url, tunnelPort, "srv-1"
	})
	err := tc.proxy.ApplyRoutes(context.Background(), []node.Route{
		{Hostnames: []string{"a.test"}, Upstream: backend(t, "A")},
		{Hostnames: []string{"t.test"}, Upstream: backend(t, "T"), TunnelOnly: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	viaTunnel := func(host string) string { return fmt.Sprintf("http://%s:%d/", host, tunnelPort) }
	for host, want := range map[string]string{"a.test": "A", "t.test": "T"} {
		if code, h, body := tc.get(t, viaTunnel(host)); code != 200 || body != want || h.Get(MarkerHeader) != "srv-1" {
			t.Errorf("tunnel %s: got %d %q (marker %q), want 200 %q", host, code, body, h.Get(MarkerHeader), want)
		}
	}
	if code, _, _ := tc.get(t, tc.url("http", "t.test", "/")); code != 404 {
		t.Errorf("public http t.test: got %d, want 404 (tunnel only)", code)
	}
	if code, _, _ := tc.get(t, tc.url("http", "a.test", "/")); code != 308 {
		t.Errorf("public http a.test: got %d, want the redirect to https", code)
	}
}

// Analytics through a real Caddy (ADR 0037): /_dock/a/* reaches the
// ingest with the route's tag and the client's address, whatever the
// visitor sent in those headers, the app gets the rest, and only the
// app's requests are counted as traffic.
func TestIntegrationAnalytics(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	port := freePorts(t, 1)[0]
	counter, err := traffic.Listen(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ingest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "ingest %s %s tag=%s ip=%s", r.Method, r.URL.Path, r.Header.Get(analyticsTagHeader), r.Header.Get(analyticsIPHeader))
	}))
	t.Cleanup(ingest.Close)
	tc := startCaddyWith(t, func(o *Options) {
		o.DisableTLS = true
		o.TrafficAddr = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		o.AnalyticsAddr = strings.TrimPrefix(ingest.URL, "http://")
	})
	err = tc.proxy.ApplyRoutes(ctx, []node.Route{{
		Hostnames: []string{"shop.test", "admin.test"}, Upstream: backend(t, "app"), Traffic: true,
		Analytics: []node.AnalyticsHosts{{Hostnames: []string{"shop.test"}, Tag: "3.40.981"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, tc.url("http", "shop.test", "/_dock/a/e"), strings.NewReader(`{}`))
	req.Header.Set(analyticsTagHeader, "1.1.1")
	req.Header.Set(analyticsIPHeader, "203.0.113.66")
	req.Header.Set("User-Agent", "Mozilla/5.0 test")
	resp, err := tc.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(b) != "ingest POST /_dock/a/e tag=3.40.981 ip=127.0.0.1" {
		t.Errorf("the ingest got %q", b)
	}
	// Not an analytics hostname: the app answers.
	if _, _, body := tc.get(t, tc.url("http", "admin.test", "/_dock/a/s.js")); !strings.HasPrefix(body, "app") {
		t.Errorf("admin.test /_dock/a/s.js answered %q, want the app", body)
	}
	if _, _, body := tc.get(t, tc.url("http", "shop.test", "/")); !strings.HasPrefix(body, "app") {
		t.Errorf("shop.test / answered %q, want the app", body)
	}

	var got []node.HostTraffic
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		d, _ := counter.DrainTraffic(ctx)
		got = append(got, d...)
		if len(got) >= 2 {
			break
		}
	}
	for _, h := range got {
		if h.Host == "shop.test" && h.Requests != 1 {
			t.Errorf("shop.test counted %d requests; the analytics one must not count", h.Requests)
		}
	}
}
