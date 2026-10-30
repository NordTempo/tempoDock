package remote

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/version"
)

// Status is a remote server's link status, held by the hub in memory
// (ADR 0014 section 5). Joining and revoked are database states; core
// derives them from the servers row.
type Status string

const (
	// StatusOnline: connected, hello passed; calls go through.
	StatusOnline Status = "online"
	// StatusOffline: not connected. Calls fail with ErrOffline.
	StatusOffline Status = "offline"
	// StatusIncompatible: connected, but the node's protocol is older than
	// the control plane drives. The link stays up for hello only; calls
	// fail with ErrIncompatible.
	StatusIncompatible Status = "incompatible"
)

// State is a remote server's link as the hub sees it.
type State struct {
	ServerID int64
	Server   string
	Status   Status
	// Hello is what the node reported when it connected; zero when offline.
	Hello Hello
}

// Server identifies a remote server: its row in the servers table.
type Server struct {
	ID   int64
	Name string
}

// NodeKey is the server a node key belongs to.
type NodeKey struct {
	Server
	// Next is true when the key is the server's next_key_fingerprint, the
	// new key of a rotation in progress. Its first hello promotes it.
	Next bool
}

// JoinRequest is a node presenting a join token's secret.
type JoinRequest struct {
	// SecretHash is JoinToken.SecretHash of the presented secret.
	SecretHash string
	// Fingerprint is the joining node's key.
	Fingerprint string
	Hello       Hello
	// RemoteAddr is where the node connected from, for the audit log.
	RemoteAddr string
}

// ErrJoinRejected is what Store.Join returns (wrapped, with details) for a
// token that doesn't exist, expired or was used. The node shows the
// message.
var ErrJoinRejected = errors.New("join token is invalid, expired or already used; create a new one with `dock server add`")

// Store is the hub's view of the servers and join_tokens tables. Core
// implements it; every method is called with a bounded ctx.
type Store interface {
	// NodeKey looks up the server whose current or next key is
	// fingerprint. A revoked server (no key) or unknown key is ok=false.
	// It runs during the TLS handshake of every connection and again just
	// before its session is registered, so revoking a key takes effect on
	// the next connect and on one still saying hello (Hub.Disconnect ends
	// the current one).
	NodeKey(ctx context.Context, fingerprint string) (key NodeKey, ok bool, err error)
	// Join redeems a join token. In one transaction it must find the token
	// by req.SecretHash, check it is unused and unexpired, mark it used and
	// create the server it was minted for, with req.Fingerprint as its key
	// and the facts from req.Hello. A bad token is ErrJoinRejected
	// (wrapped); single use relies on this being atomic.
	Join(ctx context.Context, req JoinRequest) (Server, error)
	// SetNextKey records the key a server is rotating to.
	SetNextKey(ctx context.Context, serverID int64, fingerprint string) error
	// PromoteKey makes the next key the server's key and forgets the old
	// one. It runs when the node first says hello with the new key.
	PromoteKey(ctx context.Context, serverID int64) error
}

// HubOptions configure the control plane's end of node connections.
type HubOptions struct {
	// Identity is the control plane's key (ControlPlaneKeyFile); nodes pin
	// its fingerprint.
	Identity Identity
	Store    Store
	// Addrs are the host:port addresses nodes dial (the [nodes] listen
	// address as reachable from nodes, e.g. the Tailscale IP), written into
	// join tokens.
	Addrs []string
	// Registry is the host:port of the control plane's registry
	// (127.0.0.1:5000), which nodes reach through the link (registry.go).
	// Empty: nodes get no registry.
	Registry string
	// OnStatus is called on every status change, in order, one call at a
	// time: online (or incompatible) after each hello, also when a
	// reconnect replaced a connection that still looked alive; offline when
	// the connection ends. Each online is a reconnect: reconcile the
	// server. It must not block for long, and must not call RotateKey or
	// Disconnect.
	OnStatus func(State)
	// CallTimeout bounds each control call (DefaultCallTimeout when zero);
	// see Client.
	CallTimeout time.Duration
	// MinProtocol defaults to the package's MinProtocol.
	MinProtocol int
	// KeepAlive defaults to DefaultKeepAlive.
	KeepAlive time.Duration
	Logger    *slog.Logger
}

const (
	// handshakeTimeout bounds TLS plus hello on a new connection.
	handshakeTimeout = 10 * time.Second
	// joinTimeout is how long a joining-mode connection may stay open.
	joinTimeout = 10 * time.Second
)

// Hub accepts node connections and hands out a Client per server.
type Hub struct {
	o     HubOptions
	log   *slog.Logger
	joins joinLimiter

	// statusMu serializes status changes with their OnStatus calls, so
	// they arrive in order. Taken before mu.
	statusMu sync.Mutex

	mu       sync.Mutex
	sessions map[int64]*session
	// names remembers server names for errors while they are offline.
	names map[int64]string
	// changed is closed and replaced whenever sessions changes.
	changed chan struct{}
}

// session is one live connection to a node.
type session struct {
	id   int64
	name string
	// fingerprint is the node's key, checked again when registering.
	fingerprint string
	hello       Hello
	status      Status
	cc          *http.ClientConn
	closed      atomic.Bool
	done        chan struct{}
	// ctx ends with the session, for calls that live as long as it.
	ctx    context.Context
	cancel context.CancelFunc
	// dials counts open Dial calls, capped at maxDials.
	dials atomic.Int32
}

// close closes the connection once. Not a sync.Once: cc.Close can run the
// state hook, which calls close again on the same goroutine.
func (s *session) close() {
	if s.closed.CompareAndSwap(false, true) {
		s.cancel()
		_ = s.cc.Close()
		close(s.done)
	}
}

// newSession wraps a new connection's HTTP/2 client.
func newSession(ctx context.Context, id int64, name string, cc *http.ClientConn) *session {
	s := &session{id: id, name: name, cc: cc, done: make(chan struct{})}
	s.ctx, s.cancel = context.WithCancel(ctx)
	return s
}

func (s *session) state() State {
	return State{ServerID: s.id, Server: s.name, Status: s.status, Hello: s.hello}
}

// NewHub creates a hub. Call Serve to accept connections.
func NewHub(o HubOptions) *Hub {
	log := o.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Hub{
		o: o, log: log, joins: joinLimiter{seen: map[string]joinWindow{}},
		sessions: map[int64]*session{}, names: map[int64]string{}, changed: make(chan struct{}),
	}
}

// Fingerprint is the control plane's key fingerprint, as nodes pin it.
func (h *Hub) Fingerprint() string { return h.o.Identity.Fingerprint() }

// MintJoinToken creates a one-time join token for HubOptions.Addrs. Core
// stores its SecretHash with the server name, role and an expiry
// (JoinTokenTTL) and shows its String once.
func (h *Hub) MintJoinToken() (JoinToken, error) {
	return newJoinToken(h.Fingerprint(), h.o.Addrs)
}

// Serve accepts node connections on ln until ctx ends, then closes ln and
// every session.
func (h *Hub) Serve(ctx context.Context, ln net.Listener) error {
	stop := context.AfterFunc(ctx, func() {
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
	})
	defer stop()
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
				h.log.Warn("node connection rejected", "remote", raw.RemoteAddr().String(), "err", explainRejection(err))
				_ = raw.Close()
			}
		}()
	}
}

// explainRejection turns a handshake failure into words for the log.
func explainRejection(err error) error {
	if strings.Contains(err.Error(), "unsupported application protocols") {
		return fmt.Errorf("the node speaks a protocol version this control plane doesn't (it speaks %v); upgrade dock, control plane first: %w", acceptedALPN, err)
	}
	return err
}

// handle authenticates a connection, then joins the node or starts its
// session.
func (h *Hub) handle(ctx context.Context, raw net.Conn) error {
	hsCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	var key NodeKey
	var keyFP string
	conn := tls.Server(raw, controlPlaneTLS(h.o.Identity, func(cs tls.ConnectionState, fp string) error {
		switch cs.NegotiatedProtocol {
		case joinALPN:
			if !h.joins.allow(raw.RemoteAddr()) {
				return errors.New("too many join attempts from this address; wait a minute")
			}
			return nil
		case ALPN:
			k, ok, err := h.o.Store.NodeKey(hsCtx, fp)
			if err != nil {
				return fmt.Errorf("look up node key: %w", err)
			}
			if !ok {
				return fmt.Errorf("unknown or revoked node key %s", fp)
			}
			key, keyFP = k, fp
			return nil
		}
		return fmt.Errorf("unsupported protocol %q", cs.NegotiatedProtocol)
	}))
	if err := conn.HandshakeContext(hsCtx); err != nil {
		return err
	}
	if conn.ConnectionState().NegotiatedProtocol == joinALPN {
		return h.join(ctx, conn)
	}
	return h.connect(ctx, hsCtx, conn, key, keyFP)
}

// newClientConn starts the control plane's HTTP/2 client on conn.
func (h *Hub) newClientConn(ctx context.Context, conn net.Conn) (*http.ClientConn, error) {
	ka := or(h.o.KeepAlive, DefaultKeepAlive)
	tr := &http.Transport{
		Protocols:              h2Only(),
		HTTP2:                  &http.HTTP2Config{SendPingTimeout: ka, PingTimeout: ka},
		MaxResponseHeaderBytes: maxHeaderBytes,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return opaqueConn{conn}, nil
		},
	}
	return tr.NewClientConn(ctx, "http", "node:80")
}

// connect says hello to a known node and registers its session.
func (h *Hub) connect(ctx, helloCtx context.Context, conn *tls.Conn, key NodeKey, fp string) error {
	cc, err := h.newClientConn(ctx, conn)
	if err != nil {
		return err
	}
	s := newSession(ctx, key.ID, key.Name, cc)
	s.fingerprint = fp
	cc.SetStateHook(func(cc *http.ClientConn) {
		if cc.Err() != nil {
			s.close()
		}
	})
	minProto := or(h.o.MinProtocol, MinProtocol)
	req := helloRequest{Version: version.Version, Protocol: Protocol, MinProtocol: minProto}
	if err := call(helloCtx, s, opHello, req, nil, nil, nil, &s.hello); err != nil {
		s.close()
		return fmt.Errorf("hello from %s: %w", s.name, err)
	}
	dropped, err := checkFacts(&s.hello.Facts)
	if err != nil {
		s.close()
		return fmt.Errorf("hello from %s: %w", s.name, err)
	}
	if len(dropped) > 0 {
		h.log.Warn("node reported what DNS can't point at; ignored, check its config.toml", "server", s.name, "ignored", dropped)
	}
	if key.Next {
		if err := h.o.Store.PromoteKey(helloCtx, s.id); err != nil {
			s.close()
			return fmt.Errorf("promote the new key of %s: %w", s.name, err)
		}
		h.log.Info("node key rotated", "server", s.name)
	}
	s.status = StatusOnline
	if s.hello.Protocol < minProto {
		s.status = StatusIncompatible
		h.log.Error("node is too old: upgrade dock on it", "server", s.name, "version", s.hello.Version,
			"protocol", s.hello.Protocol, "min_protocol", minProto)
	}
	if err := h.register(helloCtx, s); err != nil {
		s.close()
		return fmt.Errorf("hello from %s: %w", s.name, err)
	}
	h.log.Info("node connected", "server", s.name, "version", s.hello.Version, "status", s.status)
	if s.status == StatusOnline && h.o.Registry != "" {
		(&registryPool{h: h, s: s}).fill()
	}
	go func() {
		<-s.done
		if h.unregister(s) {
			h.log.Info("node disconnected", "server", s.name)
		}
	}()
	return nil
}

// checkFacts rejects a hello the control plane can't act on, and drops
// what DNS should point at (the tunnel hostname, the public IPs) unless
// it is what it says it is: the control plane shows it as records to
// create, and uses the tunnel in Cloudflare (ADR 0035), so a compromised
// node can't point a domain somewhere else. Dropping, not refusing,
// keeps a node with an odd public_ip (a DDNS name) connected. It returns
// what was dropped, for the log.
func checkFacts(f *Facts) (dropped []string, err error) {
	if f.Ingress != IngressDirect && f.Ingress != IngressCloudflareTunnel {
		return nil, fmt.Errorf("unknown ingress mode %q (want %s or %s); check ingress in the node's node.toml", f.Ingress, IngressDirect, IngressCloudflareTunnel)
	}
	if f.TunnelHostname != "" {
		if t := strings.ToLower(f.TunnelHostname); config.ValidTunnelHostname(t) {
			f.TunnelHostname = t
		} else {
			dropped = append(dropped, "tunnel hostname "+f.TunnelHostname)
			f.TunnelHostname = ""
		}
	}
	ips := f.PublicIPs[:0]
	for _, ip := range f.PublicIPs {
		if a, err := netip.ParseAddr(ip); err == nil && a.Zone() == "" && !a.IsUnspecified() && !a.IsLoopback() && !a.IsMulticast() && !a.IsLinkLocalUnicast() {
			ips = append(ips, ip)
		} else {
			dropped = append(dropped, "public IP "+ip)
		}
	}
	f.PublicIPs = ips
	return dropped, nil
}

// register makes s the server's session. A node that reconnects before its
// old connection timed out replaces it.
//
// The node's key was checked in the handshake, but hello can take seconds,
// and a revoke in between finds no session to close. So the key is looked
// up again here, under statusMu, which Disconnect takes too: either this
// lookup sees the revoked (or removed) key, or Disconnect sees the session.
func (h *Hub) register(ctx context.Context, s *session) error {
	h.statusMu.Lock()
	defer h.statusMu.Unlock()
	k, ok, err := h.o.Store.NodeKey(ctx, s.fingerprint)
	if err != nil {
		return fmt.Errorf("look up node key: %w", err)
	}
	if !ok || k.ID != s.id {
		return fmt.Errorf("node key %s was revoked during hello", s.fingerprint)
	}
	h.mu.Lock()
	old := h.sessions[s.id]
	h.sessions[s.id] = s
	h.names[s.id] = s.name
	h.notifyLocked()
	h.mu.Unlock()
	if old != nil {
		old.close()
	}
	if h.o.OnStatus != nil {
		h.o.OnStatus(s.state())
	}
	return nil
}

// unregister removes s if it is still the server's session.
func (h *Hub) unregister(s *session) bool {
	h.statusMu.Lock()
	defer h.statusMu.Unlock()
	h.mu.Lock()
	current := h.sessions[s.id] == s
	if current {
		delete(h.sessions, s.id)
		h.notifyLocked()
	}
	h.mu.Unlock()
	if current && h.o.OnStatus != nil {
		h.o.OnStatus(State{ServerID: s.id, Server: s.name, Status: StatusOffline})
	}
	return current
}

func (h *Hub) notifyLocked() {
	close(h.changed)
	h.changed = make(chan struct{})
}

// Runtime returns the node.Runtime of an online server, for core's
// Options.Remote. ok is false while the server is offline or
// incompatible.
func (h *Hub) Runtime(serverID int64) (node.Runtime, bool) {
	if s := h.session(serverID); s == nil || s.status != StatusOnline {
		return nil, false
	}
	return h.Client(serverID), true
}

// Client returns a server's runtime whether or not it is connected: calls
// fail with ErrOffline while it isn't, and Events follows it across
// reconnects. Core subscribes to a server's events through it.
func (h *Hub) Client(serverID int64) *Client { return &Client{hub: h, id: serverID} }

// State returns a server's link status and what it said in hello.
func (h *Hub) State(serverID int64) State {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.sessions[serverID]; s != nil {
		return s.state()
	}
	return State{ServerID: serverID, Server: h.names[serverID], Status: StatusOffline}
}

// Disconnect drops a server's connection. Revoking a server is clearing
// its key in the store, then Disconnect: the node's next handshake fails,
// and a connection still in hello isn't registered (see register).
func (h *Hub) Disconnect(serverID int64) {
	h.statusMu.Lock()
	defer h.statusMu.Unlock()
	if s := h.session(serverID); s != nil {
		s.close()
	}
}

// RotateKey has an online node create a new key (ADR 0014 section 2),
// records it as the server's next key and drops the connection. The node
// reconnects with the new key, and its hello promotes it. It returns the
// new fingerprint.
func (h *Hub) RotateKey(ctx context.Context, serverID int64) (string, error) {
	s, err := h.Client(serverID).session(ctx)
	if err != nil {
		return "", err
	}
	var r rotateResult
	callCtx, cancel := context.WithTimeout(ctx, h.callTimeout())
	err = call(callCtx, s, opRotate, empty{}, nil, nil, nil, &r)
	cancel()
	if err != nil {
		return "", err
	}
	if _, err := fingerprintBytes(r.Fingerprint); err != nil {
		return "", fmt.Errorf("server %s: %w", s.name, err)
	}
	if err := h.o.Store.SetNextKey(ctx, serverID, r.Fingerprint); err != nil {
		return "", err
	}
	s.close()
	return r.Fingerprint, nil
}

// callTimeout is HubOptions.CallTimeout or its default.
func (h *Hub) callTimeout() time.Duration { return or(h.o.CallTimeout, DefaultCallTimeout) }

func (h *Hub) session(serverID int64) *session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[serverID]
}

func (h *Hub) name(serverID int64) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if n := h.names[serverID]; n != "" {
		return n
	}
	return fmt.Sprintf("#%d", serverID)
}

// WaitOnline waits until the server is online, or ctx ends.
func (h *Hub) WaitOnline(ctx context.Context, serverID int64) error {
	_, err := h.wait(ctx, serverID)
	return err
}

// wait returns the server's online session, waiting for it to connect.
func (h *Hub) wait(ctx context.Context, serverID int64) (*session, error) {
	for {
		h.mu.Lock()
		s, changed := h.sessions[serverID], h.changed
		h.mu.Unlock()
		if s != nil && s.status == StatusOnline {
			return s, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// join runs a joining-mode connection: the node presents the token's
// secret and its facts, the store redeems the token, and the node learns
// which server it is. Nothing else is ever called on it.
func (h *Hub) join(ctx context.Context, conn *tls.Conn) error {
	ctx, cancel := context.WithTimeout(ctx, joinTimeout)
	defer cancel()
	_ = conn.SetDeadline(time.Now().Add(joinTimeout))
	fp, err := peerFingerprint(conn.ConnectionState())
	if err != nil {
		return err
	}
	remote := conn.RemoteAddr().String()
	cc, err := h.newClientConn(ctx, conn)
	if err != nil {
		return err
	}
	s := newSession(ctx, 0, "joining node at "+remote, cc)
	defer s.close()

	var jr joinResult
	if err := call(ctx, s, opJoin, empty{}, nil, nil, nil, &jr); err != nil {
		return fmt.Errorf("join from %s: %w", remote, err)
	}
	srv, err := h.redeem(ctx, fp, jr, remote)
	reply := joinedRequest{ServerID: srv.ID, Server: srv.Name}
	if err != nil {
		reply = joinedRequest{Error: err.Error()}
		var internal *internalJoinError
		if errors.As(err, &internal) {
			reply.Error = "the control plane couldn't complete the join; see its logs"
		}
	}
	if cerr := call(ctx, s, opJoined, reply, nil, nil, nil, nil); cerr != nil && err == nil {
		// The server exists now; the node can't know. It joins again with
		// a new token after the admin removes this server.
		h.log.Warn("node joined but missed the answer", "server", srv.Name, "err", cerr)
	}
	if err != nil {
		return fmt.Errorf("join from %s: %w", remote, err)
	}
	h.log.Info("node joined", "server", srv.Name, "key", fp, "remote", remote)
	return nil
}

// redeem checks what a joining node sent and redeems its token.
func (h *Hub) redeem(ctx context.Context, fp string, jr joinResult, remote string) (Server, error) {
	if len(jr.Secret) != secretSize {
		return Server{}, ErrJoinRejected
	}
	dropped, err := checkFacts(&jr.Hello.Facts)
	if err != nil {
		return Server{}, err
	}
	if len(dropped) > 0 {
		h.log.Warn("joining node reported what DNS can't point at; ignored, check its config.toml", "remote", remote, "ignored", dropped)
	}
	srv, err := h.o.Store.Join(ctx, JoinRequest{SecretHash: hashSecret(jr.Secret), Fingerprint: fp, Hello: jr.Hello, RemoteAddr: remote})
	if err != nil && !errors.Is(err, ErrJoinRejected) {
		return Server{}, &internalJoinError{err}
	}
	return srv, err
}

// internalJoinError is a store failure: logged on the control plane, not
// shown to the node.
type internalJoinError struct{ err error }

func (e *internalJoinError) Error() string { return e.err.Error() }
func (e *internalJoinError) Unwrap() error { return e.err }

// joinLimiter rate limits joining-mode connections per source address
// (ADR 0014): a token's secret is 256 bits, so this only keeps a flood of
// handshakes from reaching the store.
type joinLimiter struct {
	mu   sync.Mutex
	seen map[string]joinWindow
}

type joinWindow struct {
	start time.Time
	n     int
}

const (
	joinAttempts = 5
	joinWindowOf = time.Minute
	// joinTracked caps the addresses remembered; when full of fresh
	// entries, joins wait.
	joinTracked = 1024
)

// allow counts a join attempt from addr and reports whether it may go on.
func (l *joinLimiter) allow(addr net.Addr) bool {
	host := addr.String()
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.seen) >= joinTracked {
		for k, w := range l.seen {
			if now.Sub(w.start) > joinWindowOf {
				delete(l.seen, k)
			}
		}
		if len(l.seen) >= joinTracked {
			return false
		}
	}
	w := l.seen[host]
	if now.Sub(w.start) > joinWindowOf {
		w = joinWindow{start: now}
	}
	if w.n >= joinAttempts {
		return false
	}
	w.n++
	l.seen[host] = w
	return true
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
