package build

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
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
		if err := extract(src, dir); err != nil {
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
			if err := extract(tarball(t, false, entries...), dir); err == nil {
				t.Fatal("expected an error")
			}
			if _, err := os.Stat(filepath.Join(parent, "outside", "evil")); err == nil {
				t.Fatal("file written outside the build context")
			}
		})
	}
}
