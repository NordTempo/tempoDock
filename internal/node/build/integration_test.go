//go:build integration

package build

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/NordTempo/platform/internal/config"
	"github.com/NordTempo/platform/internal/node"
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

func newBuilder(t *testing.T, docker *client.Client, memoryMB int) *Builder {
	t.Helper()
	cfg := config.Default().BuildKit
	cfg.MemoryMB = memoryMB
	return New(docker, cfg, t.TempDir())
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
	var logs bytes.Buffer
	req.Logs = io.MultiWriter(&logs, testWriter{t})
	id, err := b.Build(t.Context(), req)
	t.Cleanup(func() {
		_, _ = b.docker.ImageRemove(context.Background(), req.Tag, client.ImageRemoveOptions{Force: true, PruneChildren: true})
	})
	return id, logs.String(), err
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	if testing.Verbose() {
		w.t.Log(strings.TrimRight(string(p), "\n"))
	}
	return len(p), nil
}

// run starts image with port 3000 published on loopback and returns its URL.
func run(t *testing.T, docker *client.Client, name, image string, cmd ...string) string {
	t.Helper()
	ctx := t.Context()
	port := network.MustParsePort("3000/tcp")
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
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
		_, _ = docker.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
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
	id, _, err := build(t, b, node.BuildRequest{
		Source:    sourceTar(t, map[string]string{"server.js": serverJS, "package.json": packageJSON, "Dockerfile": dockerfile}),
		BuildArgs: map[string]string{"GREETING": "dockerfile"},
		Tag:       "platform-test/hello:dockerfile",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "sha256:") {
		t.Fatalf("image ID = %q", id)
	}
	if body := get(t, run(t, docker, "platform-test-dockerfile", id)); body != "hello dockerfile" {
		t.Fatalf("body = %q", body)
	}
}

func TestBuildRailpack(t *testing.T) {
	docker := dockerClient(t)
	b := newBuilder(t, docker, 1024)
	id, logs, err := build(t, b, node.BuildRequest{
		Source: sourceTar(t, map[string]string{"server.js": serverJS, "package.json": packageJSON}),
		Tag:    "platform-test/hello:railpack",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs, "Detected Node") {
		t.Errorf("logs don't mention the detected provider:\n%s", logs)
	}
	if body := get(t, run(t, docker, "platform-test-railpack", id)); body != "hello railpack" {
		t.Fatalf("body = %q", body)
	}
}

func TestBuildMissingDockerfile(t *testing.T) {
	b := newBuilder(t, dockerClient(t), 1024)
	_, _, err := build(t, b, node.BuildRequest{
		Source:     sourceTar(t, map[string]string{"server.js": serverJS}),
		Dockerfile: "../Dockerfile",
		Tag:        "platform-test/hello:missing",
	})
	if err == nil || !strings.Contains(err.Error(), "inside the source") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildOutOfMemory(t *testing.T) {
	b := newBuilder(t, dockerClient(t), 256)
	_, _, err := build(t, b, node.BuildRequest{
		Source: sourceTar(t, map[string]string{"Dockerfile": `FROM node:22-alpine
RUN node -e 'const a = []; for (;;) a.push(Buffer.alloc(16 << 20, 1))'
`}),
		Tag: "platform-test/hello:oom",
	})
	t.Logf("error users see: %v", err)
	if err == nil || !strings.Contains(err.Error(), "ran out of memory") {
		t.Fatalf("err = %v", err)
	}
}

// TestSpike measures the numbers the M0 spike asks for. It builds a real
// Next.js app, so it's slow and only runs with PLATFORM_SPIKE=1.
func TestSpike(t *testing.T) {
	if os.Getenv("PLATFORM_SPIKE") == "" {
		t.Skip("set PLATFORM_SPIKE=1 to run")
	}
	docker := dockerClient(t)
	ctx := t.Context()
	pull, err := docker.ImagePull(ctx, "node:22-alpine", client.ImagePullOptions{})
	if err == nil {
		err = pull.Wait(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}

	for _, memMB := range []int{1024, 256} {
		t.Run(fmt.Sprintf("%dMB", memMB), func(t *testing.T) {
			b := newBuilder(t, docker, memMB)
			bk, err := b.ensureDaemon(ctx, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			_ = bk.Close()
			time.Sleep(10 * time.Second)
			idle := watchMemory(t, docker)
			time.Sleep(3 * time.Second)
			t.Logf("buildkit idle: %s", idle())

			app := run(t, docker, "platform-test-latency", "node:22-alpine", "node", "-e", serverJS)
			get(t, app)
			t.Logf("app latency idle: %s", latency(app, 10*time.Second))

			done := make(chan struct{})
			during := make(chan string)
			go func() { during <- latency(app, 0, done) }()
			peak := watchMemory(t, docker)
			start := time.Now()
			_, logs, err := build(t, b, node.BuildRequest{Source: nextApp(t), Tag: fmt.Sprintf("platform-test/next:%d", memMB)})
			close(done)
			t.Logf("build took %s, err: %v", time.Since(start).Round(time.Second), err)
			if err != nil {
				t.Logf("last log lines:\n%s", tail(logs, 15))
			}
			t.Logf("buildkit peak during build: %s", peak())
			t.Logf("app latency during build: %s", <-during)
		})
	}
}

// nextApp is a minimal Next.js app, the classic memory-hungry Node build. A
// nonce in package.json defeats the build cache.
func nextApp(t *testing.T) io.Reader {
	return sourceTar(t, map[string]string{
		"package.json": fmt.Sprintf(`{"name": "next-spike", "private": true, "description": "%d",
  "scripts": {"build": "next build", "start": "next start"},
  "dependencies": {"next": "15.5.4", "react": "19.1.1", "react-dom": "19.1.1"}}`, time.Now().UnixNano()),
		"app/layout.js": `export default function RootLayout({ children }) { return <html><body>{children}</body></html> }`,
		"app/page.js":   `export default function Page() { return <h1>Hello</h1> }`,
		"Dockerfile": `FROM node:22-alpine
WORKDIR /app
COPY package.json ./
RUN npm install --no-audit --no-fund
COPY . .
RUN npm run build
CMD ["npm", "start"]
`,
	})
}

// watchMemory streams the BuildKit container's memory stats and returns a
// func reporting the maxima seen so far. "anon" is process memory, what the
// OOM killer acts on; "working set" adds active page cache, as docker stats
// shows it.
func watchMemory(t *testing.T, docker *client.Client) func() string {
	ctx, cancel := context.WithCancel(t.Context())
	res, err := docker.ContainerStats(ctx, ContainerName, client.ContainerStatsOptions{Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var anon, working uint64
	go func() {
		defer func() { _ = res.Body.Close() }()
		dec := json.NewDecoder(res.Body)
		for {
			var s container.StatsResponse
			if dec.Decode(&s) != nil {
				return
			}
			m := s.MemoryStats
			mu.Lock()
			anon = max(anon, m.Stats["anon"])
			working = max(working, m.Usage-m.Stats["inactive_file"])
			mu.Unlock()
		}
	}()
	return func() string {
		cancel()
		mu.Lock()
		defer mu.Unlock()
		return fmt.Sprintf("anon %s, working set %s", mb(anon), mb(working))
	}
}

func mb(v uint64) string { return fmt.Sprintf("%.0f MB", float64(v)/(1<<20)) }

// latency sends sequential requests every 20ms until d passes (or done
// closes) and summarizes the response times.
func latency(url string, d time.Duration, done ...chan struct{}) string {
	var ms []float64
	var failed int
	end := time.Now().Add(d)
	httpc := &http.Client{Timeout: 5 * time.Second}
	for {
		if len(done) > 0 {
			select {
			case <-done[0]:
				return summarize(ms, failed)
			default:
			}
		} else if time.Now().After(end) {
			return summarize(ms, failed)
		}
		start := time.Now()
		resp, err := httpc.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			ms = append(ms, float64(time.Since(start).Microseconds())/1000)
		} else {
			failed++
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func summarize(ms []float64, failed int) string {
	if len(ms) == 0 {
		return fmt.Sprintf("no successful requests, %d failed", failed)
	}
	slices.Sort(ms)
	p := func(q float64) float64 { return ms[int(q*float64(len(ms)-1))] }
	return fmt.Sprintf("n=%d p50=%.2fms p99=%.2fms max=%.2fms failed=%d", len(ms), p(0.5), p(0.99), ms[len(ms)-1], failed)
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}
