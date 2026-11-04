package docker

import (
	"bytes"
	"slices"
	"testing"

	"github.com/NordTempo/tempoDock/internal/node"
)

// TestLineWriterCapsLongLines checks that a line with no newline is cut at
// maxLogLine and the next line still arrives whole.
func TestLineWriterCapsLongLines(t *testing.T) {
	var got []node.LogLine
	w := &lineWriter{stream: "stdout", emit: func(l node.LogLine) error { got = append(got, l); return nil }}
	for chunk := range slices.Chunk(bytes.Repeat([]byte("x"), 200<<10), 4096) {
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Write([]byte("\nok\n")); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2", len(got))
	}
	for _, l := range got {
		if len(l.Text) > maxLogLine {
			t.Fatalf("line of %d bytes exceeds %d", len(l.Text), maxLogLine)
		}
	}
	if got[1].Text != "ok" {
		t.Fatalf("second line = %q, want %q", got[1].Text, "ok")
	}
}
