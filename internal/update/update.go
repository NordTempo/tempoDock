// Package update finds, downloads and verifies Dock releases, and holds
// the files the daemon and the root updater hand each other (ADR 0028).
//
// The daemon (as the dock user) stages a release under
// <data_dir>/update/releases/<version>/ and writes RequestFile; systemd's
// dock-update.path sees it and starts dock-update.service, which runs `dock
// admin update` as root (internal/updater). The updater renames the
// request to ApplyingFile, applies the release, writes ResultFile and tells
// the daemon. A node's updater does the same with a release its control
// plane sent over the node link.
//
//	<data_dir>/update/
//	  releases/<version>/   dock-linux-<arch>, dock-install-<version>.tar.gz, SHA256SUMS
//	  request.json          Request: the trigger, written by the daemon
//	  applying.json         the request, while the updater runs
//	  result.json           Result, written by the updater
//	  last-result.json      the result, once the control plane audited it
//	  connected.json        Connected, written by `dock node` once it reaches its control plane
//
// Nothing here imports the daemon's packages, so the node side and the
// updater use it too.
package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"golang.org/x/mod/semver"
)

// Files in Dir.
const (
	ReleasesDir    = "releases"
	RequestFile    = "request.json"
	ApplyingFile   = "applying.json"
	ResultFile     = "result.json"
	LastResultFile = "last-result.json"
	ConnectedFile  = "connected.json"
)

// PathUnit is the systemd path unit that starts the updater when a
// request appears; install.sh installs it. Without it nobody acts on a
// request, so the daemon checks for it before writing one.
const PathUnit = "/etc/systemd/system/dock-update.path"

// NotifyPath is where the updater tells the control plane its result is
// written, on the daemon's loopback internal listener.
const NotifyPath = "/internal/update"

// Assets of a release (scripts/release/build.sh).
const SumsAsset = "SHA256SUMS"

// BinaryAsset is the server binary for linux/arch.
func BinaryAsset(arch string) string { return "dock-linux-" + arch }

// CaddyAsset is Dock's Caddy for linux/arch (cmd/dock-caddy, ADR 0106),
// which install.sh installs as /usr/local/bin/caddy. Releases from before
// it have none, so an update stages and sends it only when the release's
// SHA256SUMS lists it.
func CaddyAsset(arch string) string { return "dock-caddy-linux-" + arch }

// CLIAsset is the CLI-only build (dock-cli) for goos/goarch, as
// install-cli.sh picks it; "" for a platform releases have no build for.
func CLIAsset(goos, goarch string) string {
	switch goos + "/" + goarch {
	case "linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64":
		return "dock-cli-" + goos + "-" + goarch
	}
	return ""
}

// InstallAsset is the install tarball of version: deploy/ with install.sh
// and the systemd units.
func InstallAsset(version string) string { return "dock-install-" + version + ".tar.gz" }

// Dir is the update directory in a data dir.
func Dir(dataDir string) string { return filepath.Join(dataDir, "update") }

// ReleaseDir is where version's assets are staged.
func ReleaseDir(dataDir, version string) string {
	return filepath.Join(Dir(dataDir), ReleasesDir, version)
}

// versionRe is a release tag: what build.sh stamps into the binary.
// Compiled on first use: the CLI imports this package for `dock
// self-update`, and nothing compiles at its start-up.
var versionRe = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)
})

// IsRelease reports whether v is a release version like v1.2.3 or
// v1.2.3-rc.1, not a development build ("dev") or anything else. Versions
// end up in file names and commands, so nothing else is accepted.
func IsRelease(v string) bool { return versionRe().MatchString(v) && semver.IsValid(v) }

// Newer reports whether release version a is newer than b.
func Newer(a, b string) bool { return semver.Compare(a, b) > 0 }

// Request asks the updater to apply a staged release. The updater acts
// on Version only; the rest is carried into the Result.
type Request struct {
	Version string `json:"version"`
	// RequestedBy names who asked, for the audit log.
	RequestedBy string    `json:"requested_by"`
	RequestedAt time.Time `json:"requested_at"`
	// Servers: once the control plane runs Version, it updates the nodes
	// behind it too.
	Servers bool `json:"servers,omitempty"`
}

// Status is how an update ended.
type Status string

const (
	// Succeeded: the new version runs and passed its health check.
	Succeeded Status = "succeeded"
	// RolledBack: it failed and the previous version runs again.
	RolledBack Status = "rolled_back"
	// Failed: it failed and so did the rollback, or it never started (a
	// staged file didn't verify). Error says what to do.
	Failed Status = "failed"
)

// Result is what the updater writes when it is done.
type Result struct {
	Request
	// From is the version that ran before.
	From       string    `json:"from"`
	Status     Status    `json:"status"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	// Backup is the control plane backup taken before the swap.
	Backup string `json:"backup,omitempty"`
	// Log is the end of the updater's output (install.sh's included).
	Log string `json:"log,omitempty"`
}

// Connected is what `dock node` writes once it has said hello to its
// control plane: the node updater's health check.
type Connected struct {
	Version string    `json:"version"`
	At      time.Time `json:"at"`
}

// maxStateFile caps the JSON files in Dir.
const maxStateFile = 256 << 10

// ReadJSON reads dir/name into v, without following a symlink there.
// A missing file is fs.ErrNotExist.
func ReadJSON(dir, name string, v any) error {
	f, err := OpenNoFollow(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() > maxStateFile {
		return fmt.Errorf("%s is over %d bytes", name, maxStateFile)
	}
	b := make([]byte, st.Size())
	if _, err := f.ReadAt(b, 0); err != nil && st.Size() > 0 {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// WriteJSON writes v to dir/name through a new file and a rename, so a
// reader never sees half of it and an existing symlink at name is
// replaced rather than followed. chown, when set, runs on the new file
// before the rename (the root updater gives its files to dock).
func WriteJSON(dir, name string, v any, chown func(*os.File) error) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	_, err = f.Write(append(b, '\n'))
	if err == nil && chown != nil {
		err = chown(f)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}

// Exists reports whether dir/name exists (a symlink counts).
func Exists(dir, name string) bool {
	_, err := os.Lstat(filepath.Join(dir, name))
	return err == nil
}

// RemoveIfExists removes dir/name; a missing file is fine.
func RemoveIfExists(dir, name string) error {
	err := os.Remove(filepath.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
