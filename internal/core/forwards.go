package core

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Forwarding (ADR 0062). A server a service moved away from keeps the
// service's old host port in server_forwards and listens on it
// (node.Proxy.Forward). Its Caddy keeps routing the service's domains to
// that port, so requests that still reach it (DNS caches, records Dock
// doesn't manage, Cloudflare catching up) work: each connection is
// piped to the service's container on the server it is on now
// (node.Containers.Dial). The listener only exists once the target
// serves, and the route holds requests while nothing listens, which is
// how the requests of a move's hold get through.
//
// Listeners exist while their server is connected: ensureForwards opens
// them at start, after a switch and when a server reconnects, and closes
// those whose rows went. No polling: a connection costs one lookup.

// forwardState is the open listeners, by server and port.
type forwardState struct {
	mu   sync.Mutex
	open map[forwardKey]net.Listener
}

type forwardKey struct {
	server, port int64
}

func newForwardState() forwardState { return forwardState{open: map[forwardKey]net.Listener{}} }

// ensureForwards opens a listener for each of a server's forwards that
// has none, and closes the ones without a row. A server that is offline
// is left alone; its reconnect calls it again.
func (c *Core) ensureForwards(ctx context.Context, serverID int64) {
	rt := c.connected(serverID)
	if rt == nil {
		return
	}
	rows, err := c.store.ListServerForwards(ctx, serverID)
	if err != nil {
		c.log.Error("list forwards", "server", serverID, "err", err)
		return
	}
	want := map[int64]bool{}
	for _, r := range rows {
		want[r.HostPort] = true
	}
	c.fwd.mu.Lock()
	defer c.fwd.mu.Unlock()
	for k, ln := range c.fwd.open {
		if k.server == serverID && !want[k.port] {
			_ = ln.Close()
			delete(c.fwd.open, k)
		}
	}
	for port := range want {
		k := forwardKey{serverID, port}
		if _, ok := c.fwd.open[k]; ok {
			continue
		}
		ln, err := rt.Forward(ctx, int(port))
		if err != nil {
			c.log.Error("forward a moved service's port", "server", serverID, "port", port, "err", c.nodeErr(serverID, err))
			continue
		}
		c.fwd.open[k] = ln
		c.wg.Go(func() { c.serveForward(k, ln) })
	}
}

// closeForwards closes every listener of a server (it is being removed).
func (c *Core) closeForwards(serverID int64) {
	c.fwd.mu.Lock()
	defer c.fwd.mu.Unlock()
	for k, ln := range c.fwd.open {
		if k.server == serverID {
			_ = ln.Close()
			delete(c.fwd.open, k)
		}
	}
}

// serveForward accepts a listener's connections until it closes. A
// remote listener ends when the link drops; the reconnect opens a new
// one.
func (c *Core) serveForward(k forwardKey, ln net.Listener) {
	stop := context.AfterFunc(c.ctx, func() { _ = ln.Close() })
	defer stop()
	defer func() {
		c.fwd.mu.Lock()
		if c.fwd.open[k] == ln {
			delete(c.fwd.open, k)
		}
		c.fwd.mu.Unlock()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) && c.ctx.Err() == nil {
				c.log.Warn("forwarded port stopped accepting", "server", k.server, "port", k.port, "err", err)
			}
			return
		}
		c.wg.Go(func() { c.forwardConn(k, conn) })
	}
}

// forwardConn pipes one connection to the forwarded service's active
// container, wherever it is now.
func (c *Core) forwardConn(k forwardKey, in net.Conn) {
	defer func() { _ = in.Close() }()
	ctx, cancel := context.WithTimeout(c.ctx, dialTimeout)
	defer cancel()
	out, err := c.dialForwarded(ctx, k)
	if err != nil {
		c.log.Warn("forward a connection", "server", k.server, "port", k.port, "err", err)
		return
	}
	defer func() { _ = out.Close() }()
	stop := context.AfterFunc(c.ctx, func() { _, _ = in.Close(), out.Close() })
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(out, in)
		closeWrite(out)
	}()
	_, _ = io.Copy(in, out)
	closeWrite(in)
	<-done
}

// dialTimeout bounds connecting a forwarded connection.
const dialTimeout = 10 * time.Second

// dialForwarded connects to the container serving a forward's service.
func (c *Core) dialForwarded(ctx context.Context, k forwardKey) (net.Conn, error) {
	f, err := c.store.GetServerForward(ctx, store.GetServerForwardParams{ServerID: k.server, HostPort: k.port})
	if err != nil {
		return nil, err
	}
	svc, err := c.store.GetService(ctx, f.ServiceID)
	if err != nil {
		return nil, err
	}
	env, err := c.store.GetEnvironment(ctx, svc.EnvironmentID)
	if err != nil {
		return nil, err
	}
	rel, err := c.store.GetActiveRelease(ctx, svc.ID)
	if err != nil {
		return nil, err
	}
	cfg, err := c.openConfig(rel)
	if err != nil {
		return nil, err
	}
	rt, err := c.runtimeFor(env.ServerID)
	if err != nil {
		return nil, err
	}
	conn, err := rt.Dial(ctx, node.DialRequest{ContainerID: rel.ContainerID, Port: cfg.Port})
	return conn, c.nodeErr(env.ServerID, err)
}

// closeWrite half-closes a connection that can, or closes it.
func closeWrite(conn net.Conn) {
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = conn.Close()
}
