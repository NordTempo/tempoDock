package remote

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// HubOptions configure the control plane's end of node connections.
type HubOptions struct {
	// Identity is the control plane's key; nodes pin its fingerprint.
	Identity tls.Certificate
	// Authorize maps a node key fingerprint to its server name. It runs
	// during the TLS handshake of every connection, so revoking a key
	// takes effect on the next connect (Disconnect ends the current one).
	Authorize func(fingerprint string) (server string, ok bool)
	// OnConnect runs after a node said hello, OnDisconnect when its
	// connection ends. Both are optional and must not block for long.
	OnConnect    func(server string, info Info)
	OnDisconnect func(server string)
	// KeepAlive defaults to DefaultKeepAlive.
	KeepAlive time.Duration
	Logger    *slog.Logger
}

// Hub accepts node connections and hands out a Client per server.
type Hub struct {
	o   HubOptions
	log *slog.Logger

	mu       sync.Mutex
	sessions map[string]*session
	// changed is closed and replaced whenever sessions changes.
	changed chan struct{}
}

// session is one live connection to a node.
type session struct {
	server string
	info   Info
	cc     *http.ClientConn
	once   sync.Once
	done   chan struct{}
}

func (s *session) close() {
	s.once.Do(func() {
		_ = s.cc.Close()
		close(s.done)
	})
}

// NewHub creates a hub. Call Serve to accept connections.
func NewHub(o HubOptions) *Hub {
	log := o.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Hub{o: o, log: log, sessions: map[string]*session{}, changed: make(chan struct{})}
}

// Serve accepts node connections on ln until ctx ends, then closes ln and
// every session.
func (h *Hub) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
		h.mu.Lock()
		all := make([]*session, 0, len(h.sessions))
		for _, s := range h.sessions {
			all = append(all, s)
		}
		h.mu.Unlock()
		for _, s := range all {
			s.close()
		}
	}()
	for {
		raw, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func() {
			if err := h.handle(ctx, raw); err != nil {
				h.log.Warn("node connection rejected", "remote", raw.RemoteAddr(), "err", err)
				_ = raw.Close()
			}
		}()
	}
}

// handle authenticates a connection, says hello and registers the session.
func (h *Hub) handle(ctx context.Context, raw net.Conn) error {
	ka := or(h.o.KeepAlive, DefaultKeepAlive)
	conn := tls.Server(raw, controlPlaneTLS(h.o.Identity, h.o.Authorize))
	hsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := conn.HandshakeContext(hsCtx); err != nil {
		return err
	}
	fp, err := peerFingerprint(conn.ConnectionState())
	if err != nil {
		return err
	}
	server, ok := h.o.Authorize(fp)
	if !ok {
		return fmt.Errorf("node key %s was revoked during the handshake", fp)
	}
	tr := &http.Transport{
		Protocols: h2Only(),
		HTTP2:     &http.HTTP2Config{SendPingTimeout: ka, PingTimeout: ka},
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return opaqueConn{conn}, nil
		},
	}
	cc, err := tr.NewClientConn(ctx, "http", "node:80")
	if err != nil {
		return err
	}
	s := &session{server: server, cc: cc, done: make(chan struct{})}
	cc.SetStateHook(func(cc *http.ClientConn) {
		if cc.Err() != nil {
			s.close()
		}
	})
	helloCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := call(helloCtx, s, opHello, noArgs{}, nil, nil, nil, &s.info); err != nil {
		s.close()
		return fmt.Errorf("hello from %s: %w", server, err)
	}
	if s.info.Protocol < 1 {
		s.close()
		return fmt.Errorf("%s speaks protocol %d, need at least 1", server, s.info.Protocol)
	}
	h.register(s)
	h.log.Info("node connected", "server", server, "version", s.info.Version)
	if h.o.OnConnect != nil {
		h.o.OnConnect(server, s.info)
	}
	go func() {
		<-s.done
		if h.unregister(s) {
			h.log.Info("node disconnected", "server", server)
			if h.o.OnDisconnect != nil {
				h.o.OnDisconnect(server)
			}
		}
	}()
	return nil
}

// register makes s the server's session. A node that reconnects before its
// old connection timed out replaces it.
func (h *Hub) register(s *session) {
	h.mu.Lock()
	old := h.sessions[s.server]
	h.sessions[s.server] = s
	h.notifyLocked()
	h.mu.Unlock()
	if old != nil {
		old.close()
	}
}

// unregister removes s if it is still the server's session.
func (h *Hub) unregister(s *session) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessions[s.server] != s {
		return false
	}
	delete(h.sessions, s.server)
	h.notifyLocked()
	return true
}

func (h *Hub) notifyLocked() {
	close(h.changed)
	h.changed = make(chan struct{})
}

// Disconnect drops a server's connection, e.g. after revoking its key.
func (h *Hub) Disconnect(server string) {
	h.mu.Lock()
	s := h.sessions[server]
	h.mu.Unlock()
	if s != nil {
		s.close()
	}
}

// Info returns what a connected server reported in hello.
func (h *Hub) Info(server string) (Info, bool) {
	s := h.session(server)
	if s == nil {
		return Info{}, false
	}
	return s.info, true
}

// Client returns the node.Runtime of a server. It exists whether or not the
// server is connected; calls fail with ErrOffline while it isn't.
func (h *Hub) Client(server string) *Client { return &Client{hub: h, server: server} }

func (h *Hub) session(server string) *session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[server]
}

// wait returns the server's session, waiting for it to connect.
func (h *Hub) wait(ctx context.Context, server string) (*session, error) {
	for {
		h.mu.Lock()
		s, changed := h.sessions[server], h.changed
		h.mu.Unlock()
		if s != nil {
			return s, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// offline wraps a transport failure. A call whose ctx ended reports that
// instead, so callers can tell "gave up" from "lost the node".
func offline(ctx context.Context, server string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return fmt.Errorf("server %s: %w", server, ErrOffline)
	}
	return fmt.Errorf("server %s: connection lost: %w: %w", server, ErrOffline, err)
}
