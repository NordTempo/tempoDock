//go:build integration

package registry_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
	dockerrt "github.com/NordTempo/tempoDock/internal/node/docker"
	"github.com/NordTempo/tempoDock/internal/registry"
	"github.com/NordTempo/tempoDock/internal/s3"
)

// minioImage is an S3-compatible store for the test (MinIO, as built by
// Pigsty: minio/minio is no longer published on Docker Hub).
const minioImage = "pgsty/minio:RELEASE.2026-08-04T00-00-00Z@sha256:b6bfe7239bfc83fb90d31612d9704d86039dd714f7904b3f1ad68f211e602372"

// TestStorageInBucket runs ADR 0034 against a real registry:3 and MinIO:
// images pushed to the disk move to a bucket, where they are pulled,
// pushed and garbage collected, and move back to the disk. A migration
// into a bucket holding another Dock's registry data is refused and
// leaves the registry writable; data this install's interrupted
// migration left is cleared instead.
func TestStorageInBucket(t *testing.T) {
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	name := "dock-regs3test-" + hex.EncodeToString(suffix)

	bucket := startMinIO(t, docker, name+"-minio")
	p := startPlatform(t, docker, name)
	if err := p.UseStorage(ctx, nil); err != nil {
		t.Fatal(err)
	}
	rt, err := dockerrt.New(netip.MustParsePrefix("10.251.0.0/16"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	// On disk.
	const repo = "test/app"
	first := push(t, docker, p, "busybox:latest", repo, "1")
	pull(t, docker, rt, p, repo, first)
	onDisk, err := p.Stored(ctx)
	if err != nil || onDisk == 0 {
		t.Fatalf("stored on disk = %d, %v", onDisk, err)
	}

	// Refused: the bucket holds another Dock's registry data. Nothing
	// changes.
	const owner = "test-install"
	stray := &s3.Client{Bucket: *bucket}
	if err := stray.Put(ctx, registry.BucketRoot+"/docker/stray", strings.NewReader("x"), 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.Migrate(ctx, bucket, owner, func() error { t.Fatal("committed"); return nil }); err == nil || !strings.Contains(err.Error(), "already holds registry data") {
		t.Fatalf("migrate into a used bucket: %v", err)
	}
	push(t, docker, p, "busybox:latest", repo, "1b") // still writable

	// Disk to bucket, over what an interrupted migration of this install
	// left there.
	if err := stray.Put(ctx, registry.OwnerKey, strings.NewReader(owner), int64(len(owner))); err != nil {
		t.Fatal(err)
	}
	committed := false
	objects, size, err := p.Migrate(ctx, bucket, owner, func() error { committed = true; return nil })
	if err != nil || !committed || objects == 0 || size == 0 {
		t.Fatalf("migrate to the bucket: %d objects, %d bytes, committed %v: %v", objects, size, committed, err)
	}
	if err := p.Drop(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Join(p.StorageDir, "docker")); len(entries) != 0 {
		t.Fatalf("the disk copy is still there: %v", entries)
	}
	inBucket, err := p.Stored(ctx)
	if err != nil || inBucket != onDisk {
		t.Fatalf("stored in the bucket = %d (on disk %d), %v", inBucket, onDisk, err)
	}
	pull(t, docker, rt, p, repo, first)
	second := push(t, docker, p, "hello-world:latest", repo, "2")
	pull(t, docker, rt, p, repo, second)

	// Prune and collect in the bucket.
	if n, err := p.Prune(ctx, []string{second}); err != nil || n != 1 {
		t.Fatalf("prune = %d, %v", n, err)
	}
	var gcLog bytes.Buffer
	p.Logs = &gcLog
	if err := p.GarbageCollect(ctx); err != nil {
		t.Fatalf("garbage collect: %v\n%s", err, gcLog.String())
	}
	if !strings.Contains(gcLog.String(), "blob eligible for deletion: "+first) {
		t.Errorf("the pruned manifest's blob wasn't collected:\n%s", gcLog.String())
	}
	collected, err := p.Stored(ctx)
	if err != nil || collected >= inBucket {
		t.Fatalf("stored after collecting = %d (before %d), %v", collected, inBucket, err)
	}

	// Bucket back to disk.
	objects, size, err = p.Migrate(ctx, nil, owner, func() error { return nil })
	if err != nil || objects == 0 {
		t.Fatalf("migrate to disk: %d objects, %v", objects, err)
	}
	if err := p.Drop(ctx, bucket); err != nil {
		t.Fatal(err)
	}
	if used, err := stray.HasPrefix(ctx, registry.BucketRoot+"/"); err != nil || used {
		t.Fatalf("the bucket copy is still there: %v, %v", used, err)
	}
	if back, err := p.Stored(ctx); err != nil || back != collected {
		t.Fatalf("stored on disk again = %d (in the bucket %d), %v", back, collected, err)
	}
	pull(t, docker, rt, p, repo, second)
	push(t, docker, p, "busybox:latest", repo, "3") // the registry owns what was written
	t.Logf("moved %d objects, %d bytes", objects, size)
}

// startMinIO runs MinIO with bucket "dock-test" and returns it. It is
// published on the Docker bridge's gateway address, so the test and the
// registry container reach it at the same endpoint.
func startMinIO(t *testing.T, docker *client.Client, name string) *s3.Bucket {
	t.Helper()
	ctx := t.Context()
	ensureImage(t, docker, minioImage)
	br, err := docker.NetworkInspect(ctx, "bridge", client.NetworkInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	gw := br.Network.IPAM.Config[0].Gateway
	port := network.MustParsePort("9000/tcp")
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:        minioImage,
			Cmd:          []string{"minio", "server", "/data"},
			Env:          []string{"MINIO_ROOT_USER=dockminio", "MINIO_ROOT_PASSWORD=dockminio-secret"},
			ExposedPorts: network.PortSet{port: {}},
		},
		HostConfig: &container.HostConfig{
			PortBindings: network.PortMap{port: {{HostIP: gw}}},
			Tmpfs:        map[string]string{"/data": ""},
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
	b := &s3.Bucket{
		Endpoint:    "http://" + net.JoinHostPort(gw.String(), inspect.Container.NetworkSettings.Ports[port][0].HostPort),
		Name:        "dock-test",
		PathStyle:   true,
		AccessKeyID: "dockminio", SecretAccessKey: "dockminio-secret",
	}
	// PUT on the bucket itself creates it.
	mk := &s3.Client{Bucket: *b}
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := mk.Put(ctx, "", nil, 0)
		if err == nil {
			return b
		}
		if time.Now().After(deadline) {
			t.Fatalf("create bucket at %s: %v", b.Endpoint, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// startPlatform runs registry:3 as name the way install.sh does (storage
// dir and config dir bind-mounted, read-only root, as an unprivileged
// user: this test's), on a fixed loopback port, and returns the Platform
// managing it.
func startPlatform(t *testing.T, docker *client.Client, name string) *registry.Platform {
	t.Helper()
	ctx := t.Context()
	key, err := registry.LoadOrCreateKey(filepath.Join(t.TempDir(), registry.KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	signer := registry.NewSigner(key, registry.Config{Service: "dock-test-registry", Realm: "http://127.0.0.1:1/token"})
	cfgDir, storage := t.TempDir(), t.TempDir()
	if _, err := registry.WriteConfig(cfgDir, signer, registry.Storage{}); err != nil {
		t.Fatal(err)
	}
	ensureImage(t, docker, registry.Image)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hostPort := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()
	port := network.MustParsePort("5000/tcp")
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:        registry.Image,
			User:         fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
			Env:          []string{"OTEL_TRACES_EXPORTER=none"},
			ExposedPorts: network.PortSet{port: {}},
		},
		HostConfig: &container.HostConfig{
			PortBindings:   network.PortMap{port: {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: hostPort}}},
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
	host := "127.0.0.1:" + hostPort
	p := &registry.Platform{
		Name: host, Signer: signer, Client: &registry.Client{URL: "http://" + host, Signer: signer},
		Docker: docker, Container: name, StorageDir: storage, ConfigDir: cfgDir,
	}
	waitUp(t, "http://"+host)
	return p
}

// push tags src (pulled from Docker Hub if needed) as repo:tag in p's
// registry, pushes it with a push token and returns its digest.
func push(t *testing.T, docker *client.Client, p *registry.Platform, src, repo, tag string) string {
	t.Helper()
	ctx := t.Context()
	ensureImage(t, docker, src)
	ref := p.Name + "/" + repo + ":" + tag
	if _, err := docker.ImageTag(ctx, client.ImageTagOptions{Source: src, Target: ref}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = docker.ImageRemove(context.Background(), ref, client.ImageRemoveOptions{}) })
	res, err := docker.ImagePush(ctx, ref, client.ImagePushOptions{RegistryAuth: dockerrt.RegistryAuth(p.Token("test", repo, true))})
	if err != nil {
		t.Fatalf("push %s: %v", ref, err)
	}
	if err := res.Wait(ctx); err != nil {
		t.Fatalf("push %s: %v", ref, err)
	}
	digest, err := p.Digest(ctx, repo, tag)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

// pull removes repo@digest locally, then pulls it from p's registry with
// a pull token.
func pull(t *testing.T, docker *client.Client, rt *dockerrt.Runtime, p *registry.Platform, repo, digest string) {
	t.Helper()
	ref := p.Name + "/" + repo + "@" + digest
	_, _ = docker.ImageRemove(t.Context(), ref, client.ImageRemoveOptions{Force: true})
	t.Cleanup(func() { _, _ = docker.ImageRemove(context.Background(), ref, client.ImageRemoveOptions{Force: true}) })
	var logs bytes.Buffer
	if _, err := rt.Pull(t.Context(), ref, &logs, node.PullOptions{Token: p.Token("test node", repo, false)}); err != nil {
		t.Fatalf("pull %s: %v\n%s", ref, err, logs.String())
	}
}

func ensureImage(t *testing.T, docker *client.Client, ref string) {
	t.Helper()
	if _, err := docker.ImageInspect(t.Context(), ref); err == nil {
		return
	}
	res, err := docker.ImagePull(t.Context(), ref, client.ImagePullOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// waitUp waits until the registry at u answers.
func waitUp(t *testing.T, u string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := http.Get(u + "/v2/")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("registry at %s not answering: %v", u, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
