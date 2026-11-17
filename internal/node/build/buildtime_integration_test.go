//go:build integration

package build

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/pack"
	"github.com/NordTempo/tempoDock/internal/registry"
)

// TestBuildTimes times a deploy's build path for the app in DOCK_BUILD_APP
// (e.g. e2e/apps/nextjs-pnpm): pack, build, export, push, start. Each
// path (DOCK_BUILD_PATHS: Dockerfile, Dockerfile with cache mounts,
// Railpack) gets a cold build on a fresh BuildKit and a warm one after a
// one-line change, and logs the steps that took a second or more.
// DOCK_BUILD_CPUS and DOCK_BUILD_MEMORY_MB set the builder's caps,
// DOCK_BUILD_LOG a directory for the build logs, DOCK_BUILD_DOCKER_PUSH
// (any value) has Docker push instead of BuildKit, and DOCK_BUILD_WARM
// (any value) fetches Railpack's images (WarmBuilder) before its cold
// build. Skipped unless
// DOCK_BUILD_APP is set (ADR 0089).
func TestBuildTimes(t *testing.T) {
	app := os.Getenv("DOCK_BUILD_APP")
	if app == "" {
		t.Skip("set DOCK_BUILD_APP to an app directory")
	}
	docker := dockerClient(t)
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	name := "dock-buildtime-" + hex.EncodeToString(suffix)

	key, err := registry.LoadOrCreateKey(t.TempDir() + "/" + registry.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	signer := registry.NewSigner(key, registry.Config{Service: "dock-test-registry", Realm: "http://127.0.0.1:1/token"})
	regHost := strings.TrimPrefix(startRegistry(t, docker, name, signer), "http://")

	paths := strings.Fields(cmp.Or(os.Getenv("DOCK_BUILD_PATHS"), "dockerfile dockerfile-cache railpack"))
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			cfg := config.Default().BuildKit
			cfg.MemoryMB = envInt("DOCK_BUILD_MEMORY_MB", cfg.MemoryMB)
			cfg.CPUs, _ = strconv.ParseFloat(os.Getenv("DOCK_BUILD_CPUS"), 64) // 0, the default, if unset
			b := New(docker, cfg, t.TempDir())
			b.name, b.cacheVolume = name+"-"+path, name+"-"+path+"-cache"
			b.Owner = b.name
			if os.Getenv("DOCK_BUILD_DOCKER_PUSH") == "" {
				withRelay(t, b)
			}
			t.Cleanup(func() {
				_, _ = docker.ContainerRemove(context.Background(), b.name, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
				_, _ = docker.VolumeRemove(context.Background(), b.cacheVolume, client.VolumeRemoveOptions{Force: true})
			})
			if path == "railpack" && os.Getenv("DOCK_BUILD_WARM") != "" {
				start := time.Now()
				if err := b.WarmBuilder(t.Context()); err != nil {
					t.Fatal(err)
				}
				t.Logf("railpack images fetched ahead (WarmBuilder) in %s", time.Since(start).Round(100*time.Millisecond))
			}
			src := copyApp(t, app, path)
			for i, round := range []string{"cold", "warm"} {
				if i > 0 {
					touch(t, filepath.Join(src, "app", "page.tsx"))
				}
				timeBuild(t, b, src, round, path, regHost, signer, i+1)
			}
		})
	}
}

// timeBuild runs one build and push of src and logs where the time went.
func timeBuild(t *testing.T, b *Builder, src, round, path, regHost string, signer *registry.Signer, n int) {
	t.Helper()
	ctx := t.Context()
	start := time.Now()
	var archive bytes.Buffer
	st, err := pack.Write(&archive, src, pack.Options{})
	if err != nil {
		t.Fatal(err)
	}
	packed, size := time.Since(start), archive.Len()

	const repo = "bench/app"
	tag := fmt.Sprintf("%s/local:%d", b.name, n)
	push := &node.PushTarget{
		Ref:   fmt.Sprintf("%s/%s:%s-%d", regHost, repo, path, n),
		Token: signer.Mint("bench", registry.PushTokenTTL, registry.Push(repo)),
	}
	log := &stampedLog{start: time.Now()}
	buildStart := time.Now()
	_, err = b.Build(ctx, node.BuildRequest{Source: &archive, Tag: tag, Push: push, Logs: log, CacheScope: "p1_e1"})
	built := time.Since(buildStart)
	t.Cleanup(func() {
		_, _ = b.docker.ImageRemove(context.Background(), tag, client.ImageRemoveOptions{Force: true})
	})
	if err != nil {
		t.Fatalf("%s %s build: %v\n%s", path, round, err, tail(log.String(), 40))
	}

	startAt := time.Now()
	url := run(t, b.docker, b.name+"-app-"+strconv.Itoa(n), tag)
	get(t, url)
	served := time.Since(startAt)

	t.Logf("%s %s: pack %s (%d files, %d KB gz) | build+push %s | start to 200 %s",
		path, round, packed.Round(time.Millisecond), st.Files, size>>10, built.Round(100*time.Millisecond), served.Round(100*time.Millisecond))
	for _, l := range log.phases() {
		t.Logf("  %s", l)
	}
	if os.Getenv("DOCK_BUILD_LOG") != "" {
		_ = os.WriteFile(filepath.Join(os.Getenv("DOCK_BUILD_LOG"), path+"-"+round+".log"), []byte(log.String()), 0o644)
	}
}

// stampedLog keeps build output with each line's offset from start.
type stampedLog struct {
	mu    sync.Mutex
	start time.Time
	buf   bytes.Buffer
	part  []byte
}

func (l *stampedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.part = append(l.part, p...)
	for {
		i := bytes.IndexByte(l.part, '\n')
		if i < 0 {
			return len(p), nil
		}
		fmt.Fprintf(&l.buf, "%7.2f %s\n", time.Since(l.start).Seconds(), l.part[:i])
		l.part = l.part[i+1:]
	}
}

func (l *stampedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

var (
	vertexName = regexp.MustCompile(`^\s*[\d.]+ #(\d+) (\[.*|[a-z].*)$`)
	vertexDone = regexp.MustCompile(`^\s*([\d.]+) #(\d+) (DONE ([\d.]+)s|CACHED)`)
	stampLine  = regexp.MustCompile(`^\s*([\d.]+) (Pushing|Pushed|Loaded into Docker) `)
)

// phases lists the build's steps that took a second or more (BuildKit's
// own DONE durations), the push (with BuildKit's, its export too) and
// the pull back into Docker.
func (l *stampedLog) phases() []string {
	names := map[string]string{}
	var out []string
	var pushStart float64
	for line := range strings.Lines(l.String()) {
		line = strings.TrimRight(line, "\n")
		if m := vertexName.FindStringSubmatch(line); m != nil {
			if _, ok := names[m[1]]; !ok {
				names[m[1]] = strings.TrimSpace(m[2])
			}
		}
		if m := vertexDone.FindStringSubmatch(line); m != nil && m[4] != "" {
			if d, _ := strconv.ParseFloat(m[4], 64); d >= 1 {
				out = append(out, fmt.Sprintf("%6.1fs %s", d, names[m[2]]))
			}
		}
		if m := stampLine.FindStringSubmatch(line); m != nil {
			at, _ := strconv.ParseFloat(m[1], 64)
			switch m[2] {
			case "Pushing":
				pushStart = at
			case "Pushed":
				out = append(out, fmt.Sprintf("%6.1fs push to registry", at-pushStart))
				pushStart = at
			default:
				out = append(out, fmt.Sprintf("%6.1fs pull into Docker", at-pushStart))
			}
		}
	}
	return out
}

// copyApp copies the app at dir into a temp directory for a build path,
// leaving out node_modules and .next. "railpack" gets no Dockerfile,
// "dockerfile-cache" the app's Dockerfile.cache as its Dockerfile.
func copyApp(t *testing.T, dir, path string) string {
	t.Helper()
	dst := t.TempDir()
	err := os.CopyFS(dst, os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"node_modules", ".next"} {
		_ = os.RemoveAll(filepath.Join(dst, p))
	}
	df, cached := filepath.Join(dst, "Dockerfile"), filepath.Join(dst, "Dockerfile.cache")
	switch path {
	case "railpack":
		_ = os.Remove(df)
	case "dockerfile-cache":
		if err := os.Rename(cached, df); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Remove(cached)
	return dst
}

// touch makes a one-line change to a source file.
func touch(t *testing.T, file string) {
	t.Helper()
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := io.WriteString(f, "// changed "+time.Now().String()+"\n"); err != nil {
		t.Fatal(err)
	}
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}
