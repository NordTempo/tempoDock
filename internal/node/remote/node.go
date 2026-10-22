package remote

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/version"
)

// Node is the `dock node` side: it dials the control plane, keeps the
// connection up and serves Runtime calls on it. It never listens.
type Node struct {
	// DataDir holds NodeKeyFile and StateFile, written by Join.
	DataDir string
	// Addr overrides the control plane addresses from StateFile ([node]
	// control_plane), for a control plane that moved. Its key stays
	// pinned.
	Addr    string
	Runtime node.Runtime
	// Facts gathers what the node reports in every hello. Nil reports
	// direct ingress and nothing else.
	Facts func(context.Context) (Facts, error)

	// KeepAlive defaults to DefaultKeepAlive.
	KeepAlive time.Duration
	// MinBackoff and MaxBackoff bound the reconnect delay (default 1s, 30s).
	MinBackoff, MaxBackoff time.Duration
	Logger                 *slog.Logger

	// alpn overrides ALPN in tests.
	alpn string

	mu   sync.Mutex
	link Link
	id   Identity
	// next is the key of a rotation in progress (nextKeyFile): the node
	// connects with it first, and its first hello makes it the key.
	next *Identity

	regOnce  sync.Once
	regConns chan net.Conn
}

// Errors that keep a node out until someone acts. Run logs them and keeps
// retrying at the slowest backoff.
var (
	errKeyRejected = errors.New("the control plane rejected this node's key: the server was revoked or removed; join again with a new token from `dock server join`")
	errVersion     = fmt.Errorf("the control plane doesn't speak %s: upgrade dock, the control plane first", ALPN)
)

// Run connects and serves until ctx ends, reconnecting with jittered
// exponential backoff whenever the connection fails or drops. It fails
// right away only if the node hasn't joined.
func (n *Node) Run(ctx context.Context) error {
	if err := n.load(); err != nil {
		return err
	}
	minB, maxB := or(n.MinBackoff, time.Second), or(n.MaxBackoff, 30*time.Second)
	log := n.logger()
	backoff := minB
	for {
		o := n.serveOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		switch {
		case o.retryNow:
			log.Warn("control plane rejected the new key; going on with the old one", "err", o.err)
			continue
		case o.stuck:
			backoff = maxB
			log.Error("can't connect to the control plane", "err", o.err, "retry_in", backoff)
		default:
			if o.helloed {
				backoff = minB
			}
			log.Warn("control plane connection ended", "err", o.err, "retry_in", backoff)
		}
		select {
		case <-time.After(backoff/2 + rand.N(backoff/2+1)):
		case <-ctx.Done():
			return nil
		}
		backoff = min(backoff*2, maxB)
	}
}

// load reads the link and keys written by Join (and a rotation).
func (n *Node) load() error {
	link, err := LoadLink(n.DataDir)
	if err != nil {
		return err
	}
	id, err := loadIdentity(filepath.Join(n.DataDir, NodeKeyFile))
	if err != nil {
		return err
	}
	var next *Identity
	if nid, err := loadIdentity(filepath.Join(n.DataDir, nextKeyFile)); err == nil {
		next = &nid
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if n.Addr != "" {
		link.Addrs = []string{n.Addr}
	}
	n.mu.Lock()
	n.link, n.id, n.next = link, id, next
	n.mu.Unlock()
	return nil
}

func (n *Node) logger() *slog.Logger {
	if n.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return n.Logger
}

// outcome is how one connection attempt ended.
type outcome struct {
	err error
	// helloed: the session got as far as hello, so backoff starts over.
	helloed bool
	// stuck: retrying won't help until someone acts (revoked key, pin
	// mismatch, protocol versions).
	stuck bool
	// retryNow: the new key of a rotation was rejected and dropped.
	retryNow bool
}

// serveOnce dials, serves the connection until it ends and reports how.
func (n *Node) serveOnce(ctx context.Context) outcome {
	n.mu.Lock()
	link, id, usingNext := n.link, n.id, n.next != nil
	if usingNext {
		id = *n.next
	}
	n.mu.Unlock()
	ka := or(n.KeepAlive, DefaultKeepAlive)
	conn, err := dialControlPlane(ctx, link.Addrs, id, link.ControlPlaneKey, or(n.alpn, ALPN), ka)
	if err != nil {
		err, stuck := explainDial(err)
		return outcome{err: err, stuck: stuck}
	}
	st := &connState{usingNext: usingNext}
	firstErr := serveConn(ctx, conn, n.handler(st), ka, n.logger())
	if st.helloed.Load() {
		return outcome{err: errors.New("connection closed"), helloed: true}
	}
	err, stuck := explainDial(firstErr)
	if usingNext && errors.Is(err, errKeyRejected) {
		// The control plane never recorded the new key (or lost it):
		// forget it and keep the old one.
		n.mu.Lock()
		n.next = nil
		n.mu.Unlock()
		_ = os.Remove(filepath.Join(n.DataDir, nextKeyFile))
		return outcome{err: err, retryNow: true}
	}
	return outcome{err: err, stuck: stuck}
}

// dialControlPlane connects to the first address that answers, pinning
// the control plane's key in the handshake.
func dialControlPlane(ctx context.Context, addrs []string, id Identity, controlPlane, proto string, ka time.Duration) (*tls.Conn, error) {
	d := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 10 * time.Second, KeepAlive: ka},
		Config:    nodeTLS(id, controlPlane, proto),
	}
	var errs []error
	for _, addr := range addrs {
		c, err := d.DialContext(ctx, "tcp", addr)
		if err == nil {
			return c.(*tls.Conn), nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", addr, err))
	}
	return nil, errors.Join(errs...)
}

// explainDial turns a failed connection into words, and says whether
// retrying can help.
func explainDial(err error) (error, bool) {
	var pe *pinError
	switch {
	case err == nil:
		return errors.New("connection closed before hello"), false
	case errors.As(err, &pe):
		return pe, true
	case remoteAlert(err, 42): // bad_certificate: the key was refused
		return errKeyRejected, true
	case remoteAlert(err, 120): // no_application_protocol
		return errVersion, true
	}
	return err, false
}

// remoteAlert reports whether err is the TLS alert a from the peer.
func remoteAlert(err error, a tls.AlertError) bool {
	var oe *net.OpError
	return errors.As(err, &oe) && oe.Op == "remote error" && oe.Err.Error() == a.Error()
}

// connState is what the node learns about one connection.
type connState struct {
	usingNext bool
	helloed   atomic.Bool
}

// handler serves the control plane's calls on one connection.
func (n *Node) handler(st *connState) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /v1/"+opHello, unary(func(ctx context.Context, req helloRequest) (Hello, error) {
		return n.hello(ctx, req, st)
	}))
	mux.Handle("POST /v1/"+opRotate, unary(func(context.Context, empty) (rotateResult, error) { return n.rotate() }))
	mux.HandleFunc("POST /v1/"+opRegistry, n.serveRegistryCall)
	runtimeRoutes(mux, n.Runtime)
	return mux
}

// hello answers the control plane's hello. The first hello on a
// connection with the new key of a rotation makes it the node's key.
func (n *Node) hello(ctx context.Context, req helloRequest, st *connState) (Hello, error) {
	h, err := gatherHello(ctx, n.Facts)
	if err != nil {
		return h, err
	}
	if Protocol < req.MinProtocol {
		n.logger().Error("the control plane needs a newer dock: upgrade dock on this server",
			"control_plane_version", req.Version, "min_protocol", req.MinProtocol, "protocol", Protocol)
	}
	if st.usingNext && !st.helloed.Load() {
		n.promoteNext()
	}
	st.helloed.Store(true)
	return h, nil
}

// gatherHello builds the hello for this binary and machine.
func gatherHello(ctx context.Context, facts func(context.Context) (Facts, error)) (Hello, error) {
	h := Hello{Version: version.Version, Protocol: Protocol}
	if facts != nil {
		f, err := facts(ctx)
		if err != nil {
			return h, err
		}
		h.Facts = f
	}
	if h.Ingress == "" {
		h.Ingress = IngressDirect
	}
	return h, nil
}

// rotate creates the next key and stores it, for Hub.RotateKey.
func (n *Node) rotate() (rotateResult, error) {
	id, err := NewIdentity()
	if err != nil {
		return rotateResult{}, err
	}
	if err := id.save(filepath.Join(n.DataDir, nextKeyFile)); err != nil {
		return rotateResult{}, err
	}
	n.mu.Lock()
	n.next = &id
	n.mu.Unlock()
	n.logger().Info("created a new node key; reconnecting with it", "key", id.Fingerprint())
	return rotateResult{Fingerprint: id.Fingerprint()}, nil
}

// promoteNext makes the next key the node's key, after the control plane
// accepted it.
func (n *Node) promoteNext() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.next == nil {
		return
	}
	if err := os.Rename(filepath.Join(n.DataDir, nextKeyFile), filepath.Join(n.DataDir, NodeKeyFile)); err != nil {
		n.logger().Error("keep the new node key", "err", err)
		return
	}
	n.id, n.next = *n.next, nil
	n.logger().Info("node key rotated", "key", n.id.Fingerprint())
}

// runtimeRoutes serves rt's methods.
func runtimeRoutes(mux *http.ServeMux, rt node.Runtime) {
	h := func(op string, fn http.HandlerFunc) { mux.Handle("POST /v1/"+op, fn) }

	h(opPull, withLogs(func(ctx context.Context, a pullRequest, _ io.Reader, logs io.Writer) (imageIDResult, error) {
		id, err := rt.Pull(ctx, a.Ref, logs, node.PullOptions{Token: a.Token})
		return imageIDResult{ImageID: id}, err
	}))
	h(opHasImage, unary(func(ctx context.Context, a imageRequest) (hasImageResult, error) {
		ok, err := rt.HasImage(ctx, a.Ref)
		return hasImageResult{Present: ok}, err
	}))
	h(opRemoveImage, unary(func(ctx context.Context, a imageRequest) (empty, error) {
		return empty{}, rt.RemoveImage(ctx, a.Ref)
	}))
	h(opEnsureNetwork, unary(func(ctx context.Context, a resourceRequest) (empty, error) {
		return empty{}, rt.EnsureNetwork(ctx, a.Name, a.Labels.node())
	}))
	h(opEnsureVolume, unary(func(ctx context.Context, a resourceRequest) (empty, error) {
		return empty{}, rt.EnsureVolume(ctx, a.Name, a.Labels.node())
	}))
	h(opStart, unary(func(ctx context.Context, a startRequest) (containerResult, error) {
		c, err := rt.Start(ctx, a.Spec.node())
		return containerResult{Container: toWireContainer(c)}, err
	}))
	h(opStop, unary(func(ctx context.Context, a stopRequest) (empty, error) {
		return empty{}, rt.Stop(ctx, a.ID, ms(a.TimeoutMS))
	}))
	h(opRemove, unary(func(ctx context.Context, a containerRequest) (empty, error) { return empty{}, rt.Remove(ctx, a.ID) }))
	h(opInspect, unary(func(ctx context.Context, a containerRequest) (containerResult, error) {
		c, err := rt.Inspect(ctx, a.ID)
		return containerResult{Container: toWireContainer(c)}, err
	}))
	h(opList, unary(func(ctx context.Context, _ empty) (containersResult, error) {
		cs, err := rt.List(ctx)
		return containersResult{Containers: toWireContainers(cs)}, err
	}))
	h(opReleaseCtrs, unary(func(ctx context.Context, a releaseRequest) (idsResult, error) {
		ids, err := rt.ReleaseContainers(ctx, a.ReleaseID)
		return idsResult{IDs: ids}, err
	}))
	h(opHealthCheck, unary(func(ctx context.Context, a healthRequest) (empty, error) {
		return empty{}, rt.HealthCheck(ctx, a.Container.node(), a.Check.node())
	}))
	h(opLogs, stream(func(ctx context.Context, a logsRequest) (<-chan node.LogLine, error) {
		return rt.Logs(ctx, a.ID, node.LogOptions{Follow: a.Follow, Tail: a.Tail, Since: a.Since})
	}, toWireLogLine))
	h(opEvents, stream(func(ctx context.Context, _ empty) (<-chan node.ContainerEvent, error) {
		return rt.Events(ctx)
	}, toWireEvent))
	h(opBuild, withLogs(func(ctx context.Context, a buildRequest, source io.Reader, logs io.Writer) (imageIDResult, error) {
		id, err := rt.Build(ctx, node.BuildRequest{
			Source: source, Dockerfile: a.Dockerfile, BuildCommand: a.BuildCommand, StartCommand: a.StartCommand,
			BuildArgs: a.BuildArgs, CacheScope: a.CacheScope, Tag: a.Tag, Push: a.Push.node(), Logs: logs,
		})
		return imageIDResult{ImageID: id}, err
	}))
	h(opApplyRoutes, unary(func(ctx context.Context, a routesRequest) (empty, error) {
		return empty{}, rt.ApplyRoutes(ctx, fromWireRoutes(a.Routes))
	}))
	h(opCheckDomain, unary(func(ctx context.Context, a domainRequest) (wireDomainCheck, error) {
		d, err := rt.CheckDomain(ctx, a.Hostname)
		return toWireDomainCheck(d), err
	}))
	h(opHostStats, unary(func(ctx context.Context, _ empty) (wireHostStats, error) {
		s, err := rt.HostStats(ctx)
		return toWireHostStats(s), err
	}))
	h(opCtrStats, unary(func(ctx context.Context, a containerStatsRequest) (containerStatsResult, error) {
		s, err := rt.ContainerStats(ctx, a.IDs)
		return containerStatsResult{Stats: toWireContainerStats(s)}, err
	}))
	h(opDial, serveDial(rt))
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

// stream serves a call that returns a channel, one item frame per element
// converted by conv, until the channel closes or the control plane
// cancels.
func stream[A, T, W any](fn func(context.Context, A) (<-chan T, error), conv func(T) W) http.HandlerFunc {
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
			raw, err := json.Marshal(conv(v))
			if err != nil {
				out.fail(wireError(err))
				return
			}
			if err := out.send(frame{Item: raw}); err != nil {
				return // the control plane went away; ctx ends the producer
			}
		}
		out.end(empty{})
	}
}

// readArgs decodes the first line of the request body and returns the rest.
func readArgs[A any](r *http.Request) (A, io.Reader, error) {
	var a A
	lr := newLineReader(r.Body)
	line, err := lr.next()
	if err != nil {
		return a, nil, err
	}
	return a, lr.br, json.Unmarshal(line, &a)
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

// Write implements io.Writer for progress logs, in frames of at most
// maxLogChunk bytes, split on rune boundaries.
func (o *responder) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		n := min(len(p), maxLogChunk)
		for n < len(p) && n > maxLogChunk-utf8.UTFMax && !utf8.RuneStart(p[n]) {
			n--
		}
		if err := o.send(frame{Log: string(p[:n])}); err != nil {
			return written, err
		}
		written += n
		p = p[n:]
	}
	return written, nil
}

func (o *responder) end(result any) {
	raw, err := json.Marshal(result)
	if err == nil && len(raw) > maxMessage-64 {
		err = fmt.Errorf("result of %d bytes is over the link's %d byte limit", len(raw), maxMessage)
	}
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

// serveConn serves the control plane's calls on conn until the connection
// ends or ctx does. Closing it cancels every call in flight. If the
// control plane never sent a byte, it returns the read error that ended
// the connection: after a TLS 1.3 handshake, that is where a refused key
// shows up.
func serveConn(ctx context.Context, conn *tls.Conn, h http.Handler, ka time.Duration, log *slog.Logger) error {
	wc := &watchedConn{Conn: opaqueConn{conn}, done: make(chan struct{})}
	srv := &http.Server{
		Handler:        h,
		Protocols:      h2Only(),
		MaxHeaderBytes: maxHeaderBytes,
		HTTP2: &http.HTTP2Config{
			SendPingTimeout: ka, PingTimeout: ka,
			// A call whose handler stops reading (a dial to a container
			// that doesn't read) keeps up to a stream window of unread
			// upload, and that counts against the connection window until
			// read. With the default 1 MB for both, one such call would
			// stall every other call on the link. Memory is only used for
			// bytes actually waiting.
			MaxReceiveBufferPerStream:     streamWindow,
			MaxReceiveBufferPerConnection: (maxDials + maxRegistryConns + 64) * streamWindow,
		},
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}
	ln := &oneConnListener{conn: wc, closed: make(chan struct{})}
	served := make(chan struct{})
	go func() { _ = srv.Serve(ln); close(served) }()
	select {
	case <-wc.done:
	case <-ctx.Done():
	}
	_ = srv.Close()
	_ = wc.Close()
	<-served
	return wc.firstErr()
}

// watchedConn closes done when the connection is closed, by either side,
// and remembers why reading failed if nothing was ever read.
type watchedConn struct {
	net.Conn
	once sync.Once
	done chan struct{}

	mu      sync.Mutex
	gotData bool
	readErr error
}

func (c *watchedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.mu.Lock()
	if n > 0 {
		c.gotData = true
	} else if err != nil && !c.gotData && c.readErr == nil {
		c.readErr = err
	}
	c.mu.Unlock()
	return n, err
}

func (c *watchedConn) firstErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readErr
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
