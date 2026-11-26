//go:build !dockcli

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig writes a daemon config file with dataDir as data_dir.
func writeConfig(t *testing.T, dataDir, extra string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "data_dir = \"" + dataDir + "\"\n" + extra
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// install.sh writes Caddy's first config before a tunnel node has joined,
// when it can't know its marker yet.
func TestCaddyConfigTunnelBeforeJoin(t *testing.T) {
	setup(t)
	path := writeConfig(t, t.TempDir(), "[ingress]\nmode = \"cloudflare_tunnel\"\n")
	code, stdout, stderr := exec(t, "admin", "caddy-config", "--config", path)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, unjoinedMarker) || !strings.Contains(stdout, "127.0.0.1:80") {
		t.Fatalf("tunnel bootstrap config:\n%s", stdout)
	}
}

// install.sh recreates the registry only when its config changed, so the
// files must come out the same on every run.
func TestRegistryConfigIsDeterministic(t *testing.T) {
	setup(t)
	data := t.TempDir()
	path := writeConfig(t, data, "[registry]\nenabled = true\npublic_name = \"dock-registry\"\n")
	var outs []string
	for range 2 {
		out := t.TempDir()
		if code, _, stderr := exec(t, "admin", "registry-config", "--config", path, "--out", out); code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		outs = append(outs, out)
	}
	for _, name := range []string{"config.yml", "jwks.json"} {
		a, _ := os.ReadFile(filepath.Join(outs[0], name))
		b, _ := os.ReadFile(filepath.Join(outs[1], name))
		if len(a) == 0 || !bytes.Equal(a, b) {
			t.Errorf("%s differs between runs or is empty:\n%s\n---\n%s", name, a, b)
		}
	}
	yml, _ := os.ReadFile(filepath.Join(outs[0], "config.yml"))
	if !strings.Contains(string(yml), "http://127.0.0.1:7071/internal/registry/token") {
		t.Errorf("realm is not the internal token endpoint:\n%s", yml)
	}

	off := writeConfig(t, data, "")
	if code, _, stderr := exec(t, "admin", "registry-config", "--config", off, "--out", t.TempDir()); code == 0 || !strings.Contains(stderr, "REGISTRY=1") {
		t.Errorf("registry off: exit %d, %s", code, stderr)
	}
}
