package remote

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// A forwarded connection that arrives while the server is at its cap of
// open connections is closed, and the listener goes on accepting: the
// ones after it are served once a slot is free.
func TestForwardOverTheDialLimit(t *testing.T) {
	h := newHarness(t)
	h.rt.DialTo(newTCPServer(t, serveEcho).addr)
	ct := h.startService("db-1")
	ln, err := h.client.Forward(context.Background(), 20000)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()

	var held []net.Conn
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()
	for range maxDials {
		held = append(held, h.dial(context.Background(), ct.ID))
	}
	over, err := net.Dial("tcp", h.rt.ForwardAddr(20000))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = over.Close() }()
	_ = over.SetReadDeadline(time.Now().Add(forwardClaimTimeout / 2))
	if _, err := over.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("connection over the limit read %v, want it closed", err)
	}

	_ = held[0].Close()
	held = held[1:]
	in, err := net.Dial("tcp", h.rt.ForwardAddr(20000))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	select {
	case c := <-accepted:
		_ = c.Close()
	case <-time.After(forwardClaimTimeout):
		t.Fatal("the listener stopped accepting after a connection over the limit")
	}
}
