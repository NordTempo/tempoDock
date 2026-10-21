#!/usr/bin/env bash
# Installs Tempo Dock on a Linux server: the dock binary, Caddy, systemd
# units, a default config, and the first admin token.
#
#   sudo DOCK_BINARY=./bin/dock ./deploy/install.sh
#
# Re-running is safe: existing config and data are kept, binaries and units
# are replaced (that is also how you upgrade).
#
# Environment:
#   DOCK_BINARY   path to a built dock binary (required for now)
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

[[ -n "${DOCK_BINARY:-}" ]] || die "set DOCK_BINARY to a built dock binary (make build)"
[[ -x "$DOCK_BINARY" ]] || die "$DOCK_BINARY is not an executable file"

say "==> user"
id dock >/dev/null 2>&1 || useradd --system --home-dir /var/lib/dock --shell /usr/sbin/nologin dock
usermod -aG docker dock

say "==> dock binary"
install -m 0755 "$DOCK_BINARY" /usr/local/bin/dock.new
mv /usr/local/bin/dock.new /usr/local/bin/dock

say "==> caddy $CADDY_VERSION"
if ! /usr/local/bin/caddy version 2>/dev/null | grep -q "v$CADDY_VERSION"; then
  tmp="$(mktemp -d)"
  curl -fsSL "https://github.com/caddyserver/caddy/releases/download/v${CADDY_VERSION}/caddy_${CADDY_VERSION}_linux_${ARCH}.tar.gz" | tar -xz -C "$tmp" caddy
  install -m 0755 "$tmp/caddy" /usr/local/bin/caddy.new
  mv /usr/local/bin/caddy.new /usr/local/bin/caddy
  rm -rf "$tmp"
fi

say "==> config"
install -d -m 0750 -o root -g dock /etc/dock
if [[ ! -f /etc/dock/config.toml ]]; then
  public_ip="${PUBLIC_IP:-$(curl -fsS4 --max-time 5 https://api.ipify.org || true)}"
  listen="${LISTEN:-}"
  if [[ -z "$listen" ]]; then
    ts_ip="$(tailscale ip -4 2>/dev/null | head -1 || true)"
    listen="${ts_ip:-127.0.0.1}:7070"
  fi
  cat > /etc/dock/config.toml <<CONF
# Tempo Dock daemon config. Every field has a default; see internal/config.
data_dir = "/var/lib/dock"
listen = "$listen"
internal_listen = "127.0.0.1:7071"
public_ip = "$public_ip"
base_domain = "${BASE_DOMAIN:-}"

[caddy]
admin_socket = "/run/dock-caddy/admin.sock"
http_port = 80
https_port = 443

[buildkit]
enabled = true
image = "$BUILDKIT_IMAGE"
memory_mb = 2048
cpus = 1
CONF
  chmod 0640 /etc/dock/config.toml
  chgrp dock /etc/dock/config.toml
fi
# The initial Caddy config only puts the admin API on the Unix socket and
# serves the 404 page. The daemon loads the real config on start.
/usr/local/bin/dock admin caddy-config --config /etc/dock/config.toml > /etc/dock/caddy.json
chmod 0644 /etc/dock/caddy.json

say "==> kernel"
# Hand queued connections to the new listener when Caddy reloads, instead of
# resetting them.
echo 'net.ipv4.tcp_migrate_req = 1' > /etc/sysctl.d/90-dock.conf
sysctl -q -p /etc/sysctl.d/90-dock.conf

say "==> buildkit image"
docker pull -q "$BUILDKIT_IMAGE" >/dev/null

say "==> systemd"
install -m 0644 "$SRC_DIR/systemd/dock.service" /etc/systemd/system/dock.service
install -m 0644 "$SRC_DIR/systemd/dock-caddy.service" /etc/systemd/system/dock-caddy.service
systemctl daemon-reload
systemctl enable --now dock-caddy.service
systemctl enable dock.service
systemctl restart dock.service

say "==> admin token"
token="$(runuser -u dock -- /usr/local/bin/dock admin bootstrap --config /etc/dock/config.toml || true)"

listen_addr="$(grep -E '^listen' /etc/dock/config.toml | cut -d'"' -f2)"
say ""
say "Tempo Dock is installed."
say "  API and UI:  http://$listen_addr"
if [[ -n "$token" ]]; then
  say "  admin token: $token"
  say "  (shown once; store it now)"
  say ""
  say "  On your laptop:  dock login http://$listen_addr --token <token>"
else
  say "  An admin already exists; no new token was created."
fi
say ""
say "Open 80/tcp and 443/tcp+udp (HTTP/3) to the internet. Keep the API port private (Tailscale)."
