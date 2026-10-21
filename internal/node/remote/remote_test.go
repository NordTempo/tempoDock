package remote

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/fake"
)

// testKeepAlive makes dead connections show up within a second.
const testKeepAlive = 300 * time.Millisecond

func TestCalls(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	ct, err := h.client.Start(ctx, node.ContainerSpec{
		Name: "web-1", Image: "app:1", Port: 8080, Labels: node.Labels{Project: "1", Release: "7"},
	})
	if err != nil {
		t.Fatal(err)
	}
	list, err := h.client.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != ct.ID || list[0].Labels.Release != "7" || list[0].HostPort == 0 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := h.client.HealthCheck(ctx, ct, node.HealthCheck{Path: "/"}); err != nil {
		t.Fatal(err)
	}
	if err := h.client.Stop(ctx, ct.ID, time.Second); err != nil {
		t.Fatal(err)
	}
	if got, err := h.client.Inspect(ctx, ct.ID); err != nil || got.State != node.StateExited {
		t.Fatalf("inspect after stop = %+v, %v", got, err)
	}
	if !slices.Equal(h.rt.Ops(), []string{"start web-1", "stop web-1"}) {
		t.Fatalf("ops on the node = %v", h.rt.Ops())
	}

	// Node errors keep their meaning across the connection.
	_, err = h.client.Inspect(ctx, "nope")
	if !errors.Is(err, node.ErrNotFound) || errors.Is(err, ErrOffline) {
		t.Fatalf("inspect unknown = %v, want ErrNotFound", err)
	}

	var progress bytes.Buffer
	if id, err := h.client.Pull(ctx, "nginx:1", &progress); err != nil || !strings.HasPrefix(id, "sha256:") {
		t.Fatalf("pull = %q, %v", id, err)
	}
	if !strings.Contains(progress.String(), "Pulled nginx:1") {
		t.Fatalf("pull progress = %q", progress.String())
	}

	routes := []node.Route{{Hostnames: []string{"web.example.com"}, Upstream: "127.0.0.1:40001"}}
	if err := h.client.ApplyRoutes(ctx, routes); err != nil || len(h.rt.Routes()) != 1 {
		t.Fatalf("apply routes: %v, node has %v", err, h.rt.Routes())
	}
	if info, ok := h.hub.Info("homelab"); !ok || info.Protocol != Protocol {
		t.Fatalf("hello = %+v, %v", info, ok)
	}

	h.rt.SetHostStats(node.HostStats{CPUs: 8, MemoryTotal: 16 << 30, Disks: []node.Disk{{Paths: []string{"/var/lib/dock"}, Total: 100, Used: 40}}})
	if hs, err := h.client.HostStats(ctx); err != nil || hs.CPUs != 8 || hs.MemoryTotal != 16<<30 || hs.Disks[0].Used != 40 {
		t.Fatalf("host stats = %+v, %v", hs, err)
	}
	sampled := time.Now().UTC().Truncate(time.Second)
	h.rt.SetContainerStats(node.ContainerStats{ID: ct.ID, Time: sampled, CPUUsec: 1500, Memory: 64 << 20, MemoryLimit: 128 << 20})
	cs, err := h.client.ContainerStats(ctx, []string{ct.ID, "gone"})
	if err != nil || len(cs) != 1 || cs[0].CPUUsec != 1500 || cs[0].MemoryLimit != 128<<20 || !cs[0].Time.Equal(sampled) {
		t.Fatalf("container stats = %+v, %v", cs, err)
	}
}

func TestLogsStreamAndCancel(t *testing.T) {
	h := newHarness(t)
	ct := h.start("web-1")

	ctx, cancel := context.WithCancel(context.Background())
	lines, err := h.client.Logs(ctx, ct.ID, node.LogOptions{Follow: true})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		h.rt.logs <- node.LogLine{Time: time.Now(), Stream: "stdout", Text: fmt.Sprint("line ", i)}
		if l := recv(t, lines); l.Text != fmt.Sprint("line ", i) {
			t.Fatalf("got %q", l.Text)
		}
	}

	// Canceling on the control plane ends the stream on the node.
	cancel()
	waitClosed(t, lines)
	select {
	case <-h.rt.logsEnded:
	case <-time.After(5 * time.Second):
		t.Fatal("the node kept streaming after cancel")
	}

	// Errors before the stream opens are returned directly.
	if _, err := h.client.Logs(context.Background(), "nope", node.LogOptions{}); !errors.Is(err, node.ErrNotFound) {
		t.Fatalf("logs of unknown container = %v", err)
	}
}

func TestBuildStreamsBothWays(t *testing.T) {
	h := newHarness(t)
	src, upload := io.Pipe()
	logs := &syncBuffer{}
	type result struct {
		id  string
		err error
	}
	done := make(chan result, 1)
	go func() {
		id, err := h.client.Build(context.Background(), node.BuildRequest{Source: src, Tag: "app:1", Logs: logs})
		done <- result{id, err}
	}()

	// Build output reaches the control plane while the context is still
	// uploading: the call is full duplex.
	waitFor(t, "first build log line", func() bool { return strings.Contains(logs.String(), "receiving context") })

	payload := make([]byte, 8<<20)
	_, _ = rand.Read(payload)
	if _, err := upload.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = upload.Close()
	res := <-done
	sum := sha256.Sum256(payload)
	if res.err != nil || res.id != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("build = %q, %v", res.id, res.err)
	}
	if !strings.Contains(logs.String(), fmt.Sprintf("received %d bytes", len(payload))) {
		t.Fatalf("logs = %q", logs.String())
	}

	t.Run("failure", func(t *testing.T) {
		logs := &syncBuffer{}
		_, err := h.client.Build(context.Background(), node.BuildRequest{Source: strings.NewReader("ctx"), Tag: "fail", Logs: logs})
		if err == nil || errors.Is(err, ErrOffline) || !strings.Contains(err.Error(), "exit code 1") {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(logs.String(), "step 2 failed") {
			t.Fatalf("logs = %q", logs.String())
		}
	})

	t.Run("cancel mid-upload", func(t *testing.T) {
	drain: // results of the builds above
		for {
			select {
			case err := <-h.rt.buildEnded:
				_ = err
			default:
				break drain
			}
		}
		src, upload := io.Pipe()
		defer func() { _ = upload.Close() }()
		ctx, cancel := context.WithCancel(context.Background())
		logs := &syncBuffer{}
		done := make(chan error, 1)
		go func() {
			_, err := h.client.Build(ctx, node.BuildRequest{Source: src, Tag: "app:2", Logs: logs})
			done <- err
		}()
		waitFor(t, "build to start", func() bool { return strings.Contains(logs.String(), "receiving context") })
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		select {
		case err := <-h.rt.buildEnded:
			if err == nil {
				t.Fatal("node build succeeded after cancel")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("node build still running after cancel")
		}
	})
}

func TestDropMidCallAndReconnect(t *testing.T) {
	h := newHarness(t)
	ct := h.start("web-1")
	ctx := context.Background()

	events, err := h.client.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := h.client.Logs(ctx, ct.ID, node.LogOptions{Follow: true})
	if err != nil {
		t.Fatal(err)
	}
	release := h.rt.HoldHealthChecks()
	defer release()
	hcErr := make(chan error, 1)
	go func() { hcErr <- h.client.HealthCheck(ctx, ct, node.HealthCheck{}) }()
	<-h.rt.healthStarted

	h.proxy.cut()

	// In-flight calls fail as offline, streams end, the node side is canceled.
	if err := <-hcErr; !errors.Is(err, ErrOffline) {
		t.Fatalf("health check during drop = %v, want ErrOffline", err)
	}
	waitClosed(t, lines)
	<-h.rt.logsEnded

	// The node reconnects on its own and calls work again.
	waitFor(t, "reconnect", func() bool { return h.connects.Load() == 2 && h.client.Online() })
	if _, err := h.client.List(ctx); err != nil {
		t.Fatal(err)
	}

	// The Events channel survived the drop and follows the new connection.
	deadline := time.After(5 * time.Second)
	for {
		h.rt.Emit(node.ContainerEvent{ContainerID: ct.ID, Action: "die", ExitCode: 3})
		select {
		case ev := <-events:
			if ev.Action != "die" || ev.ExitCode != 3 || ev.Labels.Release != "7" {
				t.Fatalf("event = %+v", ev)
			}
			return
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			t.Fatal("no event after reconnect")
		}
	}
}

func TestOffline(t *testing.T) {
	h := newHarness(t)
	h.stopNode()
	waitFor(t, "disconnect", func() bool { return !h.client.Online() })
	if _, err := h.client.List(context.Background()); !errors.Is(err, ErrOffline) {
		t.Fatalf("list while offline = %v", err)
	}
	if h.disconnects.Load() != 1 {
		t.Fatalf("OnDisconnect ran %d times", h.disconnects.Load())
	}
}

func TestHalfOpenConnectionIsDetected(t *testing.T) {
	h := newHarness(t)
	// Packets stop flowing but nothing closes, like a NAT entry expiring.
	h.proxy.freeze()
	start := time.Now()
	waitFor(t, "ping timeout on the control plane", func() bool { return h.disconnects.Load() == 1 })
	t.Logf("dead connection detected after %v (keepalive %v)", time.Since(start).Round(time.Millisecond), testKeepAlive)
	// The node noticed too and came back on a fresh connection.
	waitFor(t, "reconnect", func() bool { return h.connects.Load() == 2 && h.client.Online() })
}

func TestAuthentication(t *testing.T) {
	h := newHarness(t)

	t.Run("revoked node stays out", func(t *testing.T) {
		h.revoked.Store(true)
		h.hub.Disconnect("homelab")
		waitFor(t, "disconnect", func() bool { return !h.client.Online() })
		time.Sleep(300 * time.Millisecond) // several reconnect attempts
		if h.client.Online() {
			t.Fatal("revoked node reconnected")
		}
		h.revoked.Store(false)
		waitFor(t, "reconnect after un-revoke", h.client.Online)
	})

	t.Run("node refuses an impostor control plane", func(t *testing.T) {
		h.stopNode()
		waitFor(t, "disconnect", func() bool { return !h.client.Online() })
		other := mustIdentity(t, "impostor")
		h.startNode(Fingerprint(other.Leaf))
		time.Sleep(300 * time.Millisecond)
		if h.client.Online() {
			t.Fatal("node connected to a control plane it didn't pin")
		}
	})
}

// BenchmarkLogLineLatency measures one log line from the node's runtime to
// the control plane's channel, over loopback TLS.
func BenchmarkLogLineLatency(b *testing.B) {
	h := newHarness(b)
	ct := h.start("web-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lines, err := h.client.Logs(ctx, ct.ID, node.LogOptions{Follow: true})
	if err != nil {
		b.Fatal(err)
	}
	var samples []time.Duration
	for b.Loop() {
		t0 := time.Now()
		h.rt.logs <- node.LogLine{Time: t0, Stream: "stdout", Text: "GET /healthz 200 1ms"}
		<-lines
		samples = append(samples, time.Since(t0))
	}
	slices.Sort(samples)
	b.ReportMetric(float64(samples[len(samples)/2].Microseconds()), "p50-µs")
	b.ReportMetric(float64(samples[len(samples)*99/100].Microseconds()), "p99-µs")
}

// BenchmarkBuildUpload measures build context throughput over loopback TLS.
func BenchmarkBuildUpload(b *testing.B) {
	h := newHarness(b)
	payload := make([]byte, 64<<20)
	b.SetBytes(int64(len(payload)))
	for b.Loop() {
		if _, err := h.client.Build(context.Background(), node.BuildRequest{Source: bytes.NewReader(payload), Tag: "app:1", Logs: io.Discard}); err != nil {
			b.Fatal(err)
		}
	}
}

// harness runs a hub and a node over loopback TCP through a proxy that can
// cut or freeze connections.
type harness struct {
	t      testing.TB
	rt     *testRuntime
	hub    *Hub
	client *Client
	proxy  *proxy
	nodeID tls.Certificate

	revoked     atomic.Bool
	connects    atomic.Int32
	disconnects atomic.Int32
	stopNode    func()
}

func newHarness(t testing.TB) *harness {
	cpID, nodeID := mustIdentity(t, "control-plane"), mustIdentity(t, "homelab")
	h := &harness{t: t, rt: newTestRuntime(), nodeID: nodeID}
	h.hub = NewHub(HubOptions{
		Identity: cpID,
		Authorize: func(fp string) (string, bool) {
			return "homelab", fp == Fingerprint(nodeID.Leaf) && !h.revoked.Load()
		},
		OnConnect:    func(string, Info) { h.connects.Add(1) },
		OnDisconnect: func(string) { h.disconnects.Add(1) },
		KeepAlive:    testKeepAlive,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = h.hub.Serve(ctx, ln) }()
	h.proxy = newProxy(t, ln.Addr().String())
	h.client = h.hub.Client("homelab")
	h.startNode(Fingerprint(cpID.Leaf))
	waitFor(t, "node to connect", h.client.Online)
	return h
}

// startNode runs a node that pins the given control plane fingerprint.
func (h *harness) startNode(pin string) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	n := &Node{
		Addr: h.proxy.addr, Identity: h.nodeID, ControlPlane: pin, Runtime: h.rt,
		KeepAlive: testKeepAlive, MinBackoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
	}
	go func() { _ = n.Run(ctx); close(done) }()
	h.stopNode = sync.OnceFunc(func() { cancel(); <-done })
	h.t.Cleanup(h.stopNode)
}

// start starts a container on the node through the client.
func (h *harness) start(name string) node.Container {
	ct, err := h.client.Start(context.Background(), node.ContainerSpec{Name: name, Image: "app:1", Labels: node.Labels{Release: "7"}})
	if err != nil {
		h.t.Fatal(err)
	}
	return ct
}

// testRuntime is the fake runtime plus logs that follow (fed by the test),
// a Build that logs while it reads its context, and signals for the node
// side of canceled calls.
type testRuntime struct {
	*fake.Runtime
	logs          chan node.LogLine
	logsEnded     chan struct{}
	buildEnded    chan error
	healthStarted chan struct{}
}

func newTestRuntime() *testRuntime {
	return &testRuntime{
		Runtime:       fake.New(),
		logs:          make(chan node.LogLine),
		logsEnded:     make(chan struct{}, 16),
		buildEnded:    make(chan error, 16),
		healthStarted: make(chan struct{}, 16),
	}
}

func (r *testRuntime) Logs(ctx context.Context, id string, _ node.LogOptions) (<-chan node.LogLine, error) {
	if _, err := r.Inspect(ctx, id); err != nil {
		return nil, err
	}
	out := make(chan node.LogLine)
	go func() {
		defer close(out)
		defer signal(r.logsEnded, struct{}{})
		for {
			select {
			case l := <-r.logs:
				select {
				case out <- l:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (r *testRuntime) HealthCheck(ctx context.Context, c node.Container, hc node.HealthCheck) error {
	signal(r.healthStarted, struct{}{})
	return r.Runtime.HealthCheck(ctx, c, hc)
}

func (r *testRuntime) Build(_ context.Context, req node.BuildRequest) (id string, err error) {
	defer func() { signal(r.buildEnded, err) }()
	_, _ = fmt.Fprintln(req.Logs, "receiving context")
	sum := sha256.New()
	n, err := io.Copy(sum, req.Source)
	if err != nil {
		return "", err
	}
	if req.Tag == "fail" {
		_, _ = fmt.Fprintln(req.Logs, "step 2 failed")
		return "", errors.New("RUN npm ci: exit code 1")
	}
	_, _ = fmt.Fprintf(req.Logs, "received %d bytes\n", n)
	return "sha256:" + hex.EncodeToString(sum.Sum(nil)), nil
}

// signal sends v if ch has room, so benchmarks that never read don't block.
func signal[T any](ch chan T, v T) {
	select {
	case ch <- v:
	default:
	}
}

// proxy forwards TCP connections and can break them on demand.
type proxy struct {
	addr  string
	mu    sync.Mutex
	pipes []*pipe
}

type pipe struct {
	a, b   net.Conn
	frozen atomic.Bool
}

func newProxy(t testing.TB, upstream string) *proxy {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &proxy{addr: ln.Addr().String()}
	t.Cleanup(func() { _ = ln.Close(); p.cut() })
	go func() {
		for {
			a, err := ln.Accept()
			if err != nil {
				return
			}
			b, err := net.Dial("tcp", upstream)
			if err != nil {
				_ = a.Close()
				continue
			}
			pp := &pipe{a: a, b: b}
			p.mu.Lock()
			p.pipes = append(p.pipes, pp)
			p.mu.Unlock()
			go pp.copy(a, b)
			go pp.copy(b, a)
		}
	}()
	return p
}

// copy forwards src to dst, or swallows the bytes while frozen.
func (pp *pipe) copy(dst, src net.Conn) {
	buf := make([]byte, 64<<10)
	for {
		n, err := src.Read(buf)
		if err != nil {
			_ = dst.Close()
			_ = src.Close()
			return
		}
		if !pp.frozen.Load() {
			if _, err := dst.Write(buf[:n]); err != nil {
				_ = src.Close()
				return
			}
		}
	}
}

// cut closes every current connection, like a network drop with RSTs.
func (p *proxy) cut() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, pp := range p.pipes {
		_ = pp.a.Close()
		_ = pp.b.Close()
	}
	p.pipes = nil
}

// freeze silently drops all traffic on current connections, which stay open.
func (p *proxy) freeze() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, pp := range p.pipes {
		pp.frozen.Store(true)
	}
	p.pipes = nil
}

func mustIdentity(t testing.TB, name string) tls.Certificate {
	id, err := NewIdentity(name)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func waitFor(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func recv[T any](t testing.TB, ch <-chan T) T {
	t.Helper()
	select {
	case v, ok := <-ch:
		if !ok {
			t.Fatal("channel closed")
		}
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out receiving")
	}
	panic("unreachable")
}

func waitClosed[T any](t testing.TB, ch <-chan T) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("channel still open")
		}
	}
}

// syncBuffer is a bytes.Buffer safe for one writer and concurrent readers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
