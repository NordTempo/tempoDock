// Package config loads the daemon's configuration (`dock serve` on the
// control plane, `dock node` on other servers).
//
// Config lives in a TOML file (default /etc/dock/config.toml). Every field
// has a default, so a missing file means "all defaults". Nothing here is
// NordTempo-specific: domains, addresses and limits are all configuration.
package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	// Pprof serves Go's runtime profiles on InternalListen at
	// /debug/pprof/. Off by default: any local user can reach that
	// listener, and a CPU profile or trace costs CPU while it runs.
	Pprof bool `toml:"pprof"`
	// PublicListen is the loopback address the control plane's Caddy
	// proxies Dock's public address to (ADR 0029): the API and UI, for
	// requests that came through Caddy. It is served even while no
	// address is set, so setting one needs no restart.
	PublicListen string `toml:"public_listen"`
	// PublicIP is the address users point DNS records at.
	PublicIP string `toml:"public_ip"`
	// BaseDomain enables generated hostnames
	// (<service>-<project>[-<env>]-<random>.<base>, ADR 0087).
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
	// Retention and Alerts are control plane settings (ADR 0020).
	Retention Retention `toml:"retention"`
	Alerts    Alerts    `toml:"alerts"`
	// Updates is where the control plane looks for new releases (ADR 0028).
	Updates Updates `toml:"updates"`
	// Analytics is this server's part in Dock Analytics (ADR 0037).
	Analytics Analytics `toml:"analytics"`
	// Traces configures the trace store, tracing over OTLP.
	Traces Traces `toml:"traces"`
	// Logs bounds the history of removed containers' output (ADR 0046).
	Logs Logs `toml:"logs"`
	// Status is where public status pages are served (ADR 0073).
	Status Status `toml:"status"`
	// Scan configures image vulnerability scanning (ADR 0074).
	Scan Scan `toml:"scan"`
	// Previews bounds copies of databases into previews (ADR 0092).
	Previews Previews `toml:"previews"`
}

// Previews configures preview environments. Control plane only.
type Previews struct {
	// CopyMaxMB is the largest database a preview copies from its base
	// (ADR 0092), 2048 when unset; a larger one starts empty.
	CopyMaxMB int `toml:"copy_max_mb"`
}

// Status configures public status pages (ADR 0073). Control plane only.
type Status struct {
	// Listen is the loopback address the control plane's Caddy sends
	// status pages' hostnames to. Its own listener, serving nothing but
	// those pages, so no other part of Dock is reachable through them.
	// Empty turns status pages' serving off.
	Listen string `toml:"listen"`
}

// Scan configures image vulnerability scanning on builders (ADR 0074).
// Whether scanning is on is a runtime setting (`dock scanning on|off`).
type Scan struct {
	// SyftImage and GrypeImage are the scanners, pinned by digest: Syft
	// makes an image's SBOM, Grype matches it against its database.
	SyftImage  string `toml:"syft_image"`
	GrypeImage string `toml:"grype_image"`
	// MemoryMB caps each scan job (1024).
	MemoryMB int `toml:"memory_mb"`
	// QuietSeconds is how long no build, deploy, wake or move may have
	// run anywhere before a scan starts (60).
	QuietSeconds int `toml:"quiet_seconds"`
	// Offline: builders never download a vulnerability database; an
	// operator imports one (`dock scanning import`), for installs without
	// internet access. Base image checks are off too.
	Offline bool `toml:"offline"`
}

// Logs bounds log history (ADR 0046): when dock removes a container, its
// output is kept on its server, per service at most KeepMBPerService
// and per server at most KeepMBPerServer (compressed; the oldest
// releases go first), for KeepDays. Nothing is kept on a disk with less
// than 1 GB or 5% free. The control plane's settings apply to every
// server.
type Logs struct {
	KeepMBPerService int `toml:"keep_mb_per_service"`
	KeepMBPerServer  int `toml:"keep_mb_per_server"`
	KeepDays         int `toml:"keep_days"`
}

// Analytics configures Dock Analytics on this server (ADR 0037): every
// server's ingest and spool, and on the analytics server the store's
// container. Which server that is, and which sites collect, are runtime
// settings on the control plane.
type Analytics struct {
	// IngestListen is the loopback address Caddy sends /_dock/a/* of
	// analytics-enabled hostnames to. Its own listener, so nothing else
	// the daemon serves is reachable through those paths.
	IngestListen string `toml:"ingest_listen"`
	// SpoolMaxMB caps the spool of events waiting for the store; past it
	// the oldest go, counted.
	SpoolMaxMB int `toml:"spool_max_mb"`
	// StorePort is the loopback port the analytics store is published on,
	// on the analytics server.
	StorePort int `toml:"store_port"`
	// StoreBinary is a dock-analytics binary (linux, Debian 12's C
	// library: `make analytics-binary`) the control plane runs the store
	// from, for development builds, which have no release to download it
	// from. Empty is the binary released with this version of dock.
	StoreBinary string `toml:"store_binary"`
	// StoreMemoryMB is the store container's memory limit.
	StoreMemoryMB int `toml:"store_memory_mb"`
	// SiteDayEvents and SiteDayReplayMB cap what a site keeps a UTC day,
	// of events and of replay chunks: anyone can send a site's ingest
	// more, and the rest of that day's is dropped. MaxGB caps the store's
	// database and replay files together; past it the oldest days of
	// replays go early, then the oldest days of raw events (their daily
	// totals stay). 0 for any of them is the store's default: 1,000,000
	// events, 1024 MB, and the smaller of 10 GB and a tenth of the
	// store's disk.
	SiteDayEvents   int64 `toml:"site_day_events"`
	SiteDayReplayMB int64 `toml:"site_day_replay_mb"`
	MaxGB           int64 `toml:"max_gb"`
	// StatsListen is the loopback address the control plane's Caddy
	// sends sites' public dashboards to (ADR 0037, A4). Its own listener,
	// serving nothing but those pages, so no other part of Dock is
	// reachable through a dashboard's hostname. Empty turns them off.
	StatsListen string `toml:"stats_listen"`
}

// Traces configures the trace store: spans for tracing over OTLP, kept by
// dock-analytics in a container of its own on the analytics server.
// Whether tracing is on is a runtime setting on the control plane.
type Traces struct {
	// StorePort is the loopback port the trace store is published on, on
	// the analytics server.
	StorePort int `toml:"store_port"`
	// StoreMemoryMB is the trace store container's memory limit.
	StoreMemoryMB int `toml:"store_memory_mb"`
	// RetentionDays is how long spans are kept, in whole UTC days.
	// Control plane only.
	RetentionDays int `toml:"retention_days"`
	// IngestListen is the loopback address of this server's trace ingest
	// (OTLP/HTTP), which its Caddy forwards containers' exports to. Its own
	// listener. Empty turns it off.
	IngestListen string `toml:"ingest_listen"`
	// SpoolMaxMB caps the spans waiting for the trace store on this
	// server, and MetricsSpoolMaxMB app metrics' points; past either the
	// oldest go.
	SpoolMaxMB        int `toml:"spool_max_mb"`
	MetricsSpoolMaxMB int `toml:"metrics_spool_max_mb"`
	// ServiceDaySpans and OrgDaySpans cap the spans a service and an org
	// keep a UTC day; past either only error spans are kept, up to
	// ErrorDaySpans a service. MaxGB caps the trace store's data; past it
	// the oldest day goes early. Control plane only.
	ServiceDaySpans int64 `toml:"service_day_spans"`
	OrgDaySpans     int64 `toml:"org_day_spans"`
	ErrorDaySpans   int64 `toml:"error_day_spans"`
	MaxGB           int   `toml:"max_gb"`
	// MetricSeries and MetricNames cap the series and metric names of app
	// metrics a service keeps a UTC day; past either its new series are
	// cut. Control plane only.
	MetricSeries int `toml:"metric_series"`
	MetricNames  int `toml:"metric_names"`
}

// DefaultUpdateSource is the GitHub repository releases are published to
// (.github/workflows/release.yml).
const DefaultUpdateSource = "github.com/NordTempo/tempoDock"

// Updates configures update checks and downloads (ADR 0028). Control
// plane only: nodes get their releases over the node link.
type Updates struct {
	// Source is "github.com/OWNER/REPO" (GitHub's releases) or an absolute
	// path to a directory of releases, <dir>/<version>/ with the assets.
	Source string `toml:"source"`
	// TokenFile holds a GitHub token that can read Source's releases, used
	// when the file exists. A private repository needs one.
	TokenFile string `toml:"token_file"`
	// TrustedKeys are Ed25519 public keys (base64, as in ADR 0088) whose
	// signatures count as Dock's own, for releases you build and sign
	// yourself. Read only from this root-owned file, never set through
	// the API. They apply only with a directory Source on the control
	// plane, and to what its control plane sends on a node (Extra).
	TrustedKeys []string `toml:"trusted_keys"`
}

// Extra returns TrustedKeys decoded, for update.TrustExtra, when they
// apply: on a node, which takes releases only from its control plane, or
// with a directory Source. A control plane on GitHub's releases trusts
// Dock's keys alone. ignored reports keys that were set but don't apply.
func (u Updates) Extra(node bool) (keys []ed25519.PublicKey, ignored bool) {
	if len(u.TrustedKeys) == 0 {
		return nil, false
	}
	if !node && !filepath.IsAbs(u.Source) {
		return nil, true
	}
	for _, k := range u.TrustedKeys {
		b, _ := base64.StdEncoding.DecodeString(k) // checked by validate
		keys = append(keys, b)
	}
	return keys, false
}

// Retention is how long the control plane keeps history, and when it
// prunes servers' disks (ADR 0020). Retention runs on the statistics tick,
// at most hourly; pruning when a server's disk passes PruneDiskPercent.
type Retention struct {
	// BuildLogDays keeps a release's history (build log, saved container
	// output, progress events) this many days. Releases in their service's
	// rollback window ([defaults] keep_releases), active and deploying
	// ones keep theirs whatever their age.
	BuildLogDays int `toml:"build_log_days"`
	// AuditDays keeps audit events this many days; AuditMaxEvents, when
	// above 0, also caps how many are kept (the oldest go first).
	AuditDays      int `toml:"audit_days"`
	AuditMaxEvents int `toml:"audit_max_events"`
	// AlertDays keeps resolved alerts this many days.
	AlertDays int `toml:"alert_days"`
	// PruneDiskPercent: when the filesystem holding Docker's data passes
	// it on a server, its BuildKit cache is pruned down to BuildCacheKeepMB
	// and its dangling images outside the rollback window are removed, at
	// most once an hour. 0 turns pruning off.
	PruneDiskPercent int `toml:"prune_disk_percent"`
	BuildCacheKeepMB int `toml:"build_cache_keep_mb"`
}

// Alerts sets when the control plane raises alerts (ADR 0020). Where
// alerts are sent (webhook, Slack, email) is configured with `dock alerts
// channel add`, not here.
type Alerts struct {
	// DiskWarningPercent and DiskCriticalPercent are how full a server's
	// disk (Docker's data, or dock's) may get before a warning or critical
	// alert.
	DiskWarningPercent  int `toml:"disk_warning_percent"`
	DiskCriticalPercent int `toml:"disk_critical_percent"`
	// ServerOfflineMinutes is how long a server may be unreachable before
	// an alert.
	ServerOfflineMinutes int `toml:"server_offline_minutes"`
	// A service whose container restarts CrashLoopRestarts times within
	// CrashLoopMinutes is crash looping.
	CrashLoopRestarts int `toml:"crash_loop_restarts"`
	CrashLoopMinutes  int `toml:"crash_loop_minutes"`
	// A server's memory used above MemoryWarningPercent (or
	// MemoryCriticalPercent) for MemoryMinutes is memory pressure, and its
	// CPUs busier than CPUPercent for CPUMinutes is sustained CPU. Both
	// read the statistics the control plane collects anyway, once a
	// minute. CPUPercent 0 turns the CPU alert off.
	MemoryWarningPercent  int `toml:"memory_warning_percent"`
	MemoryCriticalPercent int `toml:"memory_critical_percent"`
	MemoryMinutes         int `toml:"memory_minutes"`
	CPUPercent            int `toml:"cpu_percent"`
	CPUMinutes            int `toml:"cpu_minutes"`
}

// Backup configures backup jobs (ADR 0019). The repository, credentials,
// retention and schedule are runtime settings (`dock backup configure`).
type Backup struct {
	// ResticImage provides the restic binary jobs use. Pin it by digest.
	// Volume snapshot jobs (ADR 0047) run in it too, for its cp and du.
	ResticImage string `toml:"restic_image"`
	// SnapshotKeep is how many volume snapshots each service keeps (ADR
	// 0047), 5 when unset; SnapshotDays how many days any is kept, 7
	// when unset.
	SnapshotKeep int `toml:"snapshot_keep"`
	SnapshotDays int `toml:"snapshot_days"`
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
	// TunnelPort is where a direct server's Caddy also serves its
	// Cloudflare tunnel, on 127.0.0.1, when the control plane runs one
	// there (ADR 0036). 0 turns that off. Tunnel mode uses http_port.
	TunnelPort int `toml:"tunnel_port"`
	// ACMEEmail is passed to the certificate authority. Optional.
	ACMEEmail string `toml:"acme_email"`
	// ACMEDirectory is the ACME directory the control plane gets base
	// domains' wildcard certificates from (ADR 0090); empty is Let's
	// Encrypt. Caddy's on-demand certificates don't use it.
	ACMEDirectory string `toml:"acme_directory"`
	// DisableTLS serves every route over plain HTTP. For local development
	// and private-only setups in direct ingress mode. Tunnel servers leave it
	// off: Caddy is plain HTTP there anyway, and apps are https:// at
	// Cloudflare's edge.
	DisableTLS bool `toml:"disable_tls"`
	// TrafficListen is the loopback UDP address the daemon (or node)
	// counts Caddy's access log on, for traffic statistics (ADR 0033).
	// Empty turns them off.
	TrafficListen string `toml:"traffic_listen"`
	// UpstreamsDir holds a file per service with its container's address,
	// which Caddy reads per request, so a traffic switch rewrites a file
	// instead of reloading Caddy (ADR 0091). install.sh makes it, owned
	// by dock and readable by Caddy's group. Missing, or empty here, puts
	// upstreams in Caddy's config, and every switch reloads it.
	UpstreamsDir string `toml:"upstreams_dir"`
}

// BuildKit configures the capped build container.
type BuildKit struct {
	// Enabled marks this server as a builder.
	Enabled bool `toml:"enabled"`
	// Image is the BuildKit image to run.
	Image string `toml:"image"`
	// MemoryMB and CPUs cap the BuildKit container. CPUs 0 lets the
	// builder pick: half the server's cores, at least two.
	MemoryMB int     `toml:"memory_mb"`
	CPUs     float64 `toml:"cpus"`
	// TimeoutMinutes fails a build that runs longer, on any builder.
	// Read by the control plane.
	TimeoutMinutes int `toml:"timeout_minutes"`
}

// Network configures app networks and what app containers may reach.
// `dock admin firewall apply` (run as root by dock-firewall.service) turns
// it into firewall rules.
type Network struct {
	// Pool is the IPv4 range dock-managed networks get their subnets from,
	// one /24 per environment. Keep it clear of the LAN and the tailnet.
	Pool netip.Prefix `toml:"pool"`
	// AllowPrivate lists private CIDRs app containers and builds may reach
	// (a NAS, a LAN database) and servers may pull images from. All other
	// private, link-local and tailnet addresses, and the server itself,
	// are blocked.
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
	// StorageDir holds the registry's blobs and manifests on the host,
	// unless they are in a bucket (ADR 0034: a runtime setting, `dock
	// registry migrate`).
	StorageDir string `toml:"storage_dir"`
	// ConfigDir holds the registry container's config.yml and jwks.json
	// (mounted at /etc/distribution). The daemon rewrites them when the
	// registry's storage changes.
	ConfigDir string `toml:"config_dir"`
	// PublicName names the registry: the audience of its tokens. Required
	// when Enabled.
	PublicName string `toml:"public_name"`
	// GatewayListen is the loopback address of the storage gateway the
	// registry reaches a bucket through (ADR 0084).
	GatewayListen string `toml:"gateway_listen"`
	// SpoolDir is where pushes land before they upload to a bucket; ""
	// is registry-spool in DataDir (which dock.service may write).
	SpoolDir string `toml:"spool_dir"`
	// SpoolMaxGB caps the spool; 0 is the smaller of 20 GB and a quarter
	// of its disk. Past it, or with less than 10% of the disk free,
	// pushes are refused.
	SpoolMaxGB int `toml:"spool_max_gb"`
}

// RegistrySpoolDir is the storage gateway's spool (ADR 0084).
func (c Config) RegistrySpoolDir() string {
	if c.Registry.SpoolDir != "" {
		return c.Registry.SpoolDir
	}
	return filepath.Join(c.DataDir, "registry-spool")
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
	// for hostnames served through the tunnel. Tunnel mode only; empty
	// when the control plane manages the tunnel (ADR 0036).
	TunnelHostname string `toml:"tunnel_hostname"`
	// CloudflaredImage is the cloudflared the server runs when the control
	// plane manages its tunnel (ADR 0036). Pin it by digest.
	CloudflaredImage string `toml:"cloudflared_image"`
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
	// for the "Details" link of commit statuses (shown to everyone who can
	// see the repository) and the link in Slack alerts. Empty: Dock's
	// public address, if it has one.
	UIURL string `toml:"ui_url"`
}

// Default returns the configuration used when no file overrides it.
func Default() Config {
	return Config{
		DataDir:        "/var/lib/dock",
		Listen:         "127.0.0.1:7070",
		InternalListen: "127.0.0.1:7071",
		PublicListen:   "127.0.0.1:7073",
		MaxUploadMB:    512,
		HostPorts:      PortRange{Min: 20000, Max: 29999},
		Caddy: Caddy{
			AdminSocket:   "/run/dock-caddy/admin.sock",
			HTTPPort:      80,
			HTTPSPort:     443,
			TrafficListen: "127.0.0.1:7074",
			TunnelPort:    7075,
			UpstreamsDir:  "/var/lib/dock-upstreams",
		},
		Analytics: Analytics{
			IngestListen:  "127.0.0.1:7077",
			SpoolMaxMB:    256,
			StorePort:     7076,
			StoreMemoryMB: 512,
			StatsListen:   "127.0.0.1:7078",
		},
		Traces: Traces{
			StorePort: 7080, StoreMemoryMB: 512, RetentionDays: 7, IngestListen: "127.0.0.1:4318", SpoolMaxMB: 128, MetricsSpoolMaxMB: 64,
			ServiceDaySpans: 2_000_000, OrgDaySpans: 5_000_000, ErrorDaySpans: 200_000, MaxGB: 4, MetricSeries: 2000, MetricNames: 200,
		},
		Status: Status{Listen: "127.0.0.1:7079"},
		BuildKit: BuildKit{
			Enabled:        true,
			Image:          "moby/buildkit:v0.34.1@sha256:99368f3e46177c6405b9740e4461d91cee004128daeecca78d4ceac50ec1da5c",
			MemoryMB:       2048,
			TimeoutMinutes: 30,
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
			Listen:        "127.0.0.1:5000",
			StorageDir:    "/var/lib/dock-registry",
			ConfigDir:     "/etc/dock/registry",
			GatewayListen: "127.0.0.1:5001",
		},
		Ingress: Ingress{
			Mode:             IngressDirect,
			CloudflaredImage: "cloudflare/cloudflared:2026.10.3@sha256:faa8e0a1475f137c910be6025f43c8e4092fdd35461b31e19fd1390657a1ff39",
		},
		Hooks: Hooks{Listen: "127.0.0.1:7072"},
		Backup: Backup{
			ResticImage: "restic/restic:0.18.1@sha256:39d9072fb5651c80d75c7a811612eb60b4c06b32ffe87c2e9f3c7222e1797e76",
		},
		Logs: Logs{KeepMBPerService: 64, KeepMBPerServer: 2048, KeepDays: 30},
		Scan: Scan{
			SyftImage:    "anchore/syft:v1.57.1@sha256:03215d0e98b661d0ecbc8575dbc14d65be1743b5de5df6410aecb7e80f95b060",
			GrypeImage:   "anchore/grype:v0.121.3@sha256:13113eeceab017260370cf540e1d25442c59665fd07d1b09b66d63bb0c282887",
			MemoryMB:     1024,
			QuietSeconds: 60,
		},
		Retention: Retention{
			BuildLogDays:     30,
			AuditDays:        365,
			AlertDays:        90,
			PruneDiskPercent: 80,
			BuildCacheKeepMB: 2048,
		},
		Updates: Updates{
			Source:    DefaultUpdateSource,
			TokenFile: "/etc/dock/github-token",
		},
		Alerts: Alerts{
			DiskWarningPercent:    85,
			DiskCriticalPercent:   95,
			ServerOfflineMinutes:  5,
			CrashLoopRestarts:     3,
			CrashLoopMinutes:      10,
			MemoryWarningPercent:  90,
			MemoryCriticalPercent: 97,
			MemoryMinutes:         5,
			CPUPercent:            90,
			CPUMinutes:            15,
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
	if err := CheckHostPort("[node] listen", c.Node.Listen); err != nil {
		return err
	}
	if err := CheckHostPort("[node] control_plane", c.Node.ControlPlane); err != nil {
		return err
	}
	if c.Registry.Enabled {
		if c.Registry.Listen == "" || c.Registry.StorageDir == "" || c.Registry.ConfigDir == "" || c.Registry.PublicName == "" {
			return errors.New("[registry] enabled needs listen, storage_dir, config_dir and public_name (the registry's name in its tokens)")
		}
		if err := CheckHostPort("[registry] listen", c.Registry.Listen); err != nil {
			return err
		}
		if err := CheckHostPort("[registry] gateway_listen", c.Registry.GatewayListen); err != nil {
			return err
		}
		if c.Registry.GatewayListen == c.Registry.Listen {
			return errors.New("[registry] gateway_listen must differ from listen")
		}
		if c.Registry.SpoolMaxGB < 0 {
			return errors.New("[registry] spool_max_gb can't be negative (0 picks the default)")
		}
	}
	if c.BuildKit.CPUs < 0 {
		return errors.New("[buildkit] cpus can't be negative (0 is half the server's cores, at least two)")
	}
	if err := c.Hooks.validate(); err != nil {
		return err
	}
	if err := c.checkTunnelPort(); err != nil {
		return err
	}
	if t := c.Caddy.TrafficListen; t != "" {
		host, _, err := net.SplitHostPort(t)
		if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("[caddy] traffic_listen %q must be a loopback IP and port, like 127.0.0.1:7074 (or empty to turn traffic statistics off)", t)
		}
	}
	if a := c.Analytics.IngestListen; a != "" {
		host, _, err := net.SplitHostPort(a)
		if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("[analytics] ingest_listen %q must be a loopback IP and port, like 127.0.0.1:7077 (or empty to turn the ingest off)", a)
		}
	}
	if a := c.Analytics.StatsListen; a != "" {
		host, _, err := net.SplitHostPort(a)
		if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("[analytics] stats_listen %q must be a loopback IP and port, like 127.0.0.1:7078 (or empty to turn public dashboards off)", a)
		}
	}
	if a := c.Status.Listen; a != "" {
		host, _, err := net.SplitHostPort(a)
		if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("[status] listen %q must be a loopback IP and port, like 127.0.0.1:7079 (or empty to turn status pages off)", a)
		}
	}
	switch {
	case c.Analytics.SpoolMaxMB < 16:
		return fmt.Errorf("[analytics] spool_max_mb is %d; it must be at least 16", c.Analytics.SpoolMaxMB)
	case c.Analytics.StorePort <= 0 || c.Analytics.StorePort > 65535:
		return fmt.Errorf("[analytics] store_port %d is not a port", c.Analytics.StorePort)
	case c.Analytics.StoreMemoryMB < 256:
		return fmt.Errorf("[analytics] store_memory_mb is %d; the store needs at least 256", c.Analytics.StoreMemoryMB)
	case c.Traces.StorePort <= 0 || c.Traces.StorePort > 65535 || c.Traces.StorePort == c.Analytics.StorePort:
		return fmt.Errorf("[traces] store_port %d must be a port other than [analytics] store_port", c.Traces.StorePort)
	case c.Traces.StoreMemoryMB < 256:
		return fmt.Errorf("[traces] store_memory_mb is %d; the trace store needs at least 256", c.Traces.StoreMemoryMB)
	case c.Traces.RetentionDays < 1 || c.Traces.RetentionDays > 30:
		return fmt.Errorf("[traces] retention_days is %d; keep spans for 1 to 30 days", c.Traces.RetentionDays)
	case c.Traces.SpoolMaxMB < 16:
		return fmt.Errorf("[traces] spool_max_mb is %d; it must be at least 16", c.Traces.SpoolMaxMB)
	case c.Traces.MetricsSpoolMaxMB < 16:
		return fmt.Errorf("[traces] metrics_spool_max_mb is %d; it must be at least 16", c.Traces.MetricsSpoolMaxMB)
	case c.Traces.MetricSeries < 1 || c.Traces.MetricNames < 1:
		return fmt.Errorf("[traces] metric_series and metric_names must be at least 1")
	case c.Analytics.SiteDayEvents < 0 || c.Analytics.SiteDayReplayMB < 0 || c.Analytics.MaxGB < 0:
		return errors.New("[analytics] site_day_events, site_day_replay_mb and max_gb can't be negative (0 picks the store's default)")
	case c.Traces.ServiceDaySpans < 1 || c.Traces.OrgDaySpans < 1 || c.Traces.ErrorDaySpans < 1 || c.Traces.MaxGB < 1:
		return errors.New("[traces] service_day_spans, org_day_spans, error_day_spans and max_gb must be at least 1")
	}
	if a := c.Traces.IngestListen; a != "" {
		host, _, err := net.SplitHostPort(a)
		if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("[traces] ingest_listen %q must be a loopback IP and port, like 127.0.0.1:4318 (or empty to turn the trace ingest off)", a)
		}
	}
	if err := c.Retention.validate(); err != nil {
		return err
	}
	if c.Logs.KeepMBPerService < 1 || c.Logs.KeepMBPerServer < 1 || c.Logs.KeepDays < 1 {
		return errors.New("[logs] keep_mb_per_service, keep_mb_per_server and keep_days must be at least 1")
	}
	if err := c.Alerts.validate(); err != nil {
		return err
	}
	if err := c.Updates.validate(); err != nil {
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
	if err := CheckHostPort("[hooks] listen", h.Listen); err != nil {
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

// CloudflaredPort is the port on 127.0.0.1 this server's cloudflared
// forwards to (ADR 0036): http_port in tunnel mode, tunnel_port (0: none)
// on a direct server.
func (c Config) CloudflaredPort() int {
	if c.Ingress.Mode == IngressCloudflareTunnel {
		return c.Caddy.HTTPPort
	}
	return c.Caddy.TunnelPort
}

// checkTunnelPort refuses a tunnel_port that another of this server's
// listeners uses: Caddy couldn't load its config with it.
func (c Config) checkTunnelPort() error {
	p := c.Caddy.TunnelPort
	if p == 0 {
		return nil
	}
	if p < 0 || p > 65535 {
		return fmt.Errorf("[caddy] tunnel_port %d is not a port (0 turns the tunnel listener off)", p)
	}
	taken := []int{c.Caddy.HTTPPort, c.Caddy.HTTPSPort}
	for _, addr := range []string{c.Listen, c.InternalListen, c.PublicListen, c.Hooks.Listen, c.Caddy.TrafficListen, c.Registry.Listen, c.Registry.GatewayListen, c.Analytics.IngestListen, c.Analytics.StatsListen, c.Status.Listen} {
		if _, port, err := net.SplitHostPort(addr); err == nil {
			if n, err := strconv.Atoi(port); err == nil {
				taken = append(taken, n)
			}
		}
	}
	if slices.Contains(taken, p) {
		return fmt.Errorf("[caddy] tunnel_port %d is used by another of this server's listeners; pick a free one (or 0 to turn the tunnel listener off)", p)
	}
	return nil
}

// tunnelHostnameRe matches a Cloudflare tunnel's CNAME target.
var tunnelHostnameRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.cfargotunnel\.com$`)

// ValidTunnelHostname reports whether h is <tunnel-id>.cfargotunnel.com,
// what a server behind a Cloudflare tunnel reports and DNS records point
// at.
func ValidTunnelHostname(h string) bool { return tunnelHostnameRe.MatchString(h) }

// hostnameRe matches lower-case DNS names with at least two labels.
var hostnameRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// CheckHostPort accepts "" (unset) or "host:port" with a port in 1-65535.
func CheckHostPort(field, addr string) error {
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

// githubSourceRe matches "github.com/OWNER/REPO".
var githubSourceRe = regexp.MustCompile(`^github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func (u Updates) validate() error {
	if !githubSourceRe.MatchString(u.Source) && !filepath.IsAbs(u.Source) {
		return fmt.Errorf("[updates] source %q must be github.com/OWNER/REPO or an absolute path to a directory of releases", u.Source)
	}
	for _, k := range u.TrustedKeys {
		if b, err := base64.StdEncoding.DecodeString(k); err != nil || len(b) != ed25519.PublicKeySize {
			return fmt.Errorf("[updates] trusted_keys: %q isn't a base64 Ed25519 public key (32 bytes)", k)
		}
	}
	return nil
}

func (r Retention) validate() error {
	switch {
	case r.BuildLogDays < 1 || r.AuditDays < 1 || r.AlertDays < 1:
		return errors.New("[retention] build_log_days, audit_days and alert_days must be at least 1")
	case r.AuditMaxEvents < 0 || r.BuildCacheKeepMB < 0:
		return errors.New("[retention] audit_max_events and build_cache_keep_mb can't be negative")
	case r.PruneDiskPercent < 0 || r.PruneDiskPercent > 100:
		return errors.New("[retention] prune_disk_percent must be 0 (off) to 100")
	}
	return nil
}

func (a Alerts) validate() error {
	switch {
	case a.DiskWarningPercent < 1 || a.DiskCriticalPercent > 100 || a.DiskWarningPercent > a.DiskCriticalPercent:
		return errors.New("[alerts] needs 1 <= disk_warning_percent <= disk_critical_percent <= 100")
	case a.ServerOfflineMinutes < 1 || a.CrashLoopMinutes < 1 || a.CrashLoopRestarts < 1:
		return errors.New("[alerts] server_offline_minutes, crash_loop_restarts and crash_loop_minutes must be at least 1")
	case a.MemoryWarningPercent < 1 || a.MemoryCriticalPercent > 100 || a.MemoryWarningPercent > a.MemoryCriticalPercent:
		return errors.New("[alerts] needs 1 <= memory_warning_percent <= memory_critical_percent <= 100")
	case a.CPUPercent < 0 || a.CPUPercent > 100:
		return errors.New("[alerts] cpu_percent must be 0 (off) to 100")
	case a.MemoryMinutes < 1 || a.CPUMinutes < 1:
		return errors.New("[alerts] memory_minutes and cpu_minutes must be at least 1")
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
