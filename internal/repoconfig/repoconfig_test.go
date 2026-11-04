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
health_check_interval = "1m"
health_check_probe_timeout = "3s"
health_check_retries = 5
health_check_start_period = "90s"
pre_deploy_command = "npx prisma migrate deploy"
pre_deploy_timeout = "15m"
`))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Build: Build{Dockerfile: "docker/Dockerfile.prod"},
		Deploy: Deploy{
			StartCommand:            "node server.js",
			Port:                    3000,
			HealthCheckPath:         "/healthz",
			HealthCheckTimeout:      45 * time.Second,
			HealthCheckInterval:     time.Minute,
			HealthCheckProbeTimeout: 3 * time.Second,
			HealthCheckRetries:      5,
			HealthCheckStartPeriod:  90 * time.Second,
			PreDeployCommand:        "npx prisma migrate deploy",
			PreDeployTimeout:        15 * time.Minute,
		},
	}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestParseIntervalOff(t *testing.T) {
	cfg, err := Parse([]byte("[deploy]\nhealth_check_interval = \"off\"\n"))
	if err != nil || cfg.Deploy.HealthCheckInterval != IntervalOff {
		t.Fatalf("got %v, %v; want IntervalOff", cfg.Deploy.HealthCheckInterval, err)
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
			want: []string{`dock.toml:3:1: unknown key "build.dockerfil"`, `dock.toml:6:1: unknown key "deploy.healthcheck"`},
		},
		{
			name: "unknown table",
			in:   "[env.production]\nport = 1\n",
			want: []string{`dock.toml:1:2: unknown key "env.production"`},
		},
		{
			name: "wrong type",
			in:   "[deploy]\nport = \"3000\"\n",
			want: []string{"dock.toml:2:8: deploy.port must be an integer, not a string"},
		},
		{
			name: "scalar where a table belongs",
			in:   "build = 3\n",
			want: []string{"dock.toml:1:9: build must be a table, not an integer"},
		},
		{
			name: "syntax",
			in:   "[deploy\n",
			want: []string{"dock.toml:1:"},
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
pre_deploy_command = ""
pre_deploy_timeout = "2h"
`,
			want: []string{
				`build.dockerfile: "../Dockerfile" must be a relative path`,
				"build.command: only applies to builds without a Dockerfile",
				"deploy.port: must be between 1 and 65535, got 70000",
				`deploy.health_check_path: "healthz" must start with /`,
				`deploy.health_check_timeout: "30" is not a duration`,
				"deploy.start_command: must not be empty",
				"deploy.pre_deploy_command: must not be empty",
				"deploy.pre_deploy_timeout: must be between 1s and 1h0m0s, got 2h0m0s",
			},
		},
		{
			name: "ongoing health check bounds",
			in: `[deploy]
health_check_interval = "never"
health_check_probe_timeout = "10m"
health_check_retries = 0
health_check_start_period = "500ms"
`,
			want: []string{
				`deploy.health_check_interval: "never" is not a duration; use a unit, e.g. "30s", or "off"`,
				"deploy.health_check_probe_timeout: must be between 1s and 5m0s, got 10m0s",
				"deploy.health_check_retries: must be between 1 and 100, got 0",
				"deploy.health_check_start_period: must be between 1s and 1h0m0s, got 500ms",
			},
		},
		{
			name: "interval too long",
			in:   "[deploy]\nhealth_check_interval = \"2h\"\n",
			want: []string{"deploy.health_check_interval: must be between 1s and 1h0m0s, got 2h0m0s"},
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
		cfg, found, err := FromTar(archive(gz, map[string]string{"./dock.toml": "[deploy]\nport = 8080\n"}))
		if err != nil || !found || cfg.Deploy.Port != 8080 {
			t.Fatalf("gzip=%v: got %+v, %v, %v", gz, cfg, found, err)
		}
	}

	_, found, err := FromTar(archive(true, map[string]string{"sub/dock.toml": "port = 1"}))
	if err != nil || found {
		t.Fatalf("nested dock.toml must be ignored: found=%v err=%v", found, err)
	}
}
