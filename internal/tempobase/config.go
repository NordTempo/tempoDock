package tempobase

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Config is the server's configuration (ADR 0024 section 2): an optional
// TOML file, then TEMPOBASE_* environment variables, which win. Every
// field has a default.
type Config struct {
	// Listen is the HTTP address. Put TLS in front (Caddy, a tunnel) for
	// anything public.
	Listen string `toml:"listen"`
	// DataDir holds tempobase.db and, by default, the key file.
	DataDir string `toml:"data_dir"`
	// SecretsKeyFile is the AES-256 key that seals connection URLs and
	// role passwords, created on first start. Point it at a mounted
	// secret to keep it out of volume backups.
	SecretsKeyFile string `toml:"secrets_key_file"`
	// BaseURL is used for links (setup, invites, resets) and decides
	// Secure cookies; empty takes it from each request.
	BaseURL string `toml:"base_url"`
	// TrustedProxies are CIDRs whose X-Forwarded-For and
	// X-Forwarded-Proto are believed.
	TrustedProxies []string `toml:"trusted_proxies"`
	// AuditRetentionDays deletes older audit events, at most hourly and
	// only while something is audited.
	AuditRetentionDays int `toml:"audit_retention_days"`

	// proxies is TrustedProxies parsed by Validate.
	proxies []netip.Prefix
}

// Defaults.
const (
	DefaultListen        = "127.0.0.1:7171"
	DefaultDataDir       = "/var/lib/tempobase"
	DefaultRetentionDays = 90
)

// LoadConfig reads the TOML file at path (none when empty), applies the
// environment (getenv, normally os.Getenv) and defaults, and validates
// the result. path empty falls back to $TEMPOBASE_CONFIG.
func LoadConfig(path string, getenv func(string) string) (Config, error) {
	var c Config
	if path == "" {
		path = getenv("TEMPOBASE_CONFIG")
	}
	if path != "" {
		b, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			return c, fmt.Errorf("config file %s does not exist; create it or leave --config out", path)
		}
		if err != nil {
			return c, fmt.Errorf("read config: %w", err)
		}
		dec := toml.NewDecoder(bytes.NewReader(b)).DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			return c, fmt.Errorf("config %s: %w", path, err)
		}
	}
	if err := c.applyEnv(getenv); err != nil {
		return c, err
	}
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	if c.DataDir == "" {
		c.DataDir = DefaultDataDir
	}
	if c.SecretsKeyFile == "" {
		c.SecretsKeyFile = filepath.Join(c.DataDir, "secrets.key")
	}
	if c.AuditRetentionDays == 0 {
		c.AuditRetentionDays = DefaultRetentionDays
	}
	return c, c.validate()
}

// applyEnv overrides fields from TEMPOBASE_* variables that are set.
func (c *Config) applyEnv(getenv func(string) string) error {
	for _, s := range []struct {
		name string
		dst  *string
	}{
		{"TEMPOBASE_LISTEN", &c.Listen},
		{"TEMPOBASE_DATA_DIR", &c.DataDir},
		{"TEMPOBASE_SECRETS_KEY_FILE", &c.SecretsKeyFile},
		{"TEMPOBASE_BASE_URL", &c.BaseURL},
	} {
		if v := getenv(s.name); v != "" {
			*s.dst = v
		}
	}
	if v := getenv("TEMPOBASE_TRUSTED_PROXIES"); v != "" {
		c.TrustedProxies = strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })
	}
	if v := getenv("TEMPOBASE_AUDIT_RETENTION_DAYS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("TEMPOBASE_AUDIT_RETENTION_DAYS=%q is not a number of days", v)
		}
		c.AuditRetentionDays = n
	}
	return nil
}

func (c *Config) validate() error {
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return fmt.Errorf("listen %q must be host:port, e.g. %s", c.Listen, DefaultListen)
	}
	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") {
			return fmt.Errorf("base_url %q must be an http or https origin, e.g. https://db.example.com", c.BaseURL)
		}
		c.BaseURL = u.Scheme + "://" + u.Host
	}
	c.proxies = c.proxies[:0]
	for _, p := range c.TrustedProxies {
		prefix, err := netip.ParsePrefix(p)
		if err != nil {
			addr, aerr := netip.ParseAddr(p)
			if aerr != nil {
				return fmt.Errorf("trusted_proxies: %q is not a CIDR (e.g. 10.0.0.0/8) or address", p)
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		c.proxies = append(c.proxies, prefix.Masked())
	}
	if c.AuditRetentionDays < 1 {
		return fmt.Errorf("audit_retention_days must be at least 1, got %d", c.AuditRetentionDays)
	}
	return nil
}

// DBPath is the SQLite database's path.
func (c Config) DBPath() string { return filepath.Join(c.DataDir, "tempobase.db") }

// Trusted reports whether a request from addr may set X-Forwarded-*.
func (c Config) Trusted(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, p := range c.proxies {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
