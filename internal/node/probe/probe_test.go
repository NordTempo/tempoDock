package probe

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An unchanged probe is left alone; a new one replaces the file through a
// rename, so a container that bind-mounted the old one keeps its inode.
func TestWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe", "dock-probe")
	if err := write(path, text("v1")); err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if first.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v", first.Mode())
	}
	if err := write(path, text("v1")); err != nil {
		t.Fatal(err)
	}
	if same, _ := os.Stat(path); !os.SameFile(first, same) {
		t.Error("an unchanged probe was rewritten")
	}
	old, err := os.Open(path) // what a running container sees
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = old.Close() }()
	if err := write(path, text("v2")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "v2" {
		t.Errorf("probe = %q, want v2", got)
	}
	got := make([]byte, 2)
	if _, err := old.Read(got); err != nil || string(got) != "v1" {
		t.Errorf("the old inode reads %q, %v; want v1", got, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("left temp files: %v", entries)
	}
}

// text is a write source that reads s.
func text(s string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(s)), nil }
}

// sameContent compares in 32 KB chunks: the file must match src exactly,
// not only as a prefix either way, including at a chunk boundary.
func TestSameContent(t *testing.T) {
	chunk := strings.Repeat("x", 32<<10)
	for _, c := range []struct {
		old, src string
		want     bool
	}{
		{"v1", "v1", true},
		{chunk + chunk, chunk + chunk, true},
		{"v1", "v2", false},
		{chunk, chunk + "y", false},
		{chunk + "y", chunk, false},
		{"", "v1", false},
	} {
		path := filepath.Join(t.TempDir(), "f")
		if err := os.WriteFile(path, []byte(c.old), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, err := sameContent(path, text(c.src)); err != nil || got != c.want {
			t.Errorf("sameContent(%d bytes, %d bytes) = %v, %v; want %v", len(c.old), len(c.src), got, err, c.want)
		}
	}
	if got, err := sameContent(filepath.Join(t.TempDir(), "missing"), text("v1")); err != nil || got {
		t.Errorf("missing file: %v, %v", got, err)
	}
}

// Install unpacks the embedded probe and dock-sync into the data dir,
// where they run; a second install leaves them alone. Needs `make probe`.
func TestInstall(t *testing.T) {
	dir := t.TempDir()
	for _, install := range []func(string) (string, error){Install, InstallSync} {
		path, err := install(dir)
		if errors.Is(err, ErrNotBuilt) || errors.Is(err, ErrSyncNotBuilt) {
			t.Skip("not embedded: run `make probe`")
		}
		if err != nil {
			t.Fatal(err)
		}
		first, _ := os.Stat(path)
		if _, err := install(dir); err != nil {
			t.Fatal(err)
		}
		if again, _ := os.Stat(path); !os.SameFile(first, again) {
			t.Errorf("%s was rewritten unchanged", path)
		}
	}
}
