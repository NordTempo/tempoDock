package probe

import (
	"os"
	"path/filepath"
	"testing"
)

// An unchanged probe is left alone; a new one replaces the file through a
// rename, so a container that bind-mounted the old one keeps its inode.
func TestWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe", "dock-probe")
	if err := write(path, []byte("v1")); err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if first.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v", first.Mode())
	}
	if err := write(path, []byte("v1")); err != nil {
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
	if err := write(path, []byte("v2")); err != nil {
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
