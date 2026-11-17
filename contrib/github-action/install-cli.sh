#!/bin/sh
# Installs the dock CLI (the dock-cli build: the client without the daemon)
# on macOS or Linux from a GitHub release, checked against its SHA256SUMS
# and that file's signature (ADR 0088).
#
#   curl -fsSL https://dock.nordtempo.com/cli.sh | sh
#   curl -fsSL https://dock.nordtempo.com/cli.sh | sh -s -- https://dock.example.com
#   curl -fsSL https://dock.nordtempo.com/cli.sh | DOCK_VERSION=v0.58.0 sh
#
# With a server's address it installs the version that server runs and
# then signs in to it (`dock login`), which is the line the console and
# install.sh print. Once installed, `dock self-update` keeps it in step
# with your server. Windows: run it in WSL.
#
# The latest release and a server's version must be signed. A release from
# before signing (v0.56.0) installs only when you name it yourself with
# DOCK_VERSION: with a warning, checked against its SHA256SUMS alone.
#
# Environment:
#   DOCK_VERSION      release tag, e.g. v0.58.0 (default: the latest release)
#   DOCK_INSTALL_DIR  where `dock` goes (default: /usr/local/bin as root, else ~/.local/bin)
#   DOCK_REQUIRE_SIGNATURE  "1" installs nothing without a signature checked
#                     here: no release from before signing, and none when the
#                     check can't run (it needs OpenSSL 3; macOS ships LibreSSL,
#                     so `brew install openssl@3` and put it first in PATH)
set -eu

repo=NordTempo/tempoDock
version="${DOCK_VERSION:-latest}"
# The server to install for and sign in to, if given.
server="${1:-}"
# pinned: the person named the release (DOCK_VERSION) instead of taking the
# latest or the one a server runs. Only a pinned release may predate
# signing. from says where the version came from, for messages.
pinned=false
from="DOCK_VERSION"
[ "$version" = latest ] || pinned=true
# Dock's release signing keys, as in internal/update/sign.go: "<key id>
# <raw Ed25519 public key, base64>". Releases from first_signed on are signed.
release_keys='bc6c45a08a65c136 8uQjZq1kO37ezVv7EzN8eyrqOYXRGK2nsCRL/d3RSdU='
first_signed=v0.56.0

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
warn() { printf 'warning: %s\n' "$*" >&2; }

# github CURL-ARGS... downloads from GitHub: https only, redirects included.
github() { curl --proto '=https' --proto-redir '=https' -fsSL "$@"; }

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

# signature_required VERSION: is VERSION (vX.Y.Z, pre-releases as their
# X.Y.Z) first_signed or newer?
signature_required() {
  printf '%s %s\n' "${1#v}" "${first_signed#v}" | awk '{
    split($1, a, /[.-]/); split($2, b, /[.-]/)
    for (i = 1; i <= 3; i++) { if (a[i] + 0 > b[i] + 0) exit 0; if (a[i] + 0 < b[i] + 0) exit 1 }
    exit 0 }'
}

# verify_sums DIR VERSION checks DIR/SHA256SUMS against DIR/SHA256SUMS.sig
# (empty when the release has none) with openssl, as dock does. A release
# from before signing passes on its SHA256SUMS alone only when the person
# pinned it and DOCK_REQUIRE_SIGNATURE isn't 1.
verify_sums() {
  if [ ! -s "$1/SHA256SUMS.sig" ]; then
    signature_required "$2" && die "release $2 has no SHA256SUMS.sig; releases from $first_signed on are signed, so don't install this one: it may have been tampered with"
    [ "${DOCK_REQUIRE_SIGNATURE:-}" != 1 ] || die "release $2 predates signed releases ($first_signed) and has no signature to check; DOCK_REQUIRE_SIGNATURE=1 installs signed releases only. Install $first_signed or later, or leave DOCK_REQUIRE_SIGNATURE out to take $2 on its SHA256SUMS alone"
    [ "$pinned" = true ] || die "release $2 ($from) predates signed releases ($first_signed) and has no signature to check, so it isn't installed unasked. To install it anyway, checked against its SHA256SUMS alone, name it yourself: curl -fsSL https://dock.nordtempo.com/cli.sh | DOCK_VERSION=$2 sh"
    warn "release $2 predates signed releases ($first_signed); checked against its SHA256SUMS only"
    return 0
  fi
  grep -q "  dock-install-$2.tar.gz\$" "$1/SHA256SUMS" || die "SHA256SUMS belongs to another release than $2"
  if ! openssl pkeyutl -help 2>&1 | grep -q -- -rawin; then
    [ "${DOCK_REQUIRE_SIGNATURE:-}" != 1 ] || die "can't check the release's signature: it needs OpenSSL 3 (openssl pkeyutl -rawin)"
    warn "can't check the release's signature without OpenSSL 3; checked against its SHA256SUMS only. To check where it was built: gh attestation verify <file> --repo $repo"
    return 0
  fi
  echo "$release_keys" | while read -r id key; do
    line="$(grep "^$id " "$1/SHA256SUMS.sig")" || continue
    printf '%s' "${line#* }" | openssl base64 -d -A > "$1/sig.bin" || continue
    printf -- '-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEA%s\n-----END PUBLIC KEY-----\n' "$key" > "$1/key.pem"
    if openssl pkeyutl -verify -pubin -inkey "$1/key.pem" -rawin -in "$1/SHA256SUMS" -sigfile "$1/sig.bin" >/dev/null 2>&1; then
      echo ok
      break
    fi
  done | grep -q ok || die "release $2's SHA256SUMS isn't signed by a Dock release key; don't install it: it may have been tampered with"
}

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  MINGW* | MSYS* | CYGWIN*) die "dock runs on Windows in WSL: open a WSL terminal (wsl --install) and run this there" ;;
  *) die "no dock-cli build for $(uname -s); build from source: make build-cli" ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) die "no dock-cli build for $(uname -m); build from source: make build-cli" ;;
esac
asset="dock-cli-$os-$arch"

if [ -n "${DOCK_INSTALL_DIR:-}" ]; then
  dir="$DOCK_INSTALL_DIR"
elif [ "$(id -u)" = 0 ]; then
  dir=/usr/local/bin
else
  dir="$HOME/.local/bin"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# A server's version, from its health endpoint, unless DOCK_VERSION says
# otherwise. Development builds have no release: the latest is used then.
if [ -n "$server" ]; then
  server="${server%/}"
  case "$server" in
    http://* | https://*) ;;
    *) server="https://$server" ;;
  esac
  if [ -z "${DOCK_VERSION:-}" ]; then
    health="$(curl -fsS --max-time 10 "$server/api/v1/health")" \
      || die "could not reach $server; check the address (and the SSH tunnel, if the console is on localhost)"
    v="$(printf '%s' "$health" | sed -n 's/.*"version" *: *"\(v[0-9][0-9]*\.[0-9][0-9]*\.[0-9][^"]*\)".*/\1/p')"
    if [ -n "$v" ]; then
      version="$v" from="the version $server runs"
    else
      warn "$server runs a development build; installing the latest release"
    fi
  fi
fi

if [ "$version" = latest ]; then
  # .../releases/latest redirects to .../releases/tag/<version>.
  url="$(github -I -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest")" \
    || die "could not find the latest release of $repo"
  version="${url##*/}" from="the latest release of $repo"
fi
# The one place a version becomes part of a URL, whatever named it.
release_version "$version" \
  || die "\"$(shown "$version")\" ($from) is not a release version like v0.58.0, so nothing was downloaded. To install a release you choose: curl -fsSL https://dock.nordtempo.com/cli.sh | DOCK_VERSION=v0.58.0 sh"
base="https://github.com/$repo/releases/download/$version"
for name in "$asset" SHA256SUMS; do
  github -o "$tmp/$name" "$base/$name" || die "could not download $name of release $version from $repo"
done
# Releases before the first signed one have no signature: a 404 (curl's
# exit 22) leaves the file empty and verify_sums decides.
rc=0
github -o "$tmp/SHA256SUMS.sig" "$base/SHA256SUMS.sig" 2>/dev/null || rc=$?
case $rc in
  0) ;;
  22) : > "$tmp/SHA256SUMS.sig" ;;
  *) die "could not download SHA256SUMS.sig of release $version from $repo" ;;
esac
verify_sums "$tmp" "$version"

grep "  $asset\$" "$tmp/SHA256SUMS" > "$tmp/sum" || die "SHA256SUMS has no entry for $asset"
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$tmp" && sha256sum -c --quiet sum) || die "$asset does not match SHA256SUMS"
else
  (cd "$tmp" && shasum -a 256 -c --quiet sum) || die "$asset does not match SHA256SUMS"
fi

mkdir -p "$dir"
install -m 0755 "$tmp/$asset" "$dir/dock"
printf 'Installed %s to %s/dock\n' "$("$dir/dock" --version)" "$dir"
case ":$PATH:" in
  *":$dir:"*) ;;
  *)
    case "${SHELL:-}" in
      */zsh) rc="$HOME/.zshrc" ;;
      */bash) rc="$HOME/.bashrc" ;;
      *) rc='your shell profile' ;;
    esac
    printf '%s is not in your PATH. Add it (to %s too, for new terminals):\n  export PATH="%s:$PATH"\n' "$dir" "$rc" "$dir"
    ;;
esac

# Sign in to the server, on the terminal: stdin is this script when piped.
if [ -n "$server" ]; then
  if (: < /dev/tty) 2> /dev/null; then
    "$dir/dock" login "$server" < /dev/tty
  else
    printf 'Sign in with: dock login %s\n' "$server"
  fi
fi
