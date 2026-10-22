package cpbackup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
)

// controlPlane is a data dir as `dock serve` leaves it: a live database
// with a row in it, the keys, build logs, and the transient files a
// backup leaves out.
type controlPlane struct {
	dir, config, registry string
	st                    *store.Store
	linkKey               string
}

func newControlPlane(t *testing.T) *controlPlane {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	cp := &controlPlane{dir: filepath.Join(root, "data"), config: filepath.Join(root, "config.toml"), registry: filepath.Join(root, "registry")}
	if err := os.Mkdir(cp.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, filepath.Join(cp.dir, DBFile))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cp.st = st
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO projects (name) VALUES ('shop')"); err != nil {
		t.Fatal(err)
	}
	id, err := remote.LoadOrCreateIdentity(filepath.Join(cp.dir, remote.ControlPlaneKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	cp.linkKey = id.Fingerprint()
	for name, body := range map[string]string{
		SecretKeyFile:              "0123456789abcdef0123456789abcdef",
		"registry.key":             "signing key",
		"builds/7.log":             "#1 building\n",
		"builds/7-container.jsonl": "{}\n",
		"builds/work/build-1/x":    "transient",
		"uploads/abc.tar":          "transient",
		"railpack/cache":           "cache",
		MovedFile:                  "{}",
		".link.key.tmp-1":          "half written",
	} {
		write(t, filepath.Join(cp.dir, name), body)
	}
	write(t, cp.config, "listen = \"100.64.0.10:7070\"\n")
	write(t, filepath.Join(cp.registry, "docker/registry/v2/blobs/sha256/ab/data"), "layer")
	return cp
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (cp *controlPlane) source(registry bool) Source {
	s := Source{
		DataDir: cp.dir, ConfigPath: cp.config,
		Snapshot: func(ctx context.Context, dst string) (store.SnapshotInfo, error) {
			return store.Snapshot(ctx, cp.st.DB, dst)
		},
	}
	if registry {
		s.RegistryDir = cp.registry
	}
	return s
}

func (cp *controlPlane) archive(t *testing.T, registry bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := Write(context.Background(), &buf, cp.source(registry)); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A backup restores on another server with the same identity: database,
// secrets key, link key (so nodes keep trusting it) and build logs, and
// nothing transient. Everything restored is owner-only.
func TestRestoreRoundTrip(t *testing.T) {
	cp := newControlPlane(t)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	m, err := Write(context.Background(), gz, cp.source(true))
	if err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if m.LinkKey != cp.linkKey || m.Schema != store.LatestSchema() || m.InstallID == "" || !m.Registry {
		t.Fatalf("manifest = %+v", m)
	}

	// The archive itself: the manifest first, then the config, owner-only modes.
	names := map[string]bool{}
	tr := tar.NewReader(bytes.NewReader(cp.archive(t, true)))
	for i := 0; ; i++ {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		names[hdr.Name] = true
		if (i == 0 && hdr.Name != ManifestName) || (i == 1 && hdr.Name != ConfigName) {
			t.Fatalf("entry %d is %s", i, hdr.Name)
		}
		if want := int64(0o600); hdr.Typeflag == tar.TypeDir {
			if hdr.Mode != 0o700 {
				t.Errorf("%s: mode %o", hdr.Name, hdr.Mode)
			}
		} else if hdr.Mode != want {
			t.Errorf("%s: mode %o", hdr.Name, hdr.Mode)
		}
	}
	for _, left := range []string{"data/uploads/abc.tar", "data/builds/work/build-1/x", "data/railpack/cache", "data/" + MovedFile, "data/.link.key.tmp-1", "data/dock.db-wal"} {
		if names[left] {
			t.Errorf("%s is in the archive", left)
		}
	}

	target := t.TempDir()
	to := Target{DataDir: filepath.Join(target, "data"), RegistryDir: filepath.Join(target, "registry")}
	got, err := Restore(context.Background(), &buf, to)
	if err != nil {
		t.Fatal(err)
	}
	if got != m {
		t.Errorf("restored manifest %+v, want %+v", got, m)
	}
	for _, name := range []string{SecretKeyFile, remote.ControlPlaneKeyFile, "registry.key", "builds/7.log", "builds/7-container.jsonl"} {
		same(t, filepath.Join(cp.dir, name), filepath.Join(to.DataDir, name))
	}
	same(t, filepath.Join(cp.registry, "docker/registry/v2/blobs/sha256/ab/data"), filepath.Join(to.RegistryDir, "docker/registry/v2/blobs/sha256/ab/data"))
	for _, gone := range []string{"uploads", "railpack", "builds/work", MovedFile, restoringDB} {
		if _, err := os.Stat(filepath.Join(to.DataDir, gone)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s was restored: %v", gone, err)
		}
	}
	if fp, err := remote.KeyFingerprint(filepath.Join(to.DataDir, remote.ControlPlaneKeyFile)); err != nil || fp != cp.linkKey {
		t.Errorf("restored link key %s, %v; want %s", fp, err, cp.linkKey)
	}
	st, err := store.Open(context.Background(), filepath.Join(to.DataDir, DBFile))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	var project string
	if err := st.DB.QueryRow("SELECT name FROM projects").Scan(&project); err != nil || project != "shop" {
		t.Errorf("restored database: project %q, %v", project, err)
	}
	if err := filepath.WalkDir(target, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == target || strings.HasPrefix(filepath.Base(p), DBFile+"-") {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if want := fs.FileMode(0o600); d.IsDir() && fi.Mode().Perm() != 0o700 || !d.IsDir() && fi.Mode().Perm() != want {
			t.Errorf("%s: mode %v", p, fi.Mode().Perm())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// The config comes out of the archive on its own, for install.sh.
	_, cfg, err := ReadConfig(bytes.NewReader(cp.archive(t, false)))
	if err != nil || string(cfg) != "listen = \"100.64.0.10:7070\"\n" {
		t.Errorf("config %q, %v", cfg, err)
	}
	// A second restore onto the same data dir is refused.
	if _, err := Restore(context.Background(), bytes.NewReader(cp.archive(t, false)), to); err == nil || !strings.Contains(err.Error(), "already holds") {
		t.Errorf("restore over a database: %v", err)
	}
}

func same(t *testing.T, a, b string) {
	t.Helper()
	x, err := os.ReadFile(a)
	if err != nil {
		t.Fatal(err)
	}
	y, err := os.ReadFile(b)
	if err != nil || !bytes.Equal(x, y) {
		t.Errorf("%s: restored %q, %v; want %q", b, y, err, x)
	}
}

// Restore takes only files and directories under the archive's own
// prefixes, never a path leaving its target or a link, and refuses
// archives from a newer dock or without a database.
func TestRestoreRefuses(t *testing.T) {
	cp := newControlPlane(t)
	good := cp.archive(t, false)
	// rewrite returns good with fn applied to every header after the
	// manifest, and extra entries appended.
	rewrite := func(fn func(*tar.Header) bool, extra ...*tar.Header) []byte {
		var out bytes.Buffer
		tw := tar.NewWriter(&out)
		tr := tar.NewReader(bytes.NewReader(good))
		for {
			hdr, err := tr.Next()
			if err != nil {
				break
			}
			if hdr.Name != ManifestName && !fn(hdr) {
				continue
			}
			_ = tw.WriteHeader(hdr)
			_, _ = io.Copy(tw, tr)
		}
		for _, h := range extra {
			_ = tw.WriteHeader(h)
		}
		_ = tw.Close()
		return out.Bytes()
	}
	keep := func(*tar.Header) bool { return true }
	for name, archive := range map[string][]byte{
		"escape":  rewrite(keep, &tar.Header{Typeflag: tar.TypeReg, Name: "data/../../evil", Mode: 0o600}),
		"symlink": rewrite(keep, &tar.Header{Typeflag: tar.TypeSymlink, Name: "data/link", Linkname: "/etc/passwd"}),
		"unknown": rewrite(keep, &tar.Header{Typeflag: tar.TypeReg, Name: "etc/passwd", Mode: 0o600}),
		"no key":  rewrite(func(h *tar.Header) bool { return h.Name != "data/"+SecretKeyFile }),
		"not one": []byte("just some bytes"),
	} {
		dir := filepath.Join(t.TempDir(), "data")
		if _, err := Restore(context.Background(), bytes.NewReader(archive), Target{DataDir: dir}); err == nil {
			t.Errorf("%s: restored", name)
		}
		if _, err := os.Stat(filepath.Join(dir, DBFile)); err == nil {
			t.Errorf("%s: left a database behind", name)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(t.TempDir()), "evil")); err == nil {
		t.Error("a path escaped the data dir")
	}

	// A newer dock's database.
	if _, err := cp.st.DB.Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	_, err := Restore(context.Background(), bytes.NewReader(cp.archive(t, false)), Target{DataDir: filepath.Join(t.TempDir(), "data")})
	if err == nil || !strings.Contains(err.Error(), "newer") {
		t.Errorf("newer schema: %v", err)
	}
}

// A data dir is locked by one holder at a time, and a moved control plane
// says so.
func TestLockAndMoved(t *testing.T) {
	dir := t.TempDir()
	unlock, err := Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock: %v", err)
	}
	unlock()
	unlock, err = Lock(dir)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	unlock()

	if err := CheckMoved(dir); err != nil {
		t.Fatal(err)
	}
	if err := MarkMoved(dir, Moved{Archive: "/root/dock.tar.gz"}); err != nil {
		t.Fatal(err)
	}
	if err := CheckMoved(dir); !errors.Is(err, ErrMoved) || !strings.Contains(err.Error(), filepath.Join(dir, MovedFile)) {
		t.Fatalf("moved: %v", err)
	}
}
