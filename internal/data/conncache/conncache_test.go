package conncache

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/data"
)

// fake is a connection that records whether it was closed.
type fake struct {
	id     int
	closed atomic.Bool
	// dirty makes it not Reusable (an open transaction).
	dirty bool
}

// newCache returns a cache of fakes with the given limits, and open, which
// opens a new fake each call.
func newCache(t *testing.T, idle time.Duration, perKey, max int, emptied func(int64)) (*Cache[*fake], func(context.Context) (*fake, error)) {
	t.Helper()
	var n atomic.Int32
	c := New(Options[*fake]{
		IdleTimeout: idle, MaxPerKey: perKey, Max: max,
		Reusable: func(f *fake) bool { return !f.dirty },
		Close:    func(f *fake, _ bool) { f.closed.Store(true) },
		Emptied:  emptied,
	})
	t.Cleanup(c.Close)
	return c, func(context.Context) (*fake, error) { return &fake{id: int(n.Add(1))}, nil }
}

var reader1 = Key{1, data.RoleReader}

func TestReuseAndLimits(t *testing.T) {
	ctx := t.Context()
	c, open := newCache(t, time.Minute, 2, 3, nil)

	a, err := c.Acquire(ctx, reader1, open)
	if err != nil {
		t.Fatal(err)
	}
	c.Release(a)
	again, err := c.Acquire(ctx, reader1, open)
	if err != nil || again != a {
		t.Fatalf("got %v (%v), want the released connection back", again, err)
	}
	b, err := c.Acquire(ctx, reader1, open)
	if err != nil {
		t.Fatal(err)
	}

	// Per key: a third caller waits for a release, until its context ends.
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := c.Acquire(short, reader1, open); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third: %v", err)
	}
	time.AfterFunc(20*time.Millisecond, func() { c.Release(a) })
	got, err := c.Acquire(ctx, reader1, open)
	if err != nil || got != a {
		t.Fatalf("waiter got %v (%v), want the released one", got, err)
	}
	c.Release(got)
	c.Release(b)

	// At the total limit, the longest-idle connection of another key makes
	// room.
	other := Key{2, data.RoleReader}
	x, err := c.Acquire(ctx, other, open)
	if err != nil {
		t.Fatal(err)
	}
	y, err := c.Acquire(ctx, other, open)
	if err != nil {
		t.Fatal(err)
	}
	if c.Len() != 3 || c.LenKey(reader1) != 1 || !a.Conn.closed.Load() {
		t.Fatalf("eviction: %d conns, %d of key 1, a closed %t", c.Len(), c.LenKey(reader1), a.Conn.closed.Load())
	}
	c.Release(x)
	c.Release(y)
}

func TestIdleClose(t *testing.T) {
	ctx := t.Context()
	emptied := make(chan int64, 1)
	c, open := newCache(t, 30*time.Millisecond, 2, 8, func(id int64) { emptied <- id })
	a, err := c.Acquire(ctx, reader1, open)
	if err != nil {
		t.Fatal(err)
	}
	c.Release(a)
	select {
	case id := <-emptied:
		if id != 1 || !a.Conn.closed.Load() || c.Len() != 0 {
			t.Fatalf("emptied %d, closed %t, %d left", id, a.Conn.closed.Load(), c.Len())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("idle connection never closed")
	}
}

func TestDiscardAndDrop(t *testing.T) {
	ctx := t.Context()
	c, open := newCache(t, time.Minute, 2, 8, nil)

	// Broken and not reusable connections close on release.
	a, _ := c.Acquire(ctx, reader1, open)
	a.Broken = true
	c.Release(a)
	b, _ := c.Acquire(ctx, reader1, open)
	b.Conn.dirty = true
	c.Release(b)
	if !a.Conn.closed.Load() || !b.Conn.closed.Load() || c.Len() != 0 {
		t.Fatalf("closed %t %t, %d left", a.Conn.closed.Load(), b.Conn.closed.Load(), c.Len())
	}

	// Drop closes idle connections now and busy ones on release.
	idle, _ := c.Acquire(ctx, reader1, open)
	busy, _ := c.Acquire(ctx, reader1, open)
	c.Release(idle)
	c.Drop(func(k Key) bool { return k.ID == 1 })
	if !idle.Conn.closed.Load() || busy.Conn.closed.Load() {
		t.Fatalf("after drop: idle closed %t, busy closed %t", idle.Conn.closed.Load(), busy.Conn.closed.Load())
	}
	c.Release(busy)
	if !busy.Conn.closed.Load() || c.Len() != 0 {
		t.Fatalf("busy not closed on release (%d left)", c.Len())
	}

	// A failed open leaves no slot taken; after Close nothing opens.
	if _, err := c.Acquire(ctx, reader1, func(context.Context) (*fake, error) { return nil, errors.New("refused") }); err == nil || c.Len() != 0 {
		t.Fatalf("failed open: %v, %d conns", err, c.Len())
	}
	c.Close()
	if _, err := c.Acquire(ctx, reader1, open); !errors.Is(err, ErrClosed) {
		t.Fatalf("after close: %v", err)
	}
}
