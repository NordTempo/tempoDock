package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func load(t *testing.T, toml string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestNetwork(t *testing.T) {
	cfg, err := load(t, `
[network]
pool = "10.99.1.0/20"
allow_private = ["192.168.10.20/32", "10.0.5.7/24"]
`)
	if err != nil {
		t.Fatal(err)
	}
	if want := netip.MustParsePrefix("10.99.0.0/20"); cfg.Network.Pool != want {
		t.Errorf("pool = %s, want %s (masked)", cfg.Network.Pool, want)
	}
	want := []netip.Prefix{netip.MustParsePrefix("192.168.10.20/32"), netip.MustParsePrefix("10.0.5.0/24")}
	if !slices.Equal(cfg.Network.AllowPrivate, want) {
		t.Errorf("allow_private = %v, want %v", cfg.Network.AllowPrivate, want)
	}

	cfg, err = load(t, `data_dir = "/tmp"`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Network.Pool != Default().Network.Pool || len(cfg.Network.AllowPrivate) != 0 {
		t.Errorf("network = %+v, want defaults", cfg.Network)
	}
}

func TestNetworkInvalid(t *testing.T) {
	for _, toml := range []string{
		`network.pool = "10.213.0.0/25"`,       // smaller than one network
		`network.pool = "fd00::/48"`,           // not IPv4
		`network.pool = ""`,                    // empty
		`network.allow_private = ["10.1.2.3"]`, // not a CIDR
	} {
		if _, err := load(t, toml); err == nil {
			t.Errorf("%s: accepted", toml)
		}
	}
}
