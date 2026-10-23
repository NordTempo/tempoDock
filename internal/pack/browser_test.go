package pack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// nodeRunner packs a directory with the web UI's packer
// (web/src/features/deploy/pack.ts) and writes the archive to stdout. It
// hands the packer what a browser would: files and directories, no
// symlinks.
const nodeRunner = `
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
const { pack } = await import(process.argv[2]);
const list = (dir) => async () =>
  readdirSync(dir, { withFileTypes: true }).flatMap((d) => {
    const p = join(dir, d.name);
    if (d.isDirectory()) return [{ kind: "dir", name: d.name, list: list(p) }];
    if (!d.isFile()) return [];
    const file = async () =>
      new File([readFileSync(p)], d.name, { lastModified: statSync(p).mtimeMs });
    return [{ kind: "file", name: d.name, file }];
  });
const { stream } = pack(list(process.argv[3]), { dockerfile: process.argv[4] });
for await (const chunk of stream) process.stdout.write(chunk);
`

// The browser packer must put the same entries in the archive as
// pack.Write: same ignore rules, order, sizes and contents, and a valid
// tar.gz. Needs Node 23.6 or later (TypeScript type stripping); skipped
// without it.
func TestBrowserPackerMatches(t *testing.T) {
	node := nodeWithTypeStripping(t)
	_, self, _, _ := runtime.Caller(0)
	packTS := filepath.Join(filepath.Dir(self), "..", "..", "web", "src", "features", "deploy", "pack.ts")
	runner := filepath.Join(t.TempDir(), "runner.mjs")
	if err := os.WriteFile(runner, []byte(nodeRunner), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		files      map[string]string
		dockerfile string
	}{
		"gitignore": {files: map[string]string{
			".gitignore":              "# deps\nnode_modules/\n*.log\n!keep.log\n/build\ndocs/*.md\n!docs/README.md\n**/secret\ntmp/\n!tmp/.gitignore\n[!a]x.txt\nesc\\ aped \n",
			".git/HEAD":               "ref",
			"app.js":                  "console.log(1)",
			"debug.log":               "",
			"keep.log":                "kept",
			"node_modules/x/index.js": "",
			"lib/node_modules":        "",
			"build/out.js":            "",
			"src/build/gen.js":        "",
			"docs/guide.md":           "",
			"docs/README.md":          "",
			"docs/deep/a.md":          "",
			"a/b/secret":              "",
			"bx.txt":                  "",
			"ax.txt":                  "",
			"esc aped":                "",
			"web/.gitignore":          "dist\n!important.log\nsub/**\n",
			"web/dist/bundle.js":      "",
			"web/important.log":       "",
			"web/src/dist/keep.js":    "",
			"web/sub/deep/x":          "",
			"tmp/.gitignore":          "",
			"vendor/lib/.git":         "gitdir: ../../.git/modules/lib", // a submodule
			"vendor/lib/lib.go":       "package lib",
			"Dockerfile":              "FROM scratch",
		}},
		"dockerignore": {files: map[string]string{
			".dockerignore":       "\uFEFF# comment\n*.md\n!README.md\nnode_modules\nbuild\n!build/keep.txt\nDockerfile\n**/*.tmp\n/abs/path\n?.txt\n[a-c].cfg\nlogs/**\n!logs/**/keep.log\n",
			".gitignore":          "app.js\n", // not consulted when .dockerignore exists
			".git/config":         "",
			"Dockerfile":          "FROM scratch",
			"dock.toml":           "",
			"app.js":              "",
			"NOTES.md":            "",
			"README.md":           "",
			"sub/NOTES.md":        "",
			"node_modules/a.js":   "",
			"build/out.js":        "",
			"build/keep.txt":      "",
			"build/deep/other.js": "",
			"a/b/c.tmp":           "",
			"abs/path":            "",
			"x.txt":               "",
			"xy.txt":              "",
			"b.cfg":               "",
			"d.cfg":               "",
			"logs/a/keep.log":     "",
			"logs/a/drop.log":     "",
		}},
		"dockerfile from dock.toml, names, modes": {
			dockerfile: "Dockerfile", // the service's; dock.toml overrides it
			files: map[string]string{
				".dockerignore":         "docker\n",
				"dock.toml":             "[deploy]\nport = 3000\n\n[build] # how\ndockerfile = \"docker/App.Dockerfile\"\n",
				"docker/App.Dockerfile": "FROM scratch",
				"docker/other":          "",
				"run.sh":                "#!/bin/sh\necho hi\n",
				"café/naïve.txt":        "unicode",
				strings.Repeat("long-directory-name/", 6) + strings.Repeat("f", 90) + ".txt": "long",
				strings.Repeat("p", 120) + "/x":                                              "single long segment",
				"big.bin":                                                                    strings.Repeat("0123456789abcdef", 10_000),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := tree(t, tc.files)
			if err := os.Mkdir(filepath.Join(dir, "empty"), 0o755); err != nil {
				t.Fatal(err)
			}
			if sh := filepath.Join(dir, "run.sh"); exists(sh) {
				if err := os.Chmod(sh, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			var want bytes.Buffer
			if _, err := Write(&want, dir, Options{Dockerfile: dockerfileFor(t, dir, tc.dockerfile)}); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(node, "--no-warnings", runner, "file://"+packTS, dir, tc.dockerfile)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			got, err := cmd.Output()
			if err != nil {
				t.Fatalf("node: %v\n%s", err, stderr.String())
			}
			w, g := listing(t, want.Bytes()), listing(t, got)
			if !slices.Equal(w, g) {
				t.Fatalf("browser archive differs\npack.Write:\n  %s\nbrowser:\n  %s", strings.Join(w, "\n  "), strings.Join(g, "\n  "))
			}
		})
	}
}

// dockerfileFor resolves the Dockerfile as `dock deploy` does: dock.toml's
// [build] dockerfile, else the service's.
func dockerfileFor(t *testing.T, dir, service string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "dock.toml"))
	if errors.Is(err, os.ErrNotExist) {
		return service
	}
	if err != nil {
		t.Fatal(err)
	}
	for l := range strings.Lines(string(b)) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "dockerfile = "); ok {
			s, _ := strconv.Unquote(v)
			return s
		}
	}
	return service
}

// listing is one line per archive entry: name, type, size, whether it is
// executable, and a hash of its content.
func listing(t *testing.T, archive []byte) []string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	var out []string
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.New()
		if _, err := io.Copy(sum, tr); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s %c size=%d exec=%t %x", h.Name, h.Typeflag, h.Size, h.Mode&0o100 != 0, sum.Sum(nil)[:6]))
	}
}

// nodeWithTypeStripping finds a Node that runs TypeScript directly.
func nodeWithTypeStripping(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found")
	}
	out, err := exec.Command(node, "--version").Output()
	if err != nil {
		t.Skip("node --version failed")
	}
	var major, minor int
	if _, err := fmt.Sscanf(string(out), "v%d.%d", &major, &minor); err != nil || major < 23 || major == 23 && minor < 6 {
		t.Skipf("node %s can't run TypeScript; needs 23.6 or later", strings.TrimSpace(string(out)))
	}
	return node
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
