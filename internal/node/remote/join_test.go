package remote

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestJoin(t *testing.T) {
	h := newHarness(t)

	// The node's key and link state are on disk, owner only.
	for _, f := range []string{NodeKeyFile, StateFile} {
		fi, err := os.Stat(filepath.Join(h.dataDir, f))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v, %v", f, fi.Mode(), err)
		}
	}
	link, err := LoadLink(h.dataDir)
	if err != nil || link.ServerID != h.id || link.Server != "homelab" || link.ControlPlaneKey != h.hub.Fingerprint() ||
		len(link.Addrs) != 1 || link.Addrs[0] != h.proxy.addr {
		t.Fatalf("link = %+v, %v", link, err)
	}
	id, err := loadIdentity(filepath.Join(h.dataDir, NodeKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	// The store got the node's key and its facts with the join.
	if srv := h.store.server(h.id); srv.key != id.Fingerprint() || !reflect.DeepEqual(srv.hello.Facts, testFacts) ||
		srv.hello.Protocol != Protocol {
		t.Fatalf("stored server = %+v", srv)
	}
}

func TestJoinRejected(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	t.Run("reused token", func(t *testing.T) {
		tok := h.mint("staging", time.Hour)
		if _, err := Join(ctx, tok, t.TempDir(), testFacts); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		_, err := Join(ctx, tok, dir, testFacts)
		if err == nil || !strings.Contains(err.Error(), "already used") {
			t.Fatalf("second join = %v", err)
		}
		assertNoFiles(t, dir)
	})

	t.Run("expired token", func(t *testing.T) {
		dir := t.TempDir()
		_, err := Join(ctx, h.mint("old", -time.Minute), dir, testFacts)
		if err == nil || !strings.Contains(err.Error(), "expired") {
			t.Fatalf("join = %v", err)
		}
		assertNoFiles(t, dir)
	})

	t.Run("unknown secret", func(t *testing.T) {
		tok, _ := h.hub.MintJoinToken() // never stored
		if _, err := Join(ctx, tok.String(), t.TempDir(), testFacts); err == nil || !strings.Contains(err.Error(), "invalid") {
			t.Fatalf("join = %v", err)
		}
	})

	t.Run("garbled token", func(t *testing.T) {
		tok := h.mint("x", time.Hour)
		for _, bad := range []string{"", "dockjoin1_", tok[:len(tok)-20], strings.Replace(tok, "dockjoin1_", "dockjoin2_", 1)} {
			if _, err := Join(ctx, bad, t.TempDir(), testFacts); err == nil || !strings.Contains(err.Error(), "invalid join token") {
				t.Fatalf("join %q = %v", bad, err)
			}
		}
	})
}

func TestJoinRefusesAnImpostor(t *testing.T) {
	h := newHarness(t)
	// An impostor answers at the token's address (DNS or routing hijacked)
	// with its own key.
	impostorStore := newMemStore()
	impostor := NewHub(HubOptions{Identity: mustIdentity(t), Store: impostorStore})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = impostor.Serve(ctx, ln) }()

	real, err := ParseJoinToken(h.mint("homelab-2", time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	real.Addrs = []string{ln.Addr().String()}
	dir := t.TempDir()
	_, err = Join(context.Background(), real.String(), dir, testFacts)
	if err == nil || !strings.Contains(err.Error(), "does not match the pinned") {
		t.Fatalf("join = %v", err)
	}
	// The handshake failed, so the impostor never asked for the secret.
	if impostorStore.joinCalls.Load() != 0 {
		t.Fatal("the impostor got a join request")
	}
	assertNoFiles(t, dir)
	// The real token is still good.
	real.Addrs = []string{h.proxy.addr}
	if _, err := Join(context.Background(), real.String(), t.TempDir(), testFacts); err != nil {
		t.Fatal(err)
	}
}

func TestJoinToken(t *testing.T) {
	id := mustIdentity(t)
	tok, err := newJoinToken(id.Fingerprint(), []string{"100.64.0.10:7443", "[fd7a::1]:7443"})
	if err != nil {
		t.Fatal(err)
	}
	s := tok.String()
	if !strings.HasPrefix(s, "dockjoin1_") || len(s) > 160 {
		t.Fatalf("token %q (%d chars)", s, len(s))
	}
	got, err := ParseJoinToken(" " + s + "\n")
	if err != nil || got.ControlPlane != id.Fingerprint() || got.Secret != tok.Secret || len(got.Addrs) != 2 || got.Addrs[1] != "[fd7a::1]:7443" {
		t.Fatalf("parsed %+v, %v", got, err)
	}
	if got.SecretHash() != tok.SecretHash() || strings.Contains(got.SecretHash(), string(tok.Secret[:4])) {
		t.Fatal("hash")
	}
	if _, err := newJoinToken(id.Fingerprint(), nil); err == nil {
		t.Fatal("token without addresses")
	}
}

func TestKeyFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), ControlPlaneKeyFile)
	a, err := LoadOrCreateIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreateIdentity(path)
	if err != nil || a.Fingerprint() != b.Fingerprint() {
		t.Fatalf("reloaded %s, %v; want %s", b.Fingerprint(), err, a.Fingerprint())
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	_ = os.Chmod(path, 0o644)
	if _, err := LoadOrCreateIdentity(path); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("world-readable key = %v", err)
	}
}

func assertNoFiles(t *testing.T, dir string) {
	t.Helper()
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a failed join left files: %v", entries)
	}
}
