package core

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The writer splits overlong lines, and the file and the published lines
// agree on every line.
func TestLogWriterSplitsLongLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "build.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	var emitted []string
	w := &logWriter{f: f, emit: func(seq int, line string) {
		if seq != len(emitted) {
			t.Errorf("seq %d, want %d", seq, len(emitted))
		}
		emitted = append(emitted, line)
	}}
	long := strings.Repeat("x", maxLogLine+10)
	for _, chunk := range []string{"one\ntw", "o\n", long[:100], long[100:], "\nlast"} {
		if n, err := w.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	w.Close()

	want := []string{"one", "two", long[:maxLogLine], long[maxLogLine:], "last"}
	if !slices.Equal(emitted, want) {
		t.Errorf("emitted %d lines, want %d", len(emitted), len(want))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n"); !slices.Equal(got, want) {
		t.Errorf("file has %d lines, want %d", len(got), len(want))
	}
}

// Past its limit a log ends with one marker line (after finishing a
// partial line) and drops the rest; the build isn't told.
func TestLogWriterStopsAtLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "build.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	var emitted []string
	w := &logWriter{f: f, limit: 1 << 20, emit: func(_ int, line string) { emitted = append(emitted, line) }}
	line := strings.Repeat("x", 1023) + "\n"
	for range 1023 {
		_, _ = w.Write([]byte(line))
	}
	partial := "part" + strings.Repeat("y", 1020) // reaches the limit
	for _, chunk := range []string{partial[:4], partial[4:], "more\n", "and more\n"} {
		if n, err := w.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	w.Close()
	marker := "[build log truncated at 1 MB; the rest of the output was dropped]"
	if len(emitted) != 1025 || emitted[1023] != partial || emitted[1024] != marker {
		t.Fatalf("emitted %d lines, ending %.40q", len(emitted), emitted[len(emitted)-2:])
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(b), "\n"+partial+"\n"+marker+"\n") || len(b) > 1<<20+100 {
		t.Fatalf("file of %d bytes ends %q", len(b), b[len(b)-90:])
	}
}

// A cursor reads complete lines in bounded batches, leaves a partial last
// line for later, and resumes where it stopped.
func TestLogCursorBatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "build.log")
	var pos logCursor
	read := func() (firsts []int, lines []string) {
		t.Helper()
		err := pos.read(path, func(first int, batch []string) error {
			if len(batch) > logBatchLines {
				t.Errorf("batch of %d lines", len(batch))
			}
			firsts = append(firsts, first)
			lines = append(lines, batch...)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return firsts, lines
	}
	if _, lines := read(); lines != nil {
		t.Fatalf("missing file: %v", lines)
	}

	var b strings.Builder
	for i := range 2500 {
		b.WriteString(strings.Repeat("y", i%7) + "\n")
	}
	b.WriteString("partial")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	firsts, lines := read()
	if !slices.Equal(firsts, []int{0, 1000, 2000}) || len(lines) != 2500 || lines[6] != "yyyyyy" {
		t.Fatalf("firsts %v, %d lines", firsts, len(lines))
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(" now done\nnext\n")
	_ = f.Close()
	firsts, lines = read()
	if !slices.Equal(firsts, []int{2500}) || !slices.Equal(lines, []string{"partial now done", "next"}) {
		t.Fatalf("resumed: firsts %v, lines %q", firsts, lines)
	}
}
