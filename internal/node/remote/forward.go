package remote

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Forwarding over the node link (ADR 0063), protocol 21. The node never
// calls the control plane, so a listener on a node is two calls:
//
//   - forward-listen {port}: the node listens on 127.0.0.1:port with its
//     runtime (node.Proxy.Forward), answers 200, then writes one line per
//     accepted connection: its ID. Ending the call closes the listener
//     and the connections nobody claimed.
//   - forward-accept {port, id}: claims an accepted connection and pipes
//     it like dial: the request body to the connection, the connection
//     to the response body.
//
// A connection not claimed within forwardClaimTimeout is closed.
const forwardClaimTimeout = 10 * time.Second

type forwardListenRequest struct {
	Port int `json:"port"`
}

type forwardAcceptRequest struct {
	Port int    `json:"port"`
	ID   uint64 `json:"id"`
}

// Forward implements node.Proxy: a listener on the node whose Accept
// returns connections over the link.
func (c *Client) Forward(ctx context.Context, port int) (net.Listener, error) {
	if err := c.needProtocol(ctx, MoveSyncProtocol, "forward a moved environment's connections"); err != nil {
		return nil, err
	}
	s, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	body, err := open(callCtx, s, opForwardListen, forwardListenRequest{Port: port}, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	return &linkListener{s: s, port: port, body: body, lines: bufio.NewReader(body), cancel: cancel,
		addr: linkAddr(fmt.Sprintf("%s/127.0.0.1:%d", s.name(), port))}, nil
}

// linkListener is a node's listener on the control plane.
type linkListener struct {
	s      *session
	port   int
	body   io.ReadCloser
	lines  *bufio.Reader
	cancel context.CancelFunc
	addr   linkAddr
	once   sync.Once
	mu     sync.Mutex // one Accept at a time reads lines
}

// Accept waits for the node's next connection and claims it.
func (l *linkListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	line, err := l.lines.ReadString('\n')
	l.mu.Unlock()
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
			return nil, net.ErrClosed
		}
		return nil, offline(context.Background(), l.s.name(), err)
	}
	id, err := strconv.ParseUint(line[:len(line)-1], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("server %s: bad connection id %q", l.s.name(), line)
	}
	if l.s.dials.Add(1) > maxDials {
		l.s.dials.Add(-1)
		return nil, fmt.Errorf("server %s: %w", l.s.name(), errTooManyDials)
	}
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	conn, err := openConn(ctx, l.s, opForwardAccept, forwardAcceptRequest{Port: l.port, ID: id}, string(l.addr), func() { l.s.dials.Add(-1) })
	if err != nil {
		l.s.dials.Add(-1)
		return nil, err
	}
	return conn, nil
}

// Close ends the listen call: the node stops listening.
func (l *linkListener) Close() error {
	l.once.Do(func() {
		l.cancel()
		_ = l.body.Close()
	})
	return nil
}

func (l *linkListener) Addr() net.Addr { return l.addr }

// forwards is a node's accepted connections waiting to be claimed.
type forwards struct {
	mu   sync.Mutex
	next uint64
	held map[forwardKey]net.Conn
}

type forwardKey struct {
	port int
	id   uint64
}

// hold keeps conn for a forward-accept and returns its ID; it is closed
// if nobody claims it in time.
func (f *forwards) hold(port int, conn net.Conn) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	k := forwardKey{port, f.next}
	f.held[k] = conn
	time.AfterFunc(forwardClaimTimeout, func() {
		if c := f.take(k); c != nil {
			_ = c.Close()
		}
	})
	return k.id
}

func (f *forwards) take(k forwardKey) net.Conn {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.held[k]
	delete(f.held, k)
	return c
}

// serveForwardListen listens with the runtime and announces every
// connection until the call ends.
func (f *forwards) serveForwardListen(rt node.Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, _, err := readArgs[forwardListenRequest](r)
		if err != nil {
			newResponder(w).fail(&Error{Code: codeBadRequest, Message: err.Error()})
			return
		}
		ln, err := rt.Forward(r.Context(), a.Port)
		if err != nil {
			newResponder(w).fail(wireError(err))
			return
		}
		stop := context.AfterFunc(r.Context(), func() { _ = ln.Close() })
		defer func() { stop(); _ = ln.Close() }()
		rc := http.NewResponseController(w)
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		if err := rc.Flush(); err != nil {
			return
		}
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			id := f.hold(a.Port, conn)
			if _, err := io.WriteString(w, strconv.FormatUint(id, 10)+"\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

// serveForwardAccept pipes a held connection through the call.
func (f *forwards) serveForwardAccept() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, up, err := readArgs[forwardAcceptRequest](r)
		if err != nil {
			newResponder(w).fail(&Error{Code: codeBadRequest, Message: err.Error()})
			return
		}
		conn := f.take(forwardKey{a.Port, a.ID})
		if conn == nil {
			newResponder(w).fail(wireError(fmt.Errorf("connection %d on port %d: %w", a.ID, a.Port, node.ErrNotFound)))
			return
		}
		pipeCall(w, r, up, conn)
	}
}
