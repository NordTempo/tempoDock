package remote

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestViaOf(t *testing.T) {
	for addr, want := range map[string]Via{
		"100.95.4.67:41000":         ViaTailscale,
		"[fd7a:115c:a1e0::1]:41000": ViaTailscale,
		"[::ffff:100.64.0.1]:41000": ViaTailscale,
		"100.128.0.1:41000":         ViaInternet, // just past 100.64.0.0/10
		"192.168.10.50:41000":       ViaLAN,
		"[fd00:1:2::3]:41000":       ViaLAN,
		"127.0.0.1:41000":           ViaLAN,
		"2.31.84.232:41000":         ViaInternet,
		"[2a01:4f8:c17:1::1]:41000": ViaInternet,
	} {
		if got := ViaOf(net.TCPAddrFromAddrPort(netip.MustParseAddrPort(addr))); got != want {
			t.Errorf("%s: %s, want %s", addr, got, want)
		}
	}
}

func TestListenAddrs(t *testing.T) {
	if got := ListenAddrs(":7443", "2.31.84.232", []string{"cp.example.com:7443"})(); !slices.Equal(got, []string{"cp.example.com:7443"}) {
		t.Errorf("explicit: %v", got)
	}
	for _, listen := range []string{"100.64.0.10:7443", "cp.tailnet:7443"} {
		if got := ListenAddrs(listen, "2.31.84.232", nil)(); !slices.Equal(got, []string{listen}) {
			t.Errorf("%s: %v", listen, got)
		}
	}
	// Every interface: this machine's Tailscale address (if it has one)
	// first, then the public IP.
	for _, listen := range []string{":7443", "0.0.0.0:7443", "[::]:7443"} {
		got := ListenAddrs(listen, "2.31.84.232", nil)()
		if len(got) == 0 || got[len(got)-1] != "2.31.84.232:7443" {
			t.Fatalf("%s: %v", listen, got)
		}
		for _, a := range got[:len(got)-1] {
			if !tailnetV4.Contains(netip.MustParseAddrPort(a).Addr()) {
				t.Errorf("%s: %v", listen, got)
			}
		}
	}
}

// A node tries the control plane's addresses in order, falls through one
// that refuses, keeps the list the control plane sends in hello (and the
// address that worked), and the hub reports the path.
func TestNodeLearnsAddrsInHello(t *testing.T) {
	var addrs atomic.Pointer[[]string]
	h := newHarness(t, func(o *HubOptions) {
		inner := o.Addrs
		o.Addrs = func() []string {
			if a := addrs.Load(); a != nil {
				return *a
			}
			return inner()
		}
	})
	if via := h.hub.State(h.id).Via; via != ViaLAN {
		t.Fatalf("via = %q", via)
	}
	refused := refusedAddr(t)
	addrs.Store(&[]string{refused, "127.0.0.1:1"})
	h.proxy.cut()
	waitFor(t, "reconnect", func() bool { return h.connects.Load() == 2 && h.client.Online() })
	// The proxy's address wasn't in the list: it stays, last.
	want := []string{refused, "127.0.0.1:1", h.proxy.addr}
	waitFor(t, "addresses saved", func() bool {
		l, err := LoadLink(h.dataDir)
		return err == nil && slices.Equal(l.Addrs, want)
	})
	// The next connection gets through the refusing addresses to the proxy.
	h.proxy.cut()
	waitFor(t, "reconnect over the last address", func() bool { return h.connects.Load() == 3 && h.client.Online() })
}

// refusedAddr is a loopback address nothing listens on.
func refusedAddr(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// Connections still in their handshake are capped per client address: one
// past the cap is closed at once.
func TestHandshakeCap(t *testing.T) {
	hub := NewHub(HubOptions{Identity: mustIdentity(t), Store: newMemStore()})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() { _ = hub.Serve(ctx, ln); close(served) }()
	t.Cleanup(func() { cancel(); <-served })

	for range maxHandshakesPerClient {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
	}
	waitFor(t, "slots taken", func() bool {
		hub.shakeMu.Lock()
		defer hub.shakeMu.Unlock()
		return hub.shaking == maxHandshakesPerClient
	})
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := c.Read(make([]byte, 1)); n != 0 || err == nil || isTimeout(err) {
		t.Fatalf("connection past the cap: read %d, %v", n, err)
	}
}

func TestCheckReach(t *testing.T) {
	h := newHarness(t)
	tok := h.mint("lab3", time.Hour)
	if addr, err := CheckReach(context.Background(), tok); err != nil || addr != h.proxy.addr {
		t.Fatalf("reach = %q, %v", addr, err)
	}
	// The token stays unused.
	if _, err := Join(context.Background(), tok, t.TempDir(), testFacts); err != nil {
		t.Fatal(err)
	}

	unreachable := JoinToken{Addrs: []string{refusedAddr(t)}, ControlPlane: h.hub.Fingerprint()}
	_, err := CheckReach(context.Background(), unreachable.String())
	if err == nil || !strings.Contains(err.Error(), "can't reach the control plane at "+unreachable.Addrs[0]) {
		t.Fatalf("unreachable: %v", err)
	}
}
