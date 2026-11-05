package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRelease writes release v into dir (a DirSource) with a binary for
// each arch, an install tarball and their SHA256SUMS.
func fakeRelease(t *testing.T, dir, v string, archs ...string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{InstallAsset(v): []byte("tarball " + v)}
	for _, a := range archs {
		files[BinaryAsset(a)] = []byte("binary " + v + " " + a)
		files[AnalyticsAsset(a)] = []byte("analytics " + v + " " + a)
	}
	var sums strings.Builder
	for name, b := range files {
		h := sha256.Sum256(b)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), name)
	}
	files[SumsAsset] = []byte(sums.String())
	rel := filepath.Join(dir, v)
	if err := os.MkdirAll(rel, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(rel, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return files
}

// countingSource counts the assets it opens.
type countingSource struct {
	Source
	opened map[string]int
}

func (c *countingSource) Open(ctx context.Context, v, asset string) (io.ReadCloser, error) {
	c.opened[asset]++
	return c.Source.Open(ctx, v, asset)
}

func TestStageVerifiesAndReuses(t *testing.T) {
	ctx := context.Background()
	src, data := t.TempDir(), t.TempDir()
	files := fakeRelease(t, src, "v1.2.0", "amd64", "arm64")
	fakeRelease(t, src, "v1.1.0", "amd64")
	if _, err := Stage(ctx, DirSource{Path: src}, data, "v1.1.0", "amd64"); err != nil {
		t.Fatal(err)
	}

	cs := &countingSource{Source: DirSource{Path: src}, opened: map[string]int{}}
	if _, err := Stage(ctx, cs, data, "v1.2.0", "amd64"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(ReleaseDir(data, "v1.2.0"), BinaryAsset("amd64")))
	if err != nil || !bytes.Equal(got, files[BinaryAsset("amd64")]) {
		t.Fatalf("staged binary %q, %v", got, err)
	}
	if _, err := os.Stat(ReleaseDir(data, "v1.1.0")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the older staged release is still there: %v", err)
	}
	// Another arch only downloads its binary.
	if _, err := Stage(ctx, cs, data, "v1.2.0", "amd64", "arm64"); err != nil {
		t.Fatal(err)
	}
	if cs.opened[BinaryAsset("amd64")] != 1 || cs.opened[InstallAsset("v1.2.0")] != 1 || cs.opened[BinaryAsset("arm64")] != 1 {
		t.Errorf("opened %v; want each asset once", cs.opened)
	}
}

// The analytics store's binary comes from the release only when it is
// needed: checked, executable, downloaded once, and older versions' gone
// once the store runs on a newer one.
func TestStageAnalytics(t *testing.T) {
	ctx := context.Background()
	src, data := t.TempDir(), t.TempDir()
	files := fakeRelease(t, src, "v1.2.0", "arm64")
	fakeRelease(t, src, "v1.1.0", "arm64")
	if _, _, err := StageAnalytics(ctx, DirSource{Path: src}, data, "v1.1.0", "arm64"); err != nil {
		t.Fatal(err)
	}
	cs := &countingSource{Source: DirSource{Path: src}, opened: map[string]int{}}
	for range 2 {
		path, sum, err := StageAnalytics(ctx, cs, data, "v1.2.0", "arm64")
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		h := sha256.Sum256(files[AnalyticsAsset("arm64")])
		if err != nil || !bytes.Equal(got, files[AnalyticsAsset("arm64")]) || sum != hex.EncodeToString(h[:]) {
			t.Fatalf("staged %q (%s), %v", got, sum, err)
		}
		if st, err := os.Stat(path); err != nil || st.Mode().Perm()&0o111 == 0 {
			t.Errorf("the binary isn't executable: %v", err)
		}
	}
	if cs.opened[AnalyticsAsset("arm64")] != 1 || cs.opened[BinaryAsset("arm64")] != 0 {
		t.Errorf("opened %v; want the store's binary once and nothing else", cs.opened)
	}
	if err := PruneAnalytics(data, "v1.2.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(AnalyticsDir(data), "v1.1.0")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the older version's binary is still there: %v", err)
	}
	if _, _, err := StageAnalytics(ctx, cs, data, "dev", "arm64"); err == nil || !strings.Contains(err.Error(), "store_binary") {
		t.Errorf("a development build: %v", err)
	}
}

func TestStageRefusesMismatch(t *testing.T) {
	src, data := t.TempDir(), t.TempDir()
	fakeRelease(t, src, "v1.2.0", "amd64")
	bin := filepath.Join(src, "v1.2.0", BinaryAsset("amd64"))
	if err := os.WriteFile(bin, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Stage(context.Background(), DirSource{Path: src}, data, "v1.2.0", "amd64")
	if !errors.Is(err, ErrMismatch) {
		t.Fatalf("err = %v, want ErrMismatch", err)
	}
	if _, err := os.Stat(filepath.Join(ReleaseDir(data, "v1.2.0"), BinaryAsset("amd64"))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the mismatched binary was kept: %v", err)
	}
}

func TestGitHubSource(t *testing.T) {
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/repos/o/r/releases":
			_, _ = fmt.Fprint(w, `[
				{"tag_name":"v1.1.0","body":"old","published_at":"2026-10-01T00:00:00Z","assets":[]},
				{"tag_name":"v1.3.0","draft":true,"assets":[]},
				{"tag_name":"nightly","assets":[]},
				{"tag_name":"v1.2.0","body":"## What's changed","html_url":"https://example/v1.2.0","published_at":"2026-10-20T00:00:00Z",
				 "assets":[{"name":"SHA256SUMS","url":"`+"http://"+r.Host+`/assets/7"}]}
			]`)
		case "/assets/7":
			if r.Header.Get("Accept") != "application/octet-stream" {
				http.Error(w, "json", http.StatusBadRequest)
				return
			}
			_, _ = fmt.Fprint(w, "sums")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	token := filepath.Join(t.TempDir(), "token")
	g := &GitHub{Repo: "o/r", API: srv.URL, TokenFile: token}

	// No token: a 404 says where one goes.
	g.Repo = "o/private"
	_, err := g.Releases(context.Background())
	se, ok := errors.AsType[*SourceError](err)
	if !ok || !strings.Contains(se.Plain, token) || strings.Contains(se.Plain, "http") || !strings.Contains(se.Detail, "404") {
		t.Fatalf("err = %#v, want a plain hint naming %s and the 404 apart", err, token)
	}
	g.Repo = "o/r"
	if err := os.WriteFile(token, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rs, err := g.Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 || rs[0].Version != "v1.2.0" || rs[1].Version != "v1.1.0" || rs[0].Notes != "## What's changed" {
		t.Fatalf("releases %+v; want v1.2.0 then v1.1.0, no draft or other tags", rs)
	}
	rc, err := g.Open(context.Background(), "v1.2.0", SumsAsset)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(b) != "sums" {
		t.Errorf("asset %q", b)
	}
	if last := auth[len(auth)-1]; last != "Bearer secret" {
		t.Errorf("Authorization %q, want the token", last)
	}
}

func TestReceiveChecksEachFile(t *testing.T) {
	data := t.TempDir()
	body := map[string][]byte{SumsAsset: []byte("s"), InstallAsset("v1.2.0"): []byte("tar"), BinaryAsset("arm64"): []byte("bin")}
	var files []File
	var all bytes.Buffer
	for _, name := range Assets("v1.2.0", "arm64") {
		h := sha256.Sum256(body[name])
		files = append(files, File{Name: name, Size: int64(len(body[name])), SHA256: hex.EncodeToString(h[:])})
		all.Write(body[name])
	}
	if err := Receive(data, "v1.2.0", "arm64", files, bytes.NewReader(all.Bytes())); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(ReleaseDir(data, "v1.2.0"), BinaryAsset("arm64"))); string(b) != "bin" {
		t.Errorf("received binary %q", b)
	}

	files[2].SHA256 = files[1].SHA256
	err := Receive(data, "v1.2.0", "arm64", files, bytes.NewReader(all.Bytes()))
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Errorf("a binary that doesn't match: err = %v", err)
	}
	if err := Receive(data, "v1.2.0", "amd64", files, bytes.NewReader(all.Bytes())); err == nil {
		t.Error("files for another arch were accepted")
	}
}

func TestParseSums(t *testing.T) {
	s, err := ParseSums([]byte("ab" + strings.Repeat("0", 62) + "  dock-linux-amd64\n" + strings.Repeat("1", 64) + " *SHA-file\n"))
	if err != nil || s["dock-linux-amd64"] == "" || s["SHA-file"] == "" {
		t.Fatalf("%v, %v", s, err)
	}
	if _, err := ParseSums([]byte("nothex  file\n")); err == nil {
		t.Error("a malformed line was accepted")
	}
}
