package pack

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// BenchmarkWrite packs a mid-size app the way `dock deploy` does: 400
// source files (about 1.6 MB) in 40 directories, plus an ignored
// node_modules of 2,000 files and a .git directory that must be skipped
// without being read.
func BenchmarkWrite(b *testing.B) {
	dir := benchApp(b)
	b.ReportAllocs()
	for b.Loop() {
		st, err := Write(io.Discard, dir, Options{})
		if err != nil {
			b.Fatal(err)
		}
		if st.Files != 403 {
			b.Fatalf("packed %d files, want 403", st.Files)
		}
	}
}

// BenchmarkWritePipe packs the same app into an io.Pipe read by another
// goroutine, as `dock deploy` streams it to the server: every write the
// packer makes is a hand-off between goroutines.
func BenchmarkWritePipe(b *testing.B) {
	dir := benchApp(b)
	b.ReportAllocs()
	for b.Loop() {
		pr, pw := io.Pipe()
		go func() {
			_, err := Write(pw, dir, Options{})
			_ = pw.CloseWithError(err)
		}()
		if _, err := io.Copy(io.Discard, pr); err != nil {
			b.Fatal(err)
		}
	}
}

// benchApp writes the benchmark app into a temp dir and sets the
// benchmark's bytes per op to its source size.
func benchApp(b *testing.B) string {
	b.Helper()
	dir := b.TempDir()
	write := func(name, body string) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	write(".gitignore", "node_modules/\ndist/\n*.log\n.env\n")
	write("Dockerfile", "FROM node:24-alpine\nCOPY . .\nRUN npm ci\nCMD [\"node\", \"server.js\"]\n")
	write("dock.toml", "[deploy]\nport = 3000\nhealth_check_path = \"/healthz\"\n")
	var size int64
	for i := range 400 {
		var sb strings.Builder
		for j := range 40 {
			fmt.Fprintf(&sb, "export function handler%d_%d(req, res) { return res.json({ id: %d, ok: true }); }\n", i, j, i*j)
		}
		write(fmt.Sprintf("src/mod%02d/file%03d.ts", i%40, i), sb.String())
		size += int64(sb.Len())
	}
	for i := range 2000 {
		write(fmt.Sprintf("node_modules/pkg%03d/index%d.js", i/10, i%10), "module.exports = {};\n")
	}
	for i := range 200 {
		write(fmt.Sprintf(".git/objects/%02x/%038d", i%256, i), "blob")
	}
	b.SetBytes(size)
	return dir
}
