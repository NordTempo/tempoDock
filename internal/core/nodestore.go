package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Remote servers (ADR 0014). The hub calls NodeStore during handshakes and
// joins, and NodeStatus whenever a server's link goes up or down. Each
// remote server that has connected since the daemon started gets one
// watcher goroutine: it follows the server's container events (across
// reconnects) and runs the reconnect procedure (reconnected) whenever the
// server comes online. Nothing polls: a watcher waits on its event channel
// and on its kick channel.

// nodeStatusTimeout bounds NodeStatus's database writes; it runs on the
// hub's goroutine.
const nodeStatusTimeout = 5 * time.Second

// serverState is what core keeps in memory per server; entries are
// created on first use and removed with the server (bounded by servers).
type serverState struct {
	// build allows one build at a time on a builder.
	build chan struct{}
	// kick asks the server's watcher to run the reconnect procedure;
	// non-nil once the watcher runs. stop ends the watcher.
	kick chan struct{}
	stop context.CancelFunc
	// ensure holds the services whose active release must be started
	// again when the server is back (a stateful recreate cut short while
	// it was unreachable), by service ID.
	ensure map[int64]bool
}

// stateLocked returns a server's state, creating it; serversMu is held.
func (c *Core) stateLocked(serverID int64) *serverState {
	st := c.servers[serverID]
	if st == nil {
		st = &serverState{}
		c.servers[serverID] = st
	}
	return st
}

// buildLock is a server's build semaphore: one build at a time per
// builder.
func (c *Core) buildLock(serverID int64) chan struct{} {
	c.serversMu.Lock()
	defer c.serversMu.Unlock()
	st := c.stateLocked(serverID)
	if st.build == nil {
		st.build = make(chan struct{}, 1)
	}
	return st.build
}

// ensureLater records that a service's active release must be started
// again when its server reconnects.
func (c *Core) ensureLater(serverID, serviceID int64) {
	c.serversMu.Lock()
	defer c.serversMu.Unlock()
	st := c.stateLocked(serverID)
	if st.ensure == nil {
		st.ensure = map[int64]bool{}
	}
	st.ensure[serviceID] = true
}

// takeEnsure returns and forgets the services to start again on a server.
func (c *Core) takeEnsure(serverID int64) map[int64]bool {
	c.serversMu.Lock()
	defer c.serversMu.Unlock()
	st := c.servers[serverID]
	if st == nil {
		return nil
	}
	e := st.ensure
	st.ensure = nil
	return e
}

// forgetServer stops a removed server's watcher and drops its state.
func (c *Core) forgetServer(serverID int64) {
	c.serversMu.Lock()
	defer c.serversMu.Unlock()
	if st := c.servers[serverID]; st != nil && st.stop != nil {
		st.stop()
	}
	delete(c.servers, serverID)
}

// NodeStore is the hub's view of the servers and join_tokens tables, for
// remote.HubOptions.Store.
func (c *Core) NodeStore() remote.Store { return nodeStore{c} }

type nodeStore struct{ c *Core }

// NodeKey implements remote.Store.
func (s nodeStore) NodeKey(ctx context.Context, fingerprint string) (remote.NodeKey, bool, error) {
	srv, err := s.c.store.GetServerByFingerprint(ctx, fingerprint)
	if store.IsNotFound(err) {
		return remote.NodeKey{}, false, nil
	}
	if err != nil {
		return remote.NodeKey{}, false, err
	}
	next := srv.NextKeyFingerprint != nil && *srv.NextKeyFingerprint == fingerprint
	return remote.NodeKey{Server: remote.Server{ID: srv.ID, Name: srv.Name}, Next: next}, true, nil
}

// Join implements remote.Store: in one transaction it consumes the token,
// creates (or re-keys a revoked) server with the node's key and facts, and
// records which server the token made.
func (s nodeStore) Join(ctx context.Context, req remote.JoinRequest) (remote.Server, error) {
	c := s.c
	var srv store.Server
	var tok store.JoinToken
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		t := now()
		var err error
		tok, err = q.ConsumeJoinToken(ctx, store.ConsumeJoinTokenParams{Now: &t, Hash: req.SecretHash})
		if store.IsNotFound(err) {
			return remote.ErrJoinRejected
		}
		if err != nil {
			return err
		}
		srv, err = q.RegisterNodeServer(ctx, store.RegisterNodeServerParams{
			Name: tok.ServerName, AcceptsProduction: tok.AcceptsProduction, AcceptsNonProduction: tok.AcceptsNonProduction,
			Builder: tok.Builder, KeyFingerprint: req.Fingerprint,
		})
		if store.IsNotFound(err) {
			return fmt.Errorf("%w: a server named %s already exists; remove or revoke it on the control plane, then add it again", remote.ErrJoinRejected, tok.ServerName)
		}
		if err != nil {
			return err
		}
		if srv, err = q.SetServerHello(ctx, helloParams(srv.ID, req.Hello, t)); err != nil {
			return err
		}
		if base := joinBaseDomain(srv, req.Hello); base != "" {
			if srv, err = q.UpdateServerSettings(ctx, store.UpdateServerSettingsParams{BaseDomain: base, Builder: srv.Builder, ID: srv.ID}); err != nil {
				return err
			}
		}
		return q.SetJoinTokenServer(ctx, store.SetJoinTokenServerParams{ServerID: srv.ID, ID: tok.ID})
	})
	if err != nil {
		return remote.Server{}, err
	}
	c.audit(ctx, System, "server.join", nil, nil, "server "+srv.Name,
		fmt.Sprintf("server %s joined from %s with a token from %s (dock %s, key %s)", srv.Name, req.RemoteAddr, tok.CreatedBy, req.Hello.Version, req.Fingerprint))
	c.publishServer(ctx, srv.ID)
	return remote.Server{ID: srv.ID, Name: srv.Name}, nil
}

// joinBaseDomain is the base domain a joining node's config names
// (BASE_DOMAIN at install), if the server has none yet and it is a valid
// hostname; "" otherwise. A re-joining server keeps the one it has.
func joinBaseDomain(srv store.Server, h remote.Hello) string {
	if srv.BaseDomain != "" || h.BaseDomain == "" {
		return ""
	}
	base, err := normalizeHostname(strings.TrimPrefix(strings.TrimSpace(h.BaseDomain), "*."))
	if err != nil {
		return ""
	}
	return base
}

// SetNextKey implements remote.Store.
func (s nodeStore) SetNextKey(ctx context.Context, serverID int64, fingerprint string) error {
	n, err := s.c.store.SetServerNextKey(ctx, store.SetServerNextKeyParams{Fingerprint: fingerprint, ID: serverID})
	if err == nil && n == 0 {
		err = errors.New("the server was revoked or removed during the key rotation")
	}
	return err
}

// PromoteKey implements remote.Store.
func (s nodeStore) PromoteKey(ctx context.Context, serverID int64) error {
	return s.c.store.Tx(ctx, func(q *store.Queries) error {
		srv, err := q.GetServer(ctx, serverID)
		if err != nil {
			return err
		}
		if srv.NextKeyFingerprint == nil {
			return errors.New("no key rotation in progress")
		}
		_, err = q.PromoteServerKey(ctx, store.PromoteServerKeyParams{ID: serverID, Fingerprint: *srv.NextKeyFingerprint})
		return err
	})
}

// helloParams records what a node reported in hello, at t.
func helloParams(serverID int64, h remote.Hello, t time.Time) store.SetServerHelloParams {
	ips := h.PublicIPs
	if ips == nil {
		ips = []string{}
	}
	raw, _ := json.Marshal(ips)
	ingress := string(h.Ingress)
	if ingress == "" {
		ingress = string(remote.IngressDirect)
	}
	return store.SetServerHelloParams{
		Version: h.Version, DockerVersion: h.DockerVersion, Ingress: ingress, PublicIps: string(raw),
		TunnelHostname: h.TunnelHostname, MemoryBytes: toInt64(h.MemoryTotal), PlainHttp: h.PlainHTTP, Now: &t, ID: serverID,
	}
}

// NodeStatus is remote.HubOptions.OnStatus: it records a remote server's
// link coming up (with its hello facts) or going down, publishes the
// change, and on every connect has the server's watcher reconcile it.
func (c *Core) NodeStatus(s remote.State) {
	if c.ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, nodeStatusTimeout)
	defer cancel()
	t := now()
	var err error
	switch s.Status {
	case remote.StatusOnline, remote.StatusIncompatible:
		_, err = c.store.SetServerHello(ctx, helloParams(s.ServerID, s.Hello, t))
		if store.IsNotFound(err) {
			return // removed meanwhile
		}
	default:
		err = c.store.SetServerDisconnected(ctx, store.SetServerDisconnectedParams{Now: &t, Reason: "connection lost", ID: s.ServerID})
	}
	if err != nil {
		c.log.Error("record server status", "server", s.Server, "status", s.Status, "err", err)
	}
	c.publishServer(ctx, s.ServerID)
	if s.Status == remote.StatusOnline {
		c.kickServer(s.ServerID)
	}
}

// kickServer has a remote server's watcher run the reconnect procedure,
// starting the watcher on the server's first connect.
func (c *Core) kickServer(serverID int64) {
	c.serversMu.Lock()
	defer c.serversMu.Unlock()
	// Close cancels c.ctx under serversMu, so no watcher starts after it
	// began waiting for them.
	if c.ctx.Err() != nil || c.nodes == nil || serverID == c.local.ID {
		return
	}
	st := c.stateLocked(serverID)
	if st.kick == nil {
		st.kick = make(chan struct{}, 1)
		ctx, stop := context.WithCancel(c.ctx)
		st.stop = stop
		c.wg.Add(1)
		go c.watchServer(ctx, serverID, st.kick)
	}
	select {
	case st.kick <- struct{}{}:
	default: // a reconnect procedure is already due
	}
}

// watchServer follows a remote server's container events and runs the
// reconnect procedure when kicked, one thing at a time, until ctx ends.
// An ActionResync event (events may have been missed) re-reads the
// server's containers.
func (c *Core) watchServer(ctx context.Context, serverID int64, kick <-chan struct{}) {
	defer c.wg.Done()
	events, err := c.nodes.Events(ctx, serverID)
	if err != nil {
		c.log.Error("follow server events", "server", serverID, "err", err)
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-kick:
			c.reconnected(ctx, serverID)
		case ev, ok := <-events:
			if !ok {
				return
			}
			if ev.Action == remote.ActionResync {
				if rt := c.connected(serverID); rt != nil {
					if err := c.reconcileServer(ctx, serverID, rt, false); err != nil {
						c.log.Warn("resync server", "server", serverID, "err", err)
					}
				}
				continue
			}
			c.onContainerEvent(ctx, ev)
		}
	}
}

// reconnected brings a server that just connected in line with the
// database (ADR 0014 section 5): containers of failed and superseded
// releases are removed, stateful releases cut short while it was away are
// started again, service statuses are read, and its routes are applied
// again (the control plane is the source of truth).
func (c *Core) reconnected(ctx context.Context, serverID int64) {
	rt := c.connected(serverID)
	if rt == nil {
		return // gone again; the next connect kicks again
	}
	if err := c.sweepRetired(ctx, serverID, rt); err != nil {
		c.log.Warn("server reconnected: remove leftover containers", "server", serverID, "err", err)
	}
	for svc := range c.takeEnsure(serverID) {
		ss, err := c.scopeOf(ctx, svc)
		if err != nil {
			continue
		}
		if active, err := c.store.GetActiveRelease(ctx, svc); err == nil {
			c.ensureRunning(ctx, ss, active)
		}
	}
	if err := c.reconcileServer(ctx, serverID, rt, true); err != nil {
		c.log.Warn("server reconnected: reconcile", "server", serverID, "err", err)
	}
	if err := c.applyRoutes(ctx, serverID); err != nil {
		c.log.Error("server reconnected: apply routes", "server", serverID, "err", err)
	}
	c.kickDomains()
	c.kickBackups() // overdue backups run now
}

// publishServer sends a server's current state to clients.
func (c *Core) publishServer(ctx context.Context, serverID int64) {
	srv, ok, err := c.serverByID(ctx, serverID)
	if err != nil || !ok {
		return
	}
	c.bus.publish(busEvent{ev: apitypes.Event{Type: apitypes.EventServer, Server: &srv}})
}

// isOffline reports whether err means a server couldn't be reached.
func isOffline(err error) bool {
	return errors.Is(err, remote.ErrOffline) || errors.Is(err, remote.ErrIncompatible) || CodeOf(err) == apitypes.CodeServerOffline
}

// nodeErr turns a runtime error that means the server is unreachable
// into a server_offline error with a plain message.
func (c *Core) nodeErr(serverID int64, err error) error {
	if err == nil || CodeOf(err) == apitypes.CodeServerOffline || !isOffline(err) {
		return err
	}
	_, err = c.runtimeFor(serverID)
	if err == nil {
		err = errf(apitypes.CodeServerOffline, "the server went offline; try again when it is back")
	}
	return err
}
