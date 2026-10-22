#!/usr/bin/env bash
# Builds every release artifact into dist/ from a built UI (web/dist):
#
#   dock-linux-{amd64,arm64}            server binary with the UI embedded
#   dock-cli-{darwin,linux}-{arm64,amd64}  CLI only (-tags dockcli)
#   dock-install-VERSION.tar.gz         deploy/ (install.sh, systemd units, VERSION)
#   install-cli.sh                      CLI installer for laptops and CI
#   SHA256SUMS                          checksums of all of the above
#
# Run it through `make release-local VERSION=v0.0.0-dev`, which builds the
# UI first; the release workflow (.github/workflows/release.yml) runs the
# same target and uploads dist/. Builds are reproducible: the same commit
# and toolchain give the same bytes. Needs GNU tar.
set -euo pipefail

version="${1:?usage: scripts/release/build.sh VERSION}"
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
[[ -f web/dist/index.html ]] || { echo "error: build the UI first (make web)" >&2; exit 1; }

# The commit's time, so the tarball is the same on every build.
SOURCE_DATE_EPOCH="$(git log -1 --format=%ct)"
export CGO_ENABLED=0 SOURCE_DATE_EPOCH
ldflags="-s -w -buildid= -X github.com/NordTempo/tempoDock/internal/version.Version=$version"

rm -rf dist
mkdir dist

# build NAME GOOS GOARCH [TAGS] builds ./cmd/dock into dist/NAME.
build() {
  echo "dist/$1"
  GOOS="$2" GOARCH="$3" go build -trimpath -tags "${4:-}" -ldflags "$ldflags" -o "dist/$1" ./cmd/dock
}
for arch in amd64 arm64; do
  build "dock-linux-$arch" linux "$arch"
done
for platform in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
  build "dock-cli-${platform%/*}-${platform#*/}" "${platform%/*}" "${platform#*/}" dockcli
done

echo "dist/dock-install-$version.tar.gz"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
cp -R deploy "$stage/deploy"
# install.sh downloads this version when DOCK_BINARY isn't given.
printf '%s\n' "$version" > "$stage/deploy/VERSION"
tar --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 --numeric-owner \
  --mode='u+rwX,go+rX,go-w' -C "$stage" -cf - deploy | gzip -9n > "dist/dock-install-$version.tar.gz"

cp scripts/release/install-cli.sh dist/
(cd dist && sha256sum -- * > SHA256SUMS)
echo "dist/SHA256SUMS"
