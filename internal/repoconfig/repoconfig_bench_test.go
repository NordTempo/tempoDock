package repoconfig

import (
	"archive/tar"
	"bytes"
	"testing"
)

const benchFile = `# Built with the Dockerfile in docker/, served on 3000.
[build]
dockerfile = "docker/Dockerfile.prod"

[deploy]
start_command = "node server.js --cluster"
port = 3000
health_check_path = "/healthz"
health_check_timeout = "45s"
`

// BenchmarkParse decodes and validates a complete dock.toml, as the CLI
// does on every deploy.
func BenchmarkParse(b *testing.B) {
	src := []byte(benchFile)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Parse(src); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFromTar finds and parses dock.toml in an uploaded build context
// after 200 other entries, as the daemon does for each source deploy.
func BenchmarkFromTar(b *testing.B) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(name string, body []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			b.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			b.Fatal(err)
		}
	}
	body := bytes.Repeat([]byte("console.log('hello');\n"), 50)
	for i := range 200 {
		add("src/"+string(rune('a'+i%26))+"/file.js", body)
	}
	add(FileName, []byte(benchFile))
	if err := tw.Close(); err != nil {
		b.Fatal(err)
	}
	archive := buf.Bytes()
	b.ReportAllocs()
	for b.Loop() {
		if _, found, err := FromTar(bytes.NewReader(archive)); err != nil || !found {
			b.Fatalf("found %t, err %v", found, err)
		}
	}
}
