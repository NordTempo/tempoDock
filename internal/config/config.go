// Package config loads the daemon's configuration (`dock serve`).
//
// Config lives in a TOML file (default /etc/dock/config.toml). Every field
// has a default, so a missing file means "all defaults". Nothing here is
// NordTempo-specific: domains, addresses and limits are all configuration.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// DefaultPath is where `dock serve` looks for its config file.
const DefaultPath = "/etc/dock/config.toml"

// Config is the daemon configuration.
type Config struct {
	// DataDir holds the SQLite DB, the secrets key, build logs and uploads.
	DataDir string `toml:"data_dir"`
	// Listen is the API and UI address. Put this on a Tailscale IP or
	// loopback; it is not meant to be the public port.
	Listen string `toml:"listen"`
	// InternalListen serves loopback-only endpoints such as Caddy's
	// on-demand TLS `ask` check.
	InternalListen string `toml:"internal_listen"`
	// PublicIP is the address users point DNS records at.
	PublicIP string `toml:"public_ip"`
	// BaseDomain enables generated hostnames (<service>-<env>.<base>).
	// Requires a wildcard DNS record pointing at PublicIP.
	BaseDomain string `toml:"base_domain"`
	// MaxUploadMB caps source uploads (`dock deploy` tarballs).
	MaxUploadMB int `toml:"max_upload_mb"`
	// HostPorts is the range of 127.0.0.1 ports published app containers are
	// bound to. Each release gets a fixed port so routes survive container
	// restarts while the daemon is down.
	HostPorts PortRange `toml:"host_ports"`

	Caddy    Caddy    `toml:"caddy"`
	BuildKit BuildKit `toml:"buildkit"`
	Network  Network  `toml:"network"`
	Defaults Defaults `toml:"defaults"`
}

// PortRange is an inclusive range of TCP ports.
type PortRange struct {
	Min int `toml:"min"`
	Max int `toml:"max"`
}

// Caddy configures how the daemon drives the Caddy process.
type Caddy struct {
	// AdminSocket is Caddy's admin endpoint as a Unix socket path.
	AdminSocket string `toml:"admin_socket"`
	HTTPPort    int    `toml:"http_port"`
	HTTPSPort   int    `toml:"https_port"`
	// ACMEEmail is passed to the certificate authority. Optional.
	ACMEEmail string `toml:"acme_email"`
	// DisableTLS serves every route over plain HTTP. For local development
	// and private-only setups.
	DisableTLS bool `toml:"disable_tls"`
}

// BuildKit configures the capped build container.
type BuildKit struct {
	// Enabled marks this server as a builder.
	Enabled bool `toml:"enabled"`
	// Image is the BuildKit image to run.
	Image string `toml:"image"`
	// MemoryMB and CPUs cap the BuildKit container.
	MemoryMB int     `toml:"memory_mb"`
	CPUs     float64 `toml:"cpus"`
}

// Network configures app networks and what app containers may reach.
// `dock admin firewall apply` (run as root by dock-firewall.service) turns
// it into firewall rules.
type Network struct {
	// Pool is the IPv4 range dock-managed networks get their subnets from,
	// one /24 per environment. Keep it clear of the LAN and the tailnet.
	Pool netip.Prefix `toml:"pool"`
	// AllowPrivate lists private CIDRs app containers and builds may reach
	// (a NAS, a LAN database). All other private, link-local and tailnet
	// addresses, and the server itself, are blocked.
	AllowPrivate []netip.Prefix `toml:"allow_private"`
}

// Defaults apply to services that don't set their own values.
type Defaults struct {
	MemoryMB           int     `toml:"memory_mb"`
	CPUs               float64 `toml:"cpus"`
	HealthCheckTimeout int     `toml:"health_check_timeout_seconds"`
	// KeepReleases is how many previous release images are kept for rollback.
	KeepReleases int `toml:"keep_releases"`
}

// Default returns the configuration used when no file overrides it.
func Default() Config {
	return Config{
		DataDir:        "/var/lib/dock",
		Listen:         "127.0.0.1:7070",
		InternalListen: "127.0.0.1:7071",
		MaxUploadMB:    512,
		HostPorts:      PortRange{Min: 20000, Max: 29999},
		Caddy: Caddy{
			AdminSocket: "/run/dock-caddy/admin.sock",
			HTTPPort:    80,
			HTTPSPort:   443,
		},
		BuildKit: BuildKit{
			Enabled:  true,
			Image:    "moby/buildkit:v0.34.1@sha256:99368f3e46177c6405b9740e4461d91cee004128daeecca78d4ceac50ec1da5c",
			MemoryMB: 2048,
			CPUs:     1,
		},
		Network: Network{
			Pool: netip.MustParsePrefix("10.213.0.0/16"),
		},
		Defaults: Defaults{
			MemoryMB:           512,
			CPUs:               1,
			HealthCheckTimeout: 60,
			KeepReleases:       5,
		},
	}
}

// Load reads path on top of the defaults. A missing file is not an error.
func Load(path string) (Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if err := toml.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.Network.validate(); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func (n *Network) validate() error {
	if !n.Pool.IsValid() || !n.Pool.Addr().Is4() || n.Pool.Bits() > 24 {
		return fmt.Errorf("[network] pool %q must be an IPv4 CIDR of /24 or larger", n.Pool)
	}
	n.Pool = n.Pool.Masked()
	for i, p := range n.AllowPrivate {
		n.AllowPrivate[i] = p.Masked()
	}
	return nil
}

// DBPath is the SQLite file inside DataDir.
func (c Config) DBPath() string { return filepath.Join(c.DataDir, "dock.db") }

// KeyPath is the AES key for secrets at rest. It lives outside the DB so a
// leaked DB file alone doesn't reveal secrets.
func (c Config) KeyPath() string { return filepath.Join(c.DataDir, "secret.key") }
