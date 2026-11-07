// Package probe embeds dock-probe (cmd/dock-probe), the health probe
// Docker runs inside app containers for their ongoing health check (ADR
// 0043), and dock-sync (cmd/dock-sync), which copies volumes between
// servers inside job containers (ADR 0062), and installs them on the
// server. `make build` builds both into bin/ before building dock; a plain
// `go build` embeds none.
package probe

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// bin holds bin/dock-probe and bin/dock-sync for the target's
// GOOS/GOARCH, or only .gitkeep.
//
//go:embed all:bin
var bin embed.FS

// ErrNotBuilt is Install's error for a dock built without the probe.
var ErrNotBuilt = errors.New("this dock binary has no dock-probe embedded, so containers get no ongoing health checks: build it with `make build`")

// ErrSyncNotBuilt is InstallSync's error for a dock built without
// dock-sync.
var ErrSyncNotBuilt = errors.New("this dock binary has no dock-sync embedded, so environments can't move without downtime: build it with `make build`")

// Install writes the embedded probe to <dataDir>/probe/dock-probe and
// checks that it runs on this server, returning its absolute path for
// docker.Runtime.Probe. The file is only replaced when its content
// changed, through a rename, so running containers keep the inode they
// bind-mounted. A noexec data dir or a probe of another architecture is
// an error, as is a binary without one (ErrNotBuilt). Callers log it and
// run without ongoing checks; start-up never fails on it.
func Install(dataDir string) (string, error) {
	return install(dataDir, "dock-probe", ErrNotBuilt)
}

// InstallSync writes the embedded dock-sync next to the probe, like
// Install, returning its path for docker.Runtime.Sync. Without it, moves
// fall back to nothing: core refuses them with ErrSyncNotBuilt's words.
func InstallSync(dataDir string) (string, error) {
	return install(dataDir, "dock-sync", ErrSyncNotBuilt)
}

// install writes bin/<name> to <dataDir>/probe/<name> and checks it runs.
func install(dataDir, name string, notBuilt error) (string, error) {
	data, err := bin.ReadFile("bin/" + name)
	if errors.Is(err, fs.ErrNotExist) {
		return "", notBuilt
	}
	if err != nil {
		return "", fmt.Errorf("read the embedded %s: %w", name, err)
	}
	dir, err := filepath.Abs(filepath.Join(dataDir, "probe"))
	if err != nil {
		return "", fmt.Errorf("install %s: %w", name, err)
	}
	path := filepath.Join(dir, name)
	if err := write(path, data); err != nil {
		return "", fmt.Errorf("install %s in %s: %w", name, dir, err)
	}
	if err := check(path, name); err != nil {
		return "", err
	}
	return path, nil
}

// write puts data at path unless it is already there, through a temp file
// and a rename.
func write(path string, data []byte) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
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
	if _, err := f.Write(data); err != nil {
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
