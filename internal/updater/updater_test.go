package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/update"
)

// fakeSystem plays systemd and install.sh on a temporary directory: its
// Install installs the release's binary like install.sh, and Healthy
// passes when the installed binary is the version asked for.
type fakeSystem struct {
	o       *Options
	calls   []string
	install error
	// unhealthy fails the health check of this version.
	unhealthy string
}

func (f *fakeSystem) call(c string) { f.calls = append(f.calls, c) }

func (f *fakeSystem) Stop(_ context.Context, unit string) error  { f.call("stop " + unit); return nil }
func (f *fakeSystem) Start(_ context.Context, unit string) error { f.call("start " + unit); return nil }
func (f *fakeSystem) DaemonReload(context.Context) error         { f.call("daemon-reload"); return nil }

// BinaryVersion reads the fake binary: "dock <version>".
func (f *fakeSystem) BinaryVersion(_ context.Context, bin string) (string, error) {
	b, err := os.ReadFile(bin)
	return strings.TrimPrefix(string(b), "dock "), err
}

func (f *fakeSystem) Install(_ context.Context, script, bin string, log io.Writer) error {
	f.call("install")
	if _, err := os.Stat(script); err != nil {
		return err
	}
	b, err := os.ReadFile(bin)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(log, "==> dock binary")
	if err := os.WriteFile(f.o.BinPath, b, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(f.o.UnitDir, "dock.service"), []byte("new unit"), 0o644); err != nil {
		return err
	}
	return f.install
}

func (f *fakeSystem) Healthy(_ context.Context, v string, _ time.Time) error {
	f.call("healthy " + v)
	got, _ := f.BinaryVersion(context.Background(), f.o.BinPath)
	if got != v || v == f.unhealthy {
		return errors.New("no answer")
	}
	return nil
}

func (f *fakeSystem) Backup(_ context.Context, path string) error {
	f.call("backup")
	return os.WriteFile(path, []byte("backup"), 0o600)
}

func (f *fakeSystem) RestoreDB(_ context.Context, path string) error {
	f.call("restore-db " + filepath.Base(path))
	return nil
}

func (f *fakeSystem) Notify(context.Context) { f.call("notify") }

// setup installs dock v1.0.0 in a temporary root and stages v1.1.0 with a
// request for it. It returns the options and the fake system.
func setup(t *testing.T) (Options, *fakeSystem) {
	t.Helper()
	root := t.TempDir()
	o := Options{
		DataDir: filepath.Join(root, "data"), StateDir: filepath.Join(root, "state"),
		BinPath: filepath.Join(root, "dock"), UnitDir: filepath.Join(root, "units"), CaddyJSON: filepath.Join(root, "caddy.json"),
		Arch: "amd64", Current: "v1.0.0", Log: io.Discard, MaxRequestAge: 10 * time.Minute,
	}
	for _, d := range []string{o.StateDir, o.UnitDir, update.ReleaseDir(o.DataDir, "v1.1.0")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write(t, o.BinPath, "dock v1.0.0")
	write(t, filepath.Join(o.UnitDir, "dock.service"), "old unit")
	write(t, filepath.Join(o.UnitDir, "docker.service"), "Docker's")
	if err := os.Mkdir(filepath.Join(o.UnitDir, "docker.service.wants"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, o.CaddyJSON, "{}")
	stageRelease(t, update.ReleaseDir(o.DataDir, "v1.1.0"), "v1.1.0", "dock v1.1.0")
	request(t, o.DataDir, "v1.1.0", time.Now())
	f := &fakeSystem{}
	o.System = f
	f.o = &o
	return o, f
}

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func request(t *testing.T, dataDir, v string, at time.Time) {
	t.Helper()
	if err := update.WriteJSON(update.Dir(dataDir), update.RequestFile, update.Request{Version: v, RequestedBy: "saile", RequestedAt: at}, nil); err != nil {
		t.Fatal(err)
	}
}

// stageRelease writes a release's binary, install tarball, SHA256SUMS and
// its signature by a key trusted for the test.
func stageRelease(t *testing.T, dir, v, binary string) {
	t.Helper()
	var tgz bytes.Buffer
	gz := gzip.NewWriter(&tgz)
	tw := tar.NewWriter(gz)
	for _, h := range []*tar.Header{{Name: "deploy/", Typeflag: tar.TypeDir, Mode: 0o755}, {Name: "deploy/install.sh", Typeflag: tar.TypeReg, Mode: 0o755, Size: 2}} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = tw.Write([]byte("#\n"))
	_ = tw.Close()
	_ = gz.Close()
	files := map[string][]byte{update.BinaryAsset("amd64"): []byte(binary), update.InstallAsset(v): tgz.Bytes()}
	var sums strings.Builder
	for name, b := range files {
		write(t, filepath.Join(dir, name), string(b))
		h := sha256.Sum256(b)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), name)
	}
	write(t, filepath.Join(dir, update.SumsAsset), sums.String())
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	update.TrustForTest(t, pub)
	write(t, filepath.Join(dir, update.SigAsset), update.Sign(key, []byte(sums.String())))
}

// resum rewrites the staged SHA256SUMS to match its files, as someone
// with the dock user's access could; its signature then no longer fits.
func resum(t *testing.T, dir, v string) {
	t.Helper()
	var sums strings.Builder
	for _, name := range []string{update.BinaryAsset("amd64"), update.InstallAsset(v)} {
		got, err := update.HashFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&sums, "%s  %s\n", got, name)
	}
	write(t, filepath.Join(dir, update.SumsAsset), sums.String())
}

func result(t *testing.T, o Options) update.Result {
	t.Helper()
	var r update.Result
	if err := update.ReadJSON(update.Dir(o.DataDir), update.ResultFile, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunUpdatesControlPlane(t *testing.T) {
	o, f := setup(t)
	res, ok, err := Run(context.Background(), o)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if res.Status != update.Succeeded || res.From != "v1.0.0" || res.Version != "v1.1.0" || res.RequestedBy != "saile" {
		t.Fatalf("result %+v", res)
	}
	want := "stop dock.service,backup,install,healthy v1.1.0,notify"
	if got := strings.Join(f.calls, ","); got != want {
		t.Errorf("calls %s, want %s", got, want)
	}
	if b, _ := os.ReadFile(o.BinPath); string(b) != "dock v1.1.0" {
		t.Errorf("installed %q", b)
	}
	if !strings.HasPrefix(filepath.Base(res.Backup), "backup-v1.0.0-") {
		t.Errorf("backup %q", res.Backup)
	}
	if r := result(t, o); r.Status != update.Succeeded || !strings.Contains(r.Log, "==> dock binary") {
		t.Errorf("result file %+v", r)
	}
	dir := update.Dir(o.DataDir)
	if update.Exists(dir, update.RequestFile) || update.Exists(dir, update.ApplyingFile) {
		t.Error("the request is still there")
	}
	// Nothing more to do: the path unit may start it again.
	if _, ok, err := Run(context.Background(), o); ok || err != nil {
		t.Errorf("second run: %v, %v", ok, err)
	}
}

func TestRunRollsBack(t *testing.T) {
	for name, fail := range map[string]func(*fakeSystem){
		"install.sh fails": func(f *fakeSystem) { f.install = errors.New("exit status 1") },
		"unhealthy":        func(f *fakeSystem) { f.unhealthy = "v1.1.0" },
	} {
		t.Run(name, func(t *testing.T) {
			o, f := setup(t)
			fail(f)
			res, _, err := Run(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != update.RolledBack || res.Error == "" {
				t.Fatalf("result %+v", res)
			}
			if f.install != nil && !strings.HasSuffix(res.Error, "==> dock binary") {
				t.Errorf("error %q; want install.sh's last line", res.Error)
			}
			if b, _ := os.ReadFile(o.BinPath); string(b) != "dock v1.0.0" {
				t.Errorf("binary after rollback %q", b)
			}
			if b, _ := os.ReadFile(filepath.Join(o.UnitDir, "dock.service")); string(b) != "old unit" {
				t.Errorf("unit after rollback %q", b)
			}
			calls := strings.Join(f.calls, ",")
			if !strings.Contains(calls, "daemon-reload,restore-db backup-v1.0.0-") || !strings.HasSuffix(calls, "start dock.service,healthy v1.0.0,notify") {
				t.Errorf("calls %s", calls)
			}
		})
	}
}

func TestRunRefusesBeforeChangingAnything(t *testing.T) {
	for name, spoil := range map[string]func(Options){
		"tampered binary": func(o Options) {
			write(t, filepath.Join(update.ReleaseDir(o.DataDir, "v1.1.0"), update.BinaryAsset("amd64")), "dock v6.6.6")
		},
		// The SHA256SUMS check alone would pass this one.
		"tampered install tarball and SHA256SUMS": func(o Options) {
			dir := update.ReleaseDir(o.DataDir, "v1.1.0")
			write(t, filepath.Join(dir, update.InstallAsset("v1.1.0")), "a root shell")
			resum(t, dir, "v1.1.0")
		},
		"unsigned": func(o Options) {
			_ = os.Remove(filepath.Join(update.ReleaseDir(o.DataDir, "v1.1.0"), update.SigAsset))
		},
		"stale request": func(o Options) { request(t, o.DataDir, "v1.1.0", time.Now().Add(-time.Hour)) },
		"downgrade":     func(o Options) { request(t, o.DataDir, "v0.9.0", time.Now()) },
		"symlinked binary": func(o Options) {
			bin := filepath.Join(update.ReleaseDir(o.DataDir, "v1.1.0"), update.BinaryAsset("amd64"))
			_ = os.Remove(bin)
			if err := os.Symlink(o.BinPath, bin); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			o, f := setup(t)
			spoil(o)
			res, _, err := Run(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != update.Failed {
				t.Fatalf("result %+v", res)
			}
			if signing := strings.Contains(name, "SHA256SUMS") || name == "unsigned"; signing && !strings.Contains(res.Error, update.SigAsset) && !strings.Contains(res.Error, "signed") {
				t.Errorf("error %q; want the signature refused", res.Error)
			}
			if got := strings.Join(f.calls, ","); got != "notify" {
				t.Errorf("calls %s; want nothing touched", got)
			}
			if b, _ := os.ReadFile(o.BinPath); string(b) != "dock v1.0.0" {
				t.Errorf("binary %q", b)
			}
		})
	}
}

func TestRunNodeSkipsBackup(t *testing.T) {
	o, f := setup(t)
	o.Node = true
	res, _, err := Run(context.Background(), o)
	if err != nil || res.Status != update.Succeeded {
		t.Fatal(res, err)
	}
	if got := strings.Join(f.calls, ","); got != "install,healthy v1.1.0" {
		t.Errorf("calls %s; a node has no database and nobody to notify", got)
	}
	if _, err := os.Stat(update.ReleaseDir(o.DataDir, "v1.1.0")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the node kept its staged release: %v", err)
	}
}
