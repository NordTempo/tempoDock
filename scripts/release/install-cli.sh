#!/bin/sh
# Installs the dock CLI (the dock-cli build: the client without the daemon)
# on macOS or Linux from a GitHub release, checked against its SHA256SUMS.
#
#   gh release download -R NordTempo/tempoDock -p install-cli.sh -O - | sh
#   gh release download v0.2.0 -R NordTempo/tempoDock -p install-cli.sh -O - | DOCK_VERSION=v0.2.0 sh
#
# Downloads with gh when it is installed and logged in, otherwise with curl
# and GITHUB_TOKEN (the repository is private).
#
# Environment:
#   DOCK_VERSION      release tag, e.g. v0.2.0 (default: the latest release)
#   DOCK_INSTALL_DIR  where `dock` goes (default: /usr/local/bin as root, else ~/.local/bin)
#   GITHUB_TOKEN      a token that can read the repository's releases (curl only)
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

# github GET-ARGS... runs curl against the GitHub API, with the token if set.
github() {
  if [ -n "${GITHUB_TOKEN:-}" ]; then
    curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" "$@"
  else
    curl -fsSL "$@"
  fi
}

# asset_url FILE NAME prints the API URL of asset NAME in a release's JSON.
# In GitHub's responses an asset's "url" comes just before its "name".
asset_url() {
  # shellcheck disable=SC2020 # one newline per character, on purpose
  tr ',{}[]' '\n\n\n\n\n' < "$1" | awk -v name="$2" '
    /"url": *"[^"]*\/releases\/assets\/[0-9]+"/ { u = $0; sub(/^[^:]*: *"/, "", u); sub(/".*$/, "", u) }
    $0 ~ "\"name\": *\"" name "\"" && u != "" { print u; found = 1; exit }
    END { exit !found }'
}

if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
  set --
  [ "$version" = latest ] || set -- "$version"
  gh release download "$@" -R "$repo" -p "$asset" -p SHA256SUMS -D "$tmp" \
    || die "could not download $asset ($version) from $repo"
else
  if [ "$version" = latest ]; then path=releases/latest; else path="releases/tags/$version"; fi
  github "https://api.github.com/repos/$repo/$path" > "$tmp/release.json" \
    || die "could not read release $version of $repo. Set GITHUB_TOKEN to a token that can read it, or log in with gh"
  for name in "$asset" SHA256SUMS; do
    url="$(asset_url "$tmp/release.json" "$name")" || die "release $version has no $name"
    github -H "Accept: application/octet-stream" -o "$tmp/$name" "$url" || die "could not download $name"
  done
fi

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
