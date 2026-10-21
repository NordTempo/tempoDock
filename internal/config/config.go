// Package config loads the daemon's configuration (`platform serve`).
//
// Config lives in a TOML file (default /etc/platform/config.toml). Every field
// has a default, so a missing file means "all defaults". Nothing here is
// NordTempo-specific: domains, addresses and limits are all configuration.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// DefaultPath is where `platform serve` looks for its config file.
const DefaultPath = "/etc/platform/config.toml"

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

	Caddy    Caddy    `toml:"caddy"`
	BuildKit BuildKit `toml:"buildkit"`
	Defaults Defaults `toml:"defaults"`
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
		DataDir:        "/var/lib/platform",
		Listen:         "127.0.0.1:7070",
		InternalListen: "127.0.0.1:7071",
		Caddy: Caddy{
			AdminSocket: "/run/platform-caddy/admin.sock",
			HTTPPort:    80,
			HTTPSPort:   443,
		},
		BuildKit: BuildKit{
			Enabled:  true,
			Image:    "moby/buildkit:v0.34.1",
			MemoryMB: 2048,
			CPUs:     1,
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
	return cfg, nil
}

// DBPath is the SQLite file inside DataDir.
func (c Config) DBPath() string { return filepath.Join(c.DataDir, "platform.db") }

// KeyPath is the AES key for secrets at rest. It lives outside the DB so a
// leaked DB file alone doesn't reveal secrets.
func (c Config) KeyPath() string { return filepath.Join(c.DataDir, "secret.key") }
