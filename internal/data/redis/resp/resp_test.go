package resp

import (
	"errors"
	"io"
	"net"
	"strings"
	"testing"
)

// pipeConn returns a Conn reading what the fake server writes, then EOF.
func pipeConn(t *testing.T, server string) *Conn {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close() })
	go func() { _, _ = b.Write([]byte(server)); _ = b.Close() }()
	return New(a)
}

func TestReplies(t *testing.T) {
	c := pipeConn(t, "+OK\r\n-WRONGPASS bad pair\r\n:42\r\n$5\r\nhello\r\n$-1\r\n*2\r\n$1\r\na\r\n*1\r\n:1\r\n$10\r\n0123456789\r\n")
	c.MaxBulk = 4
	c.pending = 7
	want := []func(Value) bool{
		func(v Value) bool { return v.Kind == Status && v.Str == "OK" },
		func(v Value) bool {
			e, ok := errors.AsType[*ServerError](v.Err())
			return ok && e.Code() == "WRONGPASS"
		},
		func(v Value) bool { return v.Kind == Int && v.Int == 42 },
		func(v Value) bool { return v.Kind == Bulk && v.Str == "hell" && v.Len == 5 && v.Cut() },
		func(v Value) bool { return v.Kind == Null },
		func(v Value) bool {
			return v.Kind == Array && len(v.Arr) == 2 && v.Arr[0].Str == "a" && v.Arr[1].Arr[0].Int == 1
		},
		func(v Value) bool { return v.Str == "0123" && v.Len == 10 },
	}
	for i, ok := range want {
		v, err := c.Receive()
		if err != nil || !ok(v) {
			t.Fatalf("reply %d: %+v %v", i, v, err)
		}
	}
}

func TestBadReplies(t *testing.T) {
	for _, server := range []string{"?x\r\n", ":12x\r\n", "$3\r\nabcd", "*999999\r\n", strings.Repeat("*1\r\n", 20) + ":1\r\n", "+no crlf\n"} {
		c := pipeConn(t, server)
		if _, err := c.Receive(); err == nil || !c.Broken() {
			t.Errorf("%q: %v, broken %t", server, err, c.Broken())
		}
	}
}

func TestSend(t *testing.T) {
	var c Conn
	c.Send("SET", "k", "a\r\nb")
	if got := string(c.w); got != "*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$4\r\na\r\nb\r\n" {
		t.Fatalf("encoded %q", got)
	}
}

// FuzzRead: a hostile server gets an error, never a panic or a huge
// allocation.
func FuzzRead(f *testing.F) {
	f.Add([]byte("*2\r\n$1\r\na\r\n:1\r\n"))
	f.Add([]byte("$-1\r\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		a, srv := net.Pipe()
		defer func() { _ = a.Close() }()
		go func() { _, _ = srv.Write(b); _ = srv.Close() }()
		c := New(a)
		c.MaxBulk = 64
		_, _ = c.read(0)
	})
}

// A pipeline's replies are held together, so the cap is on all of them:
// 100 replies of 1 MiB, each well within one reply's cap, fail the
// pipeline; a few of them are read.
func TestPipelineBudget(t *testing.T) {
	pipeline := func(n int) (*Conn, []Value, error) {
		a, b := net.Pipe()
		t.Cleanup(func() { _ = a.Close() })
		go func() { _, _ = io.Copy(io.Discard, b) }()
		go func() {
			defer func() { _ = b.Close() }()
			big := []byte("$1048576\r\n" + strings.Repeat("x", 1<<20) + "\r\n")
			for range n {
				if _, err := b.Write(big); err != nil {
					return
				}
			}
		}()
		c := New(a)
		cmds := make([][]string, n)
		for i := range cmds {
			cmds[i] = []string{"GET", "k"}
		}
		replies, err := c.Pipeline(cmds...)
		return c, replies, err
	}
	if c, _, err := pipeline(100); !errors.Is(err, ErrBadConn) || !c.Broken() {
		t.Fatalf("100 MiB of replies: got %v, want ErrBadConn", err)
	}
	if c, replies, err := pipeline(3); err != nil || len(replies) != 3 || c.Broken() || c.Pending() != 0 {
		t.Fatalf("3 MiB of replies: %d replies, %v", len(replies), err)
	}
}

// A whole reply is capped, not only each array: 64 arrays of 200 000
// empty arrays (48 MB sent) decoded to ~860 MB before.
func TestReplyBudget(t *testing.T) {
	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close() })
	go func() {
		defer func() { _ = b.Close() }()
		inner := "*200000\r\n" + strings.Repeat("*0\r\n", 200_000)
		if _, err := b.Write([]byte("*64\r\n")); err != nil {
			return
		}
		for range 64 {
			if _, err := b.Write([]byte(inner)); err != nil {
				return
			}
		}
	}()
	c := New(a)
	if _, err := c.Receive(); !errors.Is(err, ErrBadConn) || !c.Broken() {
		t.Fatalf("got %v, want ErrBadConn", err)
	}
	// Status lines count against the bytes: 5 000 of 15 KB is 75 MB.
	c = pipeConn(t, "*5000\r\n"+strings.Repeat("+"+strings.Repeat("x", 15<<10)+"\r\n", 5000))
	if _, err := c.Receive(); !errors.Is(err, ErrBadConn) {
		t.Fatalf("status lines: got %v, want ErrBadConn", err)
	}
}
