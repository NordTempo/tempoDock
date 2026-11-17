package build

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/distribution/reference"

	"github.com/NordTempo/tempoDock/internal/node"
)

// The relay carries BuildKit's push to the platform registry (ADR 0089
// amendment). The registry is on the host's loopback (127.0.0.1:5000:
// the registry itself on the control plane, the node link's tunnel on
// other servers), which the BuildKit container on its own network can't
// reach. So while a build exports, and only then:
//
//   - the daemon serves a Unix socket in RelayDir, which the container
//     sees at relaySockDir, and joins each connection on it to the
//     registry's loopback address on the host;
//   - dock-probe relay, run in the container through the exec API,
//     listens on the registry's address in the container's own network
//     namespace and joins each connection to that socket.
//
// BuildKit then pushes to the same reference Docker would, plain HTTP
// on loopback. Build steps share the container's network namespace, but
// the relay starts after the build's last step finished (the build
// function evaluates the result first) and stops when the export ends,
// so no step of any build ever reaches the registry through it.
const (
	// relayProbeDir is where the container sees Probe's directory.
	relayProbeDir = "/dock/probe"
	// relaySockDir is where the container sees RelayDir.
	relaySockDir = "/run/dock-relay"
	relaySock    = "registry.sock"
	// relayReady bounds how long dock-probe relay may take to listen.
	relayReady = 10 * time.Second
	// relayDial bounds the daemon's connection to the registry.
	relayDial = 10 * time.Second
)

// relayHost returns the registry host BuildKit can push p to through
// the relay: p's registry when it is on loopback and this builder has
// the probe and a socket directory. Otherwise "", and Docker pushes.
func (b *Builder) relayHost(p *node.PushTarget) string {
	if b.Probe == "" || b.RelayDir == "" {
		return ""
	}
	named, err := reference.ParseNormalizedNamed(p.Ref)
	if err != nil {
		return ""
	}
	host := reference.Domain(named)
	ap, err := netip.ParseAddrPort(host)
	if err != nil || !ap.Addr().IsLoopback() {
		return ""
	}
	return host
}

// openRelay serves the registry at host (a loopback address) to the
// BuildKit container until stop is called: the daemon's socket first,
// then dock-probe relay in the container, once it listens.
func (b *Builder) openRelay(ctx context.Context, host string) (stop func(), err error) {
	srv, err := listenRelay(filepath.Join(b.RelayDir, relaySock), host)
	if err != nil {
		return nil, fmt.Errorf("the registry relay: %w", err)
	}
	conn, err := b.exec(ctx, relayProbeDir+"/"+filepath.Base(b.Probe), "relay", host, relaySockDir+"/"+relaySock)
	if err != nil {
		srv.close()
		return nil, fmt.Errorf("start the registry relay in %s: %w", b.name, err)
	}
	stopExec := func() {
		if hc, ok := conn.(halfCloser); ok {
			_ = hc.CloseWrite() // its stdin ends: dock-probe relay exits
		}
		_ = conn.Close()
	}
	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(io.LimitReader(conn, 512)).ReadString('\n')
		switch {
		case strings.TrimSpace(line) == "ready":
			ready <- nil
		case strings.TrimSpace(line) != "":
			ready <- errors.New(strings.TrimSpace(line))
		default:
			ready <- err
		}
	}()
	timer := time.NewTimer(relayReady)
	defer timer.Stop()
	select {
	case err = <-ready:
	case <-timer.C:
		err = fmt.Errorf("not listening after %s", relayReady)
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err != nil {
		stopExec()
		srv.close()
		return nil, fmt.Errorf("start the registry relay in %s: %w", b.name, err)
	}
	return func() {
		stopExec()
		srv.close()
	}, nil
}

// relayServer is the daemon's end of the relay: a Unix socket whose
// connections it joins to the registry.
type relayServer struct {
	ln     net.Listener
	target string

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

// listenRelay serves path (replacing a socket a previous daemon left)
// for connections to target.
func listenRelay(path, target string) (*relayServer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	s := &relayServer{ln: ln, target: target, conns: map[net.Conn]struct{}{}}
	s.wg.Go(s.serve)
	return s, nil
}

func (s *relayServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		if !s.track(c) {
			return
		}
		s.wg.Go(func() {
			defer s.untrack(c)
			r, err := net.DialTimeout("tcp", s.target, relayDial)
			if err != nil {
				return
			}
			if !s.track(r) {
				return
			}
			defer s.untrack(r)
			join(c, r)
		})
	}
}

// track records c so close can end it; false (and c closed) once closed.
func (s *relayServer) track(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		_ = c.Close()
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *relayServer) untrack(c net.Conn) {
	_ = c.Close()
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

// close stops listening and ends every connection, including the idle
// keep-alive ones BuildKit's client would hold open, then waits for
// their goroutines.
func (s *relayServer) close() {
	_ = s.ln.Close()
	s.mu.Lock()
	s.closed = true
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	_ = os.Remove(s.ln.Addr().String())
}

// halfCloser is a connection that can end its write side alone.
type halfCloser interface{ CloseWrite() error }

// join copies between a and b until both sides are done, passing on
// each side's end of stream.
func join(a, b net.Conn) {
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(b, a)
		if hc, ok := b.(halfCloser); ok {
			_ = hc.CloseWrite()
		}
		close(done)
	}()
	_, _ = io.Copy(a, b)
	if hc, ok := a.(halfCloser); ok {
		_ = hc.CloseWrite()
	}
	<-done
}
