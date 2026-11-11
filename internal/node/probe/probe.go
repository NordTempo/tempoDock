// Package probe embeds dock-probe (cmd/dock-probe), the health probe
// Docker runs inside app containers for their ongoing health check (ADR
// 0043), and dock-sync (cmd/dock-sync), which copies volumes between
// servers inside job containers (ADR 0063), and installs them on the
// server. `make build` builds both into bin/, gzipped, before building
// dock; a plain `go build` embeds none.
package probe

import (
	"bytes"
	"compress/gzip"
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// bin holds bin/dock-probe.gz and bin/dock-sync.gz for the target's
// GOOS/GOARCH, or only .gitkeep. They are gzipped (`make probe`): about
// 0.9 MB each instead of 2 MB.
//
//go:embed all:bin
var bin embed.FS

// ErrNotBuilt is Install's error for a dock built without the probe.
var ErrNotBuilt = errors.New("this dock binary has no dock-probe embedded, so containers get no ongoing health checks: build it with `make build`")

// ErrSyncNotBuilt is InstallSync's error for a dock built without
// dock-sync.
var ErrSyncNotBuilt = errors.New("this dock binary has no dock-sync embedded, so environments can't move without downtime: build it with `make build`")

// Install unpacks the embedded probe to <dataDir>/probe/dock-probe and
// checks that it runs on this server, returning its absolute path for
// docker.Runtime.Probe. The file is only replaced when its content
// changed, through a rename, so running containers keep the inode they
// bind-mounted. A noexec data dir or a probe of another architecture is
// an error, as is a binary without one (ErrNotBuilt). Callers log it and
// run without ongoing checks; start-up never fails on it.
func Install(dataDir string) (string, error) {
	return install(dataDir, "dock-probe", ErrNotBuilt)
}

// InstallSync unpacks the embedded dock-sync next to the probe, like
// Install, returning its path for docker.Runtime.Sync. Without it, moves
// fall back to nothing: core refuses them with ErrSyncNotBuilt's words.
func InstallSync(dataDir string) (string, error) {
	return install(dataDir, "dock-sync", ErrSyncNotBuilt)
}

// install unpacks bin/<name>.gz to <dataDir>/probe/<name> and checks it
// runs.
func install(dataDir, name string, notBuilt error) (string, error) {
	gz := "bin/" + name + ".gz"
	if _, err := fs.Stat(bin, gz); errors.Is(err, fs.ErrNotExist) {
		return "", notBuilt
	}
	dir, err := filepath.Abs(filepath.Join(dataDir, "probe"))
	if err != nil {
		return "", fmt.Errorf("install %s: %w", name, err)
	}
	path := filepath.Join(dir, name)
	if err := write(path, func() (io.ReadCloser, error) { return gunzip(gz) }); err != nil {
		return "", fmt.Errorf("install %s in %s: %w", name, dir, err)
	}
	if err := check(path, name); err != nil {
		return "", err
	}
	return path, nil
}

// gunzip opens an embedded .gz file, unpacked. Reading it to the end
// checks its CRC.
func gunzip(name string) (io.ReadCloser, error) {
	f, err := bin.Open(name)
	if err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("read the embedded %s: %w", name, err)
	}
	return gzFile{zr, f}, nil
}

// gzFile is an unpacking reader that closes the file under it too.
type gzFile struct {
	*gzip.Reader
	f fs.File
}

func (g gzFile) Close() error { return errors.Join(g.Reader.Close(), g.f.Close()) }

// write puts what src reads at path unless the file there already holds
// it, through a temp file and a rename. src is opened once to compare and
// again to copy, so neither side is ever held in memory whole.
func write(path string, src func() (io.ReadCloser, error)) error {
	if same, err := sameContent(path, src); err != nil {
		return err
	} else if same {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // a no-op once renamed
	r, err := src()
	if err != nil {
		_ = f.Close()
		return err
	}
	_, err = io.Copy(f, r)
	if err = errors.Join(err, r.Close()); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(0o755); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// sameContent reports whether the file at path holds exactly what src
// reads. A missing file is not the same; an unreadable src is an error.
func sameContent(path string, src func() (io.ReadCloser, error)) (bool, error) {
	old, err := os.Open(path)
	if err != nil {
		return false, nil // missing or unreadable: write replaces it
	}
	defer func() { _ = old.Close() }()
	r, err := src()
	if err != nil {
		return false, err
	}
	defer func() { _ = r.Close() }()
	want, got := make([]byte, 32<<10), make([]byte, 32<<10)
	for {
		n, err := io.ReadFull(r, want)
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return false, err
		}
		if _, err := io.ReadFull(old, got[:n]); err != nil || !bytes.Equal(want[:n], got[:n]) {
			return false, nil
		}
		if n < len(want) { // src ended: the same only if old ends here too
			_, err := old.Read(got[:1])
			return errors.Is(err, io.EOF), nil
		}
	}
}

// check runs `<name> version` on the host, which fails on a noexec
// mount or a binary of another architecture.
func check(path, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return fmt.Errorf("run %s: %w (is the data dir mounted noexec, or the probe built for another architecture?)", path, err)
	}
	if !strings.HasPrefix(string(out), name+" ") {
		return fmt.Errorf("run %s: unexpected output %q", path, strings.TrimSpace(string(out)))
	}
	return nil
}
