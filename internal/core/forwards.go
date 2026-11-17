package core

import (
	"context"
	"errors"
	"io"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Forwarding (ADR 0063). A server a service moved away from keeps the
// service's old host port in server_forwards and listens on it
// (node.Proxy.Forward). Its Caddy keeps routing the service's domains to
// that port, so requests that still reach it (DNS caches, records Dock
// doesn't manage, Cloudflare catching up) work: each connection is
// piped to the service's container on the server it is on now
// (node.Containers.Dial).
//
// A move's hold waits here too, not in Caddy: Caddy's retries of a held
// route end when its config reloads (any deploy on the server), each
// with a 502, while a connection Caddy made survives a reload. So once
// a service's container stopped in a hold, the source listens on its
// port (holdPort) and the connections wait for the hold to end
// (waitHold), as do those of a forward on the target when the
// environment moves back to a server that was forwarding it. The
// switch turns the source's listener into the forward, so nothing
// there changes; a rollback closes it before the containers start
// again on their ports.
//
// Listeners exist while their server is connected: ensureForwards opens
// them at start, after a switch and when a server reconnects, and closes
// those whose rows went. No polling: a connection costs one lookup.

// forwardState is the open listeners, by server and port.
type forwardState struct {
	mu   sync.Mutex
	open map[forwardKey]net.Listener
	// holding are the listeners of a move's hold that have no forward row
	// (yet): the service each one's connections are for.
	holding map[forwardKey]int64
}

type forwardKey struct {
	server, port int64
}

func newForwardState() forwardState {
	return forwardState{open: map[forwardKey]net.Listener{}, holding: map[forwardKey]int64{}}
}

// ensureForwards opens a listener for each of a server's forwards (and
// ports a hold listens on) that has none, and closes the ones without a
// row or hold. A server that is offline is left alone; its reconnect
// calls it again.
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
	for k := range c.fwd.holding {
		if k.server == serverID {
			want[k.port] = true
		}
	}
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
// container, wherever it is now, once no move holds the service.
func (c *Core) forwardConn(k forwardKey, in net.Conn) {
	defer func() { _ = in.Close() }()
	svcID, err := c.forwardedService(k)
	if err != nil {
		c.log.Warn("forward a connection", "server", k.server, "port", k.port, "err", err)
		return
	}
	c.waitHold(svcID)
	ctx, cancel := context.WithTimeout(c.ctx, dialTimeout)
	defer cancel()
	out, err := c.dialServiceID(ctx, svcID)
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

// forwardedService is the service a listener's connections are for: its
// hold's (holdPort), or its forward row's. It is read before the
// connection waits, since a switch ends the target's forward rows.
func (c *Core) forwardedService(k forwardKey) (int64, error) {
	c.fwd.mu.Lock()
	id, ok := c.fwd.holding[k]
	c.fwd.mu.Unlock()
	if ok {
		return id, nil
	}
	f, err := c.store.GetServerForward(c.ctx, store.GetServerForwardParams{ServerID: k.server, HostPort: k.port})
	return f.ServiceID, err
}

// dialServiceID connects to a service's active container, wherever it is.
func (c *Core) dialServiceID(ctx context.Context, svcID int64) (net.Conn, error) {
	svc, err := c.store.GetService(ctx, svcID)
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

// waitHold blocks while a move holds a service's requests, for at most
// node.HoldFor, as long as Caddy's retries of a held route wait: one
// timer per waiting connection, only during a hold.
func (c *Core) waitHold(svcID int64) {
	var timeout <-chan time.Time
	for {
		c.moves.mu.Lock()
		held, changed := c.moves.held[svcID], c.moves.holdEnded
		c.moves.mu.Unlock()
		if !held {
			return
		}
		if timeout == nil {
			t := time.NewTimer(node.HoldFor)
			defer t.Stop()
			timeout = t.C
		}
		select {
		case <-changed:
		case <-timeout:
			return
		case <-c.ctx.Done():
			return
		}
	}
}

// holdPort listens on a server's port for a service of a move in its
// hold, once the service's container there stopped and freed it. When
// that fails, Caddy's retries still hold the requests.
func (c *Core) holdPort(ctx context.Context, serverID, port, svcID int64) {
	rt := c.connected(serverID)
	if rt == nil {
		return
	}
	k := forwardKey{serverID, port}
	c.fwd.mu.Lock()
	defer c.fwd.mu.Unlock()
	if _, ok := c.fwd.open[k]; ok {
		return
	}
	ln, err := rt.Forward(ctx, int(port))
	if err != nil {
		c.log.Warn("hold a moving service's port", "server", serverID, "port", port, "err", c.nodeErr(serverID, err))
		return
	}
	c.fwd.open[k] = ln
	c.fwd.holding[k] = svcID
	c.wg.Go(func() { c.serveForward(k, ln) })
}

// endPortHolds forgets the ports a server holds for services. After a
// switch their listeners stay, as forwards; a rollback closes them
// (closing), so the services' containers can take their ports again.
func (c *Core) endPortHolds(serverID int64, svcIDs []int64, closing bool) {
	c.fwd.mu.Lock()
	defer c.fwd.mu.Unlock()
	for k, id := range c.fwd.holding {
		if k.server != serverID || !slices.Contains(svcIDs, id) {
			continue
		}
		delete(c.fwd.holding, k)
		if ln, ok := c.fwd.open[k]; ok && closing {
			_ = ln.Close()
			delete(c.fwd.open, k)
		}
	}
}

// closeWrite half-closes a connection that can, or closes it.
func closeWrite(conn net.Conn) {
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = conn.Close()
}
