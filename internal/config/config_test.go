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

func TestLinkSections(t *testing.T) {
	cfg, err := load(t, `data_dir = "/tmp"`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Node.Listen != "" || cfg.Registry.Enabled || cfg.Ingress.Mode != IngressDirect {
		t.Errorf("node %+v, registry %+v, ingress %+v: want today's single-server defaults", cfg.Node, cfg.Registry, cfg.Ingress)
	}

	cfg, err = load(t, `
[node]
listen = "100.64.0.10:7443"

[registry]
enabled = true
public_name = "registry.example.dev"

[ingress]
mode = "cloudflare_tunnel"
tunnel_hostname = "6ff42ae2-765d-4adf-8112-31c55c1551ef.cfargotunnel.com"
`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Ingress.Mode != IngressCloudflareTunnel || cfg.Registry.Listen != "127.0.0.1:5000" || cfg.Node.Listen != "100.64.0.10:7443" {
		t.Errorf("got ingress %+v, registry %+v, node %+v", cfg.Ingress, cfg.Registry, cfg.Node)
	}
}

func TestLinkSectionsInvalid(t *testing.T) {
	for _, toml := range []string{
		`node.listen = "7443"`,                                           // no host part
		`node.control_plane = "cp.example.dev:http"`,                     // port not numeric
		`registry.enabled = true`,                                        // no public_name
		`ingress.mode = "tunnel"`,                                        // unknown mode
		"ingress.mode = \"cloudflare_tunnel\"\ncaddy.disable_tls = true", // apps would show http:// URLs
	} {
		if _, err := load(t, toml); err == nil {
			t.Errorf("%s: accepted", toml)
		}
	}
}

func TestUpdateSource(t *testing.T) {
	for toml, ok := range map[string]bool{
		``:                                    true, // GitHub, NordTempo/tempoDock
		`updates.source = "/srv/releases"`:    true,
		`updates.source = "github.com/o/r"`:   true,
		`updates.source = "releases"`:         false, // relative path
		`updates.source = "gitlab.com/o/r"`:   false,
		`updates.source = "github.com/o/r/x"`: false,
		`updates.trusted_keys = ["8uQjZq1kO37ezVv7EzN8eyrqOYXRGK2nsCRL/d3RSdU="]`: true,
		`updates.trusted_keys = ["bm90IGEga2V5"]`:                                 false, // 9 bytes
		`updates.trusted_keys = ["not base64!"]`:                                  false,
	} {
		if _, err := load(t, toml); (err == nil) != ok {
			t.Errorf("%q: err = %v", toml, err)
		}
	}
}

// trusted_keys apply on a node, and on a control plane only with a
// directory source.
func TestUpdatesExtra(t *testing.T) {
	keys := []string{"8uQjZq1kO37ezVv7EzN8eyrqOYXRGK2nsCRL/d3RSdU="}
	for _, c := range []struct {
		name          string
		u             Updates
		node          bool
		keys, ignored bool
	}{
		{"directory source", Updates{Source: "/srv/releases", TrustedKeys: keys}, false, true, false},
		{"GitHub source", Updates{Source: DefaultUpdateSource, TrustedKeys: keys}, false, false, true},
		{"node", Updates{Source: DefaultUpdateSource, TrustedKeys: keys}, true, true, false},
		{"none", Updates{Source: "/srv/releases"}, false, false, false},
	} {
		got, ignored := c.u.Extra(c.node)
		if (len(got) == 1) != c.keys || ignored != c.ignored {
			t.Errorf("%s: %d keys, ignored %t; want keys %t, ignored %t", c.name, len(got), ignored, c.keys, c.ignored)
		}
	}
}
