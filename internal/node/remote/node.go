package remote

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/version"
)

// Node is the `dock node` side: it dials the control plane, keeps the
// connection up and serves Runtime calls on it. It never listens.
type Node struct {
	// Addr is the control plane's node endpoint, host:port.
	Addr string
	// Identity is this node's key, created at join.
	Identity tls.Certificate
	// ControlPlane is the pinned fingerprint of the control plane's key,
	// from the join token.
	ControlPlane string
	Runtime      node.Runtime

	// KeepAlive defaults to DefaultKeepAlive.
	KeepAlive time.Duration
	// MinBackoff and MaxBackoff bound the reconnect delay (default 1s, 30s).
	MinBackoff, MaxBackoff time.Duration
	Logger                 *slog.Logger
}

// Run connects and serves until ctx ends, reconnecting with jittered
// exponential backoff whenever the connection fails or drops.
func (n *Node) Run(ctx context.Context) error {
	minB, maxB := or(n.MinBackoff, time.Second), or(n.MaxBackoff, 30*time.Second)
	log := n.logger()
	backoff := minB
	for {
		connected, err := n.serveOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if connected {
			backoff = minB
		}
		log.Warn("control plane connection ended", "addr", n.Addr, "err", err, "retry_in", backoff)
		select {
		case <-time.After(backoff/2 + rand.N(backoff/2+1)):
		case <-ctx.Done():
			return nil
		}
		backoff = min(backoff*2, maxB)
	}
}

func (n *Node) logger() *slog.Logger {
	if n.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return n.Logger
}

// serveOnce dials, serves the connection until it ends and reports whether
// it got as far as a TLS session.
func (n *Node) serveOnce(ctx context.Context) (bool, error) {
	ka := or(n.KeepAlive, DefaultKeepAlive)
	d := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 10 * time.Second, KeepAlive: ka},
		Config:    nodeTLS(n.Identity, n.ControlPlane),
	}
	raw, err := d.DialContext(ctx, "tcp", n.Addr)
	if err != nil {
		return false, err
	}
	conn := &watchedConn{Conn: opaqueConn{raw}, done: make(chan struct{})}
	srv := &http.Server{
		Handler:   Handler(n.Runtime),
		Protocols: h2Only(),
		HTTP2: &http.HTTP2Config{
			SendPingTimeout: ka, PingTimeout: ka,
			// A call whose handler stops reading (a dial to a container
			// that doesn't read) keeps up to a stream window of unread
			// upload, and that counts against the connection window until
			// read. With the default 1 MB for both, one such call would
			// stall every other call on the link. Memory is only used for
			// bytes actually waiting.
			MaxReceiveBufferPerStream:     streamWindow,
			MaxReceiveBufferPerConnection: (maxDials + 64) * streamWindow,
		},
		ErrorLog: slog.NewLogLogger(n.logger().Handler(), slog.LevelDebug),
	}
	ln := &oneConnListener{conn: conn, closed: make(chan struct{})}
	go func() { _ = srv.Serve(ln) }()
	select {
	case <-conn.done:
	case <-ctx.Done():
	}
	// Closing the connection cancels the context of every call in flight.
	_ = srv.Close()
	_ = conn.Close()
	return true, errors.New("connection closed")
}

// Handler serves rt's methods. It only ever runs on the connection the node
// dialed.
func Handler(rt node.Runtime) http.Handler {
	mux := http.NewServeMux()
	h := func(op string, fn http.HandlerFunc) { mux.Handle("POST /v1/"+op, fn) }

	h(opHello, unary(func(context.Context, noArgs) (Info, error) {
		return Info{Version: version.Version, Protocol: Protocol}, nil
	}))
	h(opPull, withLogs(func(ctx context.Context, a refArgs, _ io.Reader, logs io.Writer) (string, error) {
		return rt.Pull(ctx, a.Ref, logs)
	}))
	h(opHasImage, unary(func(ctx context.Context, a refArgs) (bool, error) { return rt.HasImage(ctx, a.Ref) }))
	h(opEnsureNetwork, unary(func(ctx context.Context, a labeledArgs) (struct{}, error) {
		return struct{}{}, rt.EnsureNetwork(ctx, a.Name, a.Labels)
	}))
	h(opEnsureVolume, unary(func(ctx context.Context, a labeledArgs) (struct{}, error) {
		return struct{}{}, rt.EnsureVolume(ctx, a.Name, a.Labels)
	}))
	h(opStart, unary(rt.Start))
	h(opStop, unary(func(ctx context.Context, a stopArgs) (struct{}, error) {
		return struct{}{}, rt.Stop(ctx, a.ID, a.Timeout)
	}))
	h(opRemove, unary(func(ctx context.Context, a idArgs) (struct{}, error) { return struct{}{}, rt.Remove(ctx, a.ID) }))
	h(opInspect, unary(func(ctx context.Context, a idArgs) (node.Container, error) { return rt.Inspect(ctx, a.ID) }))
	h(opList, unary(func(ctx context.Context, _ noArgs) ([]node.Container, error) { return rt.List(ctx) }))
	h(opHealthCheck, unary(func(ctx context.Context, a healthArgs) (struct{}, error) {
		return struct{}{}, rt.HealthCheck(ctx, a.Container, a.Check)
	}))
	h(opLogs, stream(func(ctx context.Context, a logsArgs) (<-chan node.LogLine, error) { return rt.Logs(ctx, a.ID, a.Opts) }))
	h(opEvents, stream(func(ctx context.Context, _ noArgs) (<-chan node.ContainerEvent, error) { return rt.Events(ctx) }))
	h(opBuild, withLogs(func(ctx context.Context, a buildArgs, source io.Reader, logs io.Writer) (string, error) {
		return rt.Build(ctx, node.BuildRequest{
			Source: source, Dockerfile: a.Dockerfile, BuildCommand: a.BuildCommand,
			StartCommand: a.StartCommand, BuildArgs: a.BuildArgs, Tag: a.Tag, Logs: logs,
		})
	}))
	h(opApplyRoutes, unary(func(ctx context.Context, a routesArgs) (struct{}, error) {
		return struct{}{}, rt.ApplyRoutes(ctx, a.Routes)
	}))
	h(opCheckDomain, unary(func(ctx context.Context, a nameArgs) (node.DomainCheck, error) {
		return rt.CheckDomain(ctx, a.Name)
	}))
	h(opHostStats, unary(func(ctx context.Context, _ noArgs) (node.HostStats, error) { return rt.HostStats(ctx) }))
	h(opCtrStats, unary(func(ctx context.Context, a idsArgs) ([]node.ContainerStats, error) {
		return rt.ContainerStats(ctx, a.IDs)
	}))
	h(opDial, serveDial(rt))
	return mux
}

// unary serves a call with one result.
func unary[A, R any](fn func(context.Context, A) (R, error)) http.HandlerFunc {
	return withLogs(func(ctx context.Context, a A, _ io.Reader, _ io.Writer) (R, error) { return fn(ctx, a) })
}

// withLogs serves a call that may read the rest of the request body and
// writes progress before its result.
func withLogs[A, R any](fn func(context.Context, A, io.Reader, io.Writer) (R, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := newResponder(w)
		a, body, err := readArgs[A](r)
		if err != nil {
			out.fail(&Error{Code: codeBadRequest, Message: err.Error()})
			return
		}
		res, err := fn(r.Context(), a, body, out)
		if err != nil {
			out.fail(wireError(err))
			return
		}
		out.end(res)
	}
}

// stream serves a call that returns a channel, one item frame per element,
// until the channel closes or the control plane cancels.
func stream[A, T any](fn func(context.Context, A) (<-chan T, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := newResponder(w)
		a, _, err := readArgs[A](r)
		if err != nil {
			out.fail(&Error{Code: codeBadRequest, Message: err.Error()})
			return
		}
		ch, err := fn(r.Context(), a)
		if err != nil {
			out.fail(wireError(err))
			return
		}
		// Send headers now: the stream is open even if nothing arrives yet.
		if err := out.send(frame{}); err != nil {
			return
		}
		for v := range ch {
			raw, err := json.Marshal(v)
			if err != nil {
				out.fail(wireError(err))
				return
			}
			if err := out.send(frame{Item: raw}); err != nil {
				return // the control plane went away; ctx ends the producer
			}
		}
		out.end(struct{}{})
	}
}

// readArgs decodes the first line of the request body and returns the rest.
func readArgs[A any](r *http.Request) (A, io.Reader, error) {
	var a A
	br := bufio.NewReader(r.Body)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return a, nil, err
	}
	return a, br, json.Unmarshal(line, &a)
}

// responder writes response frames. Before the first frame it can still
// answer with an error status instead; after it, errors are end frames.
// It is also the io.Writer handed to Pull and Build for their logs.
type responder struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	rc      *http.ResponseController
	enc     *json.Encoder
	started bool
}

func newResponder(w http.ResponseWriter) *responder {
	return &responder{w: w, rc: http.NewResponseController(w), enc: json.NewEncoder(w)}
}

// send writes and flushes one frame. An empty frame only sends headers.
func (o *responder) send(f frame) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.started {
		o.w.Header().Set("Content-Type", "application/x-ndjson")
		o.w.WriteHeader(http.StatusOK)
		o.started = true
	}
	if f.Log != "" || f.Item != nil || f.End {
		if err := o.enc.Encode(f); err != nil {
			return err
		}
	}
	return o.rc.Flush()
}

// Write implements io.Writer for progress logs.
func (o *responder) Write(p []byte) (int, error) {
	if err := o.send(frame{Log: string(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (o *responder) end(result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		o.fail(wireError(err))
		return
	}
	_ = o.send(frame{End: true, Result: raw})
}

func (o *responder) fail(e *Error) {
	o.mu.Lock()
	if !o.started {
		defer o.mu.Unlock()
		status := http.StatusInternalServerError
		switch e.Code {
		case codeNotFound:
			status = http.StatusNotFound
		case codeBadRequest:
			status = http.StatusBadRequest
		}
		o.w.Header().Set("Content-Type", "application/json")
		o.w.WriteHeader(status)
		o.started = true
		_ = o.enc.Encode(e)
		return
	}
	o.mu.Unlock()
	_ = o.send(frame{End: true, Error: e})
}

// watchedConn closes done when the connection is closed, by either side.
type watchedConn struct {
	net.Conn
	once sync.Once
	done chan struct{}
}

func (c *watchedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.done) })
	return err
}

// oneConnListener hands http.Server the single dialed connection.
type oneConnListener struct {
	mu     sync.Mutex
	conn   net.Conn
	closed chan struct{}
	once   sync.Once
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	c := l.conn
	l.conn = nil
	l.mu.Unlock()
	if c != nil {
		return c, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *oneConnListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *oneConnListener) Addr() net.Addr { return dummyAddr{} }

type dummyAddr struct{}

func (dummyAddr) Network() string { return "dock-node" }
func (dummyAddr) String() string  { return "control-plane" }

// h2Only makes net/http speak HTTP/2 with prior knowledge and nothing else.
func h2Only() *http.Protocols {
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	return &p
}

func or[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}
