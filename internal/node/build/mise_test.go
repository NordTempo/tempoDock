package build

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/railwayapp/railpack/core/mise"
)

// The mise version Railpack runs has digests for every platform builds
// run on, so a Railpack upgrade that moves to another mise fails here
// instead of building with an unchecked binary.
func TestMiseDigestsCoverRailpack(t *testing.T) {
	sum := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		a, ok := miseDigests[miseVersion()][platform]
		if !ok {
			t.Errorf("miseDigests has no entry for mise %s on %s: add it from that release's SHASUMS256.txt", miseVersion(), platform)
			continue
		}
		if a.platform == "" || !sum.MatchString(a.archive) || !sum.MatchString(a.binary) {
			t.Errorf("miseDigests entry for mise %s on %s is incomplete: %+v", miseVersion(), platform, a)
		}
	}
}

// miseRelease builds a release archive holding binary and the asset
// entry that matches it.
func miseRelease(t *testing.T, binary string) ([]byte, miseAsset) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{"mise/README.md": "docs", miseInArchive: binary} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	archive, bin := sha256.Sum256(buf.Bytes()), sha256.Sum256([]byte(binary))
	return buf.Bytes(), miseAsset{platform: "test", archive: hex.EncodeToString(archive[:]), binary: hex.EncodeToString(bin[:])}
}

// noNetwork fails every request made through http.DefaultTransport (what
// Railpack's own mise download uses) until the test ends, and counts them.
func noNetwork(t *testing.T) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	old := http.DefaultTransport
	http.DefaultTransport = roundTripper(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, http.ErrUseLastResponse
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	return &calls
}

// roundTripper makes a function an http.RoundTripper.
type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A release that matches its digests is placed where Railpack looks, once;
// one that doesn't is refused and leaves nothing behind.
func TestPlaceMise(t *testing.T) {
	version := miseVersion()
	binary := "#!/bin/sh\necho " + version + "\n"
	archive, asset := miseRelease(t, binary)
	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v"+version+"/mise-v"+version+"-test.tar.gz" {
			http.NotFound(w, r)
			return
		}
		downloads.Add(1)
		_, _ = w.Write(archive)
	}))
	defer srv.Close()
	// place runs placeMise in dir and returns its log output.
	place := func(dir string, a miseAsset) (string, error) {
		var logs strings.Builder
		err := placeMise(t.Context(), dir, srv.URL, version, a, &logs)
		return logs.String(), err
	}
	// entries are the names in dir.
	entries := func(dir string) []string {
		t.Helper()
		es, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, e := range es {
			names = append(names, e.Name())
		}
		return names
	}

	t.Run("placed once and found by Railpack", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "mise")
		if logs, err := place(dir, asset); err != nil || !strings.Contains(logs, "downloading mise "+version) {
			t.Fatalf("placeMise: %v, logs %q", err, logs)
		}
		bin := filepath.Join(dir, "mise-"+version)
		got, err := os.ReadFile(bin)
		if err != nil || string(got) != binary {
			t.Fatalf("placed binary = %q, %v", got, err)
		}
		if fi, _ := os.Stat(bin); fi.Mode().Perm() != 0o755 {
			t.Errorf("placed binary has mode %v, want 0755", fi.Mode().Perm())
		}

		if logs, err := place(dir, asset); err != nil || logs != "" || downloads.Load() != 1 {
			t.Errorf("second placeMise: %v, logs %q, %d downloads; want no work", err, logs, downloads.Load())
		}

		// Railpack takes the binary as it is: no download of its own.
		calls := noNetwork(t)
		if _, err := mise.New(dir); err != nil {
			t.Errorf("Railpack did not accept the placed binary: %v", err)
		}
		if calls.Load() != 0 {
			t.Errorf("Railpack made %d requests with the binary in place", calls.Load())
		}
	})

	t.Run("a binary changed since it was checked is replaced", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := place(dir, asset); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(dir, "mise-"+version)
		if err := os.WriteFile(bin, []byte("#!/bin/sh\necho other\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		before := downloads.Load()
		if _, err := place(dir, asset); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(bin); string(got) != binary || downloads.Load() != before+1 {
			t.Errorf("binary = %q after %d downloads, want the release's after one", got, downloads.Load()-before)
		}
	})

	t.Run("an unmarked binary that matches is kept", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "mise-"+version), []byte(binary), 0o755); err != nil {
			t.Fatal(err)
		}
		before := downloads.Load()
		for range 2 {
			if _, err := place(dir, asset); err != nil {
				t.Fatal(err)
			}
		}
		if downloads.Load() != before {
			t.Errorf("%d downloads for a binary that already matched", downloads.Load()-before)
		}
	})

	for name, wrong := range map[string]miseAsset{
		"wrong archive digest": {platform: asset.platform, archive: strings.Repeat("0", 64), binary: asset.binary},
		"wrong binary digest":  {platform: asset.platform, archive: asset.archive, binary: strings.Repeat("0", 64)},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			_, err := place(dir, wrong)
			if err == nil || !strings.Contains(err.Error(), "SHA-256") || !strings.Contains(err.Error(), "retry the build") {
				t.Errorf("placeMise = %v, want a digest error that says what to do", err)
			}
			if left := entries(dir); len(left) != 0 {
				t.Errorf("a refused download left %v", left)
			}
		})
	}
}

// Railpack asks mise for an app's tools with the app's own config kept
// inert (safe mode, no parent directories, none of the host's config).
// Dock relies on that for sources it doesn't trust, so a Railpack upgrade
// that drops it fails here. A stand-in mise records what it is run with.
func TestRailpackRunsMiseSafe(t *testing.T) {
	dir, app := t.TempDir(), filepath.Join(t.TempDir(), "app")
	if err := os.Mkdir(app, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nenv > " + filepath.Join(dir, "env") + "\necho \"$@\" > " + filepath.Join(dir, "args") + "\necho '{}'\n"
	if err := os.WriteFile(filepath.Join(dir, "mise-"+miseVersion()), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCK_TEST_HOST_ENV", "leaked")
	m, err := mise.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetCurrentList(app, nil); err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(filepath.Join(dir, "env"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(env)), "\n")
	for _, want := range []string{
		"MISE_SAFE=1", "MISE_PARANOID=1", "MISE_CEILING_PATHS=" + filepath.Dir(app), "HOME=" + dir,
		"MISE_SYSTEM_CONFIG_DIR=" + filepath.Join(dir, "system"),
	} {
		if !slices.Contains(lines, want) {
			t.Errorf("Railpack runs mise without %s; its environment:\n%s", want, env)
		}
	}
	if strings.Contains(string(env), "DOCK_TEST_HOST_ENV") {
		t.Error("Railpack passes the host's environment to mise")
	}
	if args, _ := os.ReadFile(filepath.Join(dir, "args")); strings.TrimSpace(string(args)) != "--cd "+app+" list --current --json" {
		t.Errorf("Railpack runs mise %s; the guard in mise_integration_test.go covers `list --current` only", args)
	}
}
