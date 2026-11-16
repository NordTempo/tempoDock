//go:build integration

package docker

import (
	"context"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"golang.org/x/crypto/bcrypt"

	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/registry"
)

// A private registry (htpasswd, as a self-hosted one or ghcr.io's basic
// auth): an anonymous pull and a wrong password are refused, the right
// username and password pull the image (ADR 0051).
func TestPullFromPrivateRegistry(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, 3*time.Minute)
	host := startPrivateRegistry(t, e, "bot", "s3cret")

	ref := host + "/private/alpine:1"
	if _, err := e.cli.ImageTag(ctx, client.ImageTagOptions{Source: shellImage, Target: ref}); err != nil {
		t.Fatal(err)
	}
	push, err := e.cli.ImagePush(ctx, ref, client.ImagePushOptions{RegistryAuth: PasswordAuth(ref, "bot", "s3cret")})
	if err != nil {
		t.Fatal(err)
	}
	if err := push.Wait(ctx); err != nil {
		t.Fatalf("push: %v", err)
	}
	if _, err := e.cli.ImageRemove(ctx, ref, client.ImageRemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.cli.ImageRemove(context.Background(), ref, client.ImageRemoveOptions{}) })

	// Core's pullRefused tells refusals by these words, to say how to fix
	// them.
	// Docker's words for an anonymous refusal vary by version.
	if _, err := e.Pull(ctx, ref, nil); err == nil || !strings.Contains(err.Error(), "denied") && !strings.Contains(err.Error(), "no basic auth credentials") {
		t.Fatalf("anonymous pull = %v, want access denied", err)
	}
	if _, err := e.Pull(ctx, ref, nil, node.PullOptions{Username: "bot", Password: "wrong"}); err == nil || !strings.Contains(strings.ToLower(err.Error()), "unauthorized") {
		t.Fatalf("pull with a wrong password = %v, want unauthorized", err)
	}
	id, err := e.Pull(ctx, ref, nil, node.PullOptions{Username: "bot", Password: "s3cret"})
	if err != nil || !strings.HasPrefix(id, "sha256:") {
		t.Fatalf("pull with credentials = %q, %v", id, err)
	}

	// The image is now on the server under its digest. Another org, with no
	// credential, naming that digest still has to get past the registry.
	img, err := e.cli.ImageInspect(ctx, ref)
	if err != nil || len(img.RepoDigests) == 0 {
		t.Fatalf("pulled image's digests: %v, %v", img.RepoDigests, err)
	}
	pinned := img.RepoDigests[0]
	if _, err := e.Pull(ctx, pinned, nil); err == nil {
		t.Fatalf("anonymous pull of %s used the image another credential pulled", pinned)
	}
	if got, err := e.Pull(ctx, pinned, nil, node.PullOptions{Username: "bot", Password: "s3cret"}); err != nil || got != id {
		t.Fatalf("pull of %s with credentials = %q, %v; want the image already here", pinned, got, err)
	}
}

// startPrivateRegistry runs Dock's registry image with htpasswd auth for
// one user on a loopback port (which Docker pulls from over plain HTTP)
// and returns its host:port.
func startPrivateRegistry(t *testing.T, e *testEnv, user, password string) string {
	t.Helper()
	ctx := t.Context()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	auth := t.TempDir()
	if err := os.WriteFile(filepath.Join(auth, "htpasswd"), []byte(user+":"+string(hash)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cli.ImageInspect(ctx, registry.Image); err != nil {
		pull, err := e.cli.ImagePull(ctx, registry.Image, client.ImagePullOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := pull.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	port := network.MustParsePort("5000/tcp")
	created, err := e.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: e.name("registry"),
		Config: &container.Config{
			Image: registry.Image,
			Env: []string{
				"REGISTRY_AUTH=htpasswd",
				"REGISTRY_AUTH_HTPASSWD_REALM=test",
				"REGISTRY_AUTH_HTPASSWD_PATH=/auth/htpasswd",
			},
			ExposedPorts: network.PortSet{port: {}},
		},
		HostConfig: &container.HostConfig{
			PortBindings: network.PortMap{port: {{HostIP: netip.MustParseAddr("127.0.0.1")}}},
			Binds:        []string{auth + ":/auth:ro"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.cli.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	})
	if _, err := e.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	inspect, err := e.cli.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	host := "127.0.0.1:" + inspect.Container.NetworkSettings.Ports[port][0].HostPort
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := http.Get("http://" + host + "/v2/")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusUnauthorized {
				return host
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("registry at %s not answering: %v", host, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
