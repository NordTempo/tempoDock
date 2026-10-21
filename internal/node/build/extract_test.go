package build

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/node"
)

type entry struct {
	name, body, link string
	typ              byte
}

func tarball(t *testing.T, gz bool, entries ...entry) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	var tw *tar.Writer
	var zw *gzip.Writer
	if gz {
		zw = gzip.NewWriter(&buf)
		tw = tar.NewWriter(zw)
	} else {
		tw = tar.NewWriter(&buf)
	}
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		hdr := &tar.Header{Name: e.name, Typeflag: typ, Mode: 0o755, Size: int64(len(e.body)), Linkname: e.link}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if zw != nil {
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return &buf
}

func TestExtract(t *testing.T) {
	for _, gz := range []bool{false, true} {
		dir := t.TempDir()
		src := tarball(t, gz,
			entry{name: "./package.json", body: "{}"},
			entry{name: "src/index.js", body: "console.log(1)"},
			entry{name: "bin/run", typ: tar.TypeSymlink, link: "../src/index.js"},
		)
		if err := extract(src, dir, defaultLimits); err != nil {
			t.Fatalf("gzip=%v: %v", gz, err)
		}
		b, err := os.ReadFile(filepath.Join(dir, "bin/run"))
		if err != nil || string(b) != "console.log(1)" {
			t.Fatalf("gzip=%v: symlinked file = %q, %v", gz, b, err)
		}
		if fi, err := os.Stat(filepath.Join(dir, "src/index.js")); err != nil || fi.Mode().Perm()&0o100 == 0 {
			t.Fatalf("gzip=%v: mode not kept: %v %v", gz, fi.Mode(), err)
		}
	}
}

func TestExtractRejectsEscapes(t *testing.T) {
	cases := map[string][]entry{
		"dotdot":       {{name: "../evil", body: "x"}},
		"nested":       {{name: "a/../../evil", body: "x"}},
		"absolute":     {{name: "/tmp/evil", body: "x"}},
		"hardlink":     {{name: "evil", typ: tar.TypeLink, link: "../../etc/passwd"}},
		"abs symlink":  {{name: "package.json", typ: tar.TypeSymlink, link: "/etc/passwd"}},
		"via symlink":  {{name: "out", typ: tar.TypeSymlink, link: "../outside"}, {name: "out/evil", body: "x"}},
		"over symlink": {{name: "out", typ: tar.TypeSymlink, link: "../outside/evil"}, {name: "out", body: "x"}},
		// s/s/s/l lands at l, where ../../../outside escapes.
		"through earlier symlink": {
			{name: "s", typ: tar.TypeSymlink, link: "."},
			{name: "s/s/s/l", typ: tar.TypeSymlink, link: "../../../outside"},
		},
		"write through in-tree symlink": {
			{name: "d", typ: tar.TypeDir},
			{name: "s", typ: tar.TypeSymlink, link: "d"},
			{name: "s/f", body: "x"},
		},
		// s/s/.. is lexically s but on disk the root's parent.
		"via link chain": {
			{name: "s", typ: tar.TypeSymlink, link: "."},
			{name: "l", typ: tar.TypeSymlink, link: "s/s/../outside"},
		},
		"link through link": {
			{name: "s", typ: tar.TypeSymlink, link: "."},
			{name: "up", typ: tar.TypeSymlink, link: "s/s/.."},
			{name: "l", typ: tar.TypeSymlink, link: "up/outside"},
		},
		"escapes once later entries exist": {
			{name: "l", typ: tar.TypeSymlink, link: "s/s/../outside"},
			{name: "s", typ: tar.TypeSymlink, link: "."},
		},
		// link(2) copies the symlink itself, moving it two levels up.
		"hard link to symlink": {
			{name: "a/b/l", typ: tar.TypeSymlink, link: "../../outside"},
			{name: "h", typ: tar.TypeLink, link: "a/b/l"},
		},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, "ctx")
			if err := os.MkdirAll(filepath.Join(parent, "outside"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := extract(tarball(t, false, entries...), dir, defaultLimits); err == nil {
				t.Fatal("expected an error")
			}
			if _, err := os.Stat(filepath.Join(parent, "outside", "evil")); err == nil {
				t.Fatal("file written outside the build context")
			}
		})
	}
}

func TestExtractKeepsInTreeSymlinks(t *testing.T) {
	dir := t.TempDir()
	src := tarball(t, true,
		entry{name: "node_modules/pkg/cli.js", body: "cli"},
		entry{name: "node_modules/.bin/pkg", typ: tar.TypeSymlink, link: "../pkg/cli.js"},
		entry{name: "releases/1/app.js", body: "app"},
		entry{name: "current", typ: tar.TypeSymlink, link: "releases/1"},
		entry{name: "latest", typ: tar.TypeSymlink, link: "current/app.js"},
		entry{name: "early", typ: tar.TypeSymlink, link: "later.txt"},
		entry{name: "later.txt", body: "later"},
		entry{name: "dangling", typ: tar.TypeSymlink, link: "missing/file"},
	)
	if err := extract(src, dir, defaultLimits); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"node_modules/.bin/pkg": "cli",
		"current/app.js":        "app",
		"latest":                "app",
		"early":                 "later",
	} {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(b) != want {
			t.Errorf("%s = %q, %v; want %q", name, b, err, want)
		}
	}
}

func TestExtractLimits(t *testing.T) {
	files := []entry{{name: "a", body: "12345"}, {name: "b", body: "12345"}, {name: "c/d", body: "12"}}
	if err := extract(tarball(t, true, files...), t.TempDir(), limits{bytes: 12, entries: 3}); err != nil {
		t.Fatalf("at the limits: %v", err)
	}
	for name, lim := range map[string]limits{
		"bytes":   {bytes: 11, entries: 3},
		"entries": {bytes: 12, entries: 2},
	} {
		err := extract(tarball(t, true, files...), t.TempDir(), lim)
		if err == nil || !strings.Contains(err.Error(), "exclude dependencies") {
			t.Errorf("%s over the limit: %v", name, err)
		}
	}
}

func TestDockerfileMustResolveInsideSource(t *testing.T) {
	parent := t.TempDir()
	src := filepath.Join(parent, "src")
	if err := os.MkdirAll(filepath.Join(parent, "host"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "host", "Dockerfile"), []byte("FROM scratch"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../host", filepath.Join(src, "l")); err != nil {
		t.Fatal(err)
	}
	if _, err := dockerfileOpt(src, "l/Dockerfile", nil, "ns", io.Discard); err == nil {
		t.Fatal("expected an error for a Dockerfile outside the source")
	}
}

func TestRailpackPathVarsMustBeLocal(t *testing.T) {
	for _, k := range []string{"RAILPACK_CONFIG_FILE", "RAILPACK_SHELL_SCRIPT"} {
		req := node.BuildRequest{BuildArgs: map[string]string{k: "../../secret.key"}}
		if _, err := railpackOpt(t.Context(), t.TempDir(), t.TempDir(), req, "ns", io.Discard); err == nil {
			t.Errorf("%s outside the source was accepted", k)
		}
	}
}
