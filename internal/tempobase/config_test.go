package tempobase

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The TOML file sets values, the environment wins, defaults fill the rest.
func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tempobase.toml")
	toml := `listen = "0.0.0.0:8080"
data_dir = "` + dir + `"
base_url = "https://db.example.com/"
trusted_proxies = ["10.0.0.0/8"]
audit_retention_days = 30
`
	if err := os.WriteFile(path, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"TEMPOBASE_CONFIG": path, "TEMPOBASE_LISTEN": "127.0.0.1:9000", "TEMPOBASE_TRUSTED_PROXIES": "192.168.1.1, fd00::/8"}
	cfg, err := LoadConfig("", func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:9000" || cfg.DataDir != dir || cfg.BaseURL != "https://db.example.com" || cfg.AuditRetentionDays != 30 {
		t.Fatalf("config %+v", cfg)
	}
	if cfg.SecretsKeyFile != filepath.Join(dir, "secrets.key") || cfg.DBPath() != filepath.Join(dir, "tempobase.db") {
		t.Fatalf("paths %q %q", cfg.SecretsKeyFile, cfg.DBPath())
	}
	if !cfg.Trusted(netip.MustParseAddr("192.168.1.1")) || !cfg.Trusted(netip.MustParseAddr("fd00::1")) || cfg.Trusted(netip.MustParseAddr("10.1.1.1")) {
		t.Fatal("env trusted_proxies didn't replace the file's")
	}

	cfg, err = LoadConfig("", func(string) string { return "" })
	if err != nil || cfg.Listen != DefaultListen || cfg.DataDir != DefaultDataDir || cfg.AuditRetentionDays != DefaultRetentionDays {
		t.Fatalf("defaults %+v, %v", cfg, err)
	}

	for _, bad := range []string{`listen = "7171"`, `base_url = "db.example.com"`, `trusted_proxies = ["nope"]`, `unknown = 1`} {
		if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path, func(string) string { return "" }); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
	if _, err := LoadConfig(filepath.Join(dir, "missing.toml"), func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing file: %v", err)
	}
}
