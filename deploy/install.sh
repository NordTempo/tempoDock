#!/usr/bin/env bash
# Installs platform on a Linux server: the platform binary, Caddy, systemd
# units, a default config, and the first admin token.
#
#   sudo PLATFORM_BINARY=./bin/platform ./deploy/install.sh
#
# Re-running is safe: existing config and data are kept, binaries and units
# are replaced (that is also how you upgrade).
#
# Environment:
#   PLATFORM_BINARY   path to a built platform binary (required for now)
#   CADDY_VERSION     Caddy release to install (default below)
#   PUBLIC_IP         address users point DNS at (default: auto-detect)
#   LISTEN            API/UI address (default: Tailscale IP:7070 if present, else 127.0.0.1:7070)
#   BASE_DOMAIN       base for generated hostnames, needs wildcard DNS (optional)
set -euo pipefail

CADDY_VERSION="${CADDY_VERSION:-2.11.7}"
BUILDKIT_IMAGE="moby/buildkit:v0.34.1"
SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

[[ $EUID -eq 0 ]] || die "run as root (sudo)"
[[ "$(uname -s)" == Linux ]] || die "Linux only"
case "$(uname -m)" in
  x86_64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "unsupported architecture $(uname -m)" ;;
esac

command -v docker >/dev/null || die "Docker is not installed. Install Docker Engine first: https://docs.docker.com/engine/install/"
docker info >/dev/null 2>&1 || die "Docker is installed but not running: systemctl enable --now docker"
command -v systemctl >/dev/null || die "systemd is required"

[[ -n "${PLATFORM_BINARY:-}" ]] || die "set PLATFORM_BINARY to a built platform binary (make build)"
[[ -x "$PLATFORM_BINARY" ]] || die "$PLATFORM_BINARY is not an executable file"

say "==> user"
id platform >/dev/null 2>&1 || useradd --system --home-dir /var/lib/platform --shell /usr/sbin/nologin platform
usermod -aG docker platform

say "==> platform binary"
install -m 0755 "$PLATFORM_BINARY" /usr/local/bin/platform.new
mv /usr/local/bin/platform.new /usr/local/bin/platform

say "==> caddy $CADDY_VERSION"
if ! /usr/local/bin/caddy version 2>/dev/null | grep -q "v$CADDY_VERSION"; then
  tmp="$(mktemp -d)"
  curl -fsSL "https://github.com/caddyserver/caddy/releases/download/v${CADDY_VERSION}/caddy_${CADDY_VERSION}_linux_${ARCH}.tar.gz" | tar -xz -C "$tmp" caddy
  install -m 0755 "$tmp/caddy" /usr/local/bin/caddy.new
  mv /usr/local/bin/caddy.new /usr/local/bin/caddy
  rm -rf "$tmp"
fi

say "==> config"
install -d -m 0750 -o root -g platform /etc/platform
if [[ ! -f /etc/platform/config.toml ]]; then
  public_ip="${PUBLIC_IP:-$(curl -fsS4 --max-time 5 https://api.ipify.org || true)}"
  listen="${LISTEN:-}"
  if [[ -z "$listen" ]]; then
    ts_ip="$(tailscale ip -4 2>/dev/null | head -1 || true)"
    listen="${ts_ip:-127.0.0.1}:7070"
  fi
  cat > /etc/platform/config.toml <<CONF
# platform daemon config. Every field has a default; see internal/config.
data_dir = "/var/lib/platform"
listen = "$listen"
internal_listen = "127.0.0.1:7071"
public_ip = "$public_ip"
base_domain = "${BASE_DOMAIN:-}"

[caddy]
admin_socket = "/run/platform-caddy/admin.sock"
http_port = 80
https_port = 443

[buildkit]
enabled = true
image = "$BUILDKIT_IMAGE"
memory_mb = 2048
cpus = 1
CONF
  chmod 0640 /etc/platform/config.toml
  chgrp platform /etc/platform/config.toml
fi
# The initial Caddy config only puts the admin API on the Unix socket and
# serves the 404 page. The daemon loads the real config on start.
/usr/local/bin/platform admin caddy-config --config /etc/platform/config.toml > /etc/platform/caddy.json
chmod 0644 /etc/platform/caddy.json

say "==> buildkit image"
docker pull -q "$BUILDKIT_IMAGE" >/dev/null

say "==> systemd"
install -m 0644 "$SRC_DIR/systemd/platform.service" /etc/systemd/system/platform.service
install -m 0644 "$SRC_DIR/systemd/platform-caddy.service" /etc/systemd/system/platform-caddy.service
systemctl daemon-reload
systemctl enable --now platform-caddy.service
systemctl enable platform.service
systemctl restart platform.service

say "==> admin token"
token="$(runuser -u platform -- /usr/local/bin/platform admin bootstrap --config /etc/platform/config.toml || true)"

listen_addr="$(grep -E '^listen' /etc/platform/config.toml | cut -d'"' -f2)"
say ""
say "platform is installed."
say "  API and UI:  http://$listen_addr"
if [[ -n "$token" ]]; then
  say "  admin token: $token"
  say "  (shown once; store it now)"
  say ""
  say "  On your laptop:  platform login http://$listen_addr --token <token>"
else
  say "  An admin already exists; no new token was created."
fi
say ""
say "Open ports 80 and 443 to the internet. Keep the API port private (Tailscale)."
