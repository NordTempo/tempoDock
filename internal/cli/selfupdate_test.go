package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/update"
	"github.com/NordTempo/tempoDock/internal/version"
)

// fakeCLIReleases serves releases (newest first; a "-rc" one is a
// prerelease) as GitHub's API does, each with this platform's dock-cli
// and its SHA256SUMS, and points `dock self-update` at them and at a
// temp binary, reached through a symlink. It returns the symlink and the
// number of binary downloads so far.
func fakeCLIReleases(t *testing.T, versions ...string) (link string, downloads *int) {
	t.Helper()
	asset := update.CLIAsset(runtime.GOOS, runtime.GOARCH)
	downloads = new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		release := func(v string) string {
			return fmt.Sprintf(`{"tag_name":%q,"prerelease":%t,"assets":[{"name":%q,"url":"%s/assets/%s/%[3]s"},{"name":"SHA256SUMS","url":"%[4]s/assets/%[5]s/SHA256SUMS"}]}`,
				v, strings.Contains(v, "-rc"), asset, base, v)
		}
		switch {
		case r.URL.Path == "/repos/"+releaseRepo+"/releases":
			var list []string
			for _, v := range versions {
				list = append(list, release(v))
			}
			_, _ = fmt.Fprint(w, "["+strings.Join(list, ",")+"]")
		case strings.HasPrefix(r.URL.Path, "/repos/"+releaseRepo+"/releases/tags/"):
			_, _ = fmt.Fprint(w, release(filepath.Base(r.URL.Path)))
		case strings.HasPrefix(r.URL.Path, "/assets/"):
			v, name := filepath.Base(filepath.Dir(r.URL.Path)), filepath.Base(r.URL.Path)
			if name == update.SumsAsset {
				// v9.9.9's sums list another binary than the one served.
				sum := sha256.Sum256([]byte("dock " + strings.Replace(v, "v9.9.9", "v0.0.0", 1)))
				_, _ = fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
				return
			}
			*downloads++
			_, _ = fmt.Fprint(w, "dock "+v)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	bin := filepath.Join(dir, "dock-cli")
	if err := os.WriteFile(bin, []byte("dock old"), 0o755); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(dir, "dock")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	saved, savedVersion := selfUpdateHost, version.Version
	t.Cleanup(func() { selfUpdateHost, version.Version = saved, savedVersion })
	selfUpdateHost.source = func() update.Source { return &update.GitHub{Repo: releaseRepo, API: srv.URL} }
	selfUpdateHost.executable = func() (string, error) { return link, nil }
	selfUpdateHost.cliOnly = true
	version.Version = "v0.3.0"
	return link, downloads
}

// selfUpdate runs `dock self-update --json` with args.
func selfUpdate(t *testing.T, args ...string) (int, selfUpdateView, string) {
	t.Helper()
	code, stdout, stderr := exec(t, append([]string{"self-update", "--json"}, args...)...)
	var v selfUpdateView
	if code == 0 {
		if err := json.Unmarshal([]byte(stdout), &v); err != nil {
			t.Fatalf("%q: %v", stdout, err)
		}
	}
	return code, v, stderr
}

// binary is what the symlinked binary holds.
func binary(t *testing.T, link string) string {
	t.Helper()
	b, err := os.ReadFile(link)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// By default the CLI becomes the server's version, replacing the file the
// symlink points at; once there, it says so without downloading.
func TestSelfUpdateToServerVersion(t *testing.T) {
	setup(t)
	link, downloads := fakeCLIReleases(t, "v1.3.0", "v1.2.0")
	newFakeAPI(t, map[string]http.HandlerFunc{
		"GET /api/v1/version": jsonReply(apitypes.Version{Version: "v1.2.0", APIVersion: apitypes.APIVersion}),
	})

	code, v, stderr := selfUpdate(t)
	if code != 0 || v.To != "v1.2.0" || v.Target != "server" || !v.Updated || v.From != "v0.3.0" {
		t.Fatalf("%d %+v %s", code, v, stderr)
	}
	if got := binary(t, link); got != "dock v1.2.0" {
		t.Errorf("binary holds %q", got)
	}
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symlink was replaced: %v %v", st, err)
	}
	if st, _ := os.Stat(link); st.Mode().Perm() != 0o755 {
		t.Errorf("mode %v", st.Mode())
	}

	version.Version = "v1.2.0"
	code, v, stderr = selfUpdate(t)
	if code != 0 || v.Updated || v.To != "v1.2.0" || *downloads != 1 {
		t.Fatalf("already current: %d %+v after %d downloads %s", code, v, *downloads, stderr)
	}
}

// With no server, the newest release that isn't a prerelease, and why;
// --dry-run downloads nothing.
func TestSelfUpdateFallsBackToLatest(t *testing.T) {
	setup(t)
	link, downloads := fakeCLIReleases(t, "v1.4.0-rc.1", "v1.3.0", "v1.2.0")
	code, v, stderr := selfUpdate(t, "--dry-run")
	if code != 0 || v.To != "v1.3.0" || v.Target != "latest" || v.ServerError == "" || v.Updated || *downloads != 0 {
		t.Fatalf("%d %+v after %d downloads %s", code, v, *downloads, stderr)
	}
	if got := binary(t, link); got != "dock old" {
		t.Errorf("a dry run changed the binary: %q", got)
	}
}

// A binary that doesn't match SHA256SUMS is refused and leaves nothing.
func TestSelfUpdateRefusesMismatch(t *testing.T) {
	setup(t)
	link, _ := fakeCLIReleases(t)
	code, _, stderr := selfUpdate(t, "--to", "v9.9.9")
	if code == 0 || !strings.Contains(stderr, "SHA256SUMS") {
		t.Fatalf("%d %s", code, stderr)
	}
	if got := binary(t, link); got != "dock old" {
		t.Errorf("binary holds %q", got)
	}
	if entries, _ := os.ReadDir(filepath.Dir(link)); len(entries) != 2 {
		t.Errorf("left behind: %v", entries)
	}
}

// A directory this user can't write to gets the root command, pinned.
func TestSelfUpdateUnwritableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	setup(t)
	link, downloads := fakeCLIReleases(t)
	dir := filepath.Dir(link)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	code, _, stderr := selfUpdate(t, "--to", "v1.2.0")
	exe, _ := filepath.EvalSymlinks(link)
	want := "sudo " + exe + " self-update --to v1.2.0"
	if code == 0 || !strings.Contains(stderr, want) || *downloads != 0 {
		t.Fatalf("%d after %d downloads: %s; want the hint %q", code, *downloads, stderr, want)
	}
}
