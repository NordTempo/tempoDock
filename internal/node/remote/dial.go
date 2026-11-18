package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Dial over the node link (ADR 0016): one call per connection. The node
// dials the container with its own runtime and answers 200 once connected;
// from then on the request body carries the caller's bytes to the container
// and the response body the container's bytes back, raw, with no framing.
//
//   - CloseWrite on the control plane ends the request body, and the node
//     half-closes its TCP connection: the container reads EOF and can still
//     answer.
//   - When the container closes its side, the node ends the call: the
//     caller reads EOF and further writes fail. (A server that half-closes
//     and keeps reading is treated as closed: HTTP/2 can't end a response
//     while still reading its request.)
//   - Close resets the stream (RST_STREAM), and the node closes its
//     connection. A dropped link ends every call with ErrOffline.
//   - Backpressure is HTTP/2 flow control end to end: the pipes below hand
//     bytes over without buffering, so a reader that stops reading stalls
//     the writer on the other side.
const (
	// maxDials caps open dial calls per server. They are long-lived and
	// share the link's HTTP/2 stream limit (250) with every other call.
	maxDials = 64
	// maxForwards caps a server's open forwarded connections (ADR 0063),
	// on a budget of their own: those it accepts for services that moved
	// away, and those dialed to its containers for another server's. A
	// proxy keeps idle keep-alive connections to every forwarded port
	// (Caddy: up to 32 per port, for two minutes), so visitors' traffic
	// doesn't compete with the data panel's dials for maxDials, nor the
	// other way round. One costs each end about 100 KB while open (two
	// copy buffers and the HTTP/2 stream). With maxDials and
	// maxRegistryConns it leaves 74 of the link's 250 streams to every
	// other call.
	maxForwards = 96
	// dialTimeout bounds connecting on the node, on top of the caller's ctx.
	dialTimeout = 10 * time.Second
	// streamWindow is the node's HTTP/2 receive window per call (the
	// default), the most a stalled call can hold.
	streamWindow = 1 << 20
)

var errTooManyDials = fmt.Errorf("too many open connections to containers on this server (limit %d)", maxDials)

var errTooManyForwards = fmt.Errorf("too many open forwarded connections on this server (limit %d)", maxForwards)

// Dial implements node.Containers. ctx bounds connecting only; closing the
// returned conn ends the call.
func (c *Client) Dial(ctx context.Context, req node.DialRequest) (net.Conn, error) {
	s, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	// A forwarded connection counts against the forwards' budget, any
	// other against the dials': which counter only, nothing on the wire.
	count, limit, tooMany := &s.dials, int32(maxDials), errTooManyDials
	if req.Forwarded {
		count, limit, tooMany = &s.forwards, maxForwards, errTooManyForwards
	}
	if count.Add(1) > limit {
		count.Add(-1)
		return nil, fmt.Errorf("server %s: %w", s.name(), tooMany)
	}
	remote := fmt.Sprintf("%s/%s:%d", s.name(), shortID(req.ContainerID), req.Port)
	lc, err := openConn(ctx, s, opDial, dialRequest{ContainerID: req.ContainerID, Port: req.Port}, remote, func() { count.Add(-1) })
	if err != nil {
		count.Add(-1)
		return nil, err
	}
	return lc, nil
}

// openConn makes a raw-bytes call (Dial, the registry tunnel) and returns
// it as a connection once the node answered 200. ctx only bounds the wait
// for that answer; the call outlives it. release runs once the call has
// ended, after a successful open.
func openConn(ctx context.Context, s *session, op string, args any, remote string, release func()) (*linkConn, error) {
	// The call is canceled by Close, or by ctx while connecting.
	callCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, cancel)
	up, upPeer := net.Pipe()
	body, err := open(callCtx, s, op, args, upPeer)
	if !stop() || err != nil {
		cancel()
		_, _ = up.Close(), upPeer.Close()
		if err == nil {
			_ = body.Close()
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	down, downPeer := net.Pipe()
	lc := &linkConn{
		up: up, upPeer: upPeer, down: down, body: body, cancel: cancel, release: release, done: make(chan struct{}),
		local: linkAddr("control-plane"), remote: linkAddr(remote),
	}
	go lc.receive(s, downPeer)
	return lc, nil
}

// linkConn is a raw-bytes call on the link as a net.Conn: a connection to
// a container on a remote server (Dial), or a registry tunnel. Two
// synchronous pipes connect it to the call: Write → up → upPeer → request
// body, and response body → downPeer → down → Read. net.Pipe gives both
// directions deadlines, which pgx relies on to cancel.
type linkConn struct {
	up, upPeer, down net.Conn
	body             io.ReadCloser // the response body
	cancel           context.CancelFunc
	release          func()
	// done is closed when receive returns; err is why the response ended,
	// if not cleanly, and is set before.
	done chan struct{}
	err  error
	once sync.Once

	local, remote linkAddr
}

// receive copies the response body into the read pipe until the call ends,
// then closes both pipes: the caller reads EOF (or err) and writes fail. It
// is the one goroutine Dial starts; the HTTP/2 transport runs another per
// call while the request body is open.
func (c *linkConn) receive(s *session, downPeer net.Conn) {
	defer close(c.done)
	defer c.release()
	_, err := io.Copy(downPeer, c.body)
	_ = c.body.Close()
	if err != nil && !errors.Is(err, io.ErrClosedPipe) && !errors.Is(err, context.Canceled) {
		c.err = offline(context.Background(), s.name(), err)
	}
	_ = downPeer.Close()
	_ = c.upPeer.Close()
	c.cancel()
}

// Read implements net.Conn.
func (c *linkConn) Read(p []byte) (int, error) {
	n, err := c.down.Read(p)
	if errors.Is(err, io.EOF) && c.err != nil {
		// The pipe's EOF came after receive set c.err, through the pipe's
		// close, so reading c.err here is ordered after writing it.
		return n, c.err
	}
	return n, err
}

// Write implements net.Conn. It returns once the link took all of p.
func (c *linkConn) Write(p []byte) (int, error) { return c.up.Write(p) }

// CloseWrite half-closes the connection: the container reads EOF, and
// reading its answer goes on.
func (c *linkConn) CloseWrite() error { return c.up.Close() }

// Close ends the call and waits until its goroutine has exited.
func (c *linkConn) Close() error {
	c.once.Do(func() {
		// Closing the response resets the stream (the node then closes its
		// connection) and wakes an upload waiting for flow control, which
		// canceling the context alone doesn't.
		c.cancel()
		_ = c.body.Close()
		_, _, _ = c.up.Close(), c.upPeer.Close(), c.down.Close()
	})
	<-c.done
	return nil
}

func (c *linkConn) LocalAddr() net.Addr  { return c.local }
func (c *linkConn) RemoteAddr() net.Addr { return c.remote }

func (c *linkConn) SetDeadline(t time.Time) error {
	return errors.Join(c.SetReadDeadline(t), c.SetWriteDeadline(t))
}
func (c *linkConn) SetReadDeadline(t time.Time) error  { return c.down.SetReadDeadline(t) }
func (c *linkConn) SetWriteDeadline(t time.Time) error { return c.up.SetWriteDeadline(t) }

// linkAddr names one end of a linkConn.
type linkAddr string

func (a linkAddr) Network() string { return "dock-node" }
func (a linkAddr) String() string  { return string(a) }

func shortID(id string) string { return id[:min(len(id), 12)] }

// serveDial serves opDial on the node: dial with the runtime, answer 200,
// then copy both ways until either side is done.
func serveDial(rt node.Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, up, err := readArgs[dialRequest](r)
		if err != nil {
			newResponder(w).fail(&Error{Code: codeBadRequest, Message: err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), dialTimeout)
		conn, err := rt.Dial(ctx, node.DialRequest{ContainerID: a.ContainerID, Port: a.Port})
		cancel()
		if err != nil {
			newResponder(w).fail(wireError(err))
			return
		}
		pipeCall(w, r, up, conn)
	}
}

// serveDialAnalytics connects a call to the analytics store's port on
// this server (node.Analytics.DialAnalyticsStore): nothing else is
// reachable through it.
func serveDialAnalytics(rt node.Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, up, err := readArgs[storeKindRequest](r)
		if err != nil {
			newResponder(w).fail(&Error{Code: codeBadRequest, Message: err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), dialTimeout)
		conn, err := rt.DialAnalyticsStore(ctx, node.StoreKind(a.Kind))
		cancel()
		if err != nil {
			newResponder(w).fail(wireError(err))
			return
		}
		pipeCall(w, r, up, conn)
	}
}

// pipeCall answers 200 and copies both ways between the call and conn
// until either side is done: up (the rest of the request body) into conn,
// conn into the response.
func pipeCall(w http.ResponseWriter, r *http.Request, up io.Reader, conn net.Conn) {
	// The call's context ends when the control plane resets it, the
	// link drops or the handler returns. Closing conn then unblocks
	// both copies, even one stuck writing to a peer that doesn't read.
	stop := context.AfterFunc(r.Context(), func() { _ = conn.Close() })
	defer func() { stop(); _ = conn.Close() }()
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		return
	}

	upDone := make(chan struct{})
	go func() {
		defer close(upDone)
		_, err := io.Copy(conn, up)
		if cw, ok := conn.(interface{ CloseWrite() error }); ok && err == nil {
			_ = cw.CloseWrite() // the caller half-closed
			return
		}
		_ = conn.Close() // reset by the control plane, or the link dropped
	}()
	_, _ = io.Copy(flushWriter{w, rc}, conn)
	// conn's side is done (or the call was reset): end the call. Closing
	// the body stops the upload copy if it is still waiting for bytes.
	_ = conn.Close()
	_ = r.Body.Close()
	<-upDone
}

// flushWriter sends every write right away, so small replies (a query
// result) don't wait in the HTTP/2 write buffer.
type flushWriter struct {
	w  io.Writer
	rc *http.ResponseController
}

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if err == nil {
		err = f.rc.Flush()
	}
	return n, err
}
