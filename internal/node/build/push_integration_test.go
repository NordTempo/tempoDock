//go:build integration

package build

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
	dockerrt "github.com/NordTempo/tempoDock/internal/node/docker"
	"github.com/NordTempo/tempoDock/internal/registry"
)

// TestPushPullPrune runs the registry flow of ADR 0014 against a real
// registry:3 configured by internal/registry on a loopback port, as every
// server sees it: build and push with a push token, pull by digest with a
// pull token, prune and garbage collect. It uses its own BuildKit
// container and registry, so it never touches dock-buildkit.
func TestPushPullPrune(t *testing.T) {
	docker := dockerClient(t)
	ctx := t.Context()
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	name := "dock-regtest-" + hex.EncodeToString(suffix)

	// The token endpoint, for Docker pull paths that follow the registry's
	// challenge with credentials. The Docker daemon reaches it on loopback.
	var realmCalls atomic.Int32
	realm := httptest.NewUnstartedServer(nil)
	realm.Start()
	t.Cleanup(realm.Close)
	key, err := registry.LoadOrCreateKey(t.TempDir() + "/" + registry.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	signer := registry.NewSigner(key, registry.Config{Service: "dock-test-registry", Realm: realm.URL + "/token"})
	realm.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		realmCalls.Add(1)
		signer.Handler().ServeHTTP(w, r)
	})

	regURL := startRegistry(t, docker, name, signer)
	reg := &registry.Client{URL: regURL, Signer: signer}
	regHost := strings.TrimPrefix(regURL, "http://")

	b := newBuilder(t, docker, 1024)
	b.name, b.cacheVolume = name+"-buildkit", name+"-buildkit-cache"
	t.Cleanup(func() {
		_, _ = docker.ContainerRemove(context.Background(), b.name, client.ContainerRemoveOptions{Force: true})
		_, _ = docker.VolumeRemove(context.Background(), b.cacheVolume, client.VolumeRemoveOptions{Force: true})
	})

	const repo = "test/app"
	push := &node.PushTarget{
		Ref:   regHost + "/" + repo + ":staging-1",
		Token: signer.Mint("test builder", registry.PushTokenTTL, registry.Pull(repo)),
	}
	src := map[string]string{"Dockerfile": "FROM busybox:latest\nRUN echo " + name + " > /built\n"}
	if _, _, err := build(t, b, node.BuildRequest{Source: sourceTar(t, src), Tag: name + "/local:1", Push: push}); err == nil {
		t.Fatal("pushed with a pull-only token")
	}
	push.Token = signer.Mint("test builder", registry.PushTokenTTL, registry.Push(repo))
	_, logs, err := build(t, b, node.BuildRequest{Source: sourceTar(t, src), Tag: name + "/local:1", Push: push})
	if err != nil {
		t.Fatalf("build and push: %v\n%s", err, tail(logs, 30))
	}
	digest, err := reg.Digest(ctx, repo, "staging-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs, "("+digest+")") {
		t.Errorf("build log doesn't report the pushed digest %s:\n%s", digest, tail(logs, 5))
	}
	if _, err := docker.ImageInspect(ctx, push.Ref); err == nil {
		t.Error("the push target name was left on the image")
	}

	// Pull on a "different server": drop the local build first.
	if _, err := docker.ImageRemove(ctx, name+"/local:1", client.ImageRemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	rt, err := dockerrt.New(netip.MustParsePrefix("10.250.0.0/16"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	ref := regHost + "/" + repo + "@" + digest
	t.Cleanup(func() {
		_, _ = docker.ImageRemove(context.Background(), ref, client.ImageRemoveOptions{Force: true})
	})
	if _, err := rt.Pull(ctx, ref, nil); err == nil {
		t.Fatal("pulled without a token")
	}
	if _, err := rt.Pull(ctx, ref, nil, node.PullOptions{Token: signer.Mint("test node", registry.TokenTTL, registry.Pull("test/other"))}); err == nil {
		t.Fatal("pulled with a token for another repository")
	}
	var pullLog bytes.Buffer
	id, err := rt.Pull(ctx, ref, &pullLog, node.PullOptions{Token: signer.Mint("test node", registry.TokenTTL, registry.Pull(repo))})
	if err != nil {
		t.Fatalf("pull with token: %v\n%s", err, pullLog.String())
	}
	if id == "" || strings.Contains(pullLog.String(), "already on the server") {
		t.Fatalf("pull didn't fetch the image: %q\n%s", id, pullLog.String())
	}
	if ok, err := rt.HasImage(ctx, ref); !ok || err != nil {
		t.Fatalf("HasImage(%s) = %v, %v", ref, ok, err)
	}
	t.Logf("token endpoint calls: %d", realmCalls.Load())

	// Prune keeps referenced digests and deletes the rest.
	if deleted, err := reg.Prune(ctx, []string{ref}); err != nil || len(deleted) != 0 {
		t.Fatalf("prune with the digest kept: deleted %v, %v", deleted, err)
	}
	deleted, err := reg.Prune(ctx, []string{"sha256:" + strings.Repeat("0", 64)})
	if err != nil || len(deleted) != 1 || deleted[0].Digest != digest {
		t.Fatalf("prune: deleted %v, %v", deleted, err)
	}
	if _, err := reg.Digest(ctx, repo, "staging-1"); err == nil {
		t.Fatal("pruned manifest still resolves")
	}

	var gcLog bytes.Buffer
	if err := registry.GarbageCollect(ctx, docker, name, io.MultiWriter(&gcLog, testWriter{t})); err != nil {
		t.Fatalf("garbage collect: %v\n%s", err, gcLog.String())
	}
	if !strings.Contains(gcLog.String(), "blob eligible for deletion: "+digest) {
		t.Errorf("the pruned manifest's blob wasn't collected:\n%s", tail(gcLog.String(), 10))
	}
	waitForRegistry(t, registryURL(t, docker, name)) // running again
}

// startRegistry runs registry:3 as name, configured by registry.WriteConfig
// and locked down like install.sh runs it, and returns its URL on a
// loopback port.
func startRegistry(t *testing.T, docker *client.Client, name string, signer *registry.Signer) string {
	t.Helper()
	ctx := t.Context()
	dir := t.TempDir()
	if _, err := registry.WriteConfig(dir, signer); err != nil {
		t.Fatal(err)
	}
	port := network.MustParsePort("5000/tcp")
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:        registry.Image,
			ExposedPorts: network.PortSet{port: {}},
		},
		HostConfig: &container.HostConfig{
			PortBindings:   network.PortMap{port: {{HostIP: netip.MustParseAddr("127.0.0.1")}}},
			Binds:          []string{dir + ":/etc/distribution:ro"},
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
	u := registryURL(t, docker, name)
	waitForRegistry(t, u)
	return u
}

// registryURL returns the registry's URL on its published loopback port,
// which changes when it restarts.
func registryURL(t *testing.T, docker *client.Client, name string) string {
	t.Helper()
	inspect, err := docker.ContainerInspect(t.Context(), name, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return "http://127.0.0.1:" + inspect.Container.NetworkSettings.Ports[network.MustParsePort("5000/tcp")][0].HostPort
}

// waitForRegistry waits until the registry at u answers, which it does
// with 401 (token required) once it is up.
func waitForRegistry(t *testing.T, u string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := http.Get(u + "/v2/")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusUnauthorized {
				return
			}
			err = errors.New(resp.Status)
		}
		if time.Now().After(deadline) {
			t.Fatalf("registry at %s not answering: %v", u, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
