package repoconfig

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
	"time"
)

func TestParseValid(t *testing.T) {
	cfg, err := Parse([]byte(`
[build]
dockerfile = "docker/Dockerfile.prod"

[deploy]
start_command = "node server.js"
port = 3000
health_check_path = "/healthz"
health_check_timeout = "45s"
`))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Build: Build{Dockerfile: "docker/Dockerfile.prod"},
		Deploy: Deploy{
			StartCommand:       "node server.js",
			Port:               3000,
			HealthCheckPath:    "/healthz",
			HealthCheckTimeout: 45 * time.Second,
		},
	}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestParseEmptyFileSetsNothing(t *testing.T) {
	cfg, err := Parse(nil)
	if err != nil || cfg != (Config{}) {
		t.Fatalf("got %+v, %v", cfg, err)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name, in string
		want     []string
	}{
		{
			name: "unknown keys with line numbers",
			in:   "[build]\ndockerfile = \"Dockerfile\"\ndockerfil = \"x\"\n\n[deploy]\nhealthcheck = \"/\"\n",
			want: []string{`platform.toml:3:1: unknown key "build.dockerfil"`, `platform.toml:6:1: unknown key "deploy.healthcheck"`},
		},
		{
			name: "unknown table",
			in:   "[env.production]\nport = 1\n",
			want: []string{`platform.toml:1:2: unknown key "env.production"`},
		},
		{
			name: "wrong type",
			in:   "[deploy]\nport = \"3000\"\n",
			want: []string{"platform.toml:2:8: deploy.port must be an integer, not a string"},
		},
		{
			name: "scalar where a table belongs",
			in:   "build = 3\n",
			want: []string{"platform.toml:1:9: build must be a table, not an integer"},
		},
		{
			name: "syntax",
			in:   "[deploy\n",
			want: []string{"platform.toml:1:"},
		},
		{
			name: "validation errors are joined",
			in: `[build]
dockerfile = "../Dockerfile"
command = "make"
[deploy]
port = 70000
health_check_path = "healthz"
health_check_timeout = "30"
start_command = " "
`,
			want: []string{
				`build.dockerfile: "../Dockerfile" must be a relative path`,
				"build.command: only applies to builds without a Dockerfile",
				"deploy.port: must be between 1 and 65535, got 70000",
				`deploy.health_check_path: "healthz" must start with /`,
				`deploy.health_check_timeout: "30" is not a duration`,
				"deploy.start_command: must not be empty",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.in))
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
		})
	}
}

func TestFromTar(t *testing.T) {
	archive := func(gz bool, files map[string]string) *bytes.Buffer {
		var buf bytes.Buffer
		var zw *gzip.Writer
		tw := tar.NewWriter(&buf)
		if gz {
			zw = gzip.NewWriter(&buf)
			tw = tar.NewWriter(zw)
		}
		for name, body := range files {
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte(body)); err != nil {
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

	for _, gz := range []bool{false, true} {
		cfg, found, err := FromTar(archive(gz, map[string]string{"./platform.toml": "[deploy]\nport = 8080\n"}))
		if err != nil || !found || cfg.Deploy.Port != 8080 {
			t.Fatalf("gzip=%v: got %+v, %v, %v", gz, cfg, found, err)
		}
	}

	_, found, err := FromTar(archive(true, map[string]string{"sub/platform.toml": "port = 1"}))
	if err != nil || found {
		t.Fatalf("nested platform.toml must be ignored: found=%v err=%v", found, err)
	}
}
