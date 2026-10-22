#!/usr/bin/env bash
# Installs Tempo Dock on a Linux server: the dock binary, Caddy, systemd
# units, the app firewall and a default config. A server is either the
# control plane (API, UI, SQLite; the default) or a node that joins one
# (--node, ADR 0014).
#
#   sudo DOCK_BINARY=./dock-linux-amd64 ./deploy/install.sh
#   sudo DOCK_BINARY=./dock-linux-amd64 NODE_JOIN_TOKEN=dockjoin1_... ./deploy/install.sh --node
#   sudo --preserve-env=GITHUB_TOKEN ./deploy/install.sh   # downloads the release's binary
#
# Re-running is safe: data is kept, binaries and units are replaced (that is
# also how you upgrade; a node is upgraded as a node). config.toml is written
# once; later runs only change the keys the options below ask for.
#
# Options:
#   --node        install as a node: `dock node` instead of `dock serve`
#
# Environment:
#   DOCK_BINARY   path to a dock binary (a release's dock-linux-<arch>, or make build)
#   DOCK_VERSION  without DOCK_BINARY: the release to download, checked against its
#                 SHA256SUMS (default: this release tarball's version, else the latest)
#   GITHUB_TOKEN  for that download: a token that can read the repository's releases
#   CADDY_VERSION     Caddy release to install (default below)
#   PUBLIC_IP         address users point DNS at (default: auto-detect; empty on tunnel servers)
#   BASE_DOMAIN       base for generated hostnames, needs wildcard DNS (optional)
#   DISABLE_TLS       "true" serves plain HTTP only: private LAN/Tailscale setups
#   CLOUDFLARE_TUNNEL_TOKEN  run a Cloudflare Tunnel (ADR 0013): installs cloudflared
#                     and dock-cloudflared.service, sets [ingress] mode = "cloudflare_tunnel"
#                     (Caddy serves plain HTTP on 127.0.0.1:<http_port> only; apps are
#                     https:// at Cloudflare's edge, so DISABLE_TLS doesn't apply)
#   CLOUDFLARED_VERSION, CLOUDFLARED_SHA256  another cloudflared release and its checksum
# Control plane only:
#   LISTEN            API/UI address (default: Tailscale IP:7070 if present, else 127.0.0.1:7070)
#   NODE_LISTEN       accept nodes on this address, e.g. "$(tailscale ip -4):7443" → [node] listen
#   REGISTRY          "1" runs the image registry (dock-registry container) → [registry] enabled
#   REGISTRY_NAME     the registry's name in its tokens (default dock-registry) → [registry] public_name
#   REGISTRY_LISTEN   host address the registry is published on (default 127.0.0.1:5000)
# Node only:
#   NODE_JOIN_TOKEN   from `dock server add` on the control plane; implies --node.
#                     Needed for the first run only (and to re-join)
set -euo pipefail

CADDY_VERSION="${CADDY_VERSION:-2.11.7}"
CLOUDFLARED_VERSION="${CLOUDFLARED_VERSION:-2026.10.3}"
# Pinned by digest; the tag is for humans.
BUILDKIT_IMAGE="moby/buildkit:v0.34.1@sha256:99368f3e46177c6405b9740e4461d91cee004128daeecca78d4ceac50ec1da5c"
REGISTRY_IMAGE="registry:3.1.2@sha256:be9a7062dd7e3316e5c0368e42740dc991a9ceb5d382317675a8738db0112af0"
SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Releases (.github/workflows/release.yml) are downloaded from here.
RELEASE_REPO=NordTempo/tempoDock
CONF=/etc/dock/config.toml
# Written by `dock node join`; its presence means this node has joined.
NODE_STATE=/var/lib/dock/node.toml
TUNNEL_ENV=/etc/dock/cloudflared.env

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

# Temporary files, removed on exit.
staged=""
download_dir=""
cleanup() { rm -rf ${staged:+"$staged"} ${download_dir:+"$download_dir"}; }
trap cleanup EXIT

# github CURL-ARGS... runs curl against the GitHub API, with GITHUB_TOKEN if set.
# The token goes to curl as a config on stdin (printf is a builtin), never in
# its arguments, which any user could read in the process list.
github() {
  if [[ -n "${GITHUB_TOKEN:-}" ]]; then
    printf 'header = "Authorization: Bearer %s"\n' "$GITHUB_TOKEN" | curl -K - -fsSL "$@"
  else
    curl -fsSL "$@"
  fi
}

# download_dock VERSION DIR downloads release VERSION's (or the latest
# release's) dock-linux-$ARCH into DIR and checks it against SHA256SUMS.
# Through the API, because release download links don't take a token.
download_dock() {
  local version=$1 dir=$2 path name url
  path="releases/tags/$version"
  [[ "$version" != latest ]] || path=releases/latest
  github "https://api.github.com/repos/$RELEASE_REPO/$path" > "$dir/release.json" \
    || die "could not read release $version of $RELEASE_REPO; set GITHUB_TOKEN to a token that can read it (sudo --preserve-env=GITHUB_TOKEN), or download it yourself and set DOCK_BINARY"
  for name in "dock-linux-$ARCH" SHA256SUMS; do
    # An asset's "url" comes just before its "name" in GitHub's JSON.
    # shellcheck disable=SC2020 # one newline per character, on purpose
    url="$(tr ',{}[]' '\n\n\n\n\n' < "$dir/release.json" | awk -v name="$name" '
      /"url": *"[^"]*\/releases\/assets\/[0-9]+"/ { u = $0; sub(/^[^:]*: *"/, "", u); sub(/".*$/, "", u) }
      $0 ~ "\"name\": *\"" name "\"" && u != "" { print u; exit }')"
    [[ -n "$url" ]] || die "release $version has no $name"
    github -H "Accept: application/octet-stream" -o "$dir/$name" "$url" || die "could not download $name"
  done
  (cd "$dir" && grep -E "  dock-linux-$ARCH\$" SHA256SUMS | sha256sum --check --quiet --strict) \
    || die "dock-linux-$ARCH does not match the release's SHA256SUMS"
  chmod 0755 "$dir/dock-linux-$ARCH"
}

# toml_get FILE SECTION KEY prints KEY's value in [SECTION] ("" is the top
# level), quotes stripped. Enough for the flat keys this script writes.
toml_get() {
  awk -v section="$2" -v key="$3" '
    BEGIN { in_section = (section == "") }
    /^[[:space:]]*\[/ {
      hdr = $0; gsub(/^[[:space:]]*\[[[:space:]]*|[[:space:]]*\].*$/, "", hdr)
      in_section = (hdr == section); next
    }
    in_section && $0 ~ "^[[:space:]]*" key "[[:space:]]*=" {
      v = $0; sub(/^[^=]*=[[:space:]]*/, "", v); sub(/[[:space:]]+#.*$/, "", v)
      gsub(/^"|"$/, "", v); print v; exit
    }' "$1"
}

# toml_set FILE SECTION KEY VALUE sets KEY = VALUE (a TOML literal, quoted by
# the caller) in [SECTION], adding the key or the section when missing.
# Comments and every other key stay as they are.
toml_set() {
  local out
  out="$(awk -v section="$2" -v key="$3" -v value="$4" '
    function emit() { print key " = " value; done = 1 }
    # Blank lines are held back so a new key goes above them, at the end
    # of its section rather than just before the next header.
    function flush() { printf "%s", blanks; blanks = "" }
    BEGIN { in_section = (section == "") }
    /^[[:space:]]*$/ { blanks = blanks $0 "\n"; next }
    /^[[:space:]]*\[/ {
      if (in_section && !done) emit()
      flush()
      hdr = $0; gsub(/^[[:space:]]*\[[[:space:]]*|[[:space:]]*\].*$/, "", hdr)
      in_section = (hdr == section)
    }
    in_section && !done && $0 ~ "^[[:space:]]*" key "[[:space:]]*=" { flush(); emit(); next }
    { flush(); print }
    END {
      if (!done) { if (!in_section) printf "\n[%s]\n", section; emit() }
      flush()
    }
  ' "$1")"
  printf '%s\n' "$out" > "$1"
}

# tunnel_id prints the tunnel ID inside a Cloudflare tunnel token: base64 of
# {"a":"<account>","t":"<tunnel id>","s":"<secret>"}.
tunnel_id() {
  local t=$1 json
  while (( ${#t} % 4 )); do t+="="; done
  json="$(printf '%s' "$t" | tr '_-' '/+' | base64 -d 2>/dev/null)" || return 1
  [[ $json =~ \"t\"[[:space:]]*:[[:space:]]*\"([0-9a-fA-F-]{36})\" ]] || return 1
  printf '%s\n' "${BASH_REMATCH[1]}"
}

node_mode=false
for arg in "$@"; do
  case "$arg" in
    --node) node_mode=true ;;
    -h|--help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
    *) die "unknown option $arg (see --help)" ;;
  esac
done
[[ -n "${NODE_JOIN_TOKEN:-}" ]] && node_mode=true

[[ $EUID -eq 0 ]] || die "run as root (sudo)"
[[ "$(uname -s)" == Linux ]] || die "Linux only"
case "$(uname -m)" in
  x86_64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "unsupported architecture $(uname -m)" ;;
esac

# A server is one or the other; re-runs keep what it is.
[[ -f /etc/systemd/system/dock-node.service ]] && node_mode=true
if [[ "$node_mode" == true && -f /etc/systemd/system/dock.service ]]; then
  die "this server runs the control plane (dock.service); a node must be a separate server"
fi
if [[ "$node_mode" == true ]]; then
  [[ -n "${NODE_JOIN_TOKEN:-}" || -f "$NODE_STATE" ]] \
    || die "set NODE_JOIN_TOKEN to a join token: run \`dock server add <name>\` against the control plane"
  for v in LISTEN NODE_LISTEN REGISTRY REGISTRY_NAME REGISTRY_LISTEN; do
    [[ -z "${!v:-}" ]] || die "$v is for the control plane; this is a node install"
  done
fi

tunnel=false
tunnel_hostname=""
if [[ -n "${CLOUDFLARE_TUNNEL_TOKEN:-}" ]]; then
  tunnel=true
  tid="$(tunnel_id "$CLOUDFLARE_TUNNEL_TOKEN")" \
    || die "CLOUDFLARE_TUNNEL_TOKEN is not a tunnel token; copy it from the tunnel's install command in the Cloudflare dashboard"
  tunnel_hostname="$tid.cfargotunnel.com"
elif [[ -f "$TUNNEL_ENV" ]]; then
  tunnel=true
fi

case "$CLOUDFLARED_VERSION/$ARCH" in
  2026.10.3/amd64) cloudflared_sha256=053b80b0a23031a038fdbf3c073f59f82b8f1f152aa67971d7383ffafc0dbab0 ;;
  2026.10.3/arm64) cloudflared_sha256=89297ebfb9e8c99c0dc013a1140601453cf3798d8306c5591651f4a3f5210be8 ;;
  *) cloudflared_sha256="${CLOUDFLARED_SHA256:-}" ;;
esac
if [[ "$tunnel" == true && -z "$cloudflared_sha256" ]]; then
  die "set CLOUDFLARED_SHA256 to the SHA-256 of cloudflared-linux-$ARCH from the $CLOUDFLARED_VERSION release notes"
fi

command -v docker >/dev/null || die "Docker is not installed. Install Docker Engine first: https://docs.docker.com/engine/install/"
docker info >/dev/null 2>&1 || die "Docker is installed but not running: systemctl enable --now docker"
command -v systemctl >/dev/null || die "systemd is required"
command -v iptables >/dev/null || die "iptables is required for the app firewall: apt install iptables"

if [[ -z "${DOCK_BINARY:-}" ]]; then
  dock_version="${DOCK_VERSION:-$(cat "$SRC_DIR/VERSION" 2>/dev/null || echo latest)}"
  say "==> download dock $dock_version"
  download_dir="$(mktemp -d)"
  download_dock "$dock_version" "$download_dir"
  DOCK_BINARY="$download_dir/dock-linux-$ARCH"
fi
[[ -x "$DOCK_BINARY" ]] || die "$DOCK_BINARY is not an executable file"

docker_version="$(docker version --format '{{.Server.Version}}' 2>/dev/null || true)"
if [[ "${docker_version%%.*}" =~ ^[0-9]+$ ]] && (( ${docker_version%%.*} < 28 )); then
  say "warning: Docker $docker_version is older than 28. Before 28, ports published on"
  say "         127.0.0.1 are reachable from the LAN. Upgrade Docker."
fi

say "==> users"
# dock runs the daemon; its unit adds the docker group to that process
# only. dock-caddy runs Caddy, with no Docker access at all.
id dock >/dev/null 2>&1 || useradd --system --home-dir /var/lib/dock --shell /usr/sbin/nologin dock
id dock-caddy >/dev/null 2>&1 || useradd --system --home-dir /var/lib/dock-caddy --shell /usr/sbin/nologin dock-caddy
# Older installs put dock in the docker group and ran Caddy as dock.
if id -nG dock | tr ' ' '\n' | grep -qx docker; then
  gpasswd -d dock docker >/dev/null
fi
if [[ -d /var/lib/dock-caddy ]]; then
  chown -R dock-caddy:dock-caddy /var/lib/dock-caddy
fi

say "==> dock binary"
install -m 0755 "$DOCK_BINARY" /usr/local/bin/dock.new
mv /usr/local/bin/dock.new /usr/local/bin/dock

say "==> caddy $CADDY_VERSION"
if ! /usr/local/bin/caddy version 2>/dev/null | grep -q "v$CADDY_VERSION"; then
  tmp="$(mktemp -d)"
  base="https://github.com/caddyserver/caddy/releases/download/v${CADDY_VERSION}"
  tarball="caddy_${CADDY_VERSION}_linux_${ARCH}.tar.gz"
  curl -fsSLo "$tmp/$tarball" "$base/$tarball"
  curl -fsSLo "$tmp/checksums.txt" "$base/caddy_${CADDY_VERSION}_checksums.txt"
  (cd "$tmp" && grep -E "  ${tarball}\$" checksums.txt | sha512sum --check --quiet --strict) \
    || die "$tarball does not match Caddy's published checksum"
  tar -xzf "$tmp/$tarball" -C "$tmp" caddy
  install -m 0755 "$tmp/caddy" /usr/local/bin/caddy.new
  mv /usr/local/bin/caddy.new /usr/local/bin/caddy
  rm -rf "$tmp"
fi

tunnel_changed=false
if [[ "$tunnel" == true ]]; then
  say "==> cloudflared $CLOUDFLARED_VERSION"
  if ! /usr/local/bin/cloudflared --version 2>/dev/null | grep -q "version $CLOUDFLARED_VERSION "; then
    tmp="$(mktemp -d)"
    curl -fsSLo "$tmp/cloudflared" \
      "https://github.com/cloudflare/cloudflared/releases/download/${CLOUDFLARED_VERSION}/cloudflared-linux-${ARCH}"
    # Cloudflare publishes checksums only in the release notes, so the
    # default version's are pinned above.
    printf '%s  %s\n' "$cloudflared_sha256" "$tmp/cloudflared" | sha256sum --check --quiet --strict \
      || die "cloudflared-linux-$ARCH does not match the expected SHA-256"
    install -m 0755 "$tmp/cloudflared" /usr/local/bin/cloudflared.new
    mv /usr/local/bin/cloudflared.new /usr/local/bin/cloudflared
    rm -rf "$tmp"
    tunnel_changed=true
  fi
  if [[ -n "${CLOUDFLARE_TUNNEL_TOKEN:-}" ]]; then
    env_new="TUNNEL_TOKEN=$CLOUDFLARE_TUNNEL_TOKEN"
    if [[ ! -f "$TUNNEL_ENV" ]] || [[ "$(cat "$TUNNEL_ENV")" != "$env_new" ]]; then
      install -d -m 0751 -o root -g dock /etc/dock
      (umask 077 && printf '%s\n' "$env_new" > "$TUNNEL_ENV")
      tunnel_changed=true
    fi
  fi
fi

say "==> config"
# 0751: Caddy (dock-caddy) reads caddy.json in here; config.toml stays 0640.
install -d -m 0751 -o root -g dock /etc/dock
# Changes go to a staged copy first and only replace config.toml once the
# new binary accepts it.
staged="$(mktemp /etc/dock/.config.toml.XXXXXX)"
if [[ -f "$CONF" ]]; then
  cat "$CONF" > "$staged"
else
  public_ip=""
  if [[ "$tunnel" == false ]]; then
    public_ip="${PUBLIC_IP:-$(curl -fsS4 --max-time 5 https://api.ipify.org || true)}"
  fi
  if [[ "$node_mode" == true ]]; then
    cat > "$staged" <<CONF
# Tempo Dock node config (dock node). Every field has a default; see
# internal/config. The link to the control plane is in $NODE_STATE.
data_dir = "/var/lib/dock"
internal_listen = "127.0.0.1:7071"
public_ip = "$public_ip"
# Reported to the control plane when this server joins; after that,
# \`dock server update\` there decides.
base_domain = "${BASE_DOMAIN:-}"
CONF
  else
    listen="${LISTEN:-}"
    if [[ -z "$listen" ]]; then
      ts_ip="$(tailscale ip -4 2>/dev/null | head -1 || true)"
      listen="${ts_ip:-127.0.0.1}:7070"
    fi
    cat > "$staged" <<CONF
# Tempo Dock daemon config. Every field has a default; see internal/config.
data_dir = "/var/lib/dock"
listen = "$listen"
internal_listen = "127.0.0.1:7071"
public_ip = "$public_ip"
base_domain = "${BASE_DOMAIN:-}"
CONF
  fi
  cat >> "$staged" <<CONF

[caddy]
admin_socket = "/run/dock-caddy/admin.sock"
http_port = 80
https_port = 443
disable_tls = ${DISABLE_TLS:-false}

[buildkit]
enabled = true
image = "$BUILDKIT_IMAGE"
memory_mb = 2048
cpus = 1

[network]
# App networks get a /24 each from this range.
pool = "10.213.0.0/16"
# Apps and builds can't reach private addresses (LAN, tailnet) or this
# server, except these CIDRs. Then: sudo systemctl restart dock-firewall
allow_private = []

[ingress]
# direct: public IP, Caddy issues certificates. cloudflare_tunnel: public
# traffic arrives through cloudflared (ADR 0013).
mode = "direct"
tunnel_hostname = ""
CONF
  if [[ "$node_mode" == false ]]; then
    cat >> "$staged" <<CONF

[node]
# Where other servers connect when they join as nodes, e.g. the Tailscale
# IP and port 7443. Empty: this server runs everything alone.
listen = ""

[registry]
# Image registry for builds and deploys across servers (dock-registry).
enabled = false
listen = "127.0.0.1:5000"
storage_dir = "/var/lib/dock-registry"
# The registry's name: the audience of its tokens.
public_name = ""
CONF
  fi
fi

if [[ "$tunnel" == true ]]; then
  toml_set "$staged" ingress mode '"cloudflare_tunnel"'
  [[ -z "$tunnel_hostname" ]] || toml_set "$staged" ingress tunnel_hostname "\"$tunnel_hostname\""
  # Caddy is plain HTTP on 127.0.0.1 in this mode regardless; this keeps
  # app URLs https://, as Cloudflare's edge serves them.
  toml_set "$staged" caddy disable_tls false
  # The home IP never goes into DNS or the control plane.
  toml_set "$staged" "" public_ip '""'
fi
[[ -z "${NODE_LISTEN:-}" ]] || toml_set "$staged" node listen "\"$NODE_LISTEN\""
if [[ "${REGISTRY:-}" == 1 ]]; then
  toml_set "$staged" registry enabled true
  # The name only has to be set; this one is as good as any.
  if [[ -z "${REGISTRY_NAME:-}" && -z "$(toml_get "$staged" registry public_name)" ]]; then
    REGISTRY_NAME=dock-registry
  fi
fi
[[ -z "${REGISTRY_NAME:-}" ]] || toml_set "$staged" registry public_name "\"$REGISTRY_NAME\""
[[ -z "${REGISTRY_LISTEN:-}" ]] || toml_set "$staged" registry listen "\"$REGISTRY_LISTEN\""

/usr/local/bin/dock admin caddy-config --config "$staged" > /etc/dock/caddy.json.new \
  || die "the config above was not accepted; nothing in $CONF was changed"
if ! cmp -s "$staged" "$CONF"; then
  chmod 0640 "$staged"
  chgrp dock "$staged"
  mv "$staged" "$CONF"
fi
# The initial Caddy config only puts the admin API on the Unix socket and
# serves the 404 page. The daemon loads the real config on start.
chmod 0644 /etc/dock/caddy.json.new
mv /etc/dock/caddy.json.new /etc/dock/caddy.json

say "==> kernel"
# Hand queued connections to the new listener when Caddy reloads, instead of
# resetting them.
echo 'net.ipv4.tcp_migrate_req = 1' > /etc/sysctl.d/90-dock.conf
sysctl -q -p /etc/sysctl.d/90-dock.conf

say "==> buildkit image"
docker pull -q "$BUILDKIT_IMAGE" >/dev/null

say "==> systemd"
if [[ "$node_mode" == true ]]; then
  daemon=dock-node.service
else
  daemon=dock.service
fi
units=("$daemon" dock-caddy.service dock-firewall.service)
[[ "$tunnel" == false ]] || units+=(dock-cloudflared.service)
caddy_unit_changed=false
cmp -s "$SRC_DIR/systemd/dock-caddy.service" /etc/systemd/system/dock-caddy.service || caddy_unit_changed=true
if [[ "$tunnel" == true ]]; then
  cmp -s "$SRC_DIR/systemd/dock-cloudflared.service" /etc/systemd/system/dock-cloudflared.service || tunnel_changed=true
fi
for unit in "${units[@]}"; do
  install -m 0644 "$SRC_DIR/systemd/$unit" "/etc/systemd/system/$unit"
done
systemctl daemon-reload

say "==> firewall"
systemctl enable dock-firewall.service
systemctl restart dock-firewall.service \
  || die "the app firewall failed to apply; see: journalctl -u dock-firewall"

systemctl enable dock-caddy.service
if [[ "$caddy_unit_changed" == true ]] && systemctl is-active -q dock-caddy.service; then
  # Picks up unit changes such as the dock-caddy user. Routes come back
  # from --resume; traffic blips for a moment. Listener changes (tunnel
  # mode's 127.0.0.1) need no restart: the daemon loads them on start.
  say "    restarting caddy (unit changed)"
  systemctl restart dock-caddy.service
else
  systemctl start dock-caddy.service
fi

if [[ "$tunnel" == true ]]; then
  say "==> cloudflare tunnel"
  systemctl enable dock-cloudflared.service
  if [[ "$tunnel_changed" == true ]]; then
    systemctl restart dock-cloudflared.service
  else
    systemctl start dock-cloudflared.service
  fi
fi

# setup_registry runs CNCF Distribution as the dock-registry container
# (ADR 0014). Docker's restart policy keeps it up across reboots, like the
# apps; the control plane execs into it for garbage collection. It is
# recreated only when its image, config or mounts change.
setup_registry() {
  local listen storage cfg_dir=/etc/dock/registry hash current
  listen="$(toml_get "$CONF" registry listen)"
  listen="${listen:-127.0.0.1:5000}"
  storage="$(toml_get "$CONF" registry storage_dir)"
  storage="${storage:-/var/lib/dock-registry}"

  id dock-registry >/dev/null 2>&1 \
    || useradd --system --no-create-home --home-dir "$storage" --shell /usr/sbin/nologin dock-registry
  install -d -m 0700 -o dock-registry -g dock-registry "$storage"
  install -d -m 0750 -o root -g dock-registry "$cfg_dir"
  # Runs as dock: it reads the control plane's token signing key. It writes
  # config.yml and jwks.json (both deterministic for a given key).
  local out
  out="$(mktemp -d)"
  chown dock "$out"
  runuser -u dock -- /usr/local/bin/dock admin registry-config --config "$CONF" --out "$out" \
    || { rm -rf "$out"; die "dock admin registry-config failed; the registry was not changed"; }
  for f in config.yml jwks.json; do
    install -m 0640 -o root -g dock-registry "$out/$f" "$cfg_dir/$f"
  done
  rm -rf "$out"

  hash="$( { printf '%s\n' "$REGISTRY_IMAGE" "$listen" "$storage"; cat "$cfg_dir"/*; } | sha256sum | cut -c1-16)"
  current="$(docker inspect -f '{{index .Config.Labels "dock.install-hash"}}' dock-registry 2>/dev/null || true)"
  if [[ "$current" == "$hash" ]]; then
    docker start dock-registry >/dev/null
    return
  fi
  docker pull -q "$REGISTRY_IMAGE" >/dev/null
  docker rm -f dock-registry >/dev/null 2>&1 || true
  docker run -d --name dock-registry --restart unless-stopped \
    --label "dock.install-hash=$hash" \
    --user "$(id -u dock-registry):$(id -g dock-registry)" \
    --read-only --tmpfs /tmp \
    --cap-drop ALL --security-opt no-new-privileges \
    --memory 256m --pids-limit 256 \
    --env OTEL_TRACES_EXPORTER=none \
    --publish "$listen:5000" \
    --volume "$storage:/var/lib/registry" \
    --volume "$cfg_dir:/etc/distribution:ro" \
    "$REGISTRY_IMAGE" >/dev/null
}

token=""
if [[ "$node_mode" == true ]]; then
  if [[ -n "${NODE_JOIN_TOKEN:-}" ]]; then
    say "==> join"
    # A new join replaces this node's key and control plane pins.
    systemctl stop dock-node.service 2>/dev/null || true
    install -d -m 0700 -o dock -g dock /var/lib/dock
    # On stdin, not as an argument: argv is visible to every user.
    printf '%s\n' "$NODE_JOIN_TOKEN" | /usr/local/bin/dock node join --config "$CONF" --token-file - \
      || die "joining failed; get a new token with \`dock server add <name>\` if this one expired"
    # join ran as root; the node daemon runs as dock.
    chown -R dock:dock /var/lib/dock
    [[ -f "$NODE_STATE" ]] || die "dock node join did not write $NODE_STATE"
  fi
  systemctl enable dock-node.service
  systemctl restart dock-node.service
else
  systemctl enable dock.service
  systemctl restart dock.service
  if [[ "$(toml_get "$CONF" registry enabled)" == true ]]; then
    say "==> registry"
    setup_registry
  fi
  say "==> admin token"
  token="$(runuser -u dock -- /usr/local/bin/dock admin bootstrap --config "$CONF" || true)"
fi

say ""
if [[ "$node_mode" == true ]]; then
  say "Tempo Dock node is installed and connecting to the control plane."
  say "  Check it there:  dock server list"
  say "  Logs here:       journalctl -u dock-node -f"
else
  listen_addr="$(toml_get "$CONF" "" listen)"
  say "Tempo Dock is installed."
  say "  API and UI:  http://$listen_addr"
  if [[ -n "$token" ]]; then
    say "  admin token: $token"
    say "  (shown once; store it now)"
    say ""
    say "  On your laptop:  dock login http://$listen_addr"
    say "  then approve in the browser (sign in with the admin email and password above)."
  else
    say "  An admin already exists; no new token was created."
  fi
  node_listen="$(toml_get "$CONF" node listen)"
  if [[ -n "$node_listen" ]]; then
    say "  Nodes connect to $node_listen. Add a server: dock server add <name>"
  fi
  if [[ "$(toml_get "$CONF" registry enabled)" == true ]]; then
    registry_listen="$(toml_get "$CONF" registry listen)"
    say "  Registry: $(toml_get "$CONF" registry public_name) (container dock-registry on ${registry_listen:-127.0.0.1:5000})"
  fi
fi
say ""
if [[ "$tunnel" == true ]]; then
  say "Public traffic arrives through the Cloudflare Tunnel; no inbound ports are needed."
  http_port="$(toml_get "$CONF" caddy http_port)"
  say "In Cloudflare, route all of the tunnel's traffic to http://127.0.0.1:${http_port:-80}"
  say "(not localhost: Caddy listens on IPv4 loopback only) and add a DNS record:"
  say "  *.<zone> CNAME $(toml_get "$CONF" ingress tunnel_hostname) (proxied)"
  say "Steps: docs/guide/install.md#cloudflare-tunnel"
  say "Status: systemctl status dock-cloudflared"
else
  say "Open 80/tcp and 443/tcp+udp (HTTP/3) to the internet. Keep the API and node ports private (Tailscale)."
fi
say "Apps and builds can't reach your LAN, tailnet or this server; to allow a LAN host,"
say "add it to [network] allow_private in $CONF, then: systemctl restart dock-firewall"
