// Package cpbackup writes and restores the control plane's own backup
// (ADR 0021): one tar archive holding a consistent SQLite snapshot, the
// keys, the build logs and the daemon config, and optionally the image
// registry's storage. `dock admin backup` writes it to a file, the backup
// scheduler streams it into restic, and `dock admin restore` (run by
// `install.sh --restore`) brings a control plane up from it on another
// server with the same identity.
//
// The archive holds every secret the control plane has: secret.key opens
// every sealed value in the database. Files are 0600 and directories 0700,
// inside the archive and wherever it is written or unpacked.
package cpbackup

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/version"
)

// Format is the archive layout's version, in the manifest. A restore
// refuses formats it doesn't know.
const Format = 1

// Names inside the archive. The manifest is always the first entry and
// the config, when there is one, the second, so both can be read without
// going through the rest.
const (
	ManifestName = "manifest.json"
	ConfigName   = "config.toml"
	dataPrefix   = "data/"
	regPrefix    = "registry/"
)

// Files in the data dir.
const (
	// DBFile is the SQLite database (config.DBPath).
	DBFile = "dock.db"
	// SecretKeyFile opens every sealed value in the database
	// (config.KeyPath). An archive without it is useless.
	SecretKeyFile = "secret.key"
	// MovedFile marks a control plane that moved to another server (`dock
	// admin backup --move`): `dock serve` refuses to start while it exists.
	MovedFile = "MOVED"
)

// Manifest describes an archive. It is its first entry.
type Manifest struct {
	Format int `json:"format"`
	// Version is the dock version that wrote the archive.
	Version string `json:"version"`
	// Schema is the database's schema version (PRAGMA user_version); a
	// dock that doesn't know it yet can't restore it.
	Schema    int    `json:"schema"`
	InstallID string `json:"install_id"`
	// Hostname is the server the archive was taken on.
	Hostname  string    `json:"hostname"`
	CreatedAt time.Time `json:"created_at"`
	// LinkKey is the fingerprint of the control plane's node link key, the
	// one nodes pin; empty if it never had one.
	LinkKey string `json:"link_key,omitempty"`
	// Registry says the archive holds the registry's storage.
	Registry bool `json:"registry"`
}

// AnalyticsFile is where an archive keeps the analytics store's snapshot
// (ADR 0037: its database and session replays), relative to the data
// dir, and AnalyticsInfoFile what it holds (AnalyticsInfo). A control
// plane restored from the archive fills its empty store from them, then
// deletes them.
const (
	AnalyticsFile     = "analytics/restore.tar"
	AnalyticsInfoFile = "analytics/restore.json"
)

// AnalyticsInfo is what an archived analytics snapshot holds.
type AnalyticsInfo struct {
	Bytes  int64 `json:"bytes"`
	Events int64 `json:"events"`
}

// Source is what Write archives.
type Source struct {
	// DataDir is the control plane's data dir; the snapshot is written to a
	// private temporary directory inside it.
	DataDir string
	// ConfigPath is the daemon config file; "" or a missing file leaves it
	// out.
	ConfigPath string
	// RegistryDir is the registry's storage; "" leaves it out.
	RegistryDir string
	// Snapshot writes a consistent copy of the database to dst
	// (store.Snapshot or store.SnapshotFile).
	Snapshot func(ctx context.Context, dst string) (store.SnapshotInfo, error)
	// Analytics opens the analytics store's snapshot: size bytes holding
	// events events. nil, or an error, leaves it out (analytics off, or
	// its server offline): the rest of the backup doesn't wait for it.
	Analytics func(ctx context.Context) (r io.ReadCloser, size, events int64, err error)
	// Warn hears what was left out and why; nil is silence.
	Warn func(msg string, err error)
}

// skipData are data dir entries never archived: the live database (the
// snapshot replaces it), transient uploads and build contexts, Railpack's
// tool cache, the moved marker, staged updates (ADR 0028), and a node's
// own identity.
var skipData = map[string]bool{
	DBFile: true, DBFile + "-wal": true, DBFile + "-shm": true, DBFile + "-journal": true,
	"uploads": true, "railpack": true, "builds/work": true, MovedFile: true, "update": true,
	remote.NodeKeyFile: true, remote.NodeKeyFile + ".next": true, remote.StateFile: true,
}

// Write writes an archive of src to w: the manifest, the config, the data
// dir with the database snapshot in place of the live file, and the
// registry's storage if asked. It returns the manifest. Files that vanish
// while it runs (build logs retention removed) are left out; files that
// grow are archived as they were when reached.
func Write(ctx context.Context, w io.Writer, src Source) (Manifest, error) {
	tmp, err := os.MkdirTemp(src.DataDir, ".backup-") // 0700
	if err != nil {
		return Manifest{}, fmt.Errorf("create a temporary directory in %s: %w", src.DataDir, err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	snap := filepath.Join(tmp, DBFile)
	info, err := src.Snapshot(ctx, snap)
	if err != nil {
		return Manifest{}, err
	}
	host, _ := os.Hostname()
	m := Manifest{
		Format: Format, Version: version.Version, Schema: info.Schema, InstallID: info.InstallID,
		Hostname: host, CreatedAt: time.Now().UTC(), Registry: src.RegistryDir != "",
	}
	if fp, err := remote.KeyFingerprint(filepath.Join(src.DataDir, remote.ControlPlaneKeyFile)); err == nil {
		m.LinkKey = fp
	} else if !errors.Is(err, fs.ErrNotExist) {
		return m, err
	}
	if _, err := os.Stat(filepath.Join(src.DataDir, SecretKeyFile)); err != nil {
		return m, fmt.Errorf("the secrets key: %w", err)
	}

	tw := tar.NewWriter(w)
	manifest, _ := json.MarshalIndent(m, "", "  ")
	if err := writeEntry(tw, ManifestName, manifest); err != nil {
		return m, err
	}
	if src.ConfigPath != "" {
		if err := addFile(tw, ConfigName, src.ConfigPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return m, fmt.Errorf("the config: %w", err)
		}
	}
	if err := addFile(tw, dataPrefix+DBFile, snap); err != nil {
		return m, err
	}
	if err := addTree(ctx, tw, src.DataDir, dataPrefix, func(rel string) bool {
		return skipData[rel] || rel == AnalyticsFile || rel == AnalyticsInfoFile || strings.HasPrefix(path.Base(rel), ".")
	}); err != nil {
		return m, err
	}
	if src.Analytics != nil {
		// Last: the store holds its writes while it streams.
		r, size, events, err := src.Analytics(ctx)
		if err != nil {
			if src.Warn != nil {
				src.Warn("the analytics store isn't in this backup", err)
			}
		} else {
			err := addStream(tw, dataPrefix+AnalyticsFile, r, size)
			_ = r.Close()
			if err != nil {
				return m, fmt.Errorf("the analytics store: %w", err)
			}
			info, _ := json.Marshal(AnalyticsInfo{Bytes: size, Events: events})
			if err := writeEntry(tw, dataPrefix+AnalyticsInfoFile, info); err != nil {
				return m, err
			}
		}
	}
	if src.RegistryDir != "" {
		if err := addTree(ctx, tw, src.RegistryDir, regPrefix, func(string) bool { return false }); err != nil {
			return m, fmt.Errorf("the registry storage: %w", err)
		}
	}
	return m, tw.Close()
}

// writeEntry adds a file with contents b.
func writeEntry(tw *tar.Writer, name string, b []byte) error {
	if err := tw.WriteHeader(fileHeader(name, int64(len(b)), time.Now())); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

func fileHeader(name string, size int64, mod time.Time) *tar.Header {
	return &tar.Header{Typeflag: tar.TypeReg, Name: name, Size: size, Mode: 0o600, ModTime: mod.UTC(), Format: tar.FormatPAX}
}

// addStream adds size bytes of r as name.
func addStream(tw *tar.Writer, name string, r io.Reader, size int64) error {
	if err := tw.WriteHeader(fileHeader(name, size, time.Now())); err != nil {
		return err
	}
	_, err := io.CopyN(tw, r, size)
	return err
}

// addFile adds the file at src as name. Its size is taken once it is open,
// and only that much is copied.
func addFile(tw *tar.Writer, name, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(fileHeader(name, fi.Size(), fi.ModTime())); err != nil {
		return err
	}
	if _, err := io.CopyN(tw, f, fi.Size()); err != nil {
		return fmt.Errorf("%s: %w", src, err)
	}
	return nil
}

// addTree adds the regular files and directories under root as prefix +
// their slash-separated path, except those skip names (and what is under
// them). Other kinds of files are left out.
func addTree(ctx context.Context, tw *tar.Writer, root, prefix string, skip func(rel string) bool) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		if skip(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case d.IsDir():
			return tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: prefix + rel + "/", Mode: 0o700, ModTime: time.Now().UTC(), Format: tar.FormatPAX})
		case d.Type().IsRegular():
			if err := addFile(tw, prefix+rel, p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		return nil
	})
}

// ErrNotArchive is a file that isn't a control plane backup.
var ErrNotArchive = errors.New("not a dock control plane backup (no manifest.json first); make one with `dock admin backup`")

// openArchive reads a tar or gzipped tar and its manifest.
func openArchive(r io.Reader) (*tar.Reader, Manifest, error) {
	br := bufio.NewReader(r)
	var in io.Reader = br
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, Manifest{}, err
		}
		in = gz
	}
	tr := tar.NewReader(in)
	hdr, err := tr.Next()
	if err != nil || hdr.Name != ManifestName {
		return nil, Manifest{}, ErrNotArchive
	}
	var m Manifest
	if err := json.NewDecoder(io.LimitReader(tr, 64<<10)).Decode(&m); err != nil {
		return nil, Manifest{}, fmt.Errorf("%w: %w", ErrNotArchive, err)
	}
	if m.Format != Format {
		return nil, m, fmt.Errorf("this backup has format %d, which dock %s can't read; restore it with the dock version that made it (%s) or newer", m.Format, version.Version, m.Version)
	}
	if m.Schema > store.LatestSchema() {
		return nil, m, fmt.Errorf("this backup comes from dock %s, whose database is newer than this dock (%s) knows; install dock %s or newer and restore with it", m.Version, version.Version, m.Version)
	}
	return tr, m, nil
}

// ReadConfig returns an archive's manifest and the daemon config in it
// (nil if it has none), reading only the first entries.
func ReadConfig(r io.Reader) (Manifest, []byte, error) {
	tr, m, err := openArchive(r)
	if err != nil {
		return m, nil, err
	}
	hdr, err := tr.Next()
	if errors.Is(err, io.EOF) || (err == nil && hdr.Name != ConfigName) {
		return m, nil, nil
	}
	if err != nil {
		return m, nil, err
	}
	b, err := io.ReadAll(io.LimitReader(tr, 1<<20))
	return m, b, err
}

// Target is where Restore unpacks an archive.
type Target struct {
	// DataDir gets the data files. It must not hold a database yet.
	DataDir string
	// RegistryDir gets the registry's storage; "" skips it.
	RegistryDir string
}

// restoringDB is the database's name while a restore is unpacking: it
// becomes DBFile last, so a restore cut short leaves no database behind
// and can simply run again.
const restoringDB = DBFile + ".restoring"

// Restore unpacks an archive into t and returns its manifest. Only
// regular files and directories under the archive's known prefixes are
// accepted, never paths leaving their target; files are written 0600 and
// directories 0700. The database is checked (quick_check) before it takes
// its name.
func Restore(ctx context.Context, r io.Reader, t Target) (Manifest, error) {
	if _, err := os.Stat(filepath.Join(t.DataDir, DBFile)); err == nil {
		return Manifest{}, fmt.Errorf("%s already holds a control plane database; restore onto a fresh server, or move %s away first", t.DataDir, t.DataDir)
	}
	tr, m, err := openArchive(r)
	if err != nil {
		return m, err
	}
	if err := os.MkdirAll(t.DataDir, 0o700); err != nil {
		return m, err
	}
	var haveDB, haveKey bool
	for {
		if err := ctx.Err(); err != nil {
			return m, err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return m, fmt.Errorf("read the backup: %w", err)
		}
		var root, rel string
		switch name := strings.TrimSuffix(hdr.Name, "/"); {
		case name == ConfigName:
			continue // the config is installed by install.sh (ReadConfig)
		case strings.HasPrefix(name, dataPrefix):
			root, rel = t.DataDir, strings.TrimPrefix(name, dataPrefix)
		case strings.HasPrefix(name, regPrefix):
			if t.RegistryDir == "" {
				continue
			}
			root, rel = t.RegistryDir, strings.TrimPrefix(name, regPrefix)
		default:
			return m, fmt.Errorf("unexpected %q in the backup", hdr.Name)
		}
		if !filepath.IsLocal(rel) {
			return m, fmt.Errorf("refusing %q in the backup: the path leaves its directory", hdr.Name)
		}
		switch {
		case root == t.DataDir && rel == DBFile:
			rel, haveDB = restoringDB, true
		case root == t.DataDir && rel == SecretKeyFile:
			haveKey = true
		}
		dst := filepath.Join(root, filepath.FromSlash(rel))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o700); err != nil {
				return m, err
			}
		case tar.TypeReg:
			if err := writeFile(dst, tr); err != nil {
				return m, err
			}
		default:
			return m, fmt.Errorf("refusing %q in the backup: only files and directories are restored", hdr.Name)
		}
	}
	if !haveDB || !haveKey {
		return m, errors.New("the backup has no database or no secrets key; it is incomplete, take a new one")
	}
	restoring := filepath.Join(t.DataDir, restoringDB)
	info, err := store.Inspect(ctx, restoring)
	if err != nil {
		return m, err
	}
	if info.InstallID != m.InstallID {
		return m, errors.New("the database in the backup doesn't match its manifest; take a new backup")
	}
	return m, os.Rename(restoring, filepath.Join(t.DataDir, DBFile))
}

// writeFile writes r to dst, 0600, creating its directory (0700).
func writeFile(dst string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// RestoreDB puts the database in an archive back over dataDir's, which
// must not be in use: the updater's rollback (ADR 0028), after the new
// version may have migrated it. The database is unpacked beside the live
// one, checked, then renamed over it, with the old one's -wal and -shm
// removed first so SQLite doesn't replay them onto the restored file.
// The other files in the archive are left alone.
func RestoreDB(ctx context.Context, r io.Reader, dataDir string) error {
	tr, m, err := openArchive(r)
	if err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("the backup has no database")
		}
		if err != nil {
			return fmt.Errorf("read the backup: %w", err)
		}
		if hdr.Name != dataPrefix+DBFile || hdr.Typeflag != tar.TypeReg {
			continue
		}
		restoring := filepath.Join(dataDir, restoringDB)
		if err := writeFile(restoring, tr); err != nil {
			return err
		}
		info, err := store.Inspect(ctx, restoring)
		if err == nil && info.InstallID != m.InstallID {
			err = errors.New("the database in the backup doesn't match its manifest")
		}
		if err != nil {
			_ = os.Remove(restoring)
			return err
		}
		db := filepath.Join(dataDir, DBFile)
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if err := os.Remove(db + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		return os.Rename(restoring, db)
	}
}
