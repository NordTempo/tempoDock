package remote

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A control plane that moved (ADR 0021): SetControlPlane points the node
// at its new address and the running node follows at its next attempt,
// without a restart or a new join. The pinned key and identity stay.
func TestSetControlPlaneWhileRunning(t *testing.T) {
	h := newHarness(t)
	before, err := LoadLink(h.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gone := ln.Addr().String()
	_ = ln.Close()

	// The old address goes dead: the node can't get back.
	if _, err := SetControlPlane(h.dataDir, gone); err != nil {
		t.Fatal(err)
	}
	h.proxy.cut()
	waitFor(t, "node to go offline", func() bool { return h.hub.State(h.id).Status == StatusOffline })
	time.Sleep(100 * time.Millisecond) // several attempts at the dead address
	if h.hub.State(h.id).Status != StatusOffline {
		t.Fatal("the node reconnected to an address it wasn't given")
	}

	// The new address: the node follows on its own.
	link, err := SetControlPlane(h.dataDir, h.proxy.addr)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "node to reconnect at the new address", func() bool { return h.hub.State(h.id).Status == StatusOnline })
	if link.ServerID != before.ServerID || link.Server != before.Server || link.ControlPlaneKey != before.ControlPlaneKey ||
		len(link.Addrs) != 1 || link.Addrs[0] != h.proxy.addr {
		t.Fatalf("link = %+v, was %+v", link, before)
	}
	if fi, err := os.Stat(filepath.Join(h.dataDir, StateFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%s: %v, %v", StateFile, fi.Mode(), err)
	}
}

// A node that never joined has nothing to point anywhere.
func TestSetControlPlaneNeedsAJoin(t *testing.T) {
	if _, err := SetControlPlane(t.TempDir(), "100.64.0.20:7443"); err == nil {
		t.Fatal("set the control plane of a node that never joined")
	}
}
