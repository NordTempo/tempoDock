#!/usr/bin/env bash
# Installs Tempo Dock on a Linux server: the dock binary, Caddy, systemd
# units, the app firewall and a default config. A server is either the
# control plane (API, UI, SQLite; the default) or a node that joins one
# (--node, ADR 0014).
#
#   curl -fsSL https://dock.nordtempo.com/install.sh | sudo bash
#   curl -fsSL https://dock.nordtempo.com/install.sh | sudo bash -s -- --yes --uninstall
#   sudo DOCK_BINARY=./dock-linux-amd64 ./deploy/install.sh
#   sudo --preserve-env=NODE_JOIN_TOKEN DOCK_BINARY=./dock-linux-amd64 ./deploy/install.sh --node
#   sudo ./deploy/install.sh                               # downloads the release's binary
#   sudo DOCK_BINARY=./dock-linux-amd64 ./deploy/install.sh --restore /root/dock-backup.tar.gz
#   curl -fsSL https://dock.nordtempo.com/install.sh | sudo bash -s -- --restore-from-bucket
#
# Piped, with no deploy/ directory beside it, the script downloads the
# release's install tarball and binary, checks them against its signed
# SHA256SUMS (ADR 0088) and runs that release's install.sh. Docker Engine
# and iptables are installed when missing, on Debian and Ubuntu, once you
# agree (or --yes).
#
# Re-running is safe: data is kept, binaries and units are replaced (that is
# also how you upgrade; a node is upgraded as a node). config.toml is written
# once; later runs only change the keys the options below ask for. Once
# installed, later versions can be applied from the console or `dock update
# apply`: dock-update.service runs this script from the new release (ADR 0028).
#
# Options:
#   --yes         install missing prerequisites (Docker Engine, iptables) without asking,
#                 and don't ask for the admin's email or the apps' domain
#   --uninstall   remove Dock: its services, binaries, containers and the apps'
#                 containers. Its data (/var/lib/dock, /etc/dock, volumes) stays,
#                 so installing again brings the projects back; then redeploy
#   --purge       with --uninstall: remove the data too. Nothing of Dock is left
#   --node        install as a node: `dock node` instead of `dock serve`. On a
#                 control plane retired by `dock admin backup --move`, this turns
#                 it into a node (with NODE_JOIN_TOKEN): its old data is moved aside
#   --restore ARCHIVE  install a control plane from `dock admin backup` (ADR 0021),
#                 on a fresh server: same identity, so nodes keep trusting it. The
#                 old config.toml is kept except what describes the machine
#                 (addresses, ingress, sizes), which is set as for a new install
#   --old-server-becomes-node  with --restore: the old server keeps its apps and
#                 joins this control plane as a node, instead of being replaced
#   --restore-from-bucket  the server is gone (ADR 0098): fetch the newest control
#                 plane backup from the backup bucket (`dock admin fetch-backup`, into
#                 /root/dock-recovered-<time>.tar), then install from it as --restore
#                 does. The control plane starts in recovery mode: `sudo dock admin
#                 recover` then shows what it will restore. The bucket comes from
#                 DOCK_RESTORE_* below; what is missing is asked on the terminal
#                 (not with --yes). A run that fails later removes the fetched archive
#
# Environment:
#   DOCK_BINARY   path to a dock binary (a release's dock-linux-<arch>, or make build)
#   DOCK_VERSION  without DOCK_BINARY: the release to download, checked against its
#                 signed SHA256SUMS (default: this release tarball's version, else the
#                 latest). A release from before signing (v0.56.0) is taken on its
#                 SHA256SUMS alone only when named here, never as the default
#   DOCK_REQUIRE_SIGNATURE  "1" fails that download without a signature checked
#                 here: no release from before signing, and none when the check can't
#                 run (it needs OpenSSL 3: Debian 12, Ubuntu 22.04 and later).
#                 Piped, it defaults to 1; "0" accepts SHA256SUMS alone
#   DOCK_ADMIN_EMAIL  the first admin's email, the console login (asked on a fresh
#                 install with a terminal; default admin@localhost)
#   DOCK_YES      "1" is --yes
#   DOCK_RESTORE_S3_ENDPOINT, DOCK_RESTORE_S3_BUCKET, DOCK_RESTORE_S3_PATH  with
#                 --restore-from-bucket: the store's URL (https://fsn1.your-objectstorage.com),
#                 the bucket and the folder in it (may be empty) of the backup repository
#   DOCK_RESTORE_REPOSITORY  instead of those three: the whole restic URL,
#                 s3:https://<endpoint>/<bucket>/<folder>
#   DOCK_RESTORE_S3_ACCESS_KEY_ID, DOCK_RESTORE_S3_SECRET_ACCESS_KEY  the bucket's keys
#   DOCK_RESTORE_S3_REGION  its region, if the store wants one
#   DOCK_RESTORE_S3_PATH_STYLE  "0" puts the bucket in the host name (default 1: in the path)
#   DOCK_RESTORE_PASSWORD  the backup repository's password
#                 (with sudo, pass them through: sudo --preserve-env=DOCK_RESTORE_PASSWORD,...)
#   GITHUB_TOKEN  for that download from a private repository: a token that can read its releases
#   DOCK_RELEASE_DIR  take the release from this directory instead of GitHub: its
#                 dock-install-<version>.tar.gz, dock-linux-<arch>, dock-caddy-linux-<arch>,
#                 SHA256SUMS and SHA256SUMS.sig, as `make release-local` or
#                 e2e/nightly.sh build them
#   DOCK_TRUSTED_KEYS  more release keys to trust (base64 Ed25519, space separated),
#                 as [updates] trusted_keys: for builds signed with your own key
#   DOCK_CADDY    path to Dock's Caddy (a release's dock-caddy-linux-<arch>, or make
#                 caddy-binary): Caddy with Dock's handlers, which add the analytics
#                 script to pages (ADR 0106). Default: the release's, downloaded with
#                 its binary; a dock binary given without its release keeps the Caddy
#                 there is, or gets Caddy's own build (CADDY_VERSION)
#   CADDY_VERSION, CADDY_SHA256  without Dock's Caddy: another Caddy release and the
#                     SHA-256 of its caddy_<version>_linux_<arch>.tar.gz
#   PUBLIC_IP         address users point DNS at (default: auto-detect; empty on tunnel servers)
#   BASE_DOMAIN       base for generated hostnames, needs wildcard DNS (optional; asked
#                     on a fresh install with a terminal)
#   DISABLE_TLS       "true" serves plain HTTP only: private LAN/Tailscale setups
#   CLOUDFLARE_TUNNEL_TOKEN  run a Cloudflare Tunnel (ADR 0013): installs cloudflared
#                     and dock-cloudflared.service, sets [ingress] mode = "cloudflare_tunnel"
#                     (Caddy serves plain HTTP on 127.0.0.1:<http_port> only; apps are
#                     https:// at Cloudflare's edge, so DISABLE_TLS doesn't apply)
#   CLOUDFLARED_VERSION, CLOUDFLARED_SHA256  another cloudflared release and its checksum
#   INGRESS           "cloudflare_tunnel" without a token: tunnel mode, with the tunnel
#                     made and run by Dock through a Cloudflare connection (ADR 0036;
#                     `dock server add --tunnel` prints it). Once Dock runs a tunnel
#                     this script installed, a re-run removes dock-cloudflared.service
# Control plane only:
#   LISTEN            API/UI address (default: Tailscale IP:7070 if present, else 127.0.0.1:7070)
#   NODE_LISTEN       accept nodes on this address → [node] listen. A new control plane
#                     takes them on every interface (":7443", ADR 0102); an address
#                     ("$(tailscale ip -4):7443") keeps them on it, empty turns nodes off
#   REGISTRY          "1" runs the image registry (dock-registry container) → [registry] enabled
#   REGISTRY_NAME     the registry's name in its tokens (default dock-registry) → [registry] public_name
#   REGISTRY_LISTEN   host address the registry is published on (default 127.0.0.1:5000)
#   REGISTRY_STORAGE  where a new registry keeps its images (ADR 0034): "local" (this
#                     server's disk, the default) or "s3" (an S3-compatible bucket:
#                     AWS, Hetzner, R2, B2, MinIO, Garage). Asked when the registry is
#                     new and there is a terminal. Later: dock registry migrate
#   REGISTRY_S3_ENDPOINT, REGISTRY_S3_REGION, REGISTRY_S3_BUCKET  with "s3": the store's
#                     base URL (https://fsn1.your-objectstorage.com), region, bucket
#   REGISTRY_S3_ACCESS_KEY_ID, REGISTRY_S3_SECRET_ACCESS_KEY  with "s3": its keys
#   REGISTRY_S3_PATH_STYLE  "false" puts the bucket in the host name (default true)
#   REGISTRY_S3_NAME  the storage connection's name in dock (default: the bucket's)
# Node only:
#   NODE_JOIN_TOKEN   from `dock server add` on the control plane; implies --node.
#                     Needed for the first run only (and to re-join)
#
# Tokens (NODE_JOIN_TOKEN, CLOUDFLARE_TUNNEL_TOKEN, the registry's keys) are
# best kept off the command line, where other users of the server can read
# them in the process list: set them in your shell first (`read -rs
# NODE_JOIN_TOKEN && export NODE_JOIN_TOKEN`) and pass them through with
# `sudo --preserve-env=NODE_JOIN_TOKEN`, or run the script from a root shell.
set -euo pipefail

CADDY_VERSION="${CADDY_VERSION:-2.11.7}"
CLOUDFLARED_VERSION="${CLOUDFLARED_VERSION:-2026.10.3}"
# Pinned by digest; the tag is for humans.
BUILDKIT_IMAGE="moby/buildkit:v0.34.1@sha256:99368f3e46177c6405b9740e4461d91cee004128daeecca78d4ceac50ec1da5c"
REGISTRY_IMAGE="registry:3.1.2@sha256:be9a7062dd7e3316e5c0368e42740dc991a9ceb5d382317675a8738db0112af0"
# The release's deploy/ directory, with systemd/ beside this script. Piped
# (curl | bash), there is none: bootstrap fetches it.
SRC_DIR=""
[[ ! -f "${BASH_SOURCE[0]:-}" ]] || SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Releases (.github/workflows/release.yml) are downloaded from here.
RELEASE_REPO=NordTempo/tempoDock
# The one-line installs: this script and install-cli.sh from the latest
# release, through the site's script, which checks them first (site/public).
INSTALL_URL=https://dock.nordtempo.com/install.sh
# The release whose asset this is: scripts/release/build.sh sets it in the
# release's install.sh, so a piped install of it installs that release.
RELEASE_VERSION=""
CLI_URL=https://dock.nordtempo.com/cli.sh
# Dock's release signing keys (ADR 0088), as in internal/update/sign.go:
# "<key id> <raw Ed25519 public key, base64>" per line. Releases from
# FIRST_SIGNED on are signed.
RELEASE_KEYS='bc6c45a08a65c136 8uQjZq1kO37ezVv7EzN8eyrqOYXRGK2nsCRL/d3RSdU='
FIRST_SIGNED=v0.56.0
CONF=/etc/dock/config.toml
# Written by `dock node join`; its presence means this node has joined.
NODE_STATE=/var/lib/dock/node.toml
TUNNEL_ENV=/etc/dock/cloudflared.env
# Written by `dock admin backup --move`: this control plane moved away.
MOVED=/var/lib/dock/MOVED
# Config keys that describe the machine rather than the install. A restore
# keeps the old config.toml but takes these from a new install's
# (SECTION:KEY, "" the top level).
MACHINE_KEYS=(:data_dir :listen :internal_listen :public_ip
  caddy:admin_socket caddy:http_port caddy:https_port
  buildkit:enabled buildkit:memory_mb buildkit:cpus
  network:pool network:allow_private
  ingress:mode ingress:tunnel_hostname
  registry:listen registry:storage_dir registry:config_dir)

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

# public_address prints this server's public address for the config's
# public_ip: $PUBLIC_IP when set, else what api.ipify.org says. Either
# way only an address goes into root's config file: digits, hex, dots and
# colons. A PUBLIC_IP that isn't one stops the install; an answer from
# the service that isn't one counts as no answer.
public_address() {
  local ip="${PUBLIC_IP:-}" asked=false
  if [[ -z "$ip" ]]; then
    ip="$(curl -fsS4 --max-time 5 https://api.ipify.org || true)" asked=true
  fi
  if [[ "$ip" =~ ^[0-9A-Fa-f.:]{2,45}$ ]]; then
    printf '%s\n' "$ip"
  elif [[ "$asked" == false ]]; then
    die "PUBLIC_IP must be this server's address, like 203.0.113.7"
  fi
}
warn() { printf 'warning: %s\n' "$*" >&2; }

# Temporary files, removed on exit.
staged=""
download_dir=""
# Where fetch_caddy downloads Dock's Caddy.
caddy_dir=""
bootstrap_err=""
# The archive --restore-from-bucket fetched, until the install is through:
# it holds every secret, so a failed run doesn't leave it lying around.
fetched=""
# What the install downloads before it changes anything (Caddy, cloudflared).
prefetched=""
cleanup() { rm -rf ${fetched:+"$fetched"} ${prefetched:+"$prefetched"} ${staged:+"$staged"} ${download_dir:+"$download_dir"} ${caddy_dir:+"$caddy_dir"} ${old_config:+"$old_config"} ${merged:+"$merged"} ${bootstrap_err:+"$bootstrap_err"}; }
trap cleanup EXIT

# github CURL-ARGS... runs curl against GitHub, https only (redirects
# included), with GITHUB_TOKEN if set. The token goes to curl as a config on
# stdin (printf is a builtin), never in its arguments, which any user could
# read in the process list.
github() {
  if [[ -n "${GITHUB_TOKEN:-}" ]]; then
    printf 'header = "Authorization: Bearer %s"\n' "$GITHUB_TOKEN" | curl -K - --proto '=https' --proto-redir '=https' -fsSL "$@"
  else
    curl --proto '=https' --proto-redir '=https' -fsSL "$@"
  fi
}

# shown TEXT prints TEXT for a message: printable characters only, on one
# line, at most 80 of them.
shown() { printf '%s' "$1" | LC_ALL=C tr -c '[:print:]' '?' | cut -c1-80; }

# release_version V: is V a release version, vX.Y.Z with an optional
# pre-release (v1.2.3-rc.1)? Versions end up in URLs and file names, so
# nothing else is accepted, wherever V came from. The same strings as
# dock's update.IsRelease: TestScriptsShareVersionGrammar runs a table
# through both, and keeps this function the same in every script.
release_version() (
  export LC_ALL=C
  case "$1" in '' | *[!0-9A-Za-z.-]*) exit 1 ;; esac
  printf '%s\n' "$1" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*))*)?$'
)

# check_version V FROM dies unless V is a release version; FROM says where
# it came from.
check_version() {
  release_version "$1" \
    || die "\"$(shown "$1")\" ($2) is not a release version like v0.58.0, so nothing was downloaded. To install a release you choose, set DOCK_VERSION: sudo DOCK_VERSION=v0.58.0 bash"
}

# signature_required VERSION: is VERSION (vX.Y.Z, pre-releases as their
# X.Y.Z) FIRST_SIGNED or newer?
signature_required() {
  printf '%s %s\n' "${1#v}" "${FIRST_SIGNED#v}" | awk '{
    split($1, a, /[.-]/); split($2, b, /[.-]/)
    for (i = 1; i <= 3; i++) { if (a[i] + 0 > b[i] + 0) exit 0; if (a[i] + 0 < b[i] + 0) exit 1 }
    exit 0 }'
}

# verify_sums DIR VERSION checks DIR/SHA256SUMS against DIR/SHA256SUMS.sig
# (missing when the release has none) with openssl, as dock does. A release
# from before signing passes on its SHA256SUMS alone only when the person
# named it with DOCK_VERSION and DOCK_REQUIRE_SIGNATURE isn't 1.
verify_sums() {
  local dir=$1 version=$2 id key line keys
  if [[ ! -s "$dir/SHA256SUMS.sig" ]]; then
    ! signature_required "$version" \
      || die "release $version has no SHA256SUMS.sig; releases from $FIRST_SIGNED on are signed, so don't install this one: it may have been tampered with"
    [[ "${DOCK_REQUIRE_SIGNATURE:-}" != 1 ]] \
      || die "release $version predates signed releases ($FIRST_SIGNED) and has no signature to check. Install $FIRST_SIGNED or later, or set DOCK_REQUIRE_SIGNATURE=0 with DOCK_VERSION=$version to take it on its SHA256SUMS alone"
    [[ -n "${DOCK_VERSION:-}" && "$DOCK_VERSION" != latest ]] \
      || die "release $version predates signed releases ($FIRST_SIGNED) and has no signature to check, so it isn't installed unasked. To install it anyway, checked against its SHA256SUMS alone, name it yourself: DOCK_VERSION=$version"
    warn "release $version predates signed releases ($FIRST_SIGNED); checked against its SHA256SUMS only"
    return 0
  fi
  grep -q "  dock-install-$version.tar.gz\$" "$dir/SHA256SUMS" || die "SHA256SUMS belongs to another release than $version"
  if ! openssl pkeyutl -help 2>&1 | grep -q -- -rawin; then
    [[ "${DOCK_REQUIRE_SIGNATURE:-}" != 1 ]] || die "can't check the release's signature: it needs OpenSSL 3 (openssl pkeyutl -rawin)"
    warn "can't check the release's signature without OpenSSL 3; checked against its SHA256SUMS only. To check where it was built: gh attestation verify dock-linux-$ARCH --repo $RELEASE_REPO"
    return 0
  fi
  keys="$RELEASE_KEYS"
  for key in ${DOCK_TRUSTED_KEYS:-}; do
    id="$(printf '%s' "$key" | openssl base64 -d -A | sha256sum | cut -c1-16)"
    keys+=$'\n'"$id $key"
  done
  while read -r id key; do
    line="$(grep "^$id " "$dir/SHA256SUMS.sig")" || continue
    printf '%s' "${line#* }" | openssl base64 -d -A > "$dir/sig.bin" || continue
    printf -- '-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEA%s\n-----END PUBLIC KEY-----\n' "$key" > "$dir/key.pem"
    if openssl pkeyutl -verify -pubin -inkey "$dir/key.pem" -rawin -in "$dir/SHA256SUMS" -sigfile "$dir/sig.bin" > /dev/null 2>&1; then
      return 0
    fi
  done <<< "$keys"
  die "release $version's SHA256SUMS isn't signed by a Dock release key; don't install it: it may have been tampered with"
}

# release_info VERSION DIR reads release VERSION ("latest" for the newest)
# from GitHub's API into DIR/release.json and prints its tag. Through the
# API, because release download links don't take a token. The one place a
# version becomes part of a URL or a file name: the version asked for and
# the one found are both release versions (check_version), or it dies.
release_info() {
  local version=$1 dir=$2 path
  [[ "$version" == latest ]] || check_version "$version" "the version asked for"
  if [[ -n "${DOCK_RELEASE_DIR:-}" ]]; then
    if [[ "$version" == latest ]]; then
      path="$(cd "$DOCK_RELEASE_DIR" && ls dock-install-*.tar.gz 2> /dev/null | head -n1)"
      version="${path#dock-install-}" version="${version%.tar.gz}"
      [[ -z "$version" ]] || check_version "$version" "the release in $DOCK_RELEASE_DIR"
    fi
    [[ -n "$version" && -f "$DOCK_RELEASE_DIR/dock-install-$version.tar.gz" ]] \
      || die "$DOCK_RELEASE_DIR has no dock-install-<version>.tar.gz${DOCK_VERSION:+ for $DOCK_VERSION}"
    printf '%s\n' "$version"
    return
  fi
  path="releases/tags/$version"
  [[ "$version" != latest ]] || path=releases/latest
  github "https://api.github.com/repos/$RELEASE_REPO/$path" > "$dir/release.json" \
    || die "could not read release $version of $RELEASE_REPO from GitHub (above), so nothing was installed. A 404 means there is no such release: see https://github.com/$RELEASE_REPO/releases. Otherwise check that this server reaches api.github.com and run this again; GitHub also limits requests without a token per address and hour (a 403): wait, or set GITHUB_TOKEN (sudo --preserve-env=GITHUB_TOKEN)"
  version="$(grep -o '"tag_name": *"[^"]*"' "$dir/release.json" | head -n1 | sed 's/.*"\([^"]*\)"$/\1/')"
  [[ -n "$version" ]] || die "GitHub's answer for the release names no version"
  check_version "$version" "the release GitHub answered with"
  printf '%s\n' "$version"
}

# download_assets DIR VERSION NAME... downloads the NAMEd assets of the
# release in DIR/release.json (release_info) into DIR, and checks each
# against the release's SHA256SUMS and that file's signature.
download_assets() {
  local dir=$1 version=$2 name url
  shift 2
  for name in "$@" SHA256SUMS SHA256SUMS.sig; do
    if [[ -n "${DOCK_RELEASE_DIR:-}" ]]; then
      [[ -f "$DOCK_RELEASE_DIR/$name" || "$name" == SHA256SUMS.sig ]] || die "$DOCK_RELEASE_DIR has no $name"
      [[ ! -f "$DOCK_RELEASE_DIR/$name" ]] || cp "$DOCK_RELEASE_DIR/$name" "$dir/$name"
      continue
    fi
    # An asset's "url" comes just before its "name" in GitHub's JSON.
    # shellcheck disable=SC2020 # one newline per character, on purpose
    url="$(tr ',{}[]' '\n\n\n\n\n' < "$dir/release.json" | awk -v name="$name" '
      /"url": *"[^"]*\/releases\/assets\/[0-9]+"/ { u = $0; sub(/^[^:]*: *"/, "", u); sub(/".*$/, "", u) }
      $0 ~ "\"name\": *\"" name "\"" && u != "" { print u; exit }')"
    # Releases before FIRST_SIGNED have no signature; verify_sums decides.
    [[ -n "$url" || "$name" == SHA256SUMS.sig ]] || die "release $version has no $name"
    [[ -z "$url" ]] || github -H "Accept: application/octet-stream" -o "$dir/$name" "$url" || die "could not download $name"
  done
  verify_sums "$dir" "$version"
  for name in "$@"; do
    (cd "$dir" && awk -v n="$name" '$2 == n' SHA256SUMS | sha256sum --check --quiet --strict) \
      || die "$name does not match the release's SHA256SUMS"
  done
}

# release_has DIR NAME reports whether the release in DIR/release.json
# (release_info), or in DOCK_RELEASE_DIR, has an asset NAME.
release_has() {
  if [[ -n "${DOCK_RELEASE_DIR:-}" ]]; then
    [[ -f "$DOCK_RELEASE_DIR/$2" ]]
  else
    grep -q "\"name\": *\"$2\"" "$1/release.json"
  fi
}

# fetch_caddy VERSION downloads release VERSION's Dock Caddy (ADR 0106)
# and sets DOCK_CADDY to it, for a dock binary that came without it: the
# one-line install's bootstrap, an updater from before Dock's Caddy, or a
# release tarball run by hand. A release from before it has none. A
# failed download only warns: the server keeps the Caddy it has, and
# pages load the analytics script themselves until the next update.
fetch_caddy() {
  local rc=0
  caddy_dir="$(mktemp -d)"
  # errexit is off on the left of ||: each step must pass for the next.
  (
    release_info "$1" "$caddy_dir" > /dev/null \
      && { release_has "$caddy_dir" "dock-caddy-linux-$ARCH" || exit 3; } \
      && download_assets "$caddy_dir" "$1" "dock-caddy-linux-$ARCH"
  ) || rc=$?
  case "$rc" in
    0) DOCK_CADDY="$caddy_dir/dock-caddy-linux-$ARCH" ;;
    3) ;;
    *) warn "could not download Dock's Caddy for $1 (above); keeping the Caddy this server has" ;;
  esac
}

# toml_get_raw FILE SECTION KEY prints KEY's value in [SECTION] ("" is the
# top level) as a TOML literal, quotes included. Enough for the flat keys
# this script writes.
toml_get_raw() {
  awk -v section="$2" -v key="$3" '
    BEGIN { in_section = (section == "") }
    /^[[:space:]]*\[/ {
      hdr = $0; gsub(/^[[:space:]]*\[[[:space:]]*|[[:space:]]*\].*$/, "", hdr)
      in_section = (hdr == section); next
    }
    in_section && $0 ~ "^[[:space:]]*" key "[[:space:]]*=" {
      v = $0; sub(/^[^=]*=[[:space:]]*/, "", v); sub(/[[:space:]]+#.*$/, "", v)
      print v; exit
    }' "$1"
}

# toml_get FILE SECTION KEY is toml_get_raw with the quotes stripped.
toml_get() {
  local v
  v="$(toml_get_raw "$@")"
  v="${v#\"}"
  printf '%s\n' "${v%\"}"
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

# toml_unset FILE SECTION KEY removes KEY from [SECTION], with the rest of
# an array value written over several lines. Everything else stays.
toml_unset() {
  local out
  out="$(awk -v section="$2" -v key="$3" '
    # bare: the line without its strings and comment, so a "]" in an
    # address ("[2001:db8::1]:7443") does not end the array.
    function bare(l) { gsub(/"[^"]*"|\047[^\047]*\047/, "", l); sub(/#.*$/, "", l); return l }
    BEGIN { in_section = (section == "") }
    skipping { if (bare($0) ~ /\]/) skipping = 0; next }
    /^[[:space:]]*\[/ {
      hdr = $0; gsub(/^[[:space:]]*\[[[:space:]]*|[[:space:]]*\].*$/, "", hdr)
      in_section = (hdr == section)
    }
    in_section && $0 ~ "^[[:space:]]*" key "[[:space:]]*=" {
      v = $0; sub(/^[^=]*=/, "", v); v = bare(v)
      if (v ~ /\[/ && v !~ /\]/) skipping = 1
      next
    }
    { print }
  ' "$1")"
  printf '%s\n' "$out" > "$1"
}

# refresh_buildkit_image FILE moves [buildkit] image in an existing config
# to this release's pinned image: config.toml is written once, so without
# this a server would build with the image of the release it was installed
# from for good. Only Dock's own default is moved: an image from another
# repository, or a moby/buildkit newer than this release's, is the
# operator's choice and stays.
refresh_buildkit_image() {
  local cur cur_tag new_tag
  cur="$(toml_get "$1" buildkit image)"
  [[ "$cur" == moby/buildkit:* && "$cur" != "$BUILDKIT_IMAGE" ]] || return 0
  cur_tag="${cur#moby/buildkit:}"; cur_tag="${cur_tag%%@*}"
  new_tag="${BUILDKIT_IMAGE#moby/buildkit:}"; new_tag="${new_tag%%@*}"
  [[ "$cur_tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || return 0
  [[ "$(printf '%s\n%s\n' "$cur_tag" "$new_tag" | sort -V | tail -n 1)" == "$new_tag" ]] || return 0
  toml_set "$1" buildkit image "\"$BUILDKIT_IMAGE\""
  say "    [buildkit] image: $cur_tag -> $new_tag"
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

# tty_open opens the terminal on fd 3 for questions; false when there is
# none (piped into ssh, the updater, CI). Piped installs read the script
# from stdin, so questions always go to /dev/tty.
tty_open() { { exec 3</dev/tty; } 2>/dev/null; }

# confirm QUESTION is true with --yes, else asks on the terminal (Enter is
# yes). Without a terminal it is false.
confirm() {
  local answer=""
  [[ "$assume_yes" == false ]] || return 0
  tty_open || return 1
  read -r -u 3 -p "$1 [Y/n] " answer || true
  exec 3<&-
  [[ "${answer:-y}" == [Yy]* ]]
}

# ask QUESTION DEFAULT prints the answer typed on the terminal, or DEFAULT
# with --yes, without a terminal or when nothing is typed.
ask() {
  local answer=""
  if [[ "$assume_yes" == false ]] && tty_open; then
    read -r -u 3 -p "$1 " answer || true
    exec 3<&-
  fi
  printf '%s\n' "${answer:-$2}"
}

# bootstrap is `curl | sudo bash`: this script came without its release.
# It downloads the release's install tarball and server binary, checks
# both against the signed SHA256SUMS (required here, unless
# DOCK_REQUIRE_SIGNATURE=0) and runs that release's install.sh with the
# same arguments.
bootstrap() {
  local version dir rc=0
  download_dir="$(mktemp -d)"
  dir=$download_dir
  version="$(release_info "${DOCK_VERSION:-${RELEASE_VERSION:-latest}}" "$dir")"
  say "==> download dock $version"
  DOCK_REQUIRE_SIGNATURE="${DOCK_REQUIRE_SIGNATURE:-1}" \
    download_assets "$dir" "$version" "dock-install-$version.tar.gz" "dock-linux-$ARCH"
  tar -xzf "$dir/dock-install-$version.tar.gz" -C "$dir"
  chmod 0755 "$dir/dock-linux-$ARCH"
  DOCK_BINARY="$dir/dock-linux-$ARCH" DOCK_VERSION="$version" \
    bash "$dir/deploy/install.sh" "$@" < /dev/null || rc=$?
  exit "$rc"
}

# ensure_prereqs makes sure Docker runs and iptables is there. Missing
# ones are installed with apt on Debian and Ubuntu (Docker Engine from
# Docker's own repository, as docs.docker.com does it) once the user
# agrees; elsewhere it says what to install.
ensure_prereqs() {
  local missing=() id="" like="" codename=""
  command -v docker > /dev/null || missing+=("Docker Engine")
  command -v iptables > /dev/null || missing+=(iptables)
  if (( ${#missing[@]} )); then
    local need="${missing[0]}"
    (( ${#missing[@]} == 1 )) || need+=" and ${missing[1]}"
    if [[ -r /etc/os-release ]]; then
      # shellcheck disable=SC1091
      id="$(. /etc/os-release && printf '%s' "${ID:-}")"
      like="$(. /etc/os-release && printf '%s' "${ID_LIKE:-}")"
      codename="$(. /etc/os-release && printf '%s' "${UBUNTU_CODENAME:-${VERSION_CODENAME:-}}")"
    fi
    [[ "$id" == ubuntu || "$like" == *ubuntu* ]] && id=ubuntu
    [[ "$id" == debian || "$id" == ubuntu ]] && [[ -n "$codename" ]] \
      || die "this server needs $need: install Docker Engine 28 or newer (https://docs.docker.com/engine/install/) and iptables, then run this again"
    confirm "This server needs $need. Install with apt (Docker Engine from download.docker.com)?" \
      || die "this server needs $need; run again with --yes to install it (curl ... | sudo bash -s -- --yes), or install Docker Engine yourself: https://docs.docker.com/engine/install/"
    install_prereqs "$id" "$codename"
  fi
  if ! docker info > /dev/null 2>&1; then
    say "==> start docker"
    systemctl enable --now docker.service > /dev/null 2>&1 || true
    for _ in $(seq 30); do docker info > /dev/null 2>&1 && break; sleep 1; done
    docker info > /dev/null 2>&1 || die "Docker is installed but doesn't start; see: journalctl -u docker"
  fi
}

# install_prereqs ID CODENAME installs iptables and, when missing, Docker
# Engine from https://download.docker.com/linux/ID (debian or ubuntu).
install_prereqs() {
  local id=$1 codename=$2 log=/var/log/dock-install-apt.log
  say "==> apt: iptables$(command -v docker > /dev/null || echo ', docker-ce') (log: $log)"
  export DEBIAN_FRONTEND=noninteractive
  { apt-get update -q && apt-get install -y -q ca-certificates curl iptables; } < /dev/null >> "$log" 2>&1 \
    || { tail -n 15 "$log" >&2; die "apt failed (above; all of it in $log)"; }
  if ! command -v docker > /dev/null; then
    install -m 0755 -d /etc/apt/keyrings
    curl -fsSL "https://download.docker.com/linux/$id/gpg" -o /etc/apt/keyrings/docker.asc
    chmod a+r /etc/apt/keyrings/docker.asc
    printf 'deb [arch=%s signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/%s %s stable\n' \
      "$(dpkg --print-architecture)" "$id" "$codename" > /etc/apt/sources.list.d/docker.list
    apt-get update -q < /dev/null >> "$log" 2>&1 \
      || die "apt can't read Docker's repository for $id $codename (see $log); install Docker Engine yourself: https://docs.docker.com/engine/install/"
    apt-get install -y -q docker-ce docker-ce-cli containerd.io < /dev/null >> "$log" 2>&1 \
      || { tail -n 15 "$log" >&2; die "installing Docker Engine failed (above; all of it in $log)"; }
  fi
}

# ask_base_domain PUBLIC_IP asks a fresh install's operator for the base
# of generated app hostnames and prints it, lower case; empty skips it
# (BASE_DOMAIN sets it without asking; `dock server update --base-domain`
# later).
ask_base_domain() {
  local domain target=${1:-this server}
  [[ "$assume_yes" == false ]] && tty_open || return 0
  exec 3<&-
  say "" >&2
  say "Apps can get generated hostnames, <app>-<random>.<domain>, under a domain whose" >&2
  say "wildcard DNS record points here: *.apps.example.com  A  $target" >&2
  domain="$(ask "Domain for apps (Enter to skip, set it later):" "")"
  domain="${domain#\*.}"
  printf '%s\n' "${domain,,}"
}

# summary prints how to reach a control plane: the console (with the SSH
# tunnel to open first when the API listens on loopback only), the first
# admin's login and token when this run created them, the one line that
# installs and signs in the CLI on a laptop, and where apps get hostnames.
summary() {
  local listen host port console ssh_line public base
  listen="$(toml_get "$CONF" "" listen)"
  host="${listen%:*}" port="${listen##*:}"
  console="http://$listen"
  public="$(toml_get "$CONF" "" public_ip)"
  if [[ "$host" == 127.* || "$host" == localhost ]]; then
    console="http://localhost:$port"
    ssh_line="ssh -L $port:127.0.0.1:$port ${SUDO_USER:-root}@${public:-$(hostname -I 2> /dev/null | awk '{ print $1 }')}"
  fi
  say "Tempo Dock $("/usr/local/bin/dock" --version 2> /dev/null | awk '{ print $NF }') is installed."
  say ""
  say "  Console   $console"
  if [[ -n "${ssh_line:-}" ]]; then
    say "            The console listens on this server only. On your laptop, open a tunnel first:"
    say "            $ssh_line"
  fi
  if [[ -n "$token" ]]; then
    say "  Sign in   $admin_email with password ${admin_password:-(see above)} (shown once)"
  else
    say "  Sign in   with the admin account from the first install"
  fi
  say "  CLI       curl -fsSL $CLI_URL | sh -s -- $console"
  say "            installs dock on your laptop (macOS, Linux or WSL) and signs it in"
  base="$(toml_get "$CONF" "" base_domain)"
  if [[ -n "$base" ]]; then
    say "  Apps      get hostnames under $base; it needs DNS: *.$base  A  ${public:-<this server>}"
  else
    say "  Apps      get hostnames once you set a domain with wildcard DNS to ${public:-this server}:"
    say "            dock server update $(hostname) --base-domain apps.example.com"
  fi
  say "  Updates   the console's Updates page, or dock update"
  if [[ -n "$token" ]]; then
    say ""
    say "  For scripts and CI, an operator token (shown once; store it now):"
    say "  admin token: $token"
  fi
}

# uninstall removes Dock from this server: its units, binaries, kernel
# setting, the app firewall and every container Dock made (apps included:
# nothing routes to them once Caddy is gone). With purge it also removes
# Dock's data, volumes, networks and system users; without, a new install
# picks the data up again. Docker itself stays.
uninstall() {
  local purge=$1 unit
  say "==> stop dock"
  for unit in dock.service dock-node.service dock-update.path dock-update.service \
    dock-cloudflared.service dock-caddy.service; do
    systemctl disable --now "$unit" 2> /dev/null || true
  done
  if command -v docker > /dev/null && docker info > /dev/null 2>&1; then
    say "==> containers"
    docker ps -aq --filter label=dock.managed | xargs -r docker rm -f > /dev/null
    docker rm -f dock-registry dock-buildkit dock-cloudflared > /dev/null 2>&1 || true
    if [[ "$purge" == true ]]; then
      docker volume ls -q --filter label=dock.managed | xargs -r docker volume rm -f > /dev/null
      docker network ls -q --filter label=dock.managed | xargs -r docker network rm > /dev/null
    fi
  fi
  say "==> firewall"
  [[ ! -x /usr/local/bin/dock ]] || /usr/local/bin/dock admin firewall remove > /dev/null 2>&1 || true
  systemctl disable --now dock-firewall.service 2> /dev/null || true
  say "==> files"
  for unit in dock.service dock-node.service dock-caddy.service dock-firewall.service \
    dock-update.path dock-update.service dock-cloudflared.service; do
    rm -f "/etc/systemd/system/$unit"
  done
  systemctl daemon-reload
  rm -f /usr/local/bin/dock /usr/local/bin/caddy /usr/local/bin/cloudflared /etc/sysctl.d/90-dock.conf
  if [[ "$purge" == true ]]; then
    rm -rf /etc/dock /var/lib/dock /var/lib/dock-caddy /var/lib/dock-upstreams /var/lib/dock-registry /var/lib/dock-update
    for unit in dock dock-caddy dock-registry; do
      ! id "$unit" > /dev/null 2>&1 || userdel "$unit" 2> /dev/null || true
    done
    say ""
    say "Tempo Dock is removed, with its data. Docker stays installed."
  else
    say ""
    say "Tempo Dock is removed. Its data stays: /etc/dock, /var/lib/dock and the apps' volumes."
    say "  Install again to get the projects back, then redeploy the apps; or remove it all:"
    say "  curl -fsSL $INSTALL_URL | sudo bash -s -- --uninstall --purge"
  fi
}

# registry_storage creates the dock-registry user, the registry's storage
# dir and its config dir, and prints the storage dir. The dock group may
# read the storage dir (the registry writes 0755 dirs and 0644 files
# inside), so the console can show its size and the daemon can copy it to
# a bucket; only the registry writes to it. The daemon writes the config
# dir (it rewrites config.yml when the registry's storage changes); it is
# setgid dock-registry, so the registry reads the 0640 files the daemon
# writes, which can hold a bucket's keys.
registry_storage() {
  local storage cfg_dir
  storage="$(toml_get "$CONF" registry storage_dir)"
  storage="${storage:-/var/lib/dock-registry}"
  cfg_dir="$(registry_config_dir)"
  id dock-registry >/dev/null 2>&1 \
    || useradd --system --no-create-home --home-dir "$storage" --shell /usr/sbin/nologin dock-registry
  install -d -m 0750 -o dock-registry -g dock "$storage"
  install -d -m 2750 -o dock -g dock-registry "$cfg_dir"
  # Files an older install wrote as root.
  find "$cfg_dir" -maxdepth 1 -type f -exec chown dock:dock-registry {} + -exec chmod 0640 {} +
  printf '%s\n' "$storage"
}

# registry_config_dir prints [registry] config_dir.
registry_config_dir() {
  local dir
  dir="$(toml_get "$CONF" registry config_dir)"
  printf '%s\n' "${dir:-/etc/dock/registry}"
}

# ask_registry_storage asks where a new registry keeps its images, when
# REGISTRY_STORAGE isn't set and there is a terminal to ask on. Local is
# the default. For S3 it fills in the REGISTRY_S3_* variables left empty.
ask_registry_storage() {
  [[ -z "${REGISTRY_STORAGE:-}" ]] || return 0
  if ! { exec 3</dev/tty; } 2>/dev/null; then
    REGISTRY_STORAGE=local
    return 0
  fi
  local choice
  say ""
  say "Where should the registry keep images?"
  say "  1) on this server's disk (default)"
  say "  2) in S3-compatible object storage: AWS, Hetzner, Cloudflare R2, Backblaze B2, MinIO, Garage"
  read -r -u 3 -p "Choice [1]: " choice || true
  if [[ "${choice:-1}" != 2 ]]; then
    REGISTRY_STORAGE=local
    exec 3<&-
    return 0
  fi
  REGISTRY_STORAGE=s3
  [[ -n "${REGISTRY_S3_ENDPOINT:-}" ]] || read -r -u 3 -p "Endpoint (base URL, e.g. https://fsn1.your-objectstorage.com): " REGISTRY_S3_ENDPOINT
  [[ -n "${REGISTRY_S3_REGION:-}" ]] || read -r -u 3 -p "Region (empty for us-east-1): " REGISTRY_S3_REGION || true
  [[ -n "${REGISTRY_S3_BUCKET:-}" ]] || read -r -u 3 -p "Bucket: " REGISTRY_S3_BUCKET
  [[ -n "${REGISTRY_S3_ACCESS_KEY_ID:-}" ]] || read -r -u 3 -p "Access key ID: " REGISTRY_S3_ACCESS_KEY_ID
  if [[ -z "${REGISTRY_S3_SECRET_ACCESS_KEY:-}" ]]; then
    read -r -s -u 3 -p "Secret access key: " REGISTRY_S3_SECRET_ACCESS_KEY
    say ""
  fi
  exec 3<&-
}

# set_registry_storage keeps a new registry's images in the bucket the
# REGISTRY_S3_* variables name (dock admin registry-storage). It runs
# before the daemon starts, which applies it. Images already on disk would
# be left behind, so it refuses then.
set_registry_storage() {
  local storage name
  storage="$(registry_storage)"
  if [[ -n "$(ls -A "$storage/docker" 2>/dev/null)" ]]; then
    die "the registry already has images in $storage; move them with \`dock registry migrate --to s3\` instead of REGISTRY_STORAGE=s3"
  fi
  for v in REGISTRY_S3_ENDPOINT REGISTRY_S3_BUCKET REGISTRY_S3_ACCESS_KEY_ID REGISTRY_S3_SECRET_ACCESS_KEY; do
    [[ -n "${!v:-}" ]] || die "REGISTRY_STORAGE=s3 needs $v"
  done
  name="${REGISTRY_S3_NAME:-$(printf '%s' "$REGISTRY_S3_BUCKET" | tr '.' '-')}"
  # The keys go on stdin, never in arguments, which any user could read.
  printf '%s\n%s\n' "$REGISTRY_S3_ACCESS_KEY_ID" "$REGISTRY_S3_SECRET_ACCESS_KEY" \
    | runuser -u dock -- /usr/local/bin/dock admin registry-storage "$name" --config "$CONF" \
      --endpoint "$REGISTRY_S3_ENDPOINT" --region "${REGISTRY_S3_REGION:-}" --bucket "$REGISTRY_S3_BUCKET" \
      --path-style="${REGISTRY_S3_PATH_STYLE:-true}" \
    || die "the registry's bucket was not accepted (see above); fix the REGISTRY_S3_* values and run the same command again"
}

# setup_registry runs CNCF Distribution as the dock-registry container
# (ADR 0014). Docker's restart policy keeps it up across reboots, like the
# apps; the control plane stops it for garbage collection, which runs in a
# one-shot container next to it. It is recreated only when its image,
# config or mounts change.
#
# It runs on the host's network (ADR 0084), listening only on [registry]
# listen (config.yml's http.addr, loopback), so it reaches the daemon's
# storage gateway on [registry] gateway_listen. A registry from before ran
# on Docker's bridge network with the port published; the network in the
# hash recreates it once. A config.yml that binds every address (:5000,
# written by an older dock binary) keeps the published port instead: on
# the host's network that would put the registry on every interface.
setup_registry() {
  local listen storage cfg_dir hash current
  listen="$(toml_get "$CONF" registry listen)"
  listen="${listen:-127.0.0.1:5000}"
  storage="$(registry_storage)"
  cfg_dir="$(registry_config_dir)"
  # Runs as dock: it reads the control plane's token signing key and the
  # registry's storage setting. It writes config.yml and jwks.json (both
  # deterministic for a given key and storage), only where they changed.
  runuser -u dock -- /usr/local/bin/dock admin registry-config --config "$CONF" --out "$cfg_dir" \
    || die "dock admin registry-config failed; the registry was not changed"

  local net=(--network host)
  if grep -Eq '^  addr: "?:5000"?$' "$cfg_dir/config.yml"; then
    net=(--publish "$listen:5000")
  fi
  hash="$( { printf '%s\n' "$REGISTRY_IMAGE" "$listen" "$storage" "${net[*]}"; cat "$cfg_dir"/*; } | sha256sum | cut -c1-16)"
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
    "${net[@]}" \
    --volume "$storage:/var/lib/registry" \
    --volume "$cfg_dir:/etc/distribution:ro" \
    "$REGISTRY_IMAGE" >/dev/null
}


# main installs, upgrades or removes Dock on this server. Everything runs from
# here, called on the last line, so a download cut short runs nothing.
main() {
  node_mode=false
  restore=""
  # from_bucket: --restore-from-bucket. restore then names the archive that
  # `dock admin fetch-backup` writes once the dock binary is installed.
  from_bucket=false
  old_server_node=false
  assume_yes=false
  [[ "${DOCK_YES:-}" != 1 ]] || assume_yes=true
  remove=false
  purge=false
  local args=("$@")
  while (( $# )); do
    case "$1" in
      --node) node_mode=true ;;
      --restore)
        [[ -n "${2:-}" ]] || die "--restore needs the archive from \`dock admin backup\`"
        restore="$(realpath "$2")"; shift ;;
      --restore-from-bucket) from_bucket=true ;;
      --old-server-becomes-node) old_server_node=true ;;
      --yes|-y) assume_yes=true ;;
      --uninstall) remove=true ;;
      --purge) purge=true ;;
      -h|--help)
        if [[ -n "$SRC_DIR" ]]; then
          awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$SRC_DIR/install.sh"
        else
          say "Installs Tempo Dock. Options and variables: https://github.com/$RELEASE_REPO/blob/main/deploy/install.sh"
        fi
        exit 0 ;;
      *) die "unknown option $1 (see --help)" ;;
    esac
    shift
  done
  [[ -n "${NODE_JOIN_TOKEN:-}" ]] && node_mode=true
  [[ "$purge" == false || "$remove" == true ]] || die "--purge goes with --uninstall"
  if [[ "$from_bucket" == true ]]; then
    [[ -z "$restore" ]] || die "--restore takes an archive you have, --restore-from-bucket fetches it; use one of them"
    [[ "$node_mode" == false ]] || die "--restore-from-bucket installs a control plane; leave out --node and NODE_JOIN_TOKEN"
    [[ "$old_server_node" == false ]] \
      || die "--restore-from-bucket is for a server that is gone, so it can't become a node; leave out --old-server-becomes-node"
    restore="/root/dock-recovered-$(date +%Y%m%d-%H%M%S).tar"
  fi

  [[ "$(uname -s)" == Linux ]] || die "Dock servers run Linux; on a Mac or Windows, install the CLI instead: curl -fsSL $CLI_URL | sh"
  [[ $EUID -eq 0 ]] || die "run as root: curl -fsSL $INSTALL_URL | sudo bash"
  case "$(uname -m)" in
    x86_64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) die "unsupported architecture $(uname -m)" ;;
  esac
  # Before anything is downloaded: Dock's services are systemd units.
  command -v systemctl > /dev/null \
    || die "Dock runs as systemd services and this machine has no systemd (no systemctl), so nothing was installed. Install it on a server that boots with systemd, like Debian or Ubuntu; not inside a container"

  if [[ "$remove" == true ]]; then
    uninstall "$purge"
    exit 0
  fi
  # Piped: fetch this release's deploy/ and run its install.sh instead.
  [[ -f "$SRC_DIR/systemd/dock.service" ]] || bootstrap "${args[@]}"

  # A server is one or the other; re-runs keep what it is. The exception is
  # a control plane that moved away (`dock admin backup --move` left its
  # marker): it may become a node of the new one, keeping its apps.
  [[ -f /etc/systemd/system/dock-node.service ]] && node_mode=true
  convert=false
  if [[ "$node_mode" == true && -f /etc/systemd/system/dock.service ]]; then
    [[ -f "$MOVED" ]] \
      || die "this server runs the control plane (dock.service); a node must be a separate server, or move the control plane away first (docs/guide/install.md#moving-the-control-plane)"
    [[ -n "${NODE_JOIN_TOKEN:-}" ]] \
      || die "set NODE_JOIN_TOKEN to turn this retired control plane into a node: run \`dock server add <name>\` against the new control plane"
    convert=true
  fi
  if [[ -n "$restore" ]]; then
    [[ "$node_mode" == false ]] || die "--restore installs a control plane; leave out --node and NODE_JOIN_TOKEN"
    [[ "$from_bucket" == true || -r "$restore" ]] || die "can't read $restore"
    [[ ! -f /var/lib/dock/dock.db ]] \
      || die "this server already has a control plane database (/var/lib/dock/dock.db); --restore and --restore-from-bucket are for a fresh server"
  elif [[ "$old_server_node" == true ]]; then
    die "--old-server-becomes-node goes with --restore"
  fi
  if [[ "$node_mode" == true ]]; then
    [[ -n "${NODE_JOIN_TOKEN:-}" || -f "$NODE_STATE" ]] \
      || die "set NODE_JOIN_TOKEN to a join token: run \`dock server add <name>\` against the control plane"
    for v in LISTEN NODE_LISTEN REGISTRY REGISTRY_NAME REGISTRY_LISTEN REGISTRY_STORAGE; do
      [[ -z "${!v:-}" ]] || die "$v is for the control plane; this is a node install"
    done
  fi

  # tunnel: the server is in tunnel mode. tunnel_unit: this script runs its
  # cloudflared (dock-cloudflared.service); without it, Dock does (ADR 0036).
  tunnel=false
  tunnel_unit=false
  tunnel_hostname=""
  if [[ -n "${CLOUDFLARE_TUNNEL_TOKEN:-}" ]]; then
    tunnel=true tunnel_unit=true
    tid="$(tunnel_id "$CLOUDFLARE_TUNNEL_TOKEN")" \
      || die "CLOUDFLARE_TUNNEL_TOKEN is not a tunnel token; copy it from the tunnel's install command in the Cloudflare dashboard"
    tunnel_hostname="$tid.cfargotunnel.com"
  elif [[ -f "$TUNNEL_ENV" ]]; then
    tunnel=true tunnel_unit=true
  fi
  case "${INGRESS:-}" in
    "" | direct) ;;
    cloudflare_tunnel) tunnel=true ;;
    *) die "INGRESS must be direct or cloudflare_tunnel" ;;
  esac
  if [[ -f "$CONF" && -z "$restore" && "$(toml_get "$CONF" ingress mode)" == cloudflare_tunnel ]]; then
    [[ "${INGRESS:-}" != direct ]] \
      || die "this server is in tunnel mode; install.sh doesn't switch it to direct: set [ingress] mode = \"direct\" and public_ip in $CONF, then restart dock"
    tunnel=true
  fi

  case "$CLOUDFLARED_VERSION/$ARCH" in
    2026.10.3/amd64) cloudflared_sha256=053b80b0a23031a038fdbf3c073f59f82b8f1f152aa67971d7383ffafc0dbab0 ;;
    2026.10.3/arm64) cloudflared_sha256=89297ebfb9e8c99c0dc013a1140601453cf3798d8306c5591651f4a3f5210be8 ;;
    *) cloudflared_sha256="${CLOUDFLARED_SHA256:-}" ;;
  esac
  if [[ "$tunnel_unit" == true && -z "$cloudflared_sha256" ]]; then
    die "set CLOUDFLARED_SHA256 to the SHA-256 of cloudflared-linux-$ARCH from the $CLOUDFLARED_VERSION release notes"
  fi

  if [[ -z "${DOCK_BINARY:-}" ]]; then
    download_dir="$(mktemp -d)"
    dock_version="$(release_info "${DOCK_VERSION:-$(cat "$SRC_DIR/VERSION" 2>/dev/null || echo latest)}" "$download_dir")"
    say "==> download dock $dock_version"
    assets=("dock-linux-$ARCH")
    if [[ -z "${DOCK_CADDY:-}" ]] && release_has "$download_dir" "dock-caddy-linux-$ARCH"; then
      assets+=("dock-caddy-linux-$ARCH")
      DOCK_CADDY="$download_dir/dock-caddy-linux-$ARCH"
    fi
    download_assets "$download_dir" "$dock_version" "${assets[@]}"
    DOCK_BINARY="$download_dir/dock-linux-$ARCH"
    chmod 0755 "$DOCK_BINARY"
  elif [[ -z "${DOCK_CADDY:-}" && -f "$SRC_DIR/VERSION" ]]; then
    fetch_caddy "$(cat "$SRC_DIR/VERSION")"
  fi
  [[ -x "$DOCK_BINARY" ]] || die "$DOCK_BINARY is not an executable file"

  # A new node must reach the control plane: find out before installing
  # anything (ADR 0102). The token stays unused.
  if [[ "$node_mode" == true && -n "${NODE_JOIN_TOKEN:-}" ]]; then
    say "==> reach the control plane"
    printf '%s\n' "$NODE_JOIN_TOKEN" | "$DOCK_BINARY" node join --check --token-file - \
      || die "nothing was installed. Once this server reaches the control plane, run this again"
  fi

  ensure_prereqs

  # Dock runs the tunnel in its dock-cloudflared container once it adopted it
  # (ADR 0036): this script's cloudflared is then a second connector of the
  # same tunnel, and goes.
  if [[ "$tunnel_unit" == true && -z "${CLOUDFLARE_TUNNEL_TOKEN:-}" ]] \
    && [[ -n "$(docker ps -q --filter name=^dock-cloudflared\$ --filter label=dock.role=cloudflared)" ]]; then
    say "==> cloudflare tunnel: Dock runs it now; removing dock-cloudflared.service"
    systemctl disable --now dock-cloudflared.service 2>/dev/null || true
    rm -f /etc/systemd/system/dock-cloudflared.service "$TUNNEL_ENV" /usr/local/bin/cloudflared
    systemctl daemon-reload
    tunnel_unit=false
  fi

  docker_version="$(docker version --format '{{.Server.Version}}' 2>/dev/null || true)"
  if [[ "${docker_version%%.*}" =~ ^[0-9]+$ ]] && (( ${docker_version%%.*} < 28 )); then
    say "warning: Docker $docker_version is older than 28. Before 28, ports published on"
    say "         127.0.0.1 are reachable from the LAN. Upgrade Docker."
  fi

  # What this run still needs from the network, before anything of Dock's
  # on this server changes: a download that fails leaves it as it was, and
  # says so.
  prefetched="$(mktemp -d)"
  caddy_new=""
  caddy_name="caddy $CADDY_VERSION"
  if [[ -n "${DOCK_CADDY:-}" ]]; then
    # Dock's Caddy (ADR 0106), checked with its release or by the updater.
    [[ -f "$DOCK_CADDY" ]] || die "$DOCK_CADDY is not a file"
    caddy_name="Dock's caddy"
    cmp -s "$DOCK_CADDY" /usr/local/bin/caddy || caddy_new="$DOCK_CADDY"
  elif ! /usr/local/bin/caddy version 2>/dev/null | grep -q "v$CADDY_VERSION"; then
    say "==> download caddy $CADDY_VERSION"
    base="https://github.com/caddyserver/caddy/releases/download/v${CADDY_VERSION}"
    tarball="caddy_${CADDY_VERSION}_linux_${ARCH}.tar.gz"
    # By a checksum kept here, as cloudflared's is: the release's own
    # checksum file comes from the same place as the tarball. CI installs
    # the same tarball by the same checksum (ci.yml; a test keeps the two
    # in step).
    case "$CADDY_VERSION/$ARCH" in
      2.11.7/amd64) caddy_sha256=727b91701a392de6ebc5027509f548bf39979e5216340d0faed8fa5e69c84f8b ;;
      2.11.7/arm64) caddy_sha256=d8fc6d179a5d283028a472a5618564f6ad8a86fed513e64f032b3b0b7cc45e42 ;;
      *) caddy_sha256="${CADDY_SHA256:-}" ;;
    esac
    [[ -n "$caddy_sha256" ]] \
      || die "set CADDY_SHA256 to the SHA-256 of $tarball from Caddy's $CADDY_VERSION release"
    curl --proto '=https' --proto-redir '=https' -fsSLo "$prefetched/$tarball" "$base/$tarball" \
      || die "could not download Caddy $CADDY_VERSION from github.com (above), so nothing of Dock's was changed. Check that this server reaches github.com and run this again"
    printf '%s  %s\n' "$caddy_sha256" "$prefetched/$tarball" | sha256sum --check --quiet --strict \
      || die "$tarball does not match its checksum"
    tar -xzf "$prefetched/$tarball" -C "$prefetched" caddy
    caddy_new="$prefetched/caddy"
  fi
  cloudflared_new=""
  if [[ "$tunnel_unit" == true ]] \
    && ! /usr/local/bin/cloudflared --version 2>/dev/null | grep -q "version $CLOUDFLARED_VERSION "; then
    say "==> download cloudflared $CLOUDFLARED_VERSION"
    curl --proto '=https' --proto-redir '=https' -fsSLo "$prefetched/cloudflared" \
      "https://github.com/cloudflare/cloudflared/releases/download/${CLOUDFLARED_VERSION}/cloudflared-linux-${ARCH}" \
      || die "could not download cloudflared $CLOUDFLARED_VERSION from github.com (above), so nothing of Dock's was changed. Check that this server reaches github.com and run this again"
    # Cloudflare publishes checksums only in the release notes, so the
    # default version's are pinned above.
    printf '%s  %s\n' "$cloudflared_sha256" "$prefetched/cloudflared" | sha256sum --check --quiet --strict \
      || die "cloudflared-linux-$ARCH does not match the expected SHA-256"
    cloudflared_new="$prefetched/cloudflared"
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
  # Each service's container address, which dock writes and Caddy reads per
  # request, so a traffic switch reloads no config (ADR 0091). setgid: the
  # files dock writes get Caddy's group.
  install -d -m 2750 -o dock -g dock-caddy /var/lib/dock-upstreams

  say "==> dock binary"
  install -m 0755 "$DOCK_BINARY" /usr/local/bin/dock.new
  mv /usr/local/bin/dock.new /usr/local/bin/dock

  # The server is gone: the archive comes out of the backup bucket, with
  # the binary just installed and Docker (ensure_prereqs). Its questions go
  # to the terminal, as this script's do; without one, what is missing in
  # DOCK_RESTORE_* is an error.
  if [[ "$from_bucket" == true ]]; then
    say "==> fetch the control plane backup"
    if [[ "$assume_yes" == false ]] && tty_open; then
      /usr/local/bin/dock admin fetch-backup -o "$restore" <&3 \
        || die "fetching the backup failed (see above); nothing was restored, run the same command again"
      exec 3<&-
    else
      /usr/local/bin/dock admin fetch-backup -o "$restore" < /dev/null \
        || die "fetching the backup failed (see above); nothing was restored, run the same command again"
    fi
    fetched=$restore
  fi

  say "==> $caddy_name"
  caddy_changed=false
  if [[ -n "$caddy_new" ]]; then
    install -m 0755 "$caddy_new" /usr/local/bin/caddy.new
    mv /usr/local/bin/caddy.new /usr/local/bin/caddy
    caddy_changed=true
  fi

  tunnel_changed=false
  if [[ "$tunnel_unit" == true ]]; then
    say "==> cloudflared $CLOUDFLARED_VERSION"
    if [[ -n "$cloudflared_new" ]]; then
      install -m 0755 "$cloudflared_new" /usr/local/bin/cloudflared.new
      mv /usr/local/bin/cloudflared.new /usr/local/bin/cloudflared
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

  # A registry this run turns on is new: install.sh may ask where it keeps
  # its images (ask_registry_storage).
  registry_was_enabled=false
  if [[ -f "$CONF" && "$(toml_get "$CONF" registry enabled)" == true ]]; then
    registry_was_enabled=true
  fi
  case "${REGISTRY_STORAGE:-}" in
    ""|local|s3) ;;
    *) die "REGISTRY_STORAGE must be local or s3" ;;
  esac
  [[ -z "$restore" || -z "${REGISTRY_STORAGE:-}" ]] \
    || die "a restored control plane keeps its registry storage from the backup; leave out REGISTRY_STORAGE (dock registry migrate moves the images later)"

  say "==> config"
  # fresh: this run writes config.toml for the first time (or restores).
  fresh=false
  # 0751: Caddy (dock-caddy) reads caddy.json in here; config.toml stays 0640.
  install -d -m 0751 -o root -g dock /etc/dock
  # Changes go to a staged copy first and only replace config.toml once the
  # new binary accepts it.
  staged="$(mktemp /etc/dock/.config.toml.XXXXXX)"
  if [[ -f "$CONF" && -z "$restore" ]]; then
    cat "$CONF" > "$staged"
  else
    fresh=true
    public_ip=""
    if [[ "$tunnel" == false ]]; then
      public_ip="$(public_address)"
    fi
    if [[ -z "${BASE_DOMAIN+x}" && -z "$restore" && "$node_mode" == false ]]; then
      BASE_DOMAIN="$(ask_base_domain "$public_ip")"
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
# 0: half this server's cores, at least two. Builds run at a quarter of
# the CPU weight of apps, so apps win any contention either way.
cpus = 0

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
# Its images, unless they are in a bucket (dock registry migrate).
storage_dir = "/var/lib/dock-registry"
config_dir = "/etc/dock/registry"
# The registry's name: the audience of its tokens.
public_name = ""
CONF
    fi
  fi

  # A restore keeps the old control plane's config, with the machine's own
  # keys from the new install written above (ADR 0021). The base domain and
  # plain HTTP go with the server's role, so they are kept only when this
  # server takes the old one's place, unless given.
  old_config=""
  if [[ -n "$restore" ]]; then
    old_config="$(mktemp)"
    /usr/local/bin/dock admin restore --print-config "$restore" > "$old_config" \
      || die "$restore is not a backup this dock can restore (see above)"
    if [[ -s "$old_config" ]]; then
      keys=("${MACHINE_KEYS[@]}")
      # From the bucket, the old allow list stays: the recovery's restore
      # jobs must reach a backup store on a private address (a NAS), which
      # a fresh install's empty list would block.
      if [[ "$from_bucket" == true ]]; then
        kept=()
        for k in "${keys[@]}"; do [[ "$k" == network:allow_private ]] || kept+=("$k"); done
        keys=("${kept[@]}")
      fi
      [[ "$old_server_node" == false && -z "${BASE_DOMAIN+x}" ]] || keys+=(:base_domain)
      [[ "$old_server_node" == false && -z "${DISABLE_TLS:-}" ]] || keys+=(caddy:disable_tls)
      merged="$(mktemp /etc/dock/.config.toml.XXXXXX)"
      cat "$old_config" > "$merged"
      for k in "${keys[@]}"; do
        v="$(toml_get_raw "$staged" "${k%%:*}" "${k#*:}")"
        [[ -z "$v" ]] || toml_set "$merged" "${k%%:*}" "${k#*:}" "$v"
      done
      # Nodes need an address here; the old one is on the old server.
      old_node_listen="$(toml_get "$old_config" node listen)"
      # NODE_LISTEN set, even to empty (no nodes), decides below.
      if [[ -z "${NODE_LISTEN+x}" ]]; then
        if [[ "$old_node_listen" == :* || "$old_node_listen" == 0.0.0.0:* || "$old_node_listen" == "[::]:"* ]]; then
          NODE_LISTEN="$old_node_listen" # every interface: the same here
        elif [[ -n "$old_node_listen" ]]; then
          ts_ip="$(tailscale ip -4 2>/dev/null | head -1 || true)"
          [[ -n "$ts_ip" ]] || die "the old control plane took nodes on $old_node_listen; set NODE_LISTEN to this server's address for them, e.g. 100.64.0.20:7443"
          NODE_LISTEN="$ts_ip:${old_node_listen##*:}"
        fi
        toml_set "$merged" node listen "\"${NODE_LISTEN:-}\""
      fi
      # [node] addrs named the old server's addresses; this one's are
      # found again (ADR 0102).
      old_addrs="$(toml_get_raw "$old_config" node addrs)"
      if [[ -n "$old_addrs" && ! "$old_addrs" =~ ^\[[[:space:]]*\]$ ]]; then
        toml_unset "$merged" node addrs
        say "  [node] addrs named the old server's addresses; cleared, so nodes get this server's"
      fi
      if [[ "$(toml_get "$old_config" ingress mode)" == cloudflare_tunnel && "$tunnel" == false ]]; then
        say "warning: the old control plane served apps through a Cloudflare Tunnel and this one serves them directly."
        say "         Point their DNS at this server, or re-run with CLOUDFLARE_TUNNEL_TOKEN (the same tunnel keeps DNS as it is)."
      fi
      mv "$merged" "$staged"
    fi
    if [[ "$old_server_node" == true && -z "${NODE_LISTEN:-}" ]]; then
      die "--old-server-becomes-node needs nodes to reach this server: set NODE_LISTEN, e.g. \"\$(tailscale ip -4):7443\""
    fi
  fi
  if [[ "$convert" == true ]]; then
    # A node serves the registry on [registry] listen itself, and takes no nodes.
    toml_set "$staged" registry enabled false
    toml_set "$staged" node listen '""'
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
  # Set, even to empty (no nodes), NODE_LISTEN replaces [node] listen.
  if [[ -n "${NODE_LISTEN+x}" && "$node_mode" == false ]]; then
    toml_set "$staged" node listen "\"$NODE_LISTEN\""
  fi
  # A new control plane takes nodes on every interface (ADR 0102): they
  # dial its Tailscale address when they can, else its public one. The link
  # pins keys, so the open port exposes nothing. NODE_LISTEN= turns it off.
  if [[ "$fresh" == true && -z "$restore" && "$node_mode" == false && -z "${NODE_LISTEN+x}" ]]; then
    toml_set "$staged" node listen '":7443"'
  fi
  if [[ "${REGISTRY:-}" == 1 ]]; then
    toml_set "$staged" registry enabled true
    # The name only has to be set; this one is as good as any.
    if [[ -z "${REGISTRY_NAME:-}" && -z "$(toml_get "$staged" registry public_name)" ]]; then
      REGISTRY_NAME=dock-registry
    fi
  fi
  [[ -z "${REGISTRY_NAME:-}" ]] || toml_set "$staged" registry public_name "\"$REGISTRY_NAME\""
  [[ -z "${REGISTRY_LISTEN:-}" ]] || toml_set "$staged" registry listen "\"$REGISTRY_LISTEN\""
  refresh_buildkit_image "$staged"

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

  retired=""
  if [[ "$convert" == true ]]; then
    say "==> retire the control plane"
    systemctl disable --now dock.service 2>/dev/null || true
    rm -f /etc/systemd/system/dock.service
    # The node serves the registry on [registry] listen itself.
    docker rm -f dock-registry >/dev/null 2>&1 || true
    # Kept until the new control plane checks out; Railpack's cache stays.
    retired="/var/lib/dock-retired-$(date +%Y%m%d-%H%M%S)"
    install -d -m 0700 "$retired"
    find /var/lib/dock -mindepth 1 -maxdepth 1 ! -name railpack -exec mv -t "$retired" {} +
  fi

  say "==> systemd"
  if [[ "$node_mode" == true ]]; then
    daemon=dock-node.service
  else
    daemon=dock.service
  fi
  units=("$daemon" dock-caddy.service dock-firewall.service dock-update.path dock-update.service)
  [[ "$tunnel_unit" == false ]] || units+=(dock-cloudflared.service)
  caddy_unit_changed=false
  cmp -s "$SRC_DIR/systemd/dock-caddy.service" /etc/systemd/system/dock-caddy.service || caddy_unit_changed=true
  if [[ "$tunnel_unit" == true ]]; then
    cmp -s "$SRC_DIR/systemd/dock-cloudflared.service" /etc/systemd/system/dock-cloudflared.service || tunnel_changed=true
  fi
  for unit in "${units[@]}"; do
    install -m 0644 "$SRC_DIR/systemd/$unit" "/etc/systemd/system/$unit"
  done
  systemctl daemon-reload
  # The daemon stages releases and asks for updates in here; dock-update.path
  # starts the updater when it does (ADR 0028).
  install -d -m 0700 -o dock -g dock /var/lib/dock /var/lib/dock/update
  systemctl enable --now dock-update.path

  say "==> firewall"
  systemctl enable dock-firewall.service
  systemctl restart dock-firewall.service \
    || die "the app firewall failed to apply; see: journalctl -u dock-firewall"

  systemctl enable dock-caddy.service
  # Caddy's own build can't resume a saved config with Dock's handlers
  # (ADR 0106) and wouldn't start: it starts on caddy.json instead, and the
  # daemon loads the routes again. Judged from what is on disk, so a run
  # that failed after putting Caddy's own build in place is caught by the
  # next. Stopped before the file goes, so the running Caddy can't save a
  # route load in between; started just below.
  autosave=/var/lib/dock-caddy/caddy/autosave.json
  if grep -q '"dock_inject"' "$autosave" 2>/dev/null \
    && ! /usr/local/bin/caddy list-modules 2>/dev/null | grep -q '^http\.handlers\.dock_inject$'; then
    systemctl stop dock-caddy.service 2>/dev/null || true
    rm -f "$autosave"
  fi
  if [[ "$caddy_unit_changed" == true || "$caddy_changed" == true ]] && systemctl is-active -q dock-caddy.service; then
    # Picks up a new binary, and unit changes such as the dock-caddy
    # user. Routes come back from --resume; traffic blips for a moment.
    # Listener changes (tunnel mode's 127.0.0.1) need no restart: the
    # daemon loads them on start, after this.
    say "    restarting caddy (binary or unit changed)"
    systemctl restart dock-caddy.service
  else
    systemctl start dock-caddy.service
  fi

  if [[ "$tunnel_unit" == true ]]; then
    say "==> cloudflare tunnel"
    systemctl enable dock-cloudflared.service
    if [[ "$tunnel_changed" == true ]]; then
      systemctl restart dock-cloudflared.service
    else
      systemctl start dock-cloudflared.service
    fi
  fi

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
    if [[ -n "$restore" ]]; then
      say "==> restore"
      systemctl stop dock.service 2>/dev/null || true
      [[ "$(toml_get "$CONF" registry enabled)" != true ]] || registry_storage >/dev/null
      restore_args=(admin restore --config "$CONF")
      [[ "$old_server_node" == false ]] || restore_args+=(--old-server-becomes-node)
      # Apps and their volumes are gone too: the daemon starts in recovery
      # mode and waits for `dock admin recover` (ADR 0098).
      [[ "$from_bucket" == false ]] || restore_args+=(--recovery)
      /usr/local/bin/dock "${restore_args[@]}" "$restore" \
        || die "the restore failed and dock was not started; fix the cause and run the same command again"
      if [[ -s "$old_config" ]]; then
        install -m 0640 -o root -g dock "$old_config" /etc/dock/config.from-backup.toml
      fi
    fi
    if [[ "$(toml_get "$CONF" registry enabled)" == true ]]; then
      registry_storage >/dev/null # the daemon writes into its config dir
      if [[ -z "$restore" && "$registry_was_enabled" == false ]]; then
        ask_registry_storage
      fi
      if [[ "${REGISTRY_STORAGE:-}" == s3 ]]; then
        say "==> registry storage"
        set_registry_storage
      fi
    elif [[ -n "${REGISTRY_STORAGE:-}" ]]; then
      die "REGISTRY_STORAGE is for the registry; set REGISTRY=1 too"
    fi
    systemctl enable dock.service
    systemctl restart dock.service
    if [[ "$(toml_get "$CONF" registry enabled)" == true ]]; then
      say "==> registry"
      setup_registry
    fi
    say "==> admin"
    # dock.service migrates the database as it starts: wait until it
    # answers, so bootstrap doesn't meet it in SQLite's write lock.
    api="$(toml_get "$CONF" "" listen)"
    for _ in $(seq 90); do
      curl -fsS -o /dev/null --max-time 2 "http://$api/api/v1/health" 2> /dev/null && break
      sleep 1
    done
    curl -fsS -o /dev/null --max-time 2 "http://$api/api/v1/health" 2> /dev/null \
      || die "dock didn't start answering on $api; see: journalctl -u dock"
    admin_email="${DOCK_ADMIN_EMAIL:-admin@localhost}"
    if [[ "$fresh" == true && -z "$restore" && -z "${DOCK_ADMIN_EMAIL:-}" ]]; then
      admin_email="$(ask "Your email, to sign in to the console [admin@localhost]:" admin@localhost)"
    fi
    # Prints nothing (and succeeds) when an admin already exists. The
    # generated password comes on stderr, for the summary below.
    bootstrap_err="$(mktemp)"
    token="$(runuser -u dock -- /usr/local/bin/dock admin bootstrap --config "$CONF" --email "$admin_email" 2> "$bootstrap_err")" ||
      { cat "$bootstrap_err" >&2; die "creating the first admin failed (see above); dock itself is installed and running"; }
    admin_password="$(sed -n 's/.*with password \([^ ]*\) .*/\1/p' "$bootstrap_err")"
  fi

  say ""
  if [[ "$node_mode" == true ]]; then
    say "Tempo Dock node is installed and connecting to the control plane."
    say "  Check it there:  dock server list"
    say "  Logs here:       journalctl -u dock-node -f"
    if [[ -n "$retired" ]]; then
      say "  The old control plane's data is in $retired. It holds every secret of the"
      say "  platform: delete it once the new control plane checks out."
    fi
  else
    summary
    node_listen="$(toml_get "$CONF" node listen)"
    if [[ "$node_listen" == :* || "$node_listen" == 0.0.0.0:* || "$node_listen" == "[::]:"* ]]; then
      say "  Nodes connect on port ${node_listen##*:}: open it in this server's firewall for nodes"
      say "  outside its Tailscale network or LAN. Add a server: dock server add <name>"
    elif [[ -n "$node_listen" ]]; then
      say "  Nodes connect to $node_listen. Add a server: dock server add <name>"
    fi
    if [[ -n "$restore" ]]; then
      say "  Restored from $restore."
      [[ ! -f /etc/dock/config.from-backup.toml ]] || say "  The old config.toml is in /etc/dock/config.from-backup.toml for reference."
      if [[ "$node_listen" == :* || "$node_listen" == 0.0.0.0:* || "$node_listen" == "[::]:"* ]]; then
        say "  On each node that should follow: sudo dock node set-control-plane <this server's address>:${node_listen##*:}"
      elif [[ -n "$node_listen" ]]; then
        say "  On each node that should follow: sudo dock node set-control-plane $node_listen"
      fi
      if [[ "$from_bucket" == true ]]; then
        fetched="" # the install is through: the archive stays until the user deletes it
        say "  The control plane is back; the apps are not yet. Next:"
        say "    sudo dock admin recover     shows what will be restored, service by service"
        say "  $restore holds every secret of the platform: delete it once this install works."
        say "  Steps: docs/guide/install.md#the-server-is-gone"
      else
        say "  Next steps: docs/guide/install.md#moving-the-control-plane"
      fi
    fi
    if [[ "$(toml_get "$CONF" registry enabled)" == true ]]; then
      registry_listen="$(toml_get "$CONF" registry listen)"
      say "  Registry: $(toml_get "$CONF" registry public_name) (container dock-registry on ${registry_listen:-127.0.0.1:5000})"
      say "  Its images: dock registry (dock registry migrate moves them to a bucket or back)"
    fi
  fi
  say ""
  if [[ "$tunnel" == true && "$tunnel_unit" == false ]]; then
    say "Public traffic arrives through a Cloudflare Tunnel that Dock creates (or adopts) and runs"
    say "in its dock-cloudflared container; no inbound ports are needed. On the control plane:"
    say "  dock server tunnel create|adopt <server>   (automatic for dock server add --tunnel)"
    say "Steps: docs/guide/install.md#cloudflare-tunnel"
  elif [[ "$tunnel" == true ]]; then
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
}

main "$@"
