//go:build integration

package registry_test

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/registry"
	"github.com/NordTempo/tempoDock/internal/registry/gateway"
	"github.com/NordTempo/tempoDock/internal/s3"
)

// TestPruneOverStuckObject prunes a registry in a bucket where an old
// release's manifest hangs on every read, as dock/p8_e34_s37:1 did on
// prod (2026-11-17). While its tag can't be read either, the prune
// deletes everything else and names the one it skipped. Once only the
// manifest's object is stuck, the prune finds its digest from the tag's
// link and deletes it, and garbage collection removes the stuck object.
func TestPruneOverStuckObject(t *testing.T) {
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	name := "dock-regprunetest-" + hex.EncodeToString(suffix)

	minio := startMinIO(t, docker, name+"-minio")
	faults := &stallProxy{}
	bucket := faults.start(t, minio)
	key, err := registry.LoadOrCreateKey(filepath.Join(t.TempDir(), registry.KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	signer := registry.NewSigner(key, registry.Config{Service: "dock-test-registry", Realm: "http://127.0.0.1:1/token"})
	access, secret := registry.GatewayKeys(signer)
	gw, err := gateway.Open(gateway.Options{
		Dir: t.TempDir(), AccessKey: access, Secret: secret,
		Timing: gateway.Timing{ReadTimeout: time.Second, MetaTimeout: time.Second, RetryMin: 200 * time.Millisecond, RetryMax: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gw.Close)
	p := startHostPlatform(t, docker, name, signer, gw)
	if err := p.UseStorage(ctx, bucket); err != nil {
		t.Fatal(err)
	}
	// As on prod, a call gives up before the gateway's three attempts at
	// one object do (60 s against 3 × 20 s).
	p.Client.HTTP = &http.Client{Timeout: 2 * time.Second}

	pushing := p.PushStarted()
	stuck := push(t, docker, p, "busybox:latest", "test/stuck", "1")
	push(t, docker, p, "hello-world:latest", "test/gone", "1")
	kept := push(t, docker, p, "alpine:latest", "test/kept", "1")
	pushing()
	if err := gw.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	sum := strings.TrimPrefix(stuck, "sha256:")
	manifest := registry.BucketRoot + "/docker/registry/v2/blobs/sha256/" + sum[:2] + "/" + sum + "/data"
	tagLink := "/repositories/test/stuck/_manifests/tags/1/current/link"

	// The manifest and its tag's link hang.
	faults.set(func(f *stallProxy) { f.stallPaths = []string{manifest, tagLink} })
	n, skipped, err := p.Prune(ctx, []string{kept})
	if err != nil || n != 1 || len(skipped) != 1 || !strings.Contains(skipped[0].Error(), "test/stuck:1") {
		t.Fatalf("prune with test/stuck:1 unreadable = %d, skipped %v, %v; want test/gone deleted and test/stuck:1 skipped", n, skipped, err)
	}
	if _, err := p.Digest(ctx, "test/gone", "1"); err == nil {
		t.Fatal("test/gone:1 is still there")
	}

	// Only the manifest hangs: the tag's link names it.
	faults.set(func(f *stallProxy) { f.stallPaths = []string{manifest} })
	if n, skipped, err := p.Prune(ctx, []string{kept}); err != nil || n != 1 || len(skipped) != 0 {
		t.Fatalf("prune with the manifest stuck = %d, skipped %v, %v; want test/stuck deleted", n, skipped, err)
	}
	if err := p.GarbageCollect(ctx); err != nil {
		t.Fatalf("garbage collect after the prune: %v", err)
	}
	direct := &s3.Client{Bucket: *minio}
	if has, err := direct.HasPrefix(ctx, manifest); err != nil || has {
		t.Fatalf("the stuck manifest is still in the bucket: %v, %v", has, err)
	}
	if d, err := p.Digest(ctx, "test/kept", "1"); err != nil || d != kept {
		t.Fatalf("the kept image after the prune: %q, %v", d, err)
	}
}
