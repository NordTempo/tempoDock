//go:build integration

package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
)

const (
	httpImage  = "traefik/whoami:latest"
	shellImage = "alpine:latest"
)

// testEnv is a Runtime plus a unique name prefix. Everything created under
// the prefix is force-removed when the test ends.
type testEnv struct {
	*Runtime
	t      *testing.T
	prefix string
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	r, err := New(netip.MustParsePrefix("10.213.0.0/16"))
	if err != nil {
		t.Fatal(err)
	}
	e := &testEnv{Runtime: r, t: t, prefix: fmt.Sprintf("pt-%d", time.Now().UnixNano())}
	ctx := context.Background()
	for _, img := range []string{httpImage, shellImage} {
		if ok, _ := r.HasImage(ctx, img); !ok {
			if _, err := r.Pull(ctx, img, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(func() {
		ctx := context.Background()
		f := client.Filters{}.Add("name", e.prefix)
		list, _ := r.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
		for _, c := range list.Items {
			_, _ = r.cli.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true})
		}
		vols, _ := r.cli.VolumeList(ctx, client.VolumeListOptions{Filters: f})
		for _, v := range vols.Items {
			_, _ = r.cli.VolumeRemove(ctx, v.Name, client.VolumeRemoveOptions{})
		}
		_, _ = r.cli.NetworkRemove(ctx, e.name("net"), client.NetworkRemoveOptions{})
		_ = r.Close()
	})
	return e
}

func (e *testEnv) name(s string) string { return e.prefix + "-" + s }

func (e *testEnv) start(spec node.ContainerSpec) node.Container {
	e.t.Helper()
	c, err := e.Start(context.Background(), spec)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func ctxTimeout(t *testing.T, d time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func TestPull(t *testing.T) {
	e := newEnv(t)
	const ref = "busybox:latest"
	if had, _ := e.HasImage(context.Background(), ref); !had {
		t.Cleanup(func() { _, _ = e.cli.ImageRemove(context.Background(), ref, client.ImageRemoveOptions{}) })
	}
	var out bytes.Buffer
	id, err := e.Pull(ctxTimeout(t, 2*time.Minute), ref, &out)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("pull output:\n%s", out.String())
	if !strings.HasPrefix(id, "sha256:") {
		t.Errorf("id = %q", id)
	}
	if !strings.Contains(out.String(), "Status:") || strings.Contains(out.String(), "{") {
		t.Errorf("progress output not readable:\n%s", out.String())
	}
	if ok, err := e.HasImage(context.Background(), ref); !ok || err != nil {
		t.Errorf("HasImage = %v, %v", ok, err)
	}
	// A digest-pinned ref that is on the server isn't pulled again.
	img, err := e.cli.ImageInspect(context.Background(), ref)
	if err != nil || len(img.RepoDigests) == 0 {
		t.Fatalf("inspect %s: %v, digests %v", ref, err, img.RepoDigests)
	}
	out.Reset()
	if pinned, err := e.Pull(ctxTimeout(t, time.Minute), img.RepoDigests[0], &out); err != nil || pinned != id {
		t.Errorf("Pull(%s) = %q, %v; want %q", img.RepoDigests[0], pinned, err, id)
	}
	if !strings.Contains(out.String(), "already on the server") {
		t.Errorf("digest pull output = %q, want no pull", out.String())
	}
	if ok, err := e.HasImage(context.Background(), "dock-test/does-not-exist:nope"); ok || err != nil {
		t.Errorf("HasImage(missing) = %v, %v", ok, err)
	}
	// The caller names the image; the error must not repeat it.
	if _, err := e.Pull(ctxTimeout(t, time.Minute), "dock-test/does-not-exist:nope", nil); err == nil || strings.HasPrefix(err.Error(), "pull ") {
		t.Errorf("Pull(missing) err = %v", err)
	}
}

func TestLifecycle(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, time.Minute)

	evCtx, stopEvents := context.WithCancel(ctx)
	events, err := e.Events(evCtx)
	if err != nil {
		t.Fatal(err)
	}

	net, vol := e.name("net"), e.name("data")
	for range 2 { // idempotent
		if err := e.EnsureNetwork(ctx, net, node.Labels{Project: "p1", Environment: "e1"}); err != nil {
			t.Fatal(err)
		}
		if err := e.EnsureVolume(ctx, vol, node.Labels{Project: "p1"}); err != nil {
			t.Fatal(err)
		}
	}
	// The network gets a /24 from the pool and a dk- bridge.
	res, err := e.cli.NetworkInspect(ctx, net, client.NetworkInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg := res.Network.IPAM.Config; len(cfg) != 1 || cfg[0].Subnet.Bits() != 24 || !e.pool.Contains(cfg[0].Subnet.Addr()) {
		t.Errorf("network subnets = %v, want one /24 in %s", cfg, e.pool)
	}
	if br := res.Network.Options[BridgeNameOption]; br != bridgeName(net) {
		t.Errorf("bridge name = %q, want %q", br, bridgeName(net))
	}
	// Another environment or project can't take over the same names.
	if err := e.EnsureNetwork(ctx, net, node.Labels{Project: "p1", Environment: "e2"}); err == nil {
		t.Error("EnsureNetwork accepted another environment's network")
	}
	if err := e.EnsureVolume(ctx, vol, node.Labels{Project: "p2"}); err == nil {
		t.Error("EnsureVolume accepted another project's volume")
	}

	labels := node.Labels{Project: "p1", Environment: "e1", Service: "s1", Release: "r1"}
	spec := node.ContainerSpec{
		Name:      e.name("web"),
		Image:     httpImage,
		Labels:    labels,
		Env:       map[string]string{"WHOAMI_NAME": "platform"},
		Network:   net,
		Aliases:   []string{"web"},
		Port:      80,
		Mounts:    []node.Mount{{Volume: vol, Path: "/data"}},
		Resources: node.Resources{MemoryBytes: 64 << 20, NanoCPUs: 5e8},
	}
	c := e.start(spec)
	if c.State != node.StateRunning || c.HostPort == 0 || c.Labels != labels || c.Name != spec.Name {
		t.Fatalf("started container = %+v", c)
	}

	if err := e.HealthCheck(ctx, c, node.HealthCheck{Path: "/", Timeout: 20 * time.Second, Interval: 200 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", c.HostPort))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if _, err := e.Start(ctx, spec); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Errorf("duplicate Start err = %v", err)
	}

	list, err := e.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(list, c.ID) {
		t.Errorf("List does not include %s", c.ID)
	}

	if err := e.Stop(ctx, c.ID, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	stopped, err := e.Inspect(ctx, c.ID)
	if err != nil || stopped.State != node.StateExited {
		t.Errorf("after Stop: %+v, %v", stopped, err)
	}

	want := map[string]bool{"start": false, "die": false}
	for !want["start"] || !want["die"] {
		select {
		case ev := <-events:
			if ev.ContainerID == c.ID {
				want[ev.Action] = true
				if ev.Labels != labels {
					t.Errorf("event labels = %+v", ev.Labels)
				}
			}
		case <-ctx.Done():
			t.Fatalf("missing events: %v", want)
		}
	}
	stopEvents()
	for range events { // channel must close once ctx is done
	}

	if err := e.Remove(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Inspect(ctx, c.ID); !cerrdefs.IsNotFound(err) {
		t.Errorf("Inspect after Remove err = %v", err)
	}
	if _, err := e.cli.VolumeInspect(ctx, vol, client.VolumeInspectOptions{}); err != nil {
		t.Errorf("volume gone after Remove: %v", err)
	}
}

func TestHealthCheckCrashFailsFast(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, time.Minute)
	cases := []struct {
		name string
		spec node.ContainerSpec
		hc   node.HealthCheck
	}{
		{"grace", node.ContainerSpec{Image: shellImage, Cmd: "exit 1"}, node.HealthCheck{Grace: 20 * time.Second}},
		{"tcp", node.ContainerSpec{Image: shellImage, Cmd: "sleep 1; exit 1", Port: 8080}, node.HealthCheck{}},
		{"http", node.ContainerSpec{Image: shellImage, Cmd: "sleep 1; exit 1", Port: 8080}, node.HealthCheck{Path: "/"}},
		// Exited before Start inspected it, so it publishes no port: still
		// reported as the exit, not as a missing port.
		{"http-exits-at-once", node.ContainerSpec{Image: shellImage, Cmd: "exit 1", Port: 8080}, node.HealthCheck{Path: "/"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.spec.Name = e.name("crash-" + tc.name)
			tc.hc.Timeout = 30 * time.Second
			c := e.start(tc.spec)
			began := time.Now()
			err := e.HealthCheck(ctx, c, tc.hc)
			if err == nil || !strings.Contains(err.Error(), "container exited with code 1 during health check") {
				t.Fatalf("err = %v", err)
			}
			if d := time.Since(began); d > 5*time.Second {
				t.Errorf("took %s to notice the crash", d)
			}
		})
	}
}

// An app over its memory limit is killed (no swap to fall back on) and the
// health check says so, with the limit, rather than reporting exit 137.
func TestHealthCheckOOM(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, time.Minute)
	c := e.start(node.ContainerSpec{
		Name: e.name("oom"), Image: shellImage, Port: 8080,
		Cmd:       "sleep 1; tail /dev/zero", // buffers forever, looking for a newline
		Resources: node.Resources{MemoryBytes: 16 << 20},
	})
	err := e.HealthCheck(ctx, c, node.HealthCheck{Timeout: 30 * time.Second, Interval: 200 * time.Millisecond})
	// Kernels that don't report OOM kills to Docker (some CI runners) give
	// the exit-137 wording instead; both name the limit.
	if err == nil || !strings.Contains(err.Error(), "out of memory") || !strings.Contains(err.Error(), "limit 16 MB") {
		t.Fatalf("err = %v", err)
	}
}

// An app's container has no swap, and on systemd hosts its scope unit says
// so too, so a `systemctl daemon-reload` can't hand it unlimited swap.
func TestNoSwap(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, time.Minute)
	c := e.start(node.ContainerSpec{
		Name: e.name("noswap"), Image: shellImage,
		Cmd:       "cat /sys/fs/cgroup/memory.swap.max; sleep 600",
		Resources: node.Resources{MemoryBytes: 32 << 20},
	})
	var out string
	for deadline := time.Now().Add(10 * time.Second); out == "" && time.Now().Before(deadline); {
		time.Sleep(100 * time.Millisecond)
		lines, err := e.Logs(ctx, c.ID, node.LogOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for l := range lines {
			out = l.Text
		}
	}
	if out != "0" {
		t.Errorf("memory.swap.max = %q, want 0", out)
	}

	unit := "docker-" + c.ID + ".scope"
	prop, err := exec.CommandContext(ctx, "systemctl", "show", unit, "-p", "MemorySwapMax", "--value").Output()
	if err != nil {
		t.Skipf("systemctl show %s: %v (not a systemd host)", unit, err)
	}
	switch got := strings.TrimSpace(string(prop)); got {
	case "", "infinity":
		t.Errorf("%s MemorySwapMax = %q: a daemon-reload would give the container unlimited swap", unit, got)
	}
}

// Containers created with Docker's plain "no swap" are switched to NoSwap.
func TestUpgradeSwapLimits(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, time.Minute)
	const mem = 32 << 20
	opts := createOptions(node.ContainerSpec{
		Name: e.name("oldswap"), Image: shellImage, Cmd: "sleep 600",
		Labels:    node.Labels{Project: "p", Environment: "e", Service: "s", Release: "1"},
		Resources: node.Resources{MemoryBytes: mem},
	})
	opts.HostConfig.MemorySwap = mem
	created, err := e.cli.ContainerCreate(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}

	if n, err := e.UpgradeSwapLimits(ctx); err != nil || n < 1 {
		t.Fatalf("UpgradeSwapLimits = %d, %v", n, err)
	}
	raw, err := e.cli.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if h := raw.Container.HostConfig; h.Memory != mem || h.MemorySwap != NoSwap(mem) {
		t.Errorf("memory = %d, swap = %d", h.Memory, h.MemorySwap)
	}
}

func TestHealthCheckTCP(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, time.Minute)

	up := e.start(node.ContainerSpec{Name: e.name("tcp-up"), Image: httpImage, Port: 80})
	if err := e.HealthCheck(ctx, up, node.HealthCheck{Timeout: 10 * time.Second, Interval: 200 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}

	// docker-proxy accepts on the host port even though nothing listens.
	idle := e.start(node.ContainerSpec{Name: e.name("tcp-idle"), Image: shellImage, Cmd: "sleep 300", Port: 8080})
	err := e.HealthCheck(ctx, idle, node.HealthCheck{Timeout: 2 * time.Second, Interval: 200 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
}

// A fixed host port survives restarts, so proxy upstreams stay valid while
// the daemon is down.
func TestFixedHostPortSurvivesRestart(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, time.Minute)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	c := e.start(node.ContainerSpec{Name: e.name("fixed"), Image: httpImage, Port: 80, HostPort: port})
	if c.HostPort != port {
		t.Fatalf("HostPort = %d, want %d", c.HostPort, port)
	}
	if _, err := e.cli.ContainerRestart(ctx, c.ID, client.ContainerRestartOptions{}); err != nil {
		t.Fatal(err)
	}
	after, err := e.Inspect(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.HostPort != port {
		t.Fatalf("HostPort after restart = %d, want %d", after.HostPort, port)
	}
	if !errors.Is(notFound("container", "x"), node.ErrNotFound) {
		t.Fatal("notFound does not match node.ErrNotFound")
	}
}

func TestHealthCheckGrace(t *testing.T) {
	e := newEnv(t)
	c := e.start(node.ContainerSpec{Name: e.name("grace"), Image: shellImage, Cmd: "sleep 300"})
	err := e.HealthCheck(ctxTimeout(t, time.Minute), c, node.HealthCheck{Grace: time.Second, Timeout: 10 * time.Second, Interval: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLogs(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, time.Minute)
	c := e.start(node.ContainerSpec{
		Name:  e.name("logs"),
		Image: shellImage,
		Cmd:   "echo one; echo two >&2; i=0; while true; do i=$((i+1)); echo tick $i; sleep 0.1; done",
	})
	time.Sleep(500 * time.Millisecond)

	lines, err := e.Logs(ctx, c.ID, node.LogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var backlog []node.LogLine
	for l := range lines { // must close after the backlog
		backlog = append(backlog, l)
	}
	if len(backlog) < 3 {
		t.Fatalf("backlog = %+v", backlog)
	}
	// stdout and stderr are separate pipes, so their relative order isn't
	// guaranteed.
	if !containsLine(backlog, node.LogLine{Stream: "stdout", Text: "one"}) ||
		!containsLine(backlog, node.LogLine{Stream: "stderr", Text: "two"}) {
		t.Errorf("backlog = %+v", backlog)
	}
	for _, l := range backlog {
		if l.Time.IsZero() {
			t.Errorf("line without timestamp: %+v", l)
		}
	}

	tail, err := e.Logs(ctx, c.ID, node.LogOptions{Tail: 1})
	if err != nil {
		t.Fatal(err)
	}
	if n := countLines(tail); n != 1 {
		t.Errorf("Tail: 1 returned %d lines", n)
	}

	future, err := e.Logs(ctx, c.ID, node.LogOptions{Since: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if n := countLines(future); n != 0 {
		t.Errorf("Since future returned %d lines", n)
	}

	fctx, cancel := context.WithCancel(ctx)
	follow, err := e.Logs(fctx, c.ID, node.LogOptions{Follow: true, Since: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		select {
		case l := <-follow:
			if !strings.HasPrefix(l.Text, "tick ") {
				t.Errorf("followed line = %+v", l)
			}
		case <-ctx.Done():
			t.Fatal("no followed lines")
		}
	}
	cancel()
	done := make(chan struct{})
	go func() { countLines(follow); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("follow channel did not close after cancel")
	}
}

// ReleaseContainers finds a release's containers by label, stopped ones
// too, and only those of the asking control plane (or from before the
// control plane label).
func TestReleaseContainers(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, time.Minute)
	rel := time.Now().UnixNano() // unique on a shared Docker host
	want := node.Labels{ControlPlane: "a", Project: "p", Environment: "e", Service: "s", Release: strconv.FormatInt(rel, 10)}
	start := func(name string, l node.Labels) node.Container {
		return e.start(node.ContainerSpec{Name: e.name(name), Image: shellImage, Cmd: "sleep 300", Labels: l})
	}
	legacy, foreign, next := want, want, want
	legacy.ControlPlane, foreign.ControlPlane, next.Release = "", "b", strconv.FormatInt(rel+1, 10)
	mine, old := start("mine", want), start("legacy", legacy)
	start("foreign", foreign)
	start("next", next)
	if err := e.Stop(ctx, mine.ID, 0); err != nil {
		t.Fatal(err)
	}
	ids, err := e.ReleaseContainers(ctx, want)
	slices.Sort(ids)
	wantIDs := []string{mine.ID, old.ID}
	slices.Sort(wantIDs)
	if err != nil || !slices.Equal(ids, wantIDs) {
		t.Fatalf("ReleaseContainers = %v, %v; want %v", ids, err, wantIDs)
	}
}

func TestUnmanagedIsInvisible(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, time.Minute)

	// Unmanaged containers and platform infrastructure (role label) alike.
	for name, labels := range map[string]map[string]string{
		"foreign": nil,
		"infra":   {node.LabelManaged: "true", node.LabelRole: "test"},
	} {
		assertInvisibleContainer(t, e, ctx, name, labels)
	}

	foreignVol := e.name("foreign-vol")
	if _, err := e.cli.VolumeCreate(ctx, client.VolumeCreateOptions{Name: foreignVol}); err != nil {
		t.Fatal(err)
	}
	if err := e.EnsureVolume(ctx, foreignVol, node.Labels{}); err == nil {
		t.Error("EnsureVolume accepted an unmanaged volume")
	}
	_, err := e.Start(ctx, node.ContainerSpec{
		Name: e.name("mounts-foreign"), Image: shellImage, Cmd: "sleep 300",
		Mounts: []node.Mount{{Volume: foreignVol, Path: "/data"}},
	})
	if err == nil || !strings.Contains(err.Error(), "not managed") {
		t.Errorf("Start with unmanaged volume err = %v", err)
	}
}

func containsID(list []node.Container, id string) bool {
	for _, c := range list {
		if c.ID == id {
			return true
		}
	}
	return false
}

func containsLine(lines []node.LogLine, want node.LogLine) bool {
	for _, l := range lines {
		if l.Stream == want.Stream && l.Text == want.Text {
			return true
		}
	}
	return false
}

func countLines(ch <-chan node.LogLine) int {
	n := 0
	for range ch {
		n++
	}
	return n
}

func assertInvisibleContainer(t *testing.T, e *testEnv, ctx context.Context, name string, labels map[string]string) {
	t.Helper()
	created, err := e.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:   e.name(name),
		Config: &container.Config{Image: shellImage, Cmd: []string{"sleep", "300"}, Labels: labels},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}

	list, err := e.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if containsID(list, created.ID) {
		t.Errorf("List includes %s container", name)
	}
	if _, err := e.Inspect(ctx, created.ID); !cerrdefs.IsNotFound(err) {
		t.Errorf("Inspect err = %v", err)
	}
	if err := e.Stop(ctx, created.ID, 0); !cerrdefs.IsNotFound(err) {
		t.Errorf("Stop err = %v", err)
	}
	if err := e.Remove(ctx, created.ID); !cerrdefs.IsNotFound(err) {
		t.Errorf("Remove err = %v", err)
	}
	if _, err := e.Logs(ctx, created.ID, node.LogOptions{}); !cerrdefs.IsNotFound(err) {
		t.Errorf("Logs err = %v", err)
	}
	if st, _ := e.cli.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{}); !st.Container.State.Running {
		t.Error("unmanaged container was stopped")
	}
}
