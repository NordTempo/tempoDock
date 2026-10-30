package remote

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime"
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

// testFacts is what the test node reports in hello.
var testFacts = Facts{
	DockerVersion: "28.3.1", Ingress: IngressCloudflareTunnel, TunnelHostname: "6ff42ae2-765d-4adf-8112-31c55c1551ef.cfargotunnel.com",
	Builder: true, MemoryTotal: 16 << 30,
}

func TestCalls(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if st := h.hub.State(h.id); st.Status != StatusOnline || st.Server != "homelab" || st.Hello.Protocol != Protocol ||
		st.Hello.MemoryTotal != testFacts.MemoryTotal || st.Hello.TunnelHostname != testFacts.TunnelHostname || !st.Hello.Builder {
		t.Fatalf("state = %+v", st)
	}
	if rt, ok := h.hub.Runtime(h.id); !ok || rt == nil {
		t.Fatal("Runtime of an online server")
	}
	if _, ok := h.hub.Runtime(h.id + 1); ok {
		t.Fatal("Runtime of an unknown server")
	}

	var progress bytes.Buffer
	if id, err := h.client.Pull(ctx, "nginx:1", &progress); err != nil || !strings.HasPrefix(id, "sha256:") {
		t.Fatalf("pull = %q, %v", id, err)
	}
	if !strings.Contains(progress.String(), "Pulled nginx:1") {
		t.Fatalf("pull progress = %q", progress.String())
	}
	if _, err := h.client.Pull(ctx, "127.0.0.1:5000/1/2@sha256:abc", io.Discard, node.PullOptions{Token: "pull-token"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(h.rt.Ops(), "pull 127.0.0.1:5000/1/2@sha256:abc token=pull-token") {
		t.Fatalf("the pull token didn't reach the node: %v", h.rt.Ops())
	}
	if ok, err := h.client.HasImage(ctx, "nginx:1"); err != nil || !ok {
		t.Fatalf("has image = %v, %v", ok, err)
	}
	if ok, err := h.client.HasImage(ctx, "nope:1"); err != nil || ok {
		t.Fatalf("has unknown image = %v, %v", ok, err)
	}
	if err := h.client.EnsureNetwork(ctx, "env-1", node.Labels{Project: "1", Environment: "2"}); err != nil {
		t.Fatal(err)
	}
	if err := h.client.EnsureNetwork(ctx, "env-1", node.Labels{Project: "1", Environment: "3"}); err == nil {
		t.Fatal("network of another environment: no error")
	}
	if err := h.client.EnsureVolume(ctx, "data", node.Labels{Service: "3"}); err != nil {
		t.Fatal(err)
	}

	spec := node.ContainerSpec{
		Name: "web-1", Image: "app:1", Port: 8080, HostPort: 41000, Network: "env-1", Aliases: []string{"web"},
		Env: map[string]string{"A": "1"}, Cmd: "serve", Mounts: []node.Mount{{Volume: "data", Path: "/data"}},
		Resources: node.Resources{MemoryBytes: 256 << 20, NanoCPUs: 5e8},
		Labels:    node.Labels{ControlPlane: "cp1", Project: "1", Environment: "2", Service: "3", Release: "7"},
	}
	ct, err := h.client.Start(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := h.rt.Spec("web-1"); !ok || !equalJSON(got, spec) {
		t.Fatalf("spec on the node = %+v", got)
	}
	list, err := h.client.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != ct.ID || list[0].Labels.Release != "7" || list[0].HostPort == 0 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if ids, err := h.client.ReleaseContainers(ctx, spec.Labels); err != nil || !slices.Equal(ids, []string{ct.ID}) {
		t.Fatalf("release containers = %v, %v", ids, err)
	}
	other := spec.Labels
	other.ControlPlane = "cp2"
	if ids, err := h.client.ReleaseContainers(ctx, other); err != nil || len(ids) != 0 {
		t.Fatalf("another control plane's release containers = %v, %v", ids, err)
	}
	if err := h.client.HealthCheck(ctx, ct, node.HealthCheck{Path: "/", Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	h.rt.AddLogs("web-1", "one", "two")
	lines, err := h.client.Logs(ctx, ct.ID, node.LogOptions{Tail: 1})
	if err != nil {
		t.Fatal(err)
	}
	if l := recv(t, lines); l.Text != "two" || l.Stream != "stdout" || l.Time.IsZero() {
		t.Fatalf("log line = %+v", l)
	}
	waitClosed(t, lines)
	if err := h.client.Stop(ctx, ct.ID, time.Second); err != nil {
		t.Fatal(err)
	}
	if got, err := h.client.Inspect(ctx, ct.ID); err != nil || got.State != node.StateExited || got.Name != "web-1" {
		t.Fatalf("inspect after stop = %+v, %v", got, err)
	}
	if err := h.client.Remove(ctx, ct.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.client.RemoveNetwork(ctx, "env-1", node.Labels{Project: "1", Environment: "2"}); err != nil {
		t.Fatal(err)
	}
	if err := h.client.RemoveNetwork(ctx, "env-1", node.Labels{Project: "1", Environment: "2"}); !errors.Is(err, node.ErrNotFound) {
		t.Fatalf("removing a removed network = %v, want ErrNotFound", err)
	}
	if _, err := h.client.PruneImages(ctx, node.PruneImagesRequest{Keep: []string{"sha256:a"}, Repos: []string{"127.0.0.1:5000/dock/p"}}); err != nil ||
		!slices.Contains(h.rt.Ops(), "prune images keep=sha256:a repos=127.0.0.1:5000/dock/p") {
		t.Fatalf("prune images: %v, node ops %v", err, h.rt.Ops())
	}

	// Node errors keep their meaning across the connection.
	_, err = h.client.Inspect(ctx, "nope")
	if !errors.Is(err, node.ErrNotFound) || errors.Is(err, ErrOffline) {
		t.Fatalf("inspect unknown = %v, want ErrNotFound", err)
	}

	routes := []node.Route{{Hostnames: []string{"web.example.com"}, Upstream: "127.0.0.1:40001"}, {Hostnames: []string{"www.example.com"}, RedirectTo: "web.example.com"}}
	if err := h.client.ApplyRoutes(ctx, routes); err != nil || !equalJSON(h.rt.Routes(), routes) {
		t.Fatalf("apply routes: %v, node has %v", err, h.rt.Routes())
	}
	want := node.DomainCheck{ExpectedIPs: []string{"1.2.3.4"}, ResolvedIPs: []string{"5.6.7.8"}, Error: "points elsewhere"}
	h.rt.SetDomainCheck(func(string) node.DomainCheck { return want })
	if dc, err := h.client.CheckDomain(ctx, "web.example.com"); err != nil || !equalJSON(dc, want) {
		t.Fatalf("check domain = %+v, %v", dc, err)
	}

	hs := node.HostStats{CPUBusy: 1, CPUTotal: 2, CPUs: 8, MemoryTotal: 16 << 30, MemoryAvailable: 3, Load1: 0.5, Disks: []node.Disk{{Paths: []string{"/var/lib/dock"}, Total: 100, Used: 40}}}
	h.rt.SetHostStats(hs)
	if got, err := h.client.HostStats(ctx); err != nil || !equalJSON(got, hs) {
		t.Fatalf("host stats = %+v, %v", got, err)
	}
	db := h.startService("db-1")
	sampled := time.Now().UTC().Truncate(time.Second)
	h.rt.SetContainerStats(node.ContainerStats{ID: db.ID, Time: sampled, CPUUsec: 1500, Memory: 64 << 20, MemoryLimit: 128 << 20, IORead: 1, IOWrite: 2})
	cs, err := h.client.ContainerStats(ctx, []string{db.ID, "gone"})
	if err != nil || len(cs) != 1 || cs[0].CPUUsec != 1500 || cs[0].MemoryLimit != 128<<20 || cs[0].IOWrite != 2 || !cs[0].Time.Equal(sampled) {
		t.Fatalf("container stats = %+v, %v", cs, err)
	}

	tr := node.HostTraffic{Host: "web.example.com", Requests: 3, Status: [4]uint64{3}, Duration: time.Second, Visitors: []byte{1, 0, 7, 2}, Paths: []node.PathCount{{Method: "GET", Path: "/", Requests: 3}}}
	h.rt.AddTraffic(tr)
	if got, err := h.client.DrainTraffic(ctx); err != nil || len(got) != 1 || !equalJSON(got[0], tr) {
		t.Fatalf("drain traffic = %+v, %v", got, err)
	}

	du := node.DiskUsage{
		ImagesRunning: 1, ImagesKept: 2, ImagesReclaimable: 3, ImagesOther: 4, ImagesShared: 5,
		Volumes: []node.VolumeUsage{{Name: "dock_v1", Service: "7", Bytes: 6}}, OtherVolumes: 8, Containers: 9, DataDir: 10,
	}
	h.rt.SetDiskUsage(du)
	if got, err := h.client.DiskUsage(ctx, node.PruneImagesRequest{Keep: []string{"sha256:k"}, Repos: []string{"r"}}); err != nil || !equalJSON(got, du) {
		t.Fatalf("disk usage = %+v, %v", got, err)
	}
	if ops := h.rt.Ops(); ops[len(ops)-1] != "disk usage keep=sha256:k repos=r" {
		t.Fatalf("the prune filter didn't reach the node: %v", ops[len(ops)-1])
	}

	tunnel := node.TunnelSpec{Token: "tunnel-token", Hostname: "6ff42ae2-765d-4adf-8112-31c55c1551ef.cfargotunnel.com"}
	if err := h.client.EnsureTunnel(ctx, &tunnel); err != nil || h.rt.Tunnel() == nil || *h.rt.Tunnel() != tunnel {
		t.Fatalf("ensure tunnel: %v, node runs %+v", err, h.rt.Tunnel())
	}
	if err := h.client.EnsureTunnel(ctx, nil); err != nil || h.rt.Tunnel() != nil {
		t.Fatalf("stop tunnel: %v, node runs %+v", err, h.rt.Tunnel())
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
		id, err := h.client.Build(context.Background(), node.BuildRequest{
			Source: src, Tag: "app:1", CacheScope: "p1_e2", Logs: logs,
			Push: &node.PushTarget{Ref: "127.0.0.1:5000/1/2:e-3", Token: "push-token"},
		})
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
	if !strings.Contains(logs.String(), fmt.Sprintf("received %d bytes for p1_e2, push 127.0.0.1:5000/1/2:e-3 with push-token", len(payload))) {
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
	h.emitUntilReceived(events, ct.ID) // subscribed
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

	// The node reconnects on its own and calls work again; core heard
	// offline, then online.
	waitFor(t, "reconnect", func() bool { return h.connects.Load() == 2 && h.client.Online() })
	if _, err := h.client.List(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.statuses(); !slices.Equal(got, []Status{StatusOnline, StatusOffline, StatusOnline}) {
		t.Fatalf("status changes = %v", got)
	}

	// The Events channel survived the drop: it says events may have been
	// missed, then follows the new connection.
	if ev := recv(t, events); ev.Action != ActionResync || ev.ContainerID != "" {
		t.Fatalf("first event after reconnect = %+v, want a resync", ev)
	}
	h.emitUntilReceived(events, ct.ID)
}

func TestOffline(t *testing.T) {
	h := newHarness(t)
	h.stopNode()
	waitFor(t, "disconnect", func() bool { return !h.client.Online() })
	if _, err := h.client.List(context.Background()); !errors.Is(err, ErrOffline) || !strings.Contains(err.Error(), "homelab") {
		t.Fatalf("list while offline = %v", err)
	}
	if _, ok := h.hub.Runtime(h.id); ok {
		t.Fatal("Runtime of an offline server")
	}
	if st := h.hub.State(h.id); st.Status != StatusOffline || st.Server != "homelab" {
		t.Fatalf("state = %+v", st)
	}
	if h.disconnects.Load() != 1 {
		t.Fatalf("offline reported %d times", h.disconnects.Load())
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

func TestRevokedNodeStaysOut(t *testing.T) {
	h := newHarness(t)
	h.store.revoke(h.id)
	h.hub.Disconnect(h.id)
	waitFor(t, "disconnect", func() bool { return !h.client.Online() })
	waitFor(t, "the node to say it was revoked", func() bool { return strings.Contains(h.nodeLog.String(), "revoked") })
	time.Sleep(200 * time.Millisecond) // several reconnect attempts
	if h.client.Online() || h.connects.Load() != 1 {
		t.Fatal("revoked node reconnected")
	}
	if !strings.Contains(h.hubLog.String(), "unknown or revoked node key") {
		t.Fatalf("hub log = %s", h.hubLog.String())
	}
}

// A node revoked while its hello is still running is not let in: the
// handshake had accepted its key, but registering checks it again.
func TestRevokeDuringHello(t *testing.T) {
	h := newHarness(t)
	h.stopNode()
	waitFor(t, "disconnect", func() bool { return !h.client.Online() })
	inHello, release := make(chan struct{}, 1), make(chan struct{})
	h.startNode(func(n *Node) {
		n.Facts = func(ctx context.Context) (Facts, error) {
			signal(inHello, struct{}{})
			select {
			case <-release:
			case <-ctx.Done():
			}
			return testFacts, nil
		}
	})
	<-inHello
	h.store.revoke(h.id)
	h.hub.Disconnect(h.id) // nothing registered yet to close
	close(release)
	waitFor(t, "the hub to refuse the session", func() bool { return strings.Contains(h.hubLog.String(), "revoked during hello") })
	time.Sleep(100 * time.Millisecond) // reconnect attempts fail in the handshake
	if h.client.Online() || h.connects.Load() != 1 {
		t.Fatalf("revoked node registered: online %v, connects %d", h.client.Online(), h.connects.Load())
	}
}

// A node that accepts a call but never answers fails it after the call
// timeout instead of holding the caller; the link stays usable.
func TestCallTimeout(t *testing.T) {
	h := newHarness(t, func(o *HubOptions) { o.CallTimeout = 200 * time.Millisecond })
	ct := h.start("web-1")
	release := h.rt.HoldHealthChecks()
	defer release()
	start := time.Now()
	err := h.client.HealthCheck(context.Background(), ct, node.HealthCheck{})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "didn't answer health-check") || errors.Is(err, ErrOffline) {
		t.Fatalf("hung health check = %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("timed out after %v", d)
	}
	if _, err := h.client.List(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Only the client itself says events were missed: a resync sent by the
// node is dropped, so it can't make core re-read the server at will.
func TestNodeCantSendResync(t *testing.T) {
	h := newHarness(t)
	ct := h.start("web-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := h.client.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h.emitUntilReceived(events, ct.ID)
	h.rt.Emit(node.ContainerEvent{Action: ActionResync})
	h.rt.Emit(node.ContainerEvent{ContainerID: ct.ID, Action: "start"})
	if ev := recv(t, events); ev.Action != "start" {
		t.Fatalf("event = %+v, want the start after the dropped resync", ev)
	}
}

func TestRotateKey(t *testing.T) {
	h := newHarness(t)
	old := h.store.key(h.id)
	fp, err := h.hub.RotateKey(context.Background(), h.id)
	if err != nil {
		t.Fatal(err)
	}
	if fp == old {
		t.Fatal("same key after rotate")
	}
	// The node reconnects with the new key; its hello promotes it.
	waitFor(t, "reconnect with the new key", func() bool { return h.connects.Load() == 2 && h.client.Online() })
	waitFor(t, "promotion", func() bool { return h.store.key(h.id) == fp })
	id, err := loadIdentity(h.dataDir + "/" + NodeKeyFile)
	if err != nil || id.Fingerprint() != fp {
		t.Fatalf("node.key = %v, %v", id.Fingerprint(), err)
	}
	if _, ok, _ := h.store.NodeKey(context.Background(), old); ok {
		t.Fatal("the old key is still accepted")
	}
	if _, err := h.client.List(context.Background()); err != nil {
		t.Fatal(err)
	}

	t.Run("new key never recorded", func(t *testing.T) {
		// The node made a key, but the control plane never stored it
		// (crashed before SetNextKey): the node drops it and goes on with
		// the key it has.
		h.stopNode()
		waitFor(t, "disconnect", func() bool { return !h.client.Online() })
		cur := h.store.key(h.id)
		stray, _ := NewIdentity()
		if err := stray.save(h.dataDir + "/" + nextKeyFile); err != nil {
			t.Fatal(err)
		}
		h.startNode()
		waitFor(t, "reconnect with the old key", h.client.Online)
		if h.store.key(h.id) != cur {
			t.Fatal("key changed")
		}
		if _, err := loadIdentity(h.dataDir + "/" + nextKeyFile); err == nil {
			t.Fatal("the stray key is still there")
		}
	})
}

func TestVersions(t *testing.T) {
	t.Run("node too old for the control plane", func(t *testing.T) {
		h := newHarness(t, func(o *HubOptions) { o.MinProtocol = Protocol + 1 })
		waitFor(t, "hello", func() bool { return h.hub.State(h.id).Status == StatusIncompatible })
		if h.client.Online() {
			t.Fatal("an incompatible node is online")
		}
		if _, ok := h.hub.Runtime(h.id); ok {
			t.Fatal("Runtime of an incompatible server")
		}
		if _, err := h.client.List(context.Background()); !errors.Is(err, ErrIncompatible) {
			t.Fatalf("list = %v, want ErrIncompatible", err)
		}
		if got := h.statuses(); !slices.Equal(got, []Status{StatusIncompatible}) {
			t.Fatalf("status changes = %v", got)
		}
		if !strings.Contains(h.nodeLog.String(), "upgrade dock on this server") {
			t.Fatalf("node log = %s", h.nodeLog.String())
		}
		if !strings.Contains(h.hubLog.String(), "upgrade dock on it") {
			t.Fatalf("hub log = %s", h.hubLog.String())
		}
	})

	t.Run("different major version", func(t *testing.T) {
		h := newHarness(t)
		h.stopNode()
		h.startNode(func(n *Node) { n.alpn = "dock-node/9" })
		waitFor(t, "the node to log the mismatch", func() bool { return strings.Contains(h.nodeLog.String(), "upgrade dock") })
		waitFor(t, "the hub to log the mismatch", func() bool { return strings.Contains(h.hubLog.String(), "protocol version this control plane doesn't") })
		if h.client.Online() {
			t.Fatal("a node of another major version connected")
		}
	})
}

func TestLimits(t *testing.T) {
	h := newHarness(t)

	t.Run("big log writes arrive whole", func(t *testing.T) {
		// One write far over a frame: split into frames on the node.
		big := strings.Repeat("é", maxMessage) // 2 bytes each
		h.rt.buildLog = big
		logs := &syncBuffer{}
		if _, err := h.client.Build(context.Background(), node.BuildRequest{Source: strings.NewReader("x"), Tag: "app:1", Logs: logs}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(logs.String(), big) {
			t.Fatal("log write didn't arrive intact")
		}
	})

	t.Run("result over the limit", func(t *testing.T) {
		h.rt.AddContainer(node.Container{ID: "huge", Name: strings.Repeat("x", maxMessage)})
		_, err := h.client.Inspect(context.Background(), "huge")
		if err == nil || !strings.Contains(err.Error(), "limit") || errors.Is(err, ErrOffline) {
			t.Fatalf("inspect = %v", err)
		}
		// The link is fine.
		if _, err := h.client.HasImage(context.Background(), "x"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("control plane caps what a node sends", func(t *testing.T) {
		// A hostile node bypassing the node side's own checks.
		huge := append(bytes.Repeat([]byte("x"), maxMessage+1), '\n')
		err := read(context.Background(), &session{name: "evil"}, io.NopCloser(bytes.NewReader(huge)), nil, nil, nil)
		if !errors.Is(err, errTooLarge) {
			t.Fatalf("read = %v, want errTooLarge", err)
		}
		lr := newLineReader(strings.NewReader("{\"end\":true}\n{\"x\""))
		if line, err := lr.next(); err != nil || string(line) != `{"end":true}` {
			t.Fatalf("line = %q, %v", line, err)
		}
		if _, err := lr.next(); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("cut line = %v", err)
		}
	})

	t.Run("lists are capped", func(t *testing.T) {
		// Empty objects are 3 bytes on the wire but a whole struct each
		// once decoded: a frame under maxMessage could hold 300k.
		many := func(n int) []byte {
			return []byte(`{"containers":[` + strings.TrimSuffix(strings.Repeat("{},", n), ",") + `]}`)
		}
		var r containersResult
		if err := json.Unmarshal(many(maxListItems+1), &r); !errors.Is(err, errTooMany) {
			t.Fatalf("over the cap = %v, %d items", err, len(r.Containers))
		}
		if err := json.Unmarshal(many(maxListItems), &r); err != nil || len(r.Containers) != maxListItems {
			t.Fatalf("at the cap = %v, %d items", err, len(r.Containers))
		}
		var ids idsResult
		if err := json.Unmarshal([]byte(`{"ids":null}`), &ids); err != nil || ids.IDs != nil {
			t.Fatalf("null list = %v, %v", ids.IDs, err)
		}
	})
}

func TestNoGoroutineLeaks(t *testing.T) {
	baseline := runtime.NumGoroutine()
	t.Run("busy link", func(t *testing.T) {
		reg := newFakeRegistry(t)
		h := newHarness(t, func(o *HubOptions) { o.Registry = reg.addr })
		base, client := h.serveRegistry()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ct := h.startService("db-1")
		h.rt.DialTo(newTCPServer(t, serveEcho).addr)
		c := h.dial(ctx, ct.ID)
		defer func() { _ = c.Close() }()
		events, err := h.client.Events(ctx)
		if err != nil {
			t.Fatal(err)
		}
		h.emitUntilReceived(events, ct.ID)
		if _, err := h.client.Logs(ctx, ct.ID, node.LogOptions{Follow: true}); err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v2/shop/web/manifests/v1", nil)
		req.Header.Set("Authorization", "Bearer pull-token")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		h.proxy.cut()
		waitFor(t, "reconnect", func() bool { return h.connects.Load() == 2 })
	})
	// Everything above ended with the test: hub, node, streams, tunnels.
	waitFor(t, "goroutines to exit", func() bool { return runtime.NumGoroutine() <= baseline })
}

func TestJoinRateLimit(t *testing.T) {
	var l joinLimiter
	l.seen = map[string]joinWindow{}
	a := &net.TCPAddr{IP: net.IPv4(100, 64, 0, 9), Port: 50000}
	for range joinAttempts {
		if !l.allow(a) {
			t.Fatal("denied within the limit")
		}
	}
	if l.allow(&net.TCPAddr{IP: a.IP, Port: 50001}) {
		t.Fatal("another port of the same address got past the limit")
	}
	if !l.allow(&net.TCPAddr{IP: net.IPv4(100, 64, 0, 10), Port: 50000}) {
		t.Fatal("another address was denied")
	}
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
// cut or freeze connections. The node joins with a real token first.
type harness struct {
	t       testing.TB
	rt      *testRuntime
	store   *memStore
	hub     *Hub
	client  *Client
	proxy   *proxy
	node    *Node
	id      int64
	dataDir string
	hubLog  *syncBuffer
	nodeLog *syncBuffer

	mu          sync.Mutex
	changes     []Status
	connects    atomic.Int32
	disconnects atomic.Int32
	stopNode    func()
}

func newHarness(t testing.TB, opts ...func(*HubOptions)) *harness {
	h := &harness{t: t, rt: newTestRuntime(), store: newMemStore(), dataDir: t.TempDir(), hubLog: &syncBuffer{}, nodeLog: &syncBuffer{}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.proxy = newProxy(t, ln.Addr().String())
	o := HubOptions{
		Identity:  mustIdentity(t),
		Store:     h.store,
		Addrs:     []string{h.proxy.addr},
		OnStatus:  h.onStatus,
		KeepAlive: testKeepAlive,
		Logger:    slog.New(slog.NewTextHandler(h.hubLog, nil)),
	}
	for _, fn := range opts {
		fn(&o)
	}
	h.hub = NewHub(o)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() { _ = h.hub.Serve(ctx, ln); close(served) }()
	t.Cleanup(func() { cancel(); <-served })

	link, err := Join(context.Background(), h.mint("homelab", time.Hour), h.dataDir, testFacts)
	if err != nil {
		t.Fatal(err)
	}
	h.id = link.ServerID
	h.client = h.hub.Client(h.id)
	h.startNode()
	waitFor(t, "node to connect", func() bool { return h.hub.State(h.id).Status != StatusOffline })
	return h
}

// mint creates a join token for a server called name.
func (h *harness) mint(name string, ttl time.Duration) string {
	tok, err := h.hub.MintJoinToken()
	if err != nil {
		h.t.Fatal(err)
	}
	h.store.addToken(tok.SecretHash(), name, time.Now().Add(ttl))
	return tok.String()
}

func (h *harness) onStatus(s State) {
	h.mu.Lock()
	h.changes = append(h.changes, s.Status)
	h.mu.Unlock()
	if s.Status == StatusOffline {
		h.disconnects.Add(1)
	} else {
		h.connects.Add(1)
	}
}

func (h *harness) statuses() []Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.changes)
}

// startNode runs the joined node.
func (h *harness) startNode(opts ...func(*Node)) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	n := &Node{
		DataDir: h.dataDir, Runtime: h.rt, Facts: func(context.Context) (Facts, error) { return testFacts, nil },
		KeepAlive: testKeepAlive, MinBackoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(h.nodeLog, nil)),
	}
	for _, fn := range opts {
		fn(n)
	}
	h.node = n
	go func() {
		if err := n.Run(ctx); err != nil {
			h.t.Error(err)
		}
		close(done)
	}()
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

// emitUntilReceived emits a die event for id on the node until it arrives
// on events: events emitted before a subscription is open are lost.
func (h *harness) emitUntilReceived(events <-chan node.ContainerEvent, id string) {
	h.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		h.rt.Emit(node.ContainerEvent{ContainerID: id, Action: "die", ExitCode: 3})
		select {
		case ev := <-events:
			if ev.Action != "die" || ev.ExitCode != 3 || ev.Labels.Release != "7" {
				h.t.Fatalf("event = %+v", ev)
			}
			// Drain the duplicates of earlier attempts.
			for {
				select {
				case <-events:
				case <-time.After(50 * time.Millisecond):
					return
				}
			}
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			h.t.Fatal("no event")
		}
	}
}

// memStore is the servers and join_tokens tables in memory.
type memStore struct {
	mu        sync.Mutex
	nextID    int64
	servers   map[int64]*memServer
	tokens    map[string]*memToken
	joinCalls atomic.Int32
}

type memServer struct {
	name, key, next string
	hello           Hello
}

type memToken struct {
	name    string
	expires time.Time
	used    bool
}

func newMemStore() *memStore {
	return &memStore{servers: map[int64]*memServer{}, tokens: map[string]*memToken{}}
}

func (m *memStore) addToken(hash, name string, expires time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[hash] = &memToken{name: name, expires: expires}
}

func (m *memStore) NodeKey(_ context.Context, fp string) (NodeKey, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.servers {
		switch fp {
		case s.key:
			return NodeKey{Server: Server{id, s.name}}, true, nil
		case s.next:
			return NodeKey{Server: Server{id, s.name}, Next: true}, true, nil
		}
	}
	return NodeKey{}, false, nil
}

func (m *memStore) Join(_ context.Context, req JoinRequest) (Server, error) {
	m.joinCalls.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tokens[req.SecretHash]
	switch {
	case t == nil:
		return Server{}, ErrJoinRejected
	case t.used:
		return Server{}, fmt.Errorf("used: %w", ErrJoinRejected)
	case time.Now().After(t.expires):
		return Server{}, fmt.Errorf("expired: %w", ErrJoinRejected)
	}
	t.used = true
	m.nextID++
	m.servers[m.nextID] = &memServer{name: t.name, key: req.Fingerprint, hello: req.Hello}
	return Server{ID: m.nextID, Name: t.name}, nil
}

func (m *memStore) SetNextKey(_ context.Context, id int64, fp string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.servers[id].next = fp
	return nil
}

func (m *memStore) PromoteKey(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.servers[id]
	s.key, s.next = s.next, ""
	return nil
}

func (m *memStore) revoke(id int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.servers[id].key, m.servers[id].next = "", ""
}

func (m *memStore) key(id int64) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.servers[id].key
}

func (m *memStore) server(id int64) memServer {
	m.mu.Lock()
	defer m.mu.Unlock()
	return *m.servers[id]
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
	// buildLog is written in one piece by every build.
	buildLog string
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

func (r *testRuntime) Logs(ctx context.Context, id string, opts node.LogOptions) (<-chan node.LogLine, error) {
	if !opts.Follow {
		return r.Runtime.Logs(ctx, id, opts)
	}
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
	_, _ = io.WriteString(req.Logs, r.buildLog)
	_, _ = fmt.Fprintf(req.Logs, "received %d bytes for %s", n, req.CacheScope)
	if req.Push != nil {
		_, _ = fmt.Fprintf(req.Logs, ", push %s with %s", req.Push.Ref, req.Push.Token)
	}
	_, _ = fmt.Fprintln(req.Logs)
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

// freeze silently drops all traffic on current connections, which stay
// open; their goroutines end when the test's other side closes.
func (p *proxy) freeze() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, pp := range p.pipes {
		pp.frozen.Store(true)
	}
}

func mustIdentity(t testing.TB) Identity {
	id, err := NewIdentity()
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

// syncBuffer is a bytes.Buffer safe for concurrent writers and readers.
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

// TestCheckFacts keeps only what DNS can point at from a hello: the
// control plane shows it as records and uses the tunnel in Cloudflare
// (ADR 0036). An unknown ingress mode is refused.
func TestCheckFacts(t *testing.T) {
	const tunnel = "6ff42ae2-765d-4adf-8112-31c55c1551ef.cfargotunnel.com"
	f := Facts{Ingress: IngressDirect, PublicIPs: []string{"203.0.113.7", "home.example.com", "127.0.0.1", "0.0.0.0", "2001:db8::1"}}
	if dropped, err := checkFacts(&f); err != nil || len(dropped) != 3 || !slices.Equal(f.PublicIPs, []string{"203.0.113.7", "2001:db8::1"}) {
		t.Errorf("public IPs %v, dropped %v, %v", f.PublicIPs, dropped, err)
	}
	for hostname, want := range map[string]string{
		strings.ToUpper(tunnel[:36]) + ".cfargotunnel.com": tunnel,
		"evil.example.com":                    "",
		"x.evil.example.com.cfargotunnel.com": "",
	} {
		f := Facts{Ingress: IngressCloudflareTunnel, TunnelHostname: hostname}
		if _, err := checkFacts(&f); err != nil || f.TunnelHostname != want {
			t.Errorf("tunnel hostname %q became %q, %v", hostname, f.TunnelHostname, err)
		}
	}
	if _, err := checkFacts(&Facts{Ingress: "other"}); err == nil {
		t.Error("unknown ingress mode passed")
	}
}
