//go:build integration

package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
)

// A pull by tag gives its reference only an image the reference's own
// registry serves. With containerd's image store a registry need only
// answer a tag with a digest: content with that digest that is on the
// server is not fetched. A registry that holds nothing, answering with
// the digest of an image another org pulled here with its credentials,
// gets no name for that image: the pull fails and leaves nothing behind.
func TestTagPullNeedsItsRegistryToServeTheImage(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, 3*time.Minute)

	// One org's private image, pulled with its credentials.
	host := startPrivateRegistry(t, e, "bot", "s3cret")
	private := host + "/private/alpine:1"
	if _, err := e.cli.ImageTag(ctx, client.ImageTagOptions{Source: shellImage, Target: private}); err != nil {
		t.Fatal(err)
	}
	push, err := e.cli.ImagePush(ctx, private, client.ImagePushOptions{RegistryAuth: PasswordAuth(private, "bot", "s3cret")})
	if err != nil {
		t.Fatal(err)
	}
	if err := push.Wait(ctx); err != nil {
		t.Fatalf("push: %v", err)
	}
	if _, err := e.cli.ImageRemove(ctx, private, client.ImageRemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.cli.ImageRemove(context.Background(), private, client.ImageRemoveOptions{}) })
	id, err := e.Pull(ctx, private, nil, node.PullOptions{Username: "bot", Password: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	img, err := e.cli.ImageInspect(ctx, private)
	if err != nil {
		t.Fatalf("inspect %s: %v", private, err)
	}
	// What a registry answers a tag with: the image's digest, type and
	// size. containerd's image store has them as the image's descriptor;
	// the classic store, which fetches a tag's manifest whatever is on
	// the server, only keeps the digest its registry gave.
	digest, mediaType, size := "", "application/vnd.docker.distribution.manifest.v2+json", int64(1024)
	if d := img.Descriptor; d != nil {
		digest, mediaType, size = d.Digest.String(), d.MediaType, d.Size
	} else {
		for _, rd := range img.RepoDigests {
			if _, d, ok := strings.Cut(rd, "@"); ok {
				digest = d
			}
		}
	}
	if digest == "" {
		t.Fatalf("%s has no digest: %+v", private, img.RepoDigests)
	}

	// Another registry, which holds no content and answers its tag with
	// that image's digest.
	var mu sync.Mutex
	var seen []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.URL.Path == "/v2/":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodHead && r.URL.Path == "/v2/other/app/manifests/latest":
			w.Header().Set("Docker-Content-Digest", digest)
			w.Header().Set("Content-Type", mediaType)
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r) // no manifest body, no config, no layer
		}
	}))
	defer fake.Close()
	other := strings.TrimPrefix(fake.URL, "http://") + "/other/app:latest"
	t.Cleanup(func() { _, _ = e.cli.ImageRemove(context.Background(), other, client.ImageRemoveOptions{}) })

	got, err := e.Pull(ctx, other, nil) // anonymous
	mu.Lock()
	t.Logf("requests the registry without content saw: %v", seen)
	mu.Unlock()
	if err == nil {
		t.Fatalf("the pull of %s gave image %s (the first org's is %s) from a registry that served no content", other, got, id)
	}
	if _, ierr := e.cli.ImageInspect(ctx, other); ierr == nil {
		t.Fatalf("the failed pull left %s on the server: %v", other, err)
	}
	// The first org's own reference is as it was.
	if img, ierr := e.cli.ImageInspect(ctx, private); ierr != nil || img.ID != id {
		t.Fatalf("the first org's image after it: %v", ierr)
	}
}
