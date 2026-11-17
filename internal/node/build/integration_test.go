//go:build integration

package build

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node"
	dockerrt "github.com/NordTempo/tempoDock/internal/node/docker"
	"github.com/NordTempo/tempoDock/internal/node/probe"
)

const (
	serverJS = `require("http").createServer((req, res) => res.end("hello " + (process.env.GREETING || "railpack")))
  .listen(process.env.PORT || 3000);
`
	packageJSON = `{"name": "hello", "version": "1.0.0", "scripts": {"start": "node server.js"}}`
	dockerfile  = `FROM node:22-alpine
WORKDIR /app
COPY . .
ARG GREETING=default
ENV GREETING=$GREETING
CMD ["node", "server.js"]
`
)

func dockerClient(t *testing.T) *client.Client {
	t.Helper()
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	return docker
}

// testContainer is this test run's BuildKit container, so parallel runs on
// one Docker host (and a real dock-buildkit there) don't recreate each
// other's. testCache is its cache volume. Both are removed after the run.
var (
	testContainer = fmt.Sprintf("dock-test-%d-buildkit", os.Getpid())
	testCache     = testContainer + "-cache"
)

func init() {
	afterTests = append(afterTests, func() {
		docker, err := client.New(client.FromEnv)
		if err != nil {
			return
		}
		defer func() { _ = docker.Close() }()
		ctx := context.Background()
		_, _ = docker.ContainerRemove(ctx, testContainer, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		_, _ = docker.VolumeRemove(ctx, testCache, client.VolumeRemoveOptions{Force: true})
	})
}

func newBuilder(t *testing.T, docker *client.Client, memoryMB int) *Builder {
	t.Helper()
	cfg := config.Default().BuildKit
	cfg.MemoryMB = memoryMB
	b := New(docker, cfg, t.TempDir())
	b.name, b.cacheVolume = testContainer, testCache
	b.Owner = testContainer
	withRelay(t, b)
	return b
}

// withRelay gives b the relay, so BuildKit pushes itself, when this test
// binary embeds dock-probe (`make probe`, which make test-integration
// runs). Without it, Docker pushes. The directories outlive each test:
// tests share the BuildKit container that mounts them.
func withRelay(t *testing.T, b *Builder) {
	t.Helper()
	relayOnce.Do(func() {
		dir, err := os.MkdirTemp("", "dock-relay-")
		if err != nil {
			relayErr = err
			return
		}
		afterTests = append(afterTests, func() { _ = os.RemoveAll(dir) })
		relayProbe, relayErr = probe.Install(dir)
		relaySocks = filepath.Join(dir, "relay")
	})
	if relayErr != nil {
		t.Logf("no relay, Docker pushes: %v", relayErr)
		return
	}
	b.Probe, b.RelayDir = relayProbe, relaySocks
}

// The relay's directories for every test's builder (withRelay).
var (
	relayOnce              sync.Once
	relayProbe, relaySocks string
	relayErr               error
)

// labelled fails the test unless image id carries node.LabelImage, which
// disk pruning needs to tell the platform's images from others.
func labelled(t *testing.T, docker *client.Client, id string) {
	t.Helper()
	img, err := docker.ImageInspect(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if img.Config == nil || img.Config.Labels[node.LabelImage] != "true" {
		t.Errorf("image %s has no %s label: %+v", id, node.LabelImage, img.Config)
	}
}

// sourceTar packs files (path -> content) into a gzipped tar stream.
func sourceTar(t *testing.T, files map[string]string) io.Reader {
	t.Helper()
	var entries []entry
	for _, name := range slices.Sorted(maps.Keys(files)) {
		entries = append(entries, entry{name: name, body: files[name]})
	}
	return tarball(t, true, entries...)
}

// build runs a build and removes the image when the test ends.
func build(t *testing.T, b *Builder, req node.BuildRequest) (string, string, error) {
	t.Helper()
	built, logs, err := buildFull(t, b, req)
	return built.ID, logs, err
}

// buildFull is build with the whole result.
func buildFull(t *testing.T, b *Builder, req node.BuildRequest) (node.Built, string, error) {
	t.Helper()
	var logs bytes.Buffer
	req.Logs = io.MultiWriter(&logs, testWriter{t})
	built, err := b.Build(t.Context(), req)
	t.Cleanup(func() {
		_, _ = b.docker.ImageRemove(context.Background(), req.Tag, client.ImageRemoveOptions{Force: true, PruneChildren: true})
	})
	return built, logs.String(), err
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	if testing.Verbose() {
		w.t.Log(strings.TrimRight(string(p), "\n"))
	}
	return len(p), nil
}

// run starts image with port 3000 published on loopback and returns its URL.
// The container is name plus the test process's ID, so test runs of other
// checkouts on the same Docker host don't collide.
func run(t *testing.T, docker *client.Client, name, image string, cmd ...string) string {
	t.Helper()
	ctx := t.Context()
	port := network.MustParsePort("3000/tcp")
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: fmt.Sprintf("%s-%d", name, os.Getpid()),
		Config: &container.Config{
			Image:        image,
			Cmd:          cmd,
			Env:          []string{"PORT=3000"},
			ExposedPorts: network.PortSet{port: {}},
		},
		HostConfig: &container.HostConfig{
			PortBindings: network.PortMap{port: {{HostIP: netip.MustParseAddr("127.0.0.1")}}},
			Resources:    container.Resources{Memory: 512 << 20, NanoCPUs: 1e9},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = docker.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	})
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	inspect, err := docker.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return "http://127.0.0.1:" + inspect.Container.NetworkSettings.Ports[port][0].HostPort
}

// get polls url until it answers 200 and returns the body.
func get(t *testing.T, url string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := http.Get(url)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return string(body)
			}
			err = fmt.Errorf("status %d", resp.StatusCode)
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s: %v", url, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestBuildDockerfile(t *testing.T) {
	docker := dockerClient(t)
	b := newBuilder(t, docker, 1024)
	built, _, err := buildFull(t, b, node.BuildRequest{
		Source:    sourceTar(t, map[string]string{"server.js": serverJS, "package.json": packageJSON, "Dockerfile": dockerfile}),
		BuildArgs: map[string]string{"GREETING": "dockerfile"},
		Tag:       "dock-test/hello:dockerfile",
	})
	if err != nil {
		t.Fatal(err)
	}
	id := built.ID
	if !strings.HasPrefix(id, "sha256:") {
		t.Fatalf("image ID = %q", id)
	}
	// It names the image it was built on, with the digest BuildKit
	// resolved (the base image hint, ADR 0074).
	if built.Base != "node:22-alpine" || !strings.HasPrefix(built.BaseDigest, "sha256:") {
		t.Fatalf("base = %q %q", built.Base, built.BaseDigest)
	}
	labelled(t, docker, id)
	if body := get(t, run(t, docker, "dock-test-dockerfile", id)); body != "hello dockerfile" {
		t.Fatalf("body = %q", body)
	}
	// The prune API answers; a terabyte to keep leaves the shared cache be.
	if res, err := b.PruneBuildCache(context.Background(), 1<<40); err != nil || res.Removed != 0 {
		t.Fatalf("PruneBuildCache = %+v, %v", res, err)
	}
}

func TestBuildRailpack(t *testing.T) {
	docker := dockerClient(t)
	b := newBuilder(t, docker, 1024)
	id, logs, err := build(t, b, node.BuildRequest{
		Source:     sourceTar(t, map[string]string{"server.js": serverJS, "package.json": packageJSON}),
		CacheScope: "test_railpack",
		Tag:        "dock-test/hello:railpack",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs, "Detected Node") {
		t.Errorf("logs don't mention the detected provider:\n%s", logs)
	}
	labelled(t, docker, id)
	if body := get(t, run(t, docker, "dock-test-railpack", id)); body != "hello railpack" {
		t.Fatalf("body = %q", body)
	}
}

// TestBuildDockerfileSecrets checks that variables reach RUN steps as
// secret mounts without landing in the image history, and that ARG-declared
// ones still arrive as build args.
func TestBuildDockerfileSecrets(t *testing.T) {
	docker := dockerClient(t)
	b := newBuilder(t, docker, 1024)
	secret := fmt.Sprintf("s3cret-%d", time.Now().UnixNano())
	// The Dockerfile itself is in the history, so it only names the hash.
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(secret)))
	id, logs, err := build(t, b, node.BuildRequest{
		Source: sourceTar(t, map[string]string{"Dockerfile": `FROM node:22-alpine
ARG GREETING
RUN --mount=type=secret,id=API_TOKEN,env=API_TOKEN printf %s "$API_TOKEN" | sha256sum | grep -q ` + sum + ` && echo env-secret-ok
RUN --mount=type=secret,id=API_TOKEN sha256sum /run/secrets/API_TOKEN | grep -q ` + sum + ` && echo file-secret-ok
RUN test "$GREETING" = hi && test -z "$API_TOKEN" && echo arg-ok
`}),
		BuildArgs:  map[string]string{"API_TOKEN": secret, "GREETING": "hi"},
		CacheScope: "test_secrets",
		Tag:        "dock-test/hello:secrets",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"env-secret-ok", "file-secret-ok", "arg-ok"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %q", want)
		}
	}
	history, err := docker.ImageHistory(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	var sawArg bool
	for _, h := range history.Items {
		if strings.Contains(h.CreatedBy, secret) {
			t.Errorf("secret in image history: %s", h.CreatedBy)
		}
		sawArg = sawArg || strings.Contains(h.CreatedBy, "GREETING=hi")
	}
	if !sawArg {
		t.Error("the ARG isn't in the history; is the history check looking at the right thing?")
	}
	inspect, err := docker.ImageInspect(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if env := strings.Join(inspect.Config.Env, " "); strings.Contains(env, secret) {
		t.Errorf("secret in image env: %s", env)
	}
}

// TestBuildCacheScope checks that cache mounts with the same ID are separate
// per scope, even when the Dockerfile asks for an external frontend.
func TestBuildCacheScope(t *testing.T) {
	b := newBuilder(t, dockerClient(t), 1024)
	nonce := fmt.Sprint(time.Now().UnixNano())
	df := `# syntax=docker/dockerfile:1
FROM node:22-alpine
ARG OWNER
ARG NONCE
RUN --mount=type=cache,id=shared,target=/cache echo "nonce $NONCE"; cat /cache/owner 2>/dev/null | sed 's/^/found /'; echo "$OWNER" > /cache/owner
`
	step := func(scope, owner string) string {
		t.Helper()
		_, logs, err := build(t, b, node.BuildRequest{
			Source:     sourceTar(t, map[string]string{"Dockerfile": df}),
			BuildArgs:  map[string]string{"OWNER": owner, "NONCE": fmt.Sprint(time.Now().UnixNano())},
			CacheScope: scope + "_" + nonce,
			Tag:        "dock-test/hello:cache-" + scope,
		})
		if err != nil {
			t.Fatal(err)
		}
		return logs
	}
	a, other := "owner-a-"+nonce, "owner-b-"+nonce
	logs := step("a", a)
	if !strings.Contains(logs, "ignoring the syntax directive") || strings.Contains(logs, "docker.io/docker/dockerfile") {
		t.Errorf("the syntax directive wasn't ignored:\n%s", logs)
	}
	if logs := step("b", other); strings.Contains(logs, "found "+a) {
		t.Fatalf("scope b saw scope a's cache:\n%s", logs)
	}
	if logs := step("a", a); !strings.Contains(logs, "found "+a) {
		t.Fatalf("scope a lost its own cache:\n%s", logs)
	}
}

// TestBuildSecretLayersPerScope checks that a step mounting a secret is
// cached per scope and secret value: another scope with the same
// Dockerfile and its own value runs the step instead of reusing the
// first scope's layer, and the same scope and value still hit the cache.
func TestBuildSecretLayersPerScope(t *testing.T) {
	b := newBuilder(t, dockerClient(t), 1024)
	nonce := fmt.Sprint(time.Now().UnixNano())
	df := `FROM node:22-alpine
RUN echo "nonce ` + nonce + `"
RUN --mount=type=secret,id=PIP_INDEX_URL,env=PIP_INDEX_URL echo "ran with $(printf %s "$PIP_INDEX_URL" | sha256sum | cut -c1-12)"
`
	step := func(scope, value string) string {
		t.Helper()
		_, logs, err := build(t, b, node.BuildRequest{
			Source:     sourceTar(t, map[string]string{"Dockerfile": df}),
			BuildArgs:  map[string]string{"PIP_INDEX_URL": value},
			CacheScope: scope + "_" + nonce,
			Tag:        "dock-test/hello:secret-" + scope,
		})
		if err != nil {
			t.Fatal(err)
		}
		return logs
	}
	ran := func(value string) string {
		return fmt.Sprintf("ran with %x", sha256.Sum256([]byte(value)))[:len("ran with ")+12]
	}
	if logs := step("a", "https://a.example/simple"); !strings.Contains(logs, ran("https://a.example/simple")) {
		t.Fatalf("scope a's first build didn't run the step:\n%s", logs)
	}
	if logs := step("b", "https://b.example/simple"); !strings.Contains(logs, ran("https://b.example/simple")) {
		t.Fatalf("scope b reused scope a's layer:\n%s", logs)
	}
	// The step's own line says "ran with $(printf ...", its output a hash.
	if logs := step("a", "https://a.example/simple"); regexp.MustCompile(`ran with [0-9a-f]{12}`).MatchString(logs) {
		t.Errorf("scope a didn't reuse its own layer:\n%s", logs)
	}
	if logs := step("a", "https://c.example/simple"); !strings.Contains(logs, ran("https://c.example/simple")) {
		t.Errorf("a changed secret didn't rebuild the step:\n%s", logs)
	}
}

func TestBuildNetwork(t *testing.T) {
	docker := dockerClient(t)
	name := testContainer + "-net"
	if _, err := docker.NetworkCreate(t.Context(), name, client.NetworkCreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// The next build recreates BuildKit on the default bridge.
		_, _ = docker.ContainerRemove(context.Background(), testContainer, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		_, _ = docker.NetworkRemove(context.Background(), name, client.NetworkRemoveOptions{})
	})
	b := newBuilder(t, docker, 1024)
	b.Network = name
	if _, _, err := build(t, b, node.BuildRequest{
		Source: sourceTar(t, map[string]string{"Dockerfile": "FROM node:22-alpine\nRUN true\n"}),
		Tag:    "dock-test/hello:network",
	}); err != nil {
		t.Fatal(err)
	}
	inspect, err := docker.ContainerInspect(t.Context(), testContainer, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if mode := inspect.Container.HostConfig.NetworkMode; string(mode) != name {
		t.Fatalf("BuildKit network = %q", mode)
	}
}

func TestBuildEntitlementsRefused(t *testing.T) {
	b := newBuilder(t, dockerClient(t), 1024)
	for _, flag := range []string{"--network=host", "--security=insecure"} {
		_, _, err := build(t, b, node.BuildRequest{
			Source: sourceTar(t, map[string]string{"Dockerfile": "FROM node:22-alpine\nRUN " + flag + " true\n"}),
			Tag:    "dock-test/hello:entitlement",
		})
		if err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("RUN %s: err = %v", flag, err)
		}
	}
}

// TestBuildRailpackSymlinkLoop checks that a source Railpack can't finish
// walking fails the build instead of hanging the daemon.
func TestBuildRailpackSymlinkLoop(t *testing.T) {
	defer func(d time.Duration) { planTimeout = d }(planTimeout)
	planTimeout = 2 * time.Second
	b := newBuilder(t, dockerClient(t), 1024)
	start := time.Now()
	_, _, err := build(t, b, node.BuildRequest{
		Source: tarball(t, true,
			entry{name: "package.json", body: packageJSON},
			entry{name: "server.js", body: serverJS},
			entry{name: "a", typ: tar.TypeSymlink, link: "."},
			entry{name: "b", typ: tar.TypeSymlink, link: "."},
		),
		Tag: "dock-test/hello:loop",
	})
	t.Logf("error users see after %s: %v", time.Since(start).Round(time.Second), err)
	if err == nil || !strings.Contains(err.Error(), "symlink loops") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > planTimeout+10*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
}

// A step that eats the builder's memory fails the build and says so. The
// kernel's OOM killer picks the step or, now and then, buildkitd itself:
// the user reads about memory either way (TestBuildKitKilledMidBuild has
// the second message to itself).
func TestBuildOutOfMemory(t *testing.T) {
	b := newBuilder(t, dockerClient(t), 256)
	_, _, err := build(t, b, node.BuildRequest{
		Source: sourceTar(t, map[string]string{"Dockerfile": `FROM node:22-alpine
RUN node -e 'const a = []; for (;;) a.push(Buffer.alloc(16 << 20, 1))'
`}),
		Tag: "dock-test/hello:oom",
	})
	t.Logf("error users see: %v", err)
	if err == nil || !strings.Contains(err.Error(), "ran out of memory") && !strings.Contains(err.Error(), "most likely out of memory") {
		t.Fatalf("err = %v", err)
	}
}

// BuildKit itself dying mid-build (the OOM killer can pick buildkitd over
// the step) drops the connection before Docker restarts the container; the
// error still says what most likely happened. Killed here once the RUN
// step runs.
func TestBuildKitKilledMidBuild(t *testing.T) {
	b := newBuilder(t, dockerClient(t), 512)
	running := make(chan struct{})
	var once sync.Once
	logs := writerFunc(func(p []byte) (int, error) {
		if strings.Contains(string(p), "RUN sleep 60") {
			once.Do(func() { close(running) })
		}
		return len(p), nil
	})
	killed := make(chan error, 1)
	go func() {
		<-running
		_, err := b.docker.ContainerKill(t.Context(), testContainer, client.ContainerKillOptions{})
		killed <- err
	}()
	_, err := b.Build(t.Context(), node.BuildRequest{
		Source: sourceTar(t, map[string]string{"Dockerfile": "FROM alpine:3.22\nRUN sleep 60\n"}),
		Tag:    "dock-test/hello:killed",
		Logs:   logs,
	})
	if kerr := <-killed; kerr != nil {
		t.Fatal(kerr)
	}
	if err == nil || !strings.Contains(err.Error(), "BuildKit restarted during the build, most likely out of memory") {
		t.Fatalf("err = %v", err)
	}
}

// writerFunc is an io.Writer from a function.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// The memory cap holds even after something outside the platform loosens
// it: `docker update`, or a write to the cgroup file that leaves Docker's
// HostConfig as it was.
func TestDaemonLimitsRestored(t *testing.T) {
	docker := dockerClient(t)
	ctx := t.Context()
	b := newBuilder(t, docker, 512)
	const mem = 512 << 20
	ensure := func() {
		t.Helper()
		var logs bytes.Buffer
		bk, err := b.ensureDaemon(ctx, &logs)
		if err != nil {
			t.Fatal(err)
		}
		_ = bk.Close()
		t.Logf("ensure: %q", logs.String())
		inspect, err := docker.ContainerInspect(ctx, testContainer, client.ContainerInspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if h := inspect.Container.HostConfig; h.Memory != mem || h.MemorySwap != dockerrt.NoSwap(mem) {
			t.Fatalf("HostConfig memory = %d, swap = %d", h.Memory, h.MemorySwap)
		}
		if p := cgroupProblem(b.readCgroupLimits(ctx), mem); p != "" {
			t.Fatal(p)
		}
	}
	ensure()

	if _, err := docker.ContainerUpdate(ctx, testContainer, client.ContainerUpdateOptions{
		Resources: &container.Resources{Memory: mem, MemorySwap: -1},
	}); err != nil {
		t.Fatal(err)
	}
	ensure()

	// The cgroup's swap limit goes back to max while Docker's HostConfig
	// still says no swap. buildkitd runs in a
	// child cgroup ("init") of the container's scope, which holds the limit.
	// Only the systemd cgroup driver's scope paths are handled.
	inspect, err := docker.ContainerInspect(ctx, testContainer, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`p=$(sed -n 's/^0::\(.*\.scope\).*/\1/p' /proc/%d/cgroup) && [ -n "$p" ] && echo max > /sys/fs/cgroup$p/memory.swap.max`, inspect.Container.State.Pid)
	if code := runHelper(t, docker, container.HostConfig{Privileged: true, CgroupnsMode: "host", PidMode: "host"}, script); code != 0 {
		t.Skipf("could not loosen the cgroup swap limit (exit %d)", code)
	}
	if p := cgroupProblem(b.readCgroupLimits(ctx), mem); !strings.Contains(p, "swap") {
		t.Fatalf("loosened limit not detected: %q", p)
	}
	ensure()
}

// A second dock runtime on the same Docker host gets a clear error instead
// of recreating (and so killing) the first one's BuildKit, and its prune
// leaves that BuildKit's cache alone.
func TestBuildKitOfAnotherRuntime(t *testing.T) {
	docker := dockerClient(t)
	ctx := t.Context()
	b := newBuilder(t, docker, 512)
	bk, err := b.ensureDaemon(ctx, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	_ = bk.Close()
	before, err := docker.ContainerInspect(ctx, testContainer, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}

	other := newBuilder(t, docker, 1024) // other settings would recreate it
	other.Owner = testContainer + "-other"
	if _, err := other.ensureDaemon(ctx, io.Discard); err == nil || !strings.Contains(err.Error(), "one dock runtime per Docker host") {
		t.Fatalf("second runtime: %v", err)
	}
	if _, err := other.PruneBuildCache(ctx, 0); err == nil {
		t.Fatal("second runtime pruned the first one's build cache")
	}
	after, err := docker.ContainerInspect(ctx, testContainer, client.ContainerInspectOptions{})
	if err != nil || after.Container.ID != before.Container.ID || after.Container.State.StartedAt != before.Container.State.StartedAt {
		t.Fatalf("BuildKit was recreated or restarted: %v", err)
	}
}

// runHelper runs sh -c script in a throwaway alpine container with host
// and returns its exit code.
func runHelper(t *testing.T, docker *client.Client, host container.HostConfig, script string) int64 {
	t.Helper()
	ctx := t.Context()
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: "alpine:latest", Cmd: []string{"sh", "-c", script}},
		HostConfig: &host,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = docker.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	}()
	wait := docker.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNextExit})
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-wait.Result:
		return res.StatusCode
	case err := <-wait.Error:
		t.Fatal(err)
	}
	return -1
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}
