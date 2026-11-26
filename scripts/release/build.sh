#!/usr/bin/env bash
# Builds every release artifact into dist/ from the built UIs (web/dist and
# web/tempobase/dist):
#
#   dock-linux-{amd64,arm64}            server binary with the UI, dock-probe and dock-sync embedded
#   dock-cli-{darwin,linux}-{arm64,amd64}  CLI only (-tags dockcli)
#   dock-caddy-linux-{amd64,arm64}      Caddy with Dock's handlers (cmd/dock-caddy, ADR 0106),
#                                       which install.sh installs as /usr/local/bin/caddy
#   dock-install-VERSION.tar.gz         deploy/ (install.sh, systemd units, VERSION)
#   install.sh                          deploy/install.sh on its own, for `curl | sudo bash`:
#                                       it fetches and checks the rest of its release
#   install-cli.sh                      CLI installer for laptops and CI
#   tempobase-linux-{amd64,arm64}       tempoBase with its UI embedded (ADR 0024);
#                                       the release workflow builds its image from them
#   tempobase-darwin-arm64              tempoBase for local trials on a Mac
#   SHA256SUMS                          checksums of all of the above
#
# Run it through `make release-local VERSION=v0.0.0-dev`, which builds the
# UIs first; the release workflow (.github/workflows/release.yml) runs the
# same target and uploads dist/. Builds are reproducible: the same commit
# and toolchain give the same bytes. Needs GNU tar.
set -euo pipefail

version="${1:?usage: scripts/release/build.sh VERSION}"
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
[[ -f web/dist/index.html ]] || { echo "error: build the UI first (make web)" >&2; exit 1; }
# tempoBase ships once cmd/tempobase exists; then its UI must be built too.
tempobase=false
if [[ -d cmd/tempobase ]]; then
  tempobase=true
  [[ -f web/tempobase/dist/index.html ]] || { echo "error: build tempoBase's UI first (make web)" >&2; exit 1; }
fi

# The commit's time, so the tarball is the same on every build.
SOURCE_DATE_EPOCH="$(git log -1 --format=%ct)"
export CGO_ENABLED=0 SOURCE_DATE_EPOCH
ldflags="-s -w -buildid= -X github.com/NordTempo/tempoDock/internal/version.Version=$version"

rm -rf dist
mkdir dist

# build NAME GOOS GOARCH [TAGS] [PKG] builds PKG (./cmd/dock) into dist/NAME.
build() {
  echo "dist/$1"
  GOOS="$2" GOARCH="$3" go build -trimpath -tags "${4:-}" -ldflags "$ldflags" -o "dist/$1" "${5:-./cmd/dock}"
}
# dock embeds dock-probe (ADR 0043) and dock-sync (ADR 0063) from
# internal/node/probe for its own architecture: build them right before
# each server binary, and remove them after, so no binary embeds another
# architecture's.
# They are embedded gzipped, as `make probe` builds them.
probe=internal/node/probe/bin/dock-probe
sync=internal/node/probe/bin/dock-sync
trap 'rm -f "$probe.gz" "$sync.gz"' EXIT
for arch in amd64 arm64; do
  GOOS=linux GOARCH="$arch" go build -trimpath -ldflags "-s -w -buildid=" -o "$probe" ./cmd/dock-probe
  GOOS=linux GOARCH="$arch" go build -trimpath -ldflags "-s -w -buildid=" -o "$sync" ./cmd/dock-sync
  gzip -9nf "$probe" "$sync"
  build "dock-linux-$arch" linux "$arch"
done
rm -f "$probe.gz" "$sync.gz"
# Dock's Caddy is its own module; the tags are Caddy's release build's.
for arch in amd64 arm64; do
  echo "dist/dock-caddy-linux-$arch"
  (cd cmd/dock-caddy && GOOS=linux GOARCH="$arch" go build -trimpath -buildvcs=false -tags nobadger,nomysql,nopgx \
    -ldflags "-s -w -buildid=" -o "../../dist/dock-caddy-linux-$arch" .)
done
for platform in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
  build "dock-cli-${platform%/*}-${platform#*/}" "${platform%/*}" "${platform#*/}" dockcli
done
if $tempobase; then
  for platform in linux/amd64 linux/arm64 darwin/arm64; do
    build "tempobase-${platform%/*}-${platform#*/}" "${platform%/*}" "${platform#*/}" "" ./cmd/tempobase
  done
fi

echo "dist/dock-install-$version.tar.gz"
stage="$(mktemp -d)"
trap 'rm -rf "$stage" "$probe.gz" "$sync.gz"' EXIT
cp -R deploy "$stage/deploy"
rm -rf "$stage/deploy/tempobase" "$stage/deploy/demo" # tempoBase's and the demo's images, not Dock's install
# install.sh downloads this version when DOCK_BINARY isn't given.
printf '%s\n' "$version" > "$stage/deploy/VERSION"
tar --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 --numeric-owner \
  --mode='u+rwX,go+rX,go-w' -C "$stage" -cf - deploy | gzip -9n > "dist/dock-install-$version.tar.gz"

cp scripts/release/install-cli.sh dist/
sed "s/^RELEASE_VERSION=\"\"\$/RELEASE_VERSION=\"$version\"/" deploy/install.sh > dist/install.sh
grep -q "^RELEASE_VERSION=\"$version\"\$" dist/install.sh || { echo "error: could not pin install.sh to $version" >&2; exit 1; }
chmod 0755 dist/install.sh
(cd dist && sha256sum -- * > SHA256SUMS)
echo "dist/SHA256SUMS"
