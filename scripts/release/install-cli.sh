#!/bin/sh
# Installs the dock CLI (the dock-cli build: the client without the daemon)
# on macOS or Linux from a GitHub release, checked against its SHA256SUMS.
#
#   curl -fsSL https://github.com/NordTempo/tempoDock/releases/latest/download/install-cli.sh | sh
#   curl -fsSL https://github.com/NordTempo/tempoDock/releases/download/v0.2.0/install-cli.sh | DOCK_VERSION=v0.2.0 sh
#
# Once installed, `dock self-update` keeps it in step with your server.
#
# Environment:
#   DOCK_VERSION      release tag, e.g. v0.2.0 (default: the latest release)
#   DOCK_INSTALL_DIR  where `dock` goes (default: /usr/local/bin as root, else ~/.local/bin)
set -eu

repo=NordTempo/tempoDock
version="${DOCK_VERSION:-latest}"

die() { printf 'error: %s\n' "$*" >&2; exit 1; }

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

if [ "$version" = latest ]; then base="https://github.com/$repo/releases/latest/download"; else base="https://github.com/$repo/releases/download/$version"; fi
for name in "$asset" SHA256SUMS; do
  curl -fsSL -o "$tmp/$name" "$base/$name" || die "could not download $name of release $version from $repo"
done

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
