//go:build integration

package registry_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	dockerrt "github.com/NordTempo/tempoDock/internal/node/docker"
	"github.com/NordTempo/tempoDock/internal/registry"
	"github.com/NordTempo/tempoDock/internal/registry/gateway"
	"github.com/NordTempo/tempoDock/internal/s3"
)

// TestGatewayOverStalledBucket runs ADR 0084 against a real registry:3 on
// the host's network and MinIO behind a proxy that stalls on demand:
// pushes and pulls work while the bucket hangs, a stuck blob is uploaded
// again by the next push, and everything reaches the bucket once it
// answers again.
func TestGatewayOverStalledBucket(t *testing.T) {
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	name := "dock-reggwtest-" + hex.EncodeToString(suffix)

	minio := startMinIO(t, docker, name+"-minio")
	faults := &stallProxy{}
	bucket := faults.start(t, minio)

	key, err := registry.LoadOrCreateKey(filepath.Join(t.TempDir(), registry.KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	signer := registry.NewSigner(key, registry.Config{Service: "dock-test-registry", Realm: "http://127.0.0.1:1/token"})
	access, secret := registry.GatewayKeys(signer)
	var mu sync.Mutex
	var states []string
	gw, err := gateway.Open(gateway.Options{
		Dir: t.TempDir(), AccessKey: access, Secret: secret,
		OnChange: func(st gateway.Status) { mu.Lock(); states = append(states, st.State); mu.Unlock() },
		Timing: gateway.Timing{
			ReadTimeout: time.Second, PushReadTimeout: 500 * time.Millisecond, Stall: 2 * time.Second, MetaTimeout: 2 * time.Second,
			RetryMin: 200 * time.Millisecond, RetryMax: time.Second, ProbeMin: 200 * time.Millisecond, ProbeMax: time.Second,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gw.Close)
	p := startHostPlatform(t, docker, name, signer, gw)
	if err := p.UseStorage(ctx, bucket); err != nil {
		t.Fatal(err)
	}
	rt, err := dockerrt.New(netip.MustParsePrefix("10.252.0.0/16"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	direct := &s3.Client{Bucket: *minio}
	drained := func(what string) {
		t.Helper()
		dctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if err := gw.Drain(dctx); err != nil {
			t.Fatalf("%s: %v (status %+v)", what, err, gw.Status())
		}
	}

	// Healthy: pushes reach the bucket.
	const repo = "test/app"
	pushing := p.PushStarted()
	first := push(t, docker, p, "busybox:latest", repo, "1")
	pushing()
	drained("the first push")
	if n, err := p.Stored(ctx); err != nil || n == 0 {
		t.Fatalf("stored in the bucket = %d, %v", n, err)
	}

	// The bucket hangs: a push still works, from the spool, and so does
	// pulling it.
	faults.set(func(f *stallProxy) { f.down = true })
	start := time.Now()
	pushing = p.PushStarted()
	second := push(t, docker, p, "hello-world:latest", repo, "2")
	pushing()
	t.Logf("pushed with the bucket down in %v", time.Since(start).Round(time.Millisecond))
	if d := time.Since(start); d > 90*time.Second {
		t.Errorf("the push took %v with the bucket down", d)
	}
	pull(t, docker, rt, p, repo, second)
	if st := gw.Status(); st.Waiting == 0 || st.State == gateway.Healthy {
		t.Errorf("with the bucket down: %+v, want objects waiting and the bucket unhealthy", st)
	}

	// It answers again: the spool drains into it.
	faults.set(func(f *stallProxy) { f.down = false })
	drained("after the bucket recovered")
	if has, err := direct.HasPrefix(ctx, registry.BucketRoot+"/docker/registry/v2/repositories/test/app/_manifests/revisions/sha256/"+strings.TrimPrefix(second, "sha256:")); err != nil || !has {
		t.Fatalf("the second manifest isn't in the bucket: %v, %v", has, err)
	}

	// One layer hangs in the bucket, as on 2026-11-13: a push to another
	// repository can't mount it, is told it's missing, and uploads it
	// again, into the spool, from where it is pulled and uploaded over the
	// stuck one.
	layer := largestBlob(t, direct)
	faults.set(func(f *stallProxy) { f.stallPaths = []string{layer} })
	pushing = p.PushStarted()
	other := push(t, docker, p, "busybox:latest", "test/other", "1")
	pushing()
	pull(t, docker, rt, p, "test/other", other)
	faults.set(func(f *stallProxy) { f.stallPaths = nil })
	drained("after the repair")
	faults.set(func(f *stallProxy) {
		if f.puts[layer] == 0 {
			t.Errorf("the stuck layer wasn't uploaded again")
		}
	})
	pull(t, docker, rt, p, repo, first)

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(strings.Join(states, " "), gateway.Healthy) {
		t.Errorf("states reported = %v, want it back to healthy at some point", states)
	}
}

// largestBlob is the key of the biggest blob in the bucket: an image's
// layer.
func largestBlob(t *testing.T, c *s3.Client) string {
	t.Helper()
	var key string
	var size int64
	err := c.List(t.Context(), registry.BucketRoot+"/docker/registry/v2/blobs/", func(page []s3.Object) error {
		for _, o := range page {
			if o.Size > size {
				key, size = o.Key, o.Size
			}
		}
		return nil
	})
	if err != nil || key == "" {
		t.Fatalf("no blob in the bucket: %v", err)
	}
	return key
}

// stallProxy forwards to a bucket, hanging requests while down or when
// their path contains one of stallPaths (HEAD and GET only).
type stallProxy struct {
	mu         sync.Mutex
	down       bool
	stallPaths []string
	puts       map[string]int // keys PUT (whole objects or parts)
}

func (f *stallProxy) set(fn func(*stallProxy)) {
	f.mu.Lock()
	fn(f)
	f.mu.Unlock()
}

// start serves the proxy on loopback and returns the bucket through it.
// The Host header passes through unchanged, so signatures still match.
func (f *stallProxy) start(t *testing.T, target *s3.Bucket) *s3.Bucket {
	t.Helper()
	u, err := url.Parse(target.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	rp := httputil.NewSingleHostReverseProxy(u)
	quit := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		if r.Method == http.MethodPut {
			if f.puts == nil {
				f.puts = map[string]int{}
			}
			_, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
			f.puts[key]++
		}
		hang := f.down
		if r.Method == http.MethodHead || r.Method == http.MethodGet {
			for _, p := range f.stallPaths {
				hang = hang || strings.Contains(r.URL.Path, p)
			}
		}
		f.mu.Unlock()
		if hang {
			select {
			case <-r.Context().Done():
			case <-quit:
			}
			return
		}
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { close(quit); srv.Close() })
	b := *target
	b.Endpoint = srv.URL
	return &b
}

// startHostPlatform runs registry:3 as name the way install.sh does since
// ADR 0084: on the host's network, listening on a loopback port, reaching
// its bucket through gw, which it serves on another.
func startHostPlatform(t *testing.T, docker *client.Client, name string, signer *registry.Signer, gw *gateway.Gateway) *registry.Platform {
	t.Helper()
	ctx := t.Context()
	gln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	logged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		start := time.Now()
		gw.ServeHTTP(rec, r)
		if os.Getenv("GATEWAY_LOG") != "" {
			t.Logf("gateway %s %s?%s → %d in %v", r.Method, r.URL.Path, r.URL.RawQuery, rec.status, time.Since(start).Round(time.Millisecond))
		}
	})
	gsrv := &http.Server{Handler: logged, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = gsrv.Serve(gln) }()
	t.Cleanup(func() { _ = gsrv.Close() })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := "127.0.0.1:" + strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()
	cfgDir, storage := t.TempDir(), t.TempDir()
	st := registry.Storage{Listen: listen, Gateway: gln.Addr().String()}
	if _, err := registry.WriteConfig(cfgDir, signer, st); err != nil {
		t.Fatal(err)
	}
	ensureImage(t, docker, registry.Image)
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image: registry.Image,
			User:  fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
			Env:   []string{"OTEL_TRACES_EXPORTER=none"},
		},
		HostConfig: &container.HostConfig{
			NetworkMode:    "host",
			Binds:          []string{cfgDir + ":/etc/distribution:ro", storage + ":/var/lib/registry"},
			ReadonlyRootfs: true,
			Tmpfs:          map[string]string{"/tmp": ""},
			CapDrop:        []string{"ALL"},
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
	p := &registry.Platform{
		Name: listen, Signer: signer, Client: &registry.Client{URL: "http://" + listen, Signer: signer},
		Docker: docker, Container: name, StorageDir: storage, ConfigDir: cfgDir,
		Listen: listen, Gateway: gw, GatewayAddr: gln.Addr().String(),
	}
	waitUp(t, "http://"+listen)
	return p
}

// statusRecorder keeps a response's status for the gateway's log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
