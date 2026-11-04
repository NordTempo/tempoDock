package logarchive

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

var t0 = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

// lines makes n stdout lines a second apart, from first on.
func lines(first, n int) []node.LogLine {
	out := make([]node.LogLine, n)
	for i := range n {
		out[i] = node.LogLine{Time: t0.Add(time.Duration(first+i) * time.Second), Stream: "stdout", Text: fmt.Sprintf("line %d", first+i)}
	}
	return out
}

func write(t testing.TB, root, service, release string, limits node.LogLimits, ls []node.LogLine) int64 {
	t.Helper()
	n, err := Write(root, service, release, limits, func(add func(node.LogLine) error) error {
		for _, l := range ls {
			if err := add(l); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func read(t *testing.T, root string, q node.LogArchiveQuery) []string {
	t.Helper()
	ch, err := Read(context.Background(), root, q)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for l := range ch {
		out = append(out, l.Text)
	}
	return out
}

// Members appended by two containers of one release read as one stream,
// in order, with time and stream kept; tail and since narrow it.
func TestWriteAndRead(t *testing.T) {
	root := t.TempDir()
	write(t, root, "4", "17", node.LogLimits{}, lines(0, 3))
	second := lines(3, 2)
	second[1].Stream = "stderr"
	write(t, root, "4", "17", node.LogLimits{}, second)

	q := node.LogArchiveQuery{Service: "4", Release: "17"}
	if got := read(t, root, q); !slices.Equal(got, []string{"line 0", "line 1", "line 2", "line 3", "line 4"}) {
		t.Fatalf("all = %v", got)
	}
	ch, _ := Read(context.Background(), root, node.LogArchiveQuery{Service: "4", Release: "17", Tail: 1})
	if l := <-ch; l.Text != "line 4" || l.Stream != "stderr" || !l.Time.Equal(t0.Add(4*time.Second)) {
		t.Fatalf("last = %+v", l)
	}
	q.Tail = 2
	if got := read(t, root, q); !slices.Equal(got, []string{"line 3", "line 4"}) {
		t.Fatalf("tail 2 = %v", got)
	}
	q.Tail, q.Since = 0, t0.Add(2*time.Second)
	if got := read(t, root, q); !slices.Equal(got, []string{"line 2", "line 3", "line 4"}) {
		t.Fatalf("since = %v", got)
	}
	q.Tail = 10
	if got := read(t, root, q); !slices.Equal(got, []string{"line 2", "line 3", "line 4"}) {
		t.Fatalf("since with a long tail = %v", got)
	}
}

func TestReadMissingAndBadIDs(t *testing.T) {
	root := t.TempDir()
	if _, err := Read(context.Background(), root, node.LogArchiveQuery{Service: "1", Release: "2"}); !errors.Is(err, node.ErrNotFound) {
		t.Fatalf("missing archive: %v", err)
	}
	for _, id := range []string{"", "..", "../1", "1/2", "01", "+1", "-1", "1 "} {
		if _, err := Read(context.Background(), root, node.LogArchiveQuery{Service: id, Release: "2"}); err == nil || errors.Is(err, node.ErrNotFound) {
			t.Errorf("service %q: %v", id, err)
		}
		if _, err := Write(root, "1", id, node.LogLimits{}, func(func(node.LogLine) error) error { return nil }); err == nil {
			t.Errorf("write release %q: no error", id)
		}
	}
}

// A member cut short by a crash while appending loses only itself.
func TestReadDamagedEnd(t *testing.T) {
	root := t.TempDir()
	write(t, root, "1", "2", node.LogLimits{}, lines(0, 3))
	p := filepath.Join(root, "1", "2.jsonl.gz")
	var member bytes.Buffer
	gz := gzip.NewWriter(&member)
	_, _ = gz.Write([]byte(`{"t":"2026-11-03T12:00:09Z","s":"stdout","x":"lost"}` + "\n"))
	_ = gz.Close()
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write(member.Bytes()[:member.Len()/2])
	_ = f.Close()
	if got := read(t, root, node.LogArchiveQuery{Service: "1", Release: "2", Tail: 2}); !slices.Equal(got, []string{"line 1", "line 2"}) {
		t.Fatalf("tail of a damaged archive = %v", got)
	}
}

// Limits drop a service's oldest archives first, by size and by age, and
// leave other services alone.
func TestLimits(t *testing.T) {
	root := t.TempDir()
	size := write(t, root, "1", "10", node.LogLimits{}, lines(0, 50))
	write(t, root, "2", "20", node.LogLimits{}, lines(0, 50))
	age := func(service, release string, d time.Duration) {
		t.Helper()
		when := time.Now().Add(-d)
		if err := os.Chtimes(filepath.Join(root, service, release+ext), when, when); err != nil {
			t.Fatal(err)
		}
	}
	age("1", "10", 3*time.Hour)
	write(t, root, "1", "11", node.LogLimits{}, lines(0, 50))
	age("1", "11", 2*time.Hour)
	// Room for two: the oldest goes.
	write(t, root, "1", "12", node.LogLimits{MaxBytes: 2*size + size/2}, lines(0, 50))
	if got := archives(t, root, "1"); !slices.Equal(got, []string{"11", "12"}) {
		t.Fatalf("after the size limit: %v", got)
	}
	r, err := Prune(root, node.LogPrune{KeepServices: []string{"1", "2"}, Limits: node.LogLimits{MaxAge: time.Hour}})
	if err != nil || r.Removed != 1 || r.ReclaimedBytes <= 0 {
		t.Fatalf("prune by age = %+v, %v", r, err)
	}
	if got := archives(t, root, "1"); !slices.Equal(got, []string{"12"}) {
		t.Fatalf("after the age limit: %v", got)
	}
	if got := archives(t, root, "2"); !slices.Equal(got, []string{"20"}) {
		t.Fatalf("another service: %v", got)
	}
}

// Prune removes the archives of services that are gone, and stale
// temporary files a crash left; anything else in the directory stays.
func TestPruneGoneServices(t *testing.T) {
	root := t.TempDir()
	write(t, root, "1", "10", node.LogLimits{}, lines(0, 5))
	write(t, root, "2", "20", node.LogLimits{}, lines(0, 5))
	stale := filepath.Join(root, "1", tempPrefix+"x")
	if err := os.WriteFile(stale, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * tempStale)
	_ = os.Chtimes(stale, old, old)
	if err := os.Mkdir(filepath.Join(root, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Service 2 may have been created after the caller listed services.
	if r, err := Prune(root, node.LogPrune{KeepServices: []string{"1"}}); err != nil || r.Removed != 0 {
		t.Fatalf("prune of a recent service = %+v, %v", r, err)
	}
	_ = os.Chtimes(filepath.Join(root, "2"), old, old)
	r, err := Prune(root, node.LogPrune{KeepServices: []string{"1"}})
	if err != nil || r.Removed != 1 {
		t.Fatalf("prune = %+v, %v", r, err)
	}
	if _, err := os.Stat(filepath.Join(root, "2")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a gone service's archives: %v", err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale temporary file: %v", err)
	}
	for _, p := range []string{filepath.Join(root, "1", "10"+ext), filepath.Join(root, "notes")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatal(err)
		}
	}
	if r, err := Prune(filepath.Join(root, "none"), node.LogPrune{}); err != nil || r.Removed != 0 {
		t.Fatalf("prune of no archives = %+v, %v", r, err)
	}
}

// archives lists a service's release IDs, sorted.
func archives(t *testing.T, root, service string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(root, service, "*"+ext))
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(m))
	for _, p := range m {
		out = append(out, filepath.Base(p)[:len(filepath.Base(p))-len(ext)])
	}
	slices.Sort(out)
	return out
}

// A failing fill leaves no archive behind.
func TestWriteFailureLeavesNothing(t *testing.T) {
	root := t.TempDir()
	_, err := Write(root, "1", "2", node.LogLimits{}, func(add func(node.LogLine) error) error {
		_ = add(node.LogLine{Text: "a"})
		return io.ErrUnexpectedEOF
	})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "1"))
	if len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
}

// A nearly full disk keeps no new history (by bytes or by percent free),
// and what was kept before stays readable.
func TestLowDiskKeepsNothing(t *testing.T) {
	root := t.TempDir()
	write(t, root, "1", "1", node.LogLimits{}, lines(0, 3))
	for _, free := range [][2]uint64{
		{512 << 20, 100 << 30}, // 512 MB of 100 GB
		{4 << 30, 200 << 30},   // 4 GB, but 2% of 200 GB
	} {
		freeSpace = func(string) (uint64, uint64, bool) { return free[0], free[1], true }
		_, err := Write(root, "1", "2", node.LogLimits{}, func(add func(node.LogLine) error) error {
			t.Fatal("read a container's log on a full disk")
			return nil
		})
		if !errors.Is(err, ErrLowDisk) {
			t.Fatalf("free %d of %d: %v", free[0], free[1], err)
		}
	}
	freeSpace = diskFree
	if got := archives(t, root, "1"); !slices.Equal(got, []string{"1"}) {
		t.Fatalf("archives = %v", got)
	}
	if got := read(t, root, node.LogArchiveQuery{Service: "1", Release: "1"}); len(got) != 3 {
		t.Fatalf("kept history = %v", got)
	}
}

// A disk that fills while a member is written (ENOSPC) leaves the
// release's earlier archive as it was, and no temporary file.
func TestDiskFullMidWrite(t *testing.T) {
	root := t.TempDir()
	write(t, root, "1", "2", node.LogLimits{}, lines(0, 2))
	_, err := Write(root, "1", "2", node.LogLimits{}, func(add func(node.LogLine) error) error {
		_ = add(node.LogLine{Text: "half"})
		return syscall.ENOSPC
	})
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatal(err)
	}
	if got := read(t, root, node.LogArchiveQuery{Service: "1", Release: "2"}); !slices.Equal(got, []string{"line 0", "line 1"}) {
		t.Fatalf("after a failed append = %v", got)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "1"))
	if len(entries) != 1 {
		t.Fatalf("left behind: %v", entries)
	}
}

// The server-wide cap removes the oldest archives whatever their service,
// on Write and on Prune.
func TestTotalLimit(t *testing.T) {
	root := t.TempDir()
	size := write(t, root, "1", "1", node.LogLimits{}, lines(0, 50))
	write(t, root, "2", "2", node.LogLimits{}, lines(0, 50))
	old := time.Now().Add(-time.Hour)
	for _, p := range []string{filepath.Join(root, "1", "1"+ext), filepath.Join(root, "2", "2"+ext)} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
		old = old.Add(time.Minute) // service 1's is the oldest
	}
	// Room for two archives of this size: writing a third drops service 1's.
	write(t, root, "3", "3", node.LogLimits{MaxTotalBytes: 2*size + size/2}, lines(0, 50))
	if a, b, c := archives(t, root, "1"), archives(t, root, "2"), archives(t, root, "3"); len(a) != 0 || len(b) != 1 || len(c) != 1 {
		t.Fatalf("after write: %v %v %v", a, b, c)
	}
	r, err := Prune(root, node.LogPrune{KeepServices: []string{"1", "2", "3"}, Limits: node.LogLimits{MaxTotalBytes: size + size/2}})
	if err != nil || r.Removed != 1 || len(archives(t, root, "2")) != 0 || len(archives(t, root, "3")) != 1 {
		t.Fatalf("prune: %+v %v, left %v %v", r, err, archives(t, root, "2"), archives(t, root, "3"))
	}
}

// A disk that fills while a member is appended to the release's archive
// cuts the half member off, so members appended later still read.
func TestDiskFullMidAppend(t *testing.T) {
	root := t.TempDir()
	write(t, root, "1", "2", node.LogLimits{}, lines(0, 2))
	copyMember = func(dst io.Writer, src io.Reader) (int64, error) {
		n, _ := io.CopyN(dst, src, 10)
		return n, syscall.ENOSPC
	}
	_, err := Write(root, "1", "2", node.LogLimits{}, func(add func(node.LogLine) error) error {
		return add(node.LogLine{Text: "lost"})
	})
	copyMember = io.Copy
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatal(err)
	}
	write(t, root, "1", "2", node.LogLimits{}, lines(2, 1))
	if got := read(t, root, node.LogArchiveQuery{Service: "1", Release: "2"}); !slices.Equal(got, []string{"line 0", "line 1", "line 2"}) {
		t.Fatalf("after a failed append = %v", got)
	}
}
