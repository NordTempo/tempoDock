//go:build !dockcli

package cli

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/api"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/node/fake"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store/storetest"
)

// A server joins with `dock node join`, `dock node` connects it, and a
// deploy placed there runs on the node's runtime over the real link. The
// node runs the fake runtime; the control plane is core on a temp DB with
// its hub, wired as `dock serve` wires them.
func TestNodeJoinAndRun(t *testing.T) {
	setup(t)
	t.Setenv("DOCK_FAKE_RUNTIME", "1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	quiet := slog.New(slog.DiscardHandler)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Node.Listen = ln.Addr().String()
	st := storetest.Open(t, filepath.Join(cfg.DataDir, "dock.db"))
	defer func() { _ = st.Close() }()
	box, _ := secrets.New(bytes.Repeat([]byte{1}, secrets.KeySize))
	id, err := remote.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	c, hub, err := startCore(ctx, core.Options{Config: cfg, Store: st, Secrets: box, Runtime: fake.New(), Logger: quiet, CutoverDrain: time.Nanosecond}, id)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	go func() { _ = hub.Serve(ctx, ln) }()
	srv := httptest.NewServer(api.New(c, api.Options{Logger: quiet}))
	defer srv.Close()
	// An org's admin token and a platform token beside it (ADR 0080):
	// server commands go to the platform's, project ones to the org's.
	ptok, err := c.CreatePlatformToken(ctx, sys, apitypes.CreatePlatformTokenRequest{Name: "operator", Role: apitypes.PlatformOperator})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := c.CreateToken(ctx, sys, apitypes.CreateTokenRequest{Name: "admin", Scope: apitypes.ScopeAdmin})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(clientconfig.EnvURL, srv.URL)
	t.Setenv(clientconfig.EnvToken, tok.Secret)
	t.Setenv(clientconfig.EnvPlatformToken, ptok.Secret)

	if code, _, stderr := exec(t, "server", "add", "homelab"); code != int(output.ExitUsage) || !strings.Contains(stderr, "--non-production") {
		t.Fatalf("add without a kind: exit %d %s", code, stderr)
	}
	code, stdout, stderr := exec(t, "server", "add", "homelab", "--non-production", "--builder")
	if code != 0 || !strings.Contains(stdout, "install.sh --node") || !strings.Contains(stdout+stderr, "accepts non-production and builds images") {
		t.Fatalf("add: exit %d\n%s%s", code, stdout, stderr)
	}
	if code, list, stderr := exec(t, "server", "list"); code != 0 || !strings.Contains(list, "BUILDS") ||
		!slices.ContainsFunc(strings.Split(list, "\n"), func(l string) bool { return strings.HasPrefix(l, "homelab") && strings.Contains(l, " yes ") }) {
		t.Fatalf("server list: exit %d\n%s%s", code, list, stderr)
	}
	var token string
	for f := range strings.FieldsSeq(stdout) {
		if v, ok := strings.CutPrefix(f, "NODE_JOIN_TOKEN="); ok {
			token = v
		}
	}
	if !strings.HasPrefix(token, "dockjoin1_") {
		t.Fatalf("no join token in the install command:\n%s", stdout)
	}
	if s := findServer(t, "homelab"); s.Status != apitypes.ServerJoining || !s.Builder {
		t.Fatalf("after add: %+v", s)
	}

	nodeCfg := writeConfig(t, t.TempDir(), "internal_listen = \"127.0.0.1:0\"\n[registry]\nlisten = \"127.0.0.1:0\"\n")
	// As install.sh joins: the token on stdin, never in argv.
	code, stdout, stderr = execStdin(t, token+"\n", "node", "join", "--config", nodeCfg, "--token-file", "-")
	if code != 0 || !strings.Contains(stdout, "as server homelab") {
		t.Fatalf("join: exit %d\n%s%s", code, stdout, stderr)
	}
	if code, _, stderr := exec(t, "node", "join", "--config", nodeCfg, token); code == 0 {
		t.Fatalf("a used token joined again: %s", stderr)
	}

	ncfg, err := config.Load(nodeCfg)
	if err != nil {
		t.Fatal(err)
	}
	nodeCtx, stopNode := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- runNode(nodeCtx, ncfg, quiet) }()
	if s := waitServer(t, "homelab", apitypes.ServerOnline); s.Ingress != apitypes.IngressDirect || s.MemoryBytes == 0 {
		t.Errorf("facts from hello: %+v", s)
	}

	// A non-production environment pinned to the node deploys there.
	for _, args := range [][]string{
		{"project", "create", "shop"},
		{"project", "pin", "shop", "--kind", "non-production", "--server", "homelab"},
		{"environment", "create", "staging", "-p", "shop"},
	} {
		if code, _, stderr := exec(t, args...); code != 0 {
			t.Fatalf("%v: %s", args, stderr)
		}
	}
	rel := ok[apitypes.Release](t, "deploy", "-p", "shop", "-e", "staging", "-s", "web", "--image", "app:1")
	if rel.Status != apitypes.ReleaseActive {
		t.Fatalf("release on the node: %s %s", rel.Status, rel.Error)
	}

	stopNode()
	if err := <-done; err != nil {
		t.Fatalf("node stopped with %v", err)
	}
	waitServer(t, "homelab", apitypes.ServerOffline)
}

// findServer returns the named server from `dock server list`.
func findServer(t *testing.T, name string) apitypes.Server {
	t.Helper()
	for _, s := range ok[[]apitypes.Server](t, "server", "list") {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no server %s", name)
	return apitypes.Server{}
}

// waitServer waits until the named server has status want.
func waitServer(t *testing.T, name string, want apitypes.ServerStatus) apitypes.Server {
	t.Helper()
	var s apitypes.Server
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if s = findServer(t, name); s.Status == want {
			return s
		}
	}
	t.Fatalf("server %s is %q, want %q", name, s.Status, want)
	return s
}
