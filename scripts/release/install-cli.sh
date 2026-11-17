#!/bin/sh
# Installs the dock CLI (the dock-cli build: the client without the daemon)
# on macOS or Linux from a GitHub release, checked against its SHA256SUMS
# and that file's signature (ADR 0088).
#
#   curl -fsSL https://github.com/NordTempo/tempoDock/releases/latest/download/install-cli.sh | sh
#   curl -fsSL https://github.com/NordTempo/tempoDock/releases/download/v0.2.0/install-cli.sh | DOCK_VERSION=v0.2.0 sh
#
# Once installed, `dock self-update` keeps it in step with your server.
#
# Environment:
#   DOCK_VERSION      release tag, e.g. v0.2.0 (default: the latest release)
#   DOCK_INSTALL_DIR  where `dock` goes (default: /usr/local/bin as root, else ~/.local/bin)
#   DOCK_REQUIRE_SIGNATURE  "1" fails when the signature can't be checked here
#                     (it needs OpenSSL 3; macOS ships LibreSSL, so `brew install
#                     openssl@3` and put it first in PATH)
set -eu

repo=NordTempo/tempoDock
version="${DOCK_VERSION:-latest}"
# Dock's release signing keys, as in internal/update/sign.go: "<key id>
# <raw Ed25519 public key, base64>". Releases from first_signed on are signed.
release_keys='bc6c45a08a65c136 8uQjZq1kO37ezVv7EzN8eyrqOYXRGK2nsCRL/d3RSdU='
first_signed=v0.56.0

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
warn() { printf 'warning: %s\n' "$*" >&2; }

# signature_required VERSION: is VERSION (vX.Y.Z, pre-releases as their
# X.Y.Z) first_signed or newer?
signature_required() {
  printf '%s %s\n' "${1#v}" "${first_signed#v}" | awk '{
    split($1, a, /[.-]/); split($2, b, /[.-]/)
    for (i = 1; i <= 3; i++) { if (a[i] + 0 > b[i] + 0) exit 0; if (a[i] + 0 < b[i] + 0) exit 1 }
    exit 0 }'
}

# verify_sums DIR VERSION checks DIR/SHA256SUMS against DIR/SHA256SUMS.sig
# (empty when the release has none) with openssl, as dock does.
verify_sums() {
  if [ ! -s "$1/SHA256SUMS.sig" ]; then
    signature_required "$2" && die "release $2 has no SHA256SUMS.sig; releases from $first_signed on are signed, so don't install this one: it may have been tampered with"
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

if [ "$version" = latest ]; then
  # .../releases/latest redirects to .../releases/tag/<version>.
  url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest")" \
    || die "could not find the latest release of $repo"
  version="${url##*/}"
fi
base="https://github.com/$repo/releases/download/$version"
for name in "$asset" SHA256SUMS; do
  curl -fsSL -o "$tmp/$name" "$base/$name" || die "could not download $name of release $version from $repo"
done
# Releases before the first signed one have no signature: a 404 (curl's
# exit 22) leaves the file empty and verify_sums decides.
rc=0
curl -fsSL -o "$tmp/SHA256SUMS.sig" "$base/SHA256SUMS.sig" 2>/dev/null || rc=$?
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
  *) printf 'Add %s to your PATH.\n' "$dir" ;;
esac
