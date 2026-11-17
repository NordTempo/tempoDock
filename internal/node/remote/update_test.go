package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/update"
	"github.com/NordTempo/tempoDock/internal/version"
)

// callsInFlight is the number of calls the control plane has open on the
// node's current session: a call counts until its reply was read.
func (h *harness) callsInFlight() int {
	h.hub.mu.Lock()
	s := h.hub.sessions[h.id]
	h.hub.mu.Unlock()
	if s == nil {
		return 0
	}
	return s.cc.InFlight()
}

// A release goes over the link to the node, which checks it, writes it
// and asks its updater; once the node is back on the version, UpdateNode
// returns.
func TestUpdateNode(t *testing.T) {
	old := version.Version
	t.Cleanup(func() { version.Version = old })
	h := newHarness(t)
	unit := filepath.Join(t.TempDir(), "dock-update.path")
	withUpdater := func(n *Node) { n.UpdaterUnit = unit }

	staged := t.TempDir()
	sums := update.Sums{}
	// The signature goes along; the node's updater checks it.
	for _, name := range append(update.Assets("v9.9.9", runtime.GOARCH), update.SigAsset) {
		b := []byte(strings.Repeat(name, 1000))
		if err := os.WriteFile(filepath.Join(staged, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
		if name != update.SumsAsset && name != update.SigAsset { // as Stage returns them
			sum := sha256.Sum256(b)
			sums[name] = hex.EncodeToString(sum[:])
		}
	}
	u := NodeUpdate{Version: "v9.9.9", RequestedBy: "saile", Dir: staged, Sums: sums}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Without an updater the node refuses, and nothing restarts.
	if _, err := h.hub.UpdateNode(ctx, h.id, u); err == nil || !strings.Contains(err.Error(), "takes no updates") {
		t.Fatalf("a node without an updater: %v", err)
	}
	connects := h.connects.Load()
	h.stopNode()
	h.startNode(withUpdater)
	waitFor(t, "reconnect", func() bool { return h.connects.Load() > connects && h.client.Online() })
	if _, err := h.hub.UpdateNode(ctx, h.id, u); err == nil || !strings.Contains(err.Error(), "no updater") {
		t.Fatalf("a node whose updater isn't installed: %v", err)
	}
	if err := os.WriteFile(unit, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	// The node's "updater": once the request is there and the control
	// plane has the node's reply (no call left in flight on the link),
	// restart the node as the new version. Restarting the moment the
	// request appears could cut the reply off; the real updater verifies
	// the release first.
	dir := update.Dir(h.dataDir)
	go func() {
		for !update.Exists(dir, update.RequestFile) || h.callsInFlight() > 0 {
			time.Sleep(5 * time.Millisecond)
		}
		version.Version = "v9.9.9"
		h.stopNode()
		h.startNode(withUpdater)
	}()
	hello, err := h.hub.UpdateNode(ctx, h.id, u)
	if err != nil || hello.Version != "v9.9.9" {
		t.Fatalf("UpdateNode = %+v, %v", hello, err)
	}
	var req update.Request
	if err := update.ReadJSON(dir, update.RequestFile, &req); err != nil || req.Version != "v9.9.9" || req.RequestedBy != "saile" {
		t.Errorf("request %+v, %v", req, err)
	}
	got, err := update.HashFile(filepath.Join(update.ReleaseDir(h.dataDir, "v9.9.9"), update.BinaryAsset(runtime.GOARCH)))
	if err != nil || got != sums[update.BinaryAsset(runtime.GOARCH)] {
		t.Errorf("received binary %s, %v", got, err)
	}
	if !update.Exists(update.ReleaseDir(h.dataDir, "v9.9.9"), update.SigAsset) {
		t.Errorf("the node didn't get %s", update.SigAsset)
	}
	var c update.Connected
	waitFor(t, "the connected file", func() bool {
		return update.ReadJSON(dir, update.ConnectedFile, &c) == nil && c.Version == "v9.9.9"
	})
}
