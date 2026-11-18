package remote

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Forwarded connections have a budget of their own: a server whose dials
// are all taken (the data panel's) still accepts them. One that arrives
// while the server is at its cap of forwarded connections is closed, and
// the listener goes on accepting: the ones after it are served once a
// slot is free.
func TestForwardOverItsLimit(t *testing.T) {
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
	first, err := net.Dial("tcp", h.rt.ForwardAddr(20000))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	select {
	case c := <-accepted:
		held = append([]net.Conn{c}, held...)
	case <-time.After(forwardClaimTimeout):
		t.Fatal("a server with every dial taken accepted no forwarded connection")
	}
	// The rest of the budget goes to connections forwarded to this server.
	for range maxForwards - 1 {
		c, err := h.client.Dial(context.Background(), node.DialRequest{ContainerID: ct.ID, Port: 5432, Forwarded: true})
		if err != nil {
			t.Fatal(err)
		}
		held = append([]net.Conn{c}, held...)
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
