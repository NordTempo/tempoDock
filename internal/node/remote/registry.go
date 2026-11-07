package remote

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// Registry over the link (ADR 0014 amendment, 2026-10-22). Every node
// serves the control plane's registry on its own 127.0.0.1:5000, so image
// references are the same on every server and the registry is never on a
// network.
//
// Docker's connections start on the node, but on the link only the control
// plane makes calls. So the control plane keeps registryIdle "registry"
// calls waiting on each node. When Docker (or BuildKit) connects to the
// node's listener, the node answers one waiting call with 200 and the call
// becomes a raw TCP tunnel, exactly like Dial in reverse: the response
// body carries Docker's bytes up, the request body carries the registry's
// bytes down, and the control plane connects it to its local registry. It
// then opens another waiting call. One tunnel per TCP connection: HTTP is
// untouched (Authorization included), blobs stream with HTTP/2 flow
// control end to end, and canceling on either side closes the other.
//
// A compromised node can reach the registry and nothing else on the
// control plane: the target is fixed, and the registry wants a token the
// control plane mints per repository and call.
const (
	opRegistry = "registry"
	// registryIdle is how many tunnel calls wait on each node, so a new
	// connection never waits for a round trip.
	registryIdle = 2
	// maxRegistryConns caps open tunnels per node (Docker pulls three
	// layers at a time). Connections beyond it wait for a free one.
	maxRegistryConns = 16
	// registryWait is how long the node holds a new connection for a
	// tunnel before closing it (the control plane is offline or every
	// tunnel is busy).
	registryWait = 10 * time.Second
	// registryLinger bounds how long a tunnel waits for the node's side to
	// finish after the registry closed its side.
	registryLinger = 10 * time.Second
)

// registryPool keeps tunnel calls waiting on one node, for the hub.
type registryPool struct {
	h *Hub
	s *session

	mu              sync.Mutex
	waiting, active int
	stopped         bool
}

// fill opens waiting calls up to registryIdle, within maxRegistryConns.
func (p *registryPool) fill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for !p.stopped && p.waiting < registryIdle && p.waiting+p.active < maxRegistryConns {
		p.waiting++
		go p.wait()
	}
}

// wait opens one tunnel call and, once the node uses it, forwards it to
// the registry. Any failure stops the pool: the session ended, or the
// node doesn't serve the registry (an older node answers 404).
func (p *registryPool) wait() {
	lc, err := openConn(p.s.ctx, p.s, opRegistry, empty{}, p.s.name()+"/registry", p.release)
	p.mu.Lock()
	p.waiting--
	if err != nil {
		already := p.stopped
		p.stopped = true
		p.mu.Unlock()
		if !already && p.s.ctx.Err() == nil {
			p.h.log.Warn("registry tunnels to node stopped", "server", p.s.name(), "err", err)
		}
		return
	}
	p.active++
	p.mu.Unlock()
	p.fill()
	p.forward(lc)
}

// release runs when a tunnel call has ended.
func (p *registryPool) release() {
	p.mu.Lock()
	p.active--
	p.mu.Unlock()
	p.fill()
}

// forward connects a tunnel to the registry and copies both ways until
// both sides are done.
func (p *registryPool) forward(lc *linkConn) {
	defer func() { _ = lc.Close() }()
	ctx, cancel := context.WithTimeout(p.s.ctx, dialTimeout)
	var d net.Dialer
	reg, err := d.DialContext(ctx, "tcp", p.h.o.Registry)
	cancel()
	if err != nil {
		p.h.log.Error("connect to the registry", "addr", p.h.o.Registry, "err", err)
		return
	}
	defer func() { _ = reg.Close() }()
	upDone := make(chan struct{})
	go func() {
		defer close(upDone)
		// Docker → registry. Docker closing its side ends the call.
		if _, err := io.Copy(reg, lc); err == nil {
			_ = reg.(*net.TCPConn).CloseWrite()
			return
		}
		_ = reg.Close()
	}()
	// Registry → Docker. CloseWrite ends the request body after the last
	// byte instead of resetting the call, which could drop the tail.
	_, _ = io.Copy(lc, reg)
	_ = lc.CloseWrite()
	_ = lc.SetReadDeadline(time.Now().Add(registryLinger))
	<-upDone
}

// ServeRegistry serves the control plane's registry on ln, normally
// 127.0.0.1:5000: every connection is carried over the link to the
// registry next to the control plane. Run it next to Run; it returns when
// ctx ends. While the control plane is offline, connections are closed
// after registryWait.
func (n *Node) ServeRegistry(ctx context.Context, ln net.Listener) error {
	conns := n.registryConns()
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func() {
			wctx, cancel := context.WithTimeout(ctx, registryWait)
			defer cancel()
			select {
			case conns <- c:
			case <-wctx.Done():
				n.logger().Warn("registry connection closed: no tunnel to the control plane", "wait", registryWait)
				_ = c.Close()
			}
		}()
	}
}

// registryConns hands connections from ServeRegistry to waiting tunnel
// calls.
func (n *Node) registryConns() chan net.Conn {
	n.regOnce.Do(func() { n.regConns = make(chan net.Conn) })
	return n.regConns
}

// serveRegistryCall is a tunnel call waiting for a connection from Docker.
func (n *Node) serveRegistryCall(w http.ResponseWriter, r *http.Request) {
	_, up, err := readArgs[empty](r)
	if err != nil {
		newResponder(w).fail(&Error{Code: codeBadRequest, Message: err.Error()})
		return
	}
	select {
	case conn := <-n.registryConns():
		pipeCall(w, r, up, conn)
	case <-r.Context().Done():
	}
}
