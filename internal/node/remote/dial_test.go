package remote

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

func TestDial(t *testing.T) {
	h := newHarness(t)
	echo := newTCPServer(t, serveEcho)
	h.rt.DialTo(echo.addr)
	ct := h.startService("db-1")
	ctx := context.Background()
	baseline := runtime.NumGoroutine()

	t.Run("round trip and half-close", func(t *testing.T) {
		c := h.dial(ctx, ct.ID)
		defer func() { _ = c.Close() }()
		if _, err := c.Write([]byte("SELECT 1;")); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 9)
		if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "SELECT 1;" {
			t.Fatalf("read %q, %v", buf, err)
		}
		// After CloseWrite the server still answers, then closes: EOF.
		if _, err := c.Write([]byte("bye")); err != nil {
			t.Fatal(err)
		}
		if err := c.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
			t.Fatal(err)
		}
		if rest, err := io.ReadAll(c); err != nil || string(rest) != "bye" {
			t.Fatalf("after CloseWrite read %q, %v", rest, err)
		}
		if _, err := c.Write([]byte("x")); err == nil {
			t.Fatal("write after CloseWrite succeeded")
		}
	})

	t.Run("large transfer both ways", func(t *testing.T) {
		c := h.dial(ctx, ct.ID)
		defer func() { _ = c.Close() }()
		payload := make([]byte, 32<<20)
		_, _ = rand.Read(payload)
		go func() {
			_, _ = c.Write(payload)
			_ = c.(interface{ CloseWrite() error }).CloseWrite()
		}()
		got, err := io.ReadAll(c)
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("echoed %d of %d bytes intact=%v, %v", len(got), len(payload), bytes.Equal(got, payload), err)
		}
	})

	t.Run("ctx only bounds connecting", func(t *testing.T) {
		dctx, cancel := context.WithCancel(ctx)
		c := h.dial(dctx, ct.ID)
		defer func() { _ = c.Close() }()
		cancel()
		roundTrip(t, c, "still open")
	})

	t.Run("deadlines", func(t *testing.T) {
		c := h.dial(ctx, ct.ID)
		defer func() { _ = c.Close() }()
		_ = c.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
		var ne net.Error
		if _, err := c.Read(make([]byte, 1)); !errors.As(err, &ne) || !ne.Timeout() || !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("read past deadline = %v, want a timeout", err)
		}
		// A timeout doesn't break the connection: clear it and go on.
		_ = c.SetReadDeadline(time.Time{})
		roundTrip(t, c, "after timeout")
	})

	t.Run("close unblocks a pending read", func(t *testing.T) {
		c := h.dial(ctx, ct.ID)
		errc := make(chan error, 1)
		go func() { _, err := c.Read(make([]byte, 1)); errc <- err }()
		time.Sleep(20 * time.Millisecond)
		_ = c.Close()
		select {
		case err := <-errc:
			if err == nil {
				t.Fatal("read after close succeeded")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("read still blocked after Close")
		}
	})

	// Every call above was closed: nothing is left running on either side,
	// and the node closed its connections to the container.
	waitFor(t, "echo connections to close", func() bool { return echo.open.Load() == 0 })
	waitFor(t, "goroutines to exit", func() bool { return runtime.NumGoroutine() <= baseline })
}

func TestDialErrors(t *testing.T) {
	h := newHarness(t)
	h.rt.DialTo(newTCPServer(t, serveEcho).addr)
	ctx := context.Background()

	if _, err := h.client.Dial(ctx, node.DialRequest{ContainerID: "nope", Port: 5432}); !errors.Is(err, node.ErrNotFound) {
		t.Fatalf("unknown container = %v, want ErrNotFound", err)
	}
	app := h.start("not-a-service") // no service label
	if _, err := h.client.Dial(ctx, node.DialRequest{ContainerID: app.ID, Port: 5432}); !errors.Is(err, node.ErrNotFound) {
		t.Fatalf("container without a service label = %v, want ErrNotFound", err)
	}
	db := h.startService("db-1")
	if err := h.client.Stop(ctx, db.ID, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.Dial(ctx, node.DialRequest{ContainerID: db.ID, Port: 5432}); err == nil || errors.Is(err, ErrOffline) {
		t.Fatalf("stopped container = %v, want an error from the node", err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := h.client.Dial(canceled, node.DialRequest{ContainerID: db.ID, Port: 5432}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ctx = %v", err)
	}
}

func TestDialLimit(t *testing.T) {
	h := newHarness(t)
	h.rt.DialTo(newTCPServer(t, serveEcho).addr)
	ct := h.startService("db-1")
	var conns []net.Conn
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for range maxDials {
		conns = append(conns, h.dial(context.Background(), ct.ID))
	}
	if _, err := h.client.Dial(context.Background(), node.DialRequest{ContainerID: ct.ID, Port: 5432}); !errors.Is(err, errTooManyDials) {
		t.Fatalf("dial over the limit = %v", err)
	}
	// Other calls still get through while all dial slots are taken.
	if _, err := h.client.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = conns[0].Close()
	conns[0] = h.dial(context.Background(), ct.ID)
}

func TestDialBackpressure(t *testing.T) {
	h := newHarness(t)
	var sent atomic.Int64
	src := newTCPServer(t, func(c net.Conn) {
		buf := make([]byte, 64<<10)
		for {
			n, err := c.Write(buf)
			sent.Add(int64(n))
			if err != nil {
				return
			}
		}
	})
	h.rt.DialTo(src.addr)
	ct := h.startService("db-1")
	c := h.dial(context.Background(), ct.ID)

	// Nobody reads: the server is held back by flow control on the link and
	// TCP buffers, instead of the bytes piling up in memory.
	time.Sleep(300 * time.Millisecond)
	stalled := sent.Load()
	time.Sleep(300 * time.Millisecond)
	if sent.Load() != stalled || stalled > 64<<20 {
		t.Fatalf("server kept sending without a reader: %d then %d bytes", stalled, sent.Load())
	}
	t.Logf("buffered while stalled: %d KB", stalled>>10)
	if _, err := io.CopyN(io.Discard, c, 2*stalled); err != nil {
		t.Fatalf("reading resumes the stream: %v", err)
	}
	_ = c.Close()
	waitFor(t, "node to close its connection", func() bool { return src.open.Load() == 0 })
}

func TestDialCancelAndDrop(t *testing.T) {
	h := newHarness(t)
	echo := newTCPServer(t, serveEcho)
	h.rt.DialTo(echo.addr)
	ct := h.startService("db-1")

	t.Run("close mid-stream", func(t *testing.T) {
		c := h.dial(context.Background(), ct.ID)
		go func() { _, _ = io.Copy(c, rand.Reader) }() // until Close
		if _, err := io.CopyN(io.Discard, c, 4<<20); err != nil {
			t.Fatal(err)
		}
		_ = c.Close()
		waitFor(t, "node to close its connection", func() bool { return echo.open.Load() == 0 })
	})

	t.Run("link drop", func(t *testing.T) {
		c := h.dial(context.Background(), ct.ID)
		defer func() { _ = c.Close() }()
		roundTrip(t, c, "before the drop")
		h.proxy.cut()
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, err := io.ReadAll(c)
		if !errors.Is(err, ErrOffline) {
			t.Fatalf("read after drop = %v, want ErrOffline", err)
		}
		waitFor(t, "node to close its connection", func() bool { return echo.open.Load() == 0 })
	})
}

// BenchmarkDial measures a connection over the node link on loopback TLS:
// bulk throughput each way, and the round trip of a small message (a
// query and its answer).
func BenchmarkDial(b *testing.B) {
	h := newHarness(b)
	ct := h.startService("db-1")
	const size = 64 << 20
	payload := make([]byte, size)

	b.Run("download", func(b *testing.B) {
		h.rt.DialTo(newTCPServer(b, func(c net.Conn) { _, _ = c.Write(payload) }).addr)
		b.SetBytes(size)
		for b.Loop() {
			c := h.dial(context.Background(), ct.ID)
			if n, err := io.Copy(io.Discard, c); err != nil || n != size {
				b.Fatal(n, err)
			}
			_ = c.Close()
		}
	})
	b.Run("upload", func(b *testing.B) {
		h.rt.DialTo(newTCPServer(b, func(c net.Conn) {
			_, _ = io.Copy(io.Discard, c)
			_, _ = c.Write([]byte{1})
		}).addr)
		b.SetBytes(size)
		for b.Loop() {
			c := h.dial(context.Background(), ct.ID)
			if _, err := c.Write(payload); err != nil {
				b.Fatal(err)
			}
			_ = c.(interface{ CloseWrite() error }).CloseWrite()
			if _, err := io.ReadFull(c, make([]byte, 1)); err != nil {
				b.Fatal(err)
			}
			_ = c.Close()
		}
	})
	b.Run("round-trip", func(b *testing.B) {
		h.rt.DialTo(newTCPServer(b, serveEcho).addr)
		c := h.dial(context.Background(), ct.ID)
		defer func() { _ = c.Close() }()
		msg, buf := []byte("SELECT 1"), make([]byte, 8)
		var samples []time.Duration
		for b.Loop() {
			t0 := time.Now()
			if _, err := c.Write(msg); err != nil {
				b.Fatal(err)
			}
			if _, err := io.ReadFull(c, buf); err != nil {
				b.Fatal(err)
			}
			samples = append(samples, time.Since(t0))
		}
		slices.Sort(samples)
		b.ReportMetric(float64(samples[len(samples)/2].Microseconds()), "p50-µs")
		b.ReportMetric(float64(samples[len(samples)*99/100].Microseconds()), "p99-µs")
	})
}

// startService starts a container with a service label, which Dial needs.
func (h *harness) startService(name string) node.Container {
	ct, err := h.client.Start(context.Background(), node.ContainerSpec{
		Name: name, Image: "postgres:17-alpine", Labels: node.Labels{Service: "3", Release: "7"},
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return ct
}

func (h *harness) dial(ctx context.Context, id string) net.Conn {
	h.t.Helper()
	c, err := h.client.Dial(ctx, node.DialRequest{ContainerID: id, Port: 5432})
	if err != nil {
		h.t.Fatal(err)
	}
	return c
}

func roundTrip(t *testing.T, c net.Conn, msg string) {
	t.Helper()
	if _, err := c.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != msg {
		t.Fatalf("echo %q = %q, %v", msg, buf, err)
	}
}

// tcpServer stands in for a database: it serves each connection with fn
// and counts the ones still open.
type tcpServer struct {
	addr string
	open atomic.Int32
}

func newTCPServer(t testing.TB, fn func(net.Conn)) *tcpServer {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s := &tcpServer{addr: ln.Addr().String()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.open.Add(1)
			go func() {
				defer s.open.Add(-1)
				defer func() { _ = c.Close() }()
				fn(c)
			}()
		}
	}()
	return s
}

// serveEcho echoes until the client half-closes, then half-closes too and
// waits for the client to close.
func serveEcho(c net.Conn) {
	_, _ = io.Copy(c, c)
	_ = c.(*net.TCPConn).CloseWrite()
	_, _ = io.Copy(io.Discard, c)
}

func TestDialStalledUploadDoesNotBlockTheLink(t *testing.T) {
	h := newHarness(t)
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	stuck := newTCPServer(t, func(c net.Conn) {
		_ = c.(*net.TCPConn).SetReadBuffer(4 << 10)
		<-done // never reads
	})
	h.rt.DialTo(stuck.addr)
	ct := h.startService("db-1")
	baseline := runtime.NumGoroutine()
	c := h.dial(context.Background(), ct.ID)
	var wrote atomic.Int64
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		buf := make([]byte, 64<<10)
		for {
			n, err := c.Write(buf)
			wrote.Add(int64(n))
			if err != nil {
				return
			}
		}
	}()
	// Wait until the upload is stuck: nothing more gets through.
	for last := int64(-1); wrote.Load() != last; {
		last = wrote.Load()
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("stalled after %d KB", wrote.Load()>>10)

	// The stuck call holds its own flow control window, not the link's:
	// other calls, uploads included, still get through.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := h.client.List(ctx); err != nil {
		t.Fatalf("list next to a stalled dial: %v", err)
	}
	payload := make([]byte, 4<<20)
	if _, err := h.client.Build(ctx, node.BuildRequest{Source: bytes.NewReader(payload), Tag: "app:1", Logs: io.Discard}); err != nil {
		t.Fatalf("build next to a stalled dial: %v", err)
	}

	// Closing unblocks the writer here and the node's copy into the
	// container. Only the stuck server's own goroutine is left.
	_ = c.Close()
	<-writerDone
	waitFor(t, "goroutines to exit", func() bool { return runtime.NumGoroutine() <= baseline+1 })
}
