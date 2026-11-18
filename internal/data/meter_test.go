package data

import (
	"errors"
	"io"
	"net"
	"testing"
)

// zeros is a connection that always has more to read.
type zeros struct{ net.Conn }

func (zeros) Read(p []byte) (int, error)  { clear(p); return len(p), nil }
func (zeros) Write(p []byte) (int, error) { return len(p), nil }

func TestMeteredConnBudgetIsPerRequest(t *testing.T) {
	m := Meter(zeros{}, 1000)
	buf := make([]byte, 600)
	for range 2 {
		if _, err := m.Read(buf); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Read(buf); !errors.Is(err, ErrReadBudget) {
		t.Fatalf("a read past the budget: %v", err)
	}
	// A new request gets a budget of its own; the total keeps counting.
	if _, err := m.Write([]byte("next")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(m, buf); err != nil {
		t.Fatal(err)
	}
	if got := m.Total(); got != 1800 {
		t.Fatalf("total = %d, want 1800", got)
	}
}

func TestMeteredConnReadDoesNotAllocate(t *testing.T) {
	m := Meter(zeros{}, 1<<40)
	buf := make([]byte, 512)
	if n := testing.AllocsPerRun(100, func() { _, _ = m.Read(buf) }); n != 0 {
		t.Fatalf("a read allocates %v times", n)
	}
}
