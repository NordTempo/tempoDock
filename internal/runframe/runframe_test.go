package runframe

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// TestRoundTrip: frames come back as written, big writes split at
// MaxPayload, and the end of the stream between frames is io.EOF.
func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	big := bytes.Repeat([]byte("x"), MaxPayload+10)
	if _, err := w.Stream(Stdout).Write(big); err != nil {
		t.Fatal(err)
	}
	_ = w.Resize(120, 40)
	_ = w.Frame(CloseStdin, nil)
	r := NewReader(&buf)
	want := []struct {
		t Type
		n int
	}{{Stdout, MaxPayload}, {Stdout, 10}, {Resize, 4}, {CloseStdin, 0}}
	for _, f := range want {
		typ, p, err := r.Next()
		if err != nil || typ != f.t || len(p) != f.n {
			t.Fatalf("frame = %c %d bytes %v, want %c %d", typ, len(p), err, f.t, f.n)
		}
		if typ == Resize {
			if c, rows, err := ParseResize(p); c != 120 || rows != 40 || err != nil {
				t.Fatalf("resize = %d x %d, %v", c, rows, err)
			}
		}
	}
	if _, _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("after the last frame: %v", err)
	}
}

// TestLimits: a frame claiming more than MaxPayload is refused before it
// is read, and a stream cut inside a frame is unexpected.
func TestLimits(t *testing.T) {
	if _, _, err := NewReader(bytes.NewReader([]byte{'o', 0xff, 0, 0, 0})).Next(); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized frame: %v", err)
	}
	if _, _, err := NewReader(bytes.NewReader([]byte{'o', 0, 0, 0, 5, 'a'})).Next(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("cut frame: %v", err)
	}
}
