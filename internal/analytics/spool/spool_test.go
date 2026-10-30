package spool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T, dir string, opts Options) *Spool {
	t.Helper()
	s, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func appendN(t *testing.T, s *Spool, from, n int) {
	t.Helper()
	for i := from; i < from+n; i++ {
		if err := s.Append(fmt.Appendf(nil, `{"i":%d}`, i)); err != nil {
			t.Fatal(err)
		}
	}
}

// readAll reads everything after p in batches of n, returning the lines
// and the position after the last.
func readAll(t *testing.T, s *Spool, p uint64, n int) ([]string, uint64) {
	t.Helper()
	var out []string
	for {
		b, err := s.Read(p, n)
		if err != nil {
			t.Fatal(err)
		}
		if b.Position < p {
			t.Fatalf("position went back from %x to %x", p, b.Position)
		}
		for _, l := range b.Lines {
			out = append(out, string(l))
		}
		if len(b.Lines) == 0 {
			return out, b.Position
		}
		p = b.Position
	}
}

func TestReadAcrossSegmentsInOrder(t *testing.T) {
	s := open(t, t.TempDir(), Options{SegmentBytes: 100})
	appendN(t, s, 0, 50)
	lines, end := readAll(t, s, 0, 7)
	if len(lines) != 50 || lines[0] != `{"i":0}` || lines[49] != `{"i":49}` {
		t.Fatalf("read %d lines: first %q, last %q", len(lines), lines[0], lines[len(lines)-1])
	}
	appendN(t, s, 50, 1)
	if more, _ := readAll(t, s, end, 7); len(more) != 1 || more[0] != `{"i":50}` {
		t.Errorf("after the end: %q", more)
	}
}

func TestAckDeletesFinishedSegments(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, Options{SegmentBytes: 100})
	appendN(t, s, 0, 50)
	_, end := readAll(t, s, 0, 100)
	if err := s.Ack(end, 50); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.seg"))
	if len(files) != 1 {
		t.Errorf("%d segments left after acknowledging everything, want the current one", len(files))
	}
	if st := s.Stats(); st.Pending != 0 {
		t.Errorf("pending = %d", st.Pending)
	}
}

func TestCapDropsTheOldestAndCountsThem(t *testing.T) {
	s := open(t, t.TempDir(), Options{SegmentBytes: 100, MaxBytes: 300})
	appendN(t, s, 0, 100)
	st := s.Stats()
	if st.Bytes > 300 || st.Dropped == 0 || st.Pending+st.Dropped != 100 {
		t.Fatalf("stats = %+v; want at most 300 bytes and every event pending or dropped", st)
	}
	b, err := s.Read(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Lines) == 0 || string(b.Lines[len(b.Lines)-1]) == `{"i":0}` {
		t.Errorf("read %d lines starting with the dropped ones", len(b.Lines))
	}
	// A reader whose position was dropped learns it.
	if b, _ := s.Read(Pos(1, 10), 10); !b.Skipped {
		t.Error("reading from a dropped segment isn't marked skipped")
	}
}

func TestReopenTrimsATornLineAndKeepsPositions(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, s, 0, 3)
	_ = s.Close()
	// A crash in the middle of a write.
	f, _ := os.OpenFile(filepath.Join(dir, "0000000001.seg"), os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"i":`)
	_ = f.Close()

	s = open(t, dir, Options{})
	appendN(t, s, 3, 1)
	lines, _ := readAll(t, s, 0, 10)
	if len(lines) != 4 || lines[3] != `{"i":3}` {
		t.Errorf("after reopening: %q", lines)
	}
	// Segments from before a restart are estimated, not read.
	if st := s.Stats(); st.Pending < 1 {
		t.Errorf("pending = %d, want the reopened events estimated", st.Pending)
	}
}

func TestWaitWakesOnAppend(t *testing.T) {
	s := open(t, t.TempDir(), Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Wait(ctx, 0) }()
	select {
	case err := <-done:
		t.Fatalf("Wait returned before an append: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	appendN(t, s, 0, 1)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAppendRefusesBadLines(t *testing.T) {
	s := open(t, t.TempDir(), Options{})
	for _, l := range []string{"", "a\nb", string(make([]byte, MaxLine+1))} {
		if s.Append([]byte(l)) == nil {
			t.Errorf("appended a %d-byte line with newlines or none", len(l))
		}
	}
}

func TestAcknowledgedSegmentsArentSkipped(t *testing.T) {
	s := open(t, t.TempDir(), Options{SegmentBytes: 100})
	appendN(t, s, 0, 30)
	b, err := s.Read(0, 5)
	if err != nil {
		t.Fatal(err)
	}
	// Acknowledge past the first segment's end, so it is deleted.
	_, end := readAll(t, s, 0, 100)
	if err := s.Ack(end, 30); err != nil {
		t.Fatal(err)
	}
	appendN(t, s, 30, 1)
	if b, _ = s.Read(b.Position, 5); b.Skipped {
		t.Error("a segment deleted after it was acknowledged reads as dropped events")
	}
}

func TestResumePastTheStore(t *testing.T) {
	s := open(t, t.TempDir(), Options{})
	appendN(t, s, 0, 2)
	// The store has more than the spool: say a power loss cut the end
	// of the segment after the store took it.
	cursor := Pos(1, 10_000)
	if err := s.Resume(cursor); err != nil {
		t.Fatal(err)
	}
	appendN(t, s, 2, 1)
	b, err := s.Read(cursor, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Lines) != 1 || string(b.Lines[0]) != `{"i":2}` || b.Position <= cursor {
		t.Errorf("after resuming: %q at %x, want the new event past the cursor %x", b.Lines, b.Position, cursor)
	}
}

func TestReadFromTheMiddleOfALine(t *testing.T) {
	s := open(t, t.TempDir(), Options{})
	appendN(t, s, 0, 3)
	b, err := s.Read(Pos(1, 3), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Lines) != 2 || string(b.Lines[0]) != `{"i":1}` {
		t.Errorf("from inside the first line: %q, want the lines after it", b.Lines)
	}
}
