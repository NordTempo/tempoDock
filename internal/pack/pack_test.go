package pack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// tree writes files (path → content) under a new temp dir.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// files packs dir and returns the regular file names in archive order.
func files(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, h := range entries(t, dir) {
		if h.Typeflag == tar.TypeReg {
			out = append(out, h.Name)
		}
	}
	return out
}

func entries(t *testing.T, dir string) []*tar.Header {
	t.Helper()
	var buf bytes.Buffer
	if _, err := Write(&buf, dir, Options{}); err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	var hs []*tar.Header
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return hs
		}
		if err != nil {
			t.Fatal(err)
		}
		hs = append(hs, h)
	}
}

func TestGitignore(t *testing.T) {
	dir := tree(t, map[string]string{
		".gitignore": "# deps\nnode_modules/\n*.log\n!keep.log\n/build\ndocs/*.md\n!docs/README.md\n**/secret\ntmp/\n!tmp/.gitignore\n",
		".git/HEAD":  "ref",
		"app.js":     "",
		"debug.log":  "",
		"keep.log":   "",
		// dir-only pattern: a file named node_modules stays, the dir goes.
		"node_modules/x/index.js": "",
		"lib/node_modules":        "",
		// anchored: only the root build/ is ignored.
		"build/out.js":     "",
		"src/build/gen.js": "",
		"docs/guide.md":    "",
		"docs/README.md":   "",
		"docs/deep/a.md":   "",
		"a/b/secret":       "",
		// nested .gitignore: relative to its own dir, overrides the root.
		"web/.gitignore":       "dist\n!important.log\n",
		"web/dist/bundle.js":   "",
		"web/important.log":    "",
		"web/src/dist/keep.js": "",
		// can't re-include a file inside an ignored directory.
		"tmp/.gitignore": "",
		"Dockerfile":     "FROM scratch",
	})

	got := files(t, dir)
	want := []string{
		".gitignore",
		"Dockerfile",
		"app.js",
		"docs/README.md",
		"docs/deep/a.md",
		"keep.log",
		"lib/node_modules",
		"src/build/gen.js",
		"web/.gitignore",
		"web/important.log",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestDockerignore(t *testing.T) {
	dir := tree(t, map[string]string{
		".dockerignore":       "*.md\n!README.md\nnode_modules\nbuild\n!build/keep.txt\nDockerfile\n",
		".gitignore":          "app.js\n", // not consulted when .dockerignore exists
		".git/config":         "",
		"Dockerfile":          "",
		"dock.toml":           "",
		"app.js":              "",
		"NOTES.md":            "",
		"README.md":           "",
		"sub/NOTES.md":        "", // "*.md" is anchored at the root in Docker
		"node_modules/a.js":   "",
		"build/out.js":        "",
		"build/keep.txt":      "",
		"build/deep/other.js": "",
	})
	got := files(t, dir)
	want := []string{
		".dockerignore",
		".gitignore",
		"Dockerfile",
		"README.md",
		"app.js",
		"build/keep.txt",
		"dock.toml",
		"sub/NOTES.md",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestModesSymlinksAndStats(t *testing.T) {
	dir := tree(t, map[string]string{"run.sh": "#!/bin/sh\n", "data.txt": "hello"})
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "outside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("data.txt", filepath.Join(dir, "inside")); err != nil {
		t.Fatal(err)
	}

	byName := map[string]*tar.Header{}
	for _, h := range entries(t, dir) {
		byName[h.Name] = h
	}
	if h := byName["run.sh"]; h == nil || h.Mode != 0o755 {
		t.Errorf("run.sh should keep mode 0755, got %+v", h)
	}
	for name, target := range map[string]string{"outside": "/etc/passwd", "inside": "data.txt"} {
		if h := byName[name]; h == nil || h.Typeflag != tar.TypeSymlink || h.Linkname != target || h.Size != 0 {
			t.Errorf("%s should be a symlink to %s, got %+v", name, target, h)
		}
	}
	if h := byName["data.txt"]; h.Uname != "" || h.Uid != 0 {
		t.Errorf("owner must not be recorded: %+v", h)
	}

	var a, b bytes.Buffer
	st, err := Write(&a, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if st != (Stats{Files: 2, Bytes: int64(len("#!/bin/sh\n") + len("hello"))}) {
		t.Errorf("stats = %+v", st)
	}
	if _, err := Write(&b, dir, Options{}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Error("packing the same tree twice should produce identical archives")
	}
}

func TestMatchSegs(t *testing.T) {
	tests := []struct {
		pattern, name string
		want          bool
	}{
		{"**/foo", "foo", true},
		{"**/foo", "a/b/foo", true},
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"a/**", "a", false},
		{"a/**", "a/x/y", true},
		{"*.go", "x/main.go", false},
		{"[!a]bc", "xbc", true},
		{"[!a]bc", "abc", false},
	}
	for _, tt := range tests {
		r, ok := parseRule("", "/"+tt.pattern)
		if !ok {
			t.Fatalf("parse %q failed", tt.pattern)
		}
		if got := r.match(tt.name, false); got != tt.want {
			t.Errorf("%q matches %q = %v, want %v", tt.pattern, tt.name, got, tt.want)
		}
	}
}
