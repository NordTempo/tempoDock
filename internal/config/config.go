// Package config loads the daemon's configuration (`dock serve` on the
// control plane, `dock node` on other servers).
//
// Config lives in a TOML file (default /etc/dock/config.toml). Every field
// has a default, so a missing file means "all defaults". Nothing here is
// NordTempo-specific: domains, addresses and limits are all configuration.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

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
	Node     Node     `toml:"node"`
	Registry Registry `toml:"registry"`
	Ingress  Ingress  `toml:"ingress"`
	Hooks    Hooks    `toml:"hooks"`
	Backup   Backup   `toml:"backup"`
}

// Backup configures backup jobs (ADR 0019). The repository, credentials,
// retention and schedule are runtime settings (`dock backup configure`).
type Backup struct {
	// ResticImage provides the restic binary jobs use. Pin it by digest.
	ResticImage string `toml:"restic_image"`
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
	// and private-only setups in direct ingress mode. Tunnel servers leave it
	// off: Caddy is plain HTTP there anyway, and apps are https:// at
	// Cloudflare's edge.
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

// Node configures the link between the control plane and other servers
// (ADR 0014). One binary plays either side: `dock serve` reads Listen,
// `dock node` reads ControlPlane and the files in DataDir (NodeKeyPath,
// NodeStatePath).
type Node struct {
	// Listen is where the control plane accepts node connections, e.g.
	// "100.64.0.10:7443" (a Tailscale IP). Empty disables the node port:
	// the control plane then only runs apps on its own server.
	Listen string `toml:"listen"`
	// ControlPlane overrides the address `dock node` dials ("host:port").
	// Empty uses the address from the join token, kept in NodeStatePath.
	// Set it when the control plane moves; its key stays pinned, so no
	// re-join is needed.
	ControlPlane string `toml:"control_plane"`
}

// Registry configures the image registry on the control plane (ADR 0014
// section 4): CNCF Distribution in the `dock-registry` container, which
// install.sh sets up when Enabled.
type Registry struct {
	// Enabled runs the registry on this server. Control plane only.
	Enabled bool `toml:"enabled"`
	// Listen is the host address the registry container is published on.
	// On a node, `dock node` serves the registry there over the link (ADR
	// 0014 amendment), so it is the registry's address on every server and
	// the prefix of every image name.
	Listen string `toml:"listen"`
	// StorageDir holds the registry's blobs and manifests on the host.
	StorageDir string `toml:"storage_dir"`
	// PublicName names the registry: the audience of its tokens. Required
	// when Enabled.
	PublicName string `toml:"public_name"`
}

// IngressMode is how public traffic reaches a server's Caddy (ADR 0013).
type IngressMode string

const (
	// IngressDirect is a public IP with ports 80 and 443 open; Caddy issues
	// certificates.
	IngressDirect IngressMode = "direct"
	// IngressCloudflareTunnel is cloudflared dialing out to Cloudflare and
	// forwarding to Caddy in plain HTTP on 127.0.0.1:<[caddy] http_port>;
	// Cloudflare's edge terminates TLS.
	IngressCloudflareTunnel IngressMode = "cloudflare_tunnel"
)

// Ingress describes how the public reaches this server. On a node these
// are facts it reports to the control plane in hello. Generated hostnames
// use the top-level BaseDomain.
type Ingress struct {
	// Mode is direct or cloudflare_tunnel.
	Mode IngressMode `toml:"mode"`
	// TunnelHostname is "<tunnel-id>.cfargotunnel.com", the CNAME target
	// for hostnames served through the tunnel. Tunnel mode only.
	TunnelHostname string `toml:"tunnel_hostname"`
}

// Hooks configures incoming webhooks for GitHub push-to-deploy (ADR 0018).
// The API stays private: only /hooks/github/ on PublicHost is routed by the
// control plane's Caddy, to a loopback listener that serves nothing else.
type Hooks struct {
	// PublicHost is the public hostname GitHub posts to, e.g.
	// "hooks.example.com"; its DNS points at this server like an app
	// domain. Empty: push-to-deploy can be connected but receives nothing.
	PublicHost string `toml:"public_host"`
	// Listen is the loopback address Caddy proxies webhook requests to.
	Listen string `toml:"listen"`
	// UIURL is the UI's base URL, e.g. "https://dock.example.com", used
	// for the "Details" link of commit statuses. Empty: statuses have no
	// link (it is shown to everyone who can see the repository).
	UIURL string `toml:"ui_url"`
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
		Registry: Registry{
			Listen:     "127.0.0.1:5000",
			StorageDir: "/var/lib/dock-registry",
		},
		Ingress: Ingress{Mode: IngressDirect},
		Hooks:   Hooks{Listen: "127.0.0.1:7072"},
		Backup: Backup{
			ResticImage: "restic/restic:0.18.1@sha256:39d9072fb5651c80d75c7a811612eb60b4c06b32ffe87c2e9f3c7222e1797e76",
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
	if err := cfg.validate(); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// validate checks the rules beyond what the field types enforce, and
// normalizes [network].
func (c *Config) validate() error {
	if err := c.Network.validate(); err != nil {
		return err
	}
	if err := checkHostPort("[node] listen", c.Node.Listen); err != nil {
		return err
	}
	if err := checkHostPort("[node] control_plane", c.Node.ControlPlane); err != nil {
		return err
	}
	if c.Registry.Enabled {
		if c.Registry.Listen == "" || c.Registry.StorageDir == "" || c.Registry.PublicName == "" {
			return errors.New("[registry] enabled needs listen, storage_dir and public_name (the registry's name in its tokens)")
		}
		if err := checkHostPort("[registry] listen", c.Registry.Listen); err != nil {
			return err
		}
	}
	if err := c.Hooks.validate(); err != nil {
		return err
	}
	switch c.Ingress.Mode {
	case "":
		c.Ingress.Mode = IngressDirect
	case IngressDirect:
	case IngressCloudflareTunnel:
		if c.Caddy.DisableTLS {
			return errors.New("[ingress] mode cloudflare_tunnel serves apps over https:// at Cloudflare's edge; set [caddy] disable_tls = false (Caddy is plain HTTP on 127.0.0.1 either way)")
		}
	default:
		return fmt.Errorf("[ingress] mode %q must be %q or %q", c.Ingress.Mode, IngressDirect, IngressCloudflareTunnel)
	}
	return nil
}

// validate normalizes public_host and ui_url and checks them.
func (h *Hooks) validate() error {
	h.PublicHost = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h.PublicHost)), ".")
	if h.PublicHost != "" && !hostnameRe.MatchString(h.PublicHost) {
		return fmt.Errorf("[hooks] public_host %q must be a hostname like hooks.example.com", h.PublicHost)
	}
	if err := checkHostPort("[hooks] listen", h.Listen); err != nil {
		return err
	}
	if h.PublicHost != "" {
		// Caddy on this server proxies to it; nothing else may reach it.
		host, _, _ := net.SplitHostPort(h.Listen)
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("[hooks] listen %q must be a loopback address like 127.0.0.1:7072", h.Listen)
		}
	}
	if h.UIURL != "" {
		u, err := url.Parse(h.UIURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("[hooks] ui_url %q must be an http(s) URL like https://dock.example.com", h.UIURL)
		}
		h.UIURL = strings.TrimSuffix(h.UIURL, "/")
	}
	return nil
}

// hostnameRe matches lower-case DNS names with at least two labels.
var hostnameRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// checkHostPort accepts "" (unset) or "host:port" with a port in 1-65535.
func checkHostPort(field, addr string) error {
	if addr == "" {
		return nil
	}
	_, port, err := net.SplitHostPort(addr)
	if err == nil {
		if n, perr := strconv.Atoi(port); perr != nil || n < 1 || n > 65535 {
			err = fmt.Errorf("bad port %q", port)
		}
	}
	if err != nil {
		return fmt.Errorf("%s %q must be host:port, e.g. \"100.64.0.10:7443\": %w", field, addr, err)
	}
	return nil
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

// LinkKeyPath is the control plane's Ed25519 key for the node link,
// created by the first `dock serve`.
func (c Config) LinkKeyPath() string { return filepath.Join(c.DataDir, "link.key") }

// NodeKeyPath is a node's Ed25519 key, created by `dock node join`.
func (c Config) NodeKeyPath() string { return filepath.Join(c.DataDir, "node.key") }

// NodeStatePath is a node's link state, written by `dock node join`: the
// control plane's address and pinned fingerprints. It lives in DataDir, not
// /etc/dock, because `dock node` rewrites the pins (`trust`) and its unit
// may only write DataDir. install.sh takes its presence to mean "joined".
func (c Config) NodeStatePath() string { return filepath.Join(c.DataDir, "node.toml") }

// KeyPath is the AES key for secrets at rest. It lives outside the DB so a
// leaked DB file alone doesn't reveal secrets.
func (c Config) KeyPath() string { return filepath.Join(c.DataDir, "secret.key") }
