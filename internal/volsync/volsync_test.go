//go:build linux

package volsync

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// pass runs one pass from src to dst the way the control plane joins the
// two jobs: recv's output into send's input and back.
func pass(t *testing.T, src, dst string, mode Mode) (Report, Report) {
	t.Helper()
	manR, manW := io.Pipe()
	opsR, opsW := io.Pipe()
	type result struct {
		rep Report
		err error
	}
	recv := make(chan result, 1)
	go func() {
		rep, err := Recv(dst, opsR, manW)
		_ = manW.CloseWithError(err)
		_ = opsR.CloseWithError(err)
		recv <- result{rep, err}
	}()
	sent, err := Send(src, manR, opsW, mode)
	_ = opsW.CloseWithError(err)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	r := <-recv
	if r.err != nil {
		t.Fatalf("recv: %v", r.err)
	}
	return sent, r.rep
}

func write(t *testing.T, p, data string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// TestPassesConverge copies a tree, changes it the ways a live service
// does (an in-place rewrite of the same size, a new file, a removed
// directory, a type change, links), and checks that a second pass makes
// the target equal and a check pass then finds nothing.
func TestPassesConverge(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	old := time.Now().Add(-time.Hour)
	write(t, filepath.Join(src, "base/16384/1259"), "page-a", old)
	write(t, filepath.Join(src, "gone/x"), "x", old)
	write(t, filepath.Join(src, "becomes-dir"), "file", old)
	write(t, filepath.Join(src, "big"), strings.Repeat("0123456789", 50_000), old)
	if err := os.Symlink("base/16384/1259", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(src, "big"), filepath.Join(src, "big-link")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(src, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(src, 0o700); err != nil {
		t.Fatal(err)
	}

	sent, got := pass(t, src, dst, Live)
	if sent.Changed == 0 || sent.Changed != got.Changed || got.Bytes != 500_000+6+1+4 {
		t.Fatalf("first pass: sent %+v, applied %+v", sent, got)
	}

	// An in-place rewrite with the same size and time: only the racy
	// mark of a recent file, or a time change, finds it.
	write(t, filepath.Join(src, "base/16384/1259"), "page-b", old.Add(time.Second))
	write(t, filepath.Join(src, "base/new"), "new", old)
	if err := os.RemoveAll(filepath.Join(src, "gone")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(src, "becomes-dir")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(src, "becomes-dir/inner"), "in", old)
	if sent, got = pass(t, src, dst, Final); sent.Deleted != 1 || got.Deleted != 1 {
		t.Fatalf("second pass: sent %+v, applied %+v", sent, got)
	}
	if sent, _ := pass(t, src, dst, Check); sent.Changed != 0 {
		t.Fatalf("check after the final pass: %+v", sent)
	}

	if b, err := os.ReadFile(filepath.Join(dst, "base/16384/1259")); err != nil || string(b) != "page-b" {
		t.Fatalf("rewritten file: %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "gone")); !os.IsNotExist(err) {
		t.Fatalf("removed directory is still there: %v", err)
	}
	if l, err := os.Readlink(filepath.Join(dst, "link")); err != nil || l != "base/16384/1259" {
		t.Fatalf("symlink: %q, %v", l, err)
	}
	var a, b syscall.Stat_t
	if syscall.Stat(filepath.Join(dst, "big"), &a) != nil || syscall.Stat(filepath.Join(dst, "big-link"), &b) != nil || a.Ino != b.Ino {
		t.Fatal("hard link not kept")
	}
	if st, err := os.Stat(dst); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("root mode: %v, %v", st.Mode(), err)
	}
	st, err := os.Stat(filepath.Join(dst, "base/16384/1259"))
	if err != nil || st.Mode().Perm() != 0o640 || !st.ModTime().Equal(old.Add(time.Second)) {
		t.Fatalf("file metadata: %v %v, %v", st.Mode(), st.ModTime(), err)
	}
	dir, _ := os.Stat(filepath.Join(src, "base"))
	if got, _ := os.Stat(filepath.Join(dst, "base")); !got.ModTime().Equal(dir.ModTime()) {
		t.Fatalf("directory time %v, source %v", got.ModTime(), dir.ModTime())
	}
}

// TestLivePassResendsRecentFiles: a file modified just now is marked
// racy, so a write in the same clock tick (same size and time) can't
// hide from the next pass.
func TestLivePassResendsRecentFiles(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	now := time.Now()
	write(t, filepath.Join(src, "wal"), "aaaa", now)
	if sent, _ := pass(t, src, dst, Live); sent.Racy != 1 {
		t.Fatalf("a file written just now isn't racy: %+v", sent)
	}
	write(t, filepath.Join(src, "wal"), "bbbb", now) // same size, same time
	if sent, _ := pass(t, src, dst, Check); sent.Changed != 1 {
		t.Fatalf("the check doesn't see the racy file: %+v", sent)
	}
	pass(t, src, dst, Final)
	if b, _ := os.ReadFile(filepath.Join(dst, "wal")); string(b) != "bbbb" {
		t.Fatalf("got %q", b)
	}
}

// TestRecvRejectsACutStream: a pass whose source stops halfway fails
// instead of passing for complete.
func TestRecvRejectsACutStream(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "f"), strings.Repeat("x", 300_000), time.Now().Add(-time.Hour))
	manR, manW := io.Pipe()
	opsR, opsW := io.Pipe()
	errs := make(chan error, 1)
	go func() {
		_, err := Recv(dst, opsR, manW)
		_ = manW.Close()
		errs <- err
	}()
	go func() {
		_, _ = Send(src, manR, &cutWriter{w: opsW, left: 1000}, Final)
		_ = opsW.Close()
	}()
	if err := <-errs; err == nil || !strings.Contains(err.Error(), "ended before the pass did") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "f")); !os.IsNotExist(err) {
		t.Fatal("a partial file was put in place")
	}
}

// cutWriter passes on the first left bytes, then fails.
type cutWriter struct {
	w    io.Writer
	left int
}

func (c *cutWriter) Write(p []byte) (int, error) {
	if len(p) > c.left {
		n, _ := c.w.Write(p[:c.left])
		c.left = 0
		return n, io.ErrClosedPipe
	}
	c.left -= len(p)
	return c.w.Write(p)
}
