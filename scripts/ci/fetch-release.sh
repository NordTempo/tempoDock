#!/bin/sh
# Fetches a GitHub release asset for CI, checks it against its SHA-256 (the
# asset's digest on GitHub) and prints the path of the checked file.
#
#   tarball=$(scripts/ci/fetch-release.sh caddyserver/caddy v2.11.7 \
#     caddy_2.11.7_linux_amd64.tar.gz <sha256>)
#
# The self-hosted runners (Zoomies) mount /opt/zoomies-cache, which outlives
# each ephemeral runner, so an asset is downloaded once per host. Without the
# cache (Blacksmith, a laptop) it is downloaded into RUNNER_TEMP every time.
# Release downloads fail often enough (502s, 503s, 504s) that curl retries,
# then gh, authenticated with GH_TOKEN, gets a turn.
set -eu

[ $# -eq 4 ] || { echo "usage: $0 OWNER/REPO TAG ASSET SHA256" >&2; exit 2; }
repo=$1 tag=$2 asset=$3 sha=$4

matches() { echo "$sha  $1" | sha256sum -c --status 2>/dev/null; }

# Keyed by digest: a new version is a new folder, and a file is checked on
# every hit, so a damaged cache only costs a download. FETCH_RELEASE_CACHE
# points it elsewhere, e.g. to try the script locally.
cache=${FETCH_RELEASE_CACHE:-/opt/zoomies-cache}/releases/$sha
if [ -f "$cache/$asset" ] && matches "$cache/$asset"; then
  echo "$cache/$asset"
  exit 0
fi

tmp=$(mktemp -d "${RUNNER_TEMP:-/tmp}/fetch-release.XXXXXX")
# Each attempt is capped: a struggling release CDN answers 503 after 15 to
# 45 seconds or not at all, and gh's authenticated route is the better bet.
if ! curl -fsSL --connect-timeout 10 --max-time 60 --retry 2 --retry-all-errors \
  --retry-delay 3 -o "$tmp/$asset" \
  "https://github.com/$repo/releases/download/$tag/$asset"; then
  echo "curl could not download $asset; trying gh" >&2
  gh release download "$tag" -R "$repo" -p "$asset" -D "$tmp" --clobber
fi
matches "$tmp/$asset" || { echo "error: $asset does not match sha256 $sha" >&2; exit 1; }

# Published by rename, so a job running alongside sees all of it or none.
part="$cache/.$asset.$$"
if mkdir -p "$cache" 2>/dev/null && cp "$tmp/$asset" "$part" 2>/dev/null && mv "$part" "$cache/$asset"; then
  rm -rf "$tmp"
  echo "$cache/$asset"
else
  rm -f "$part" 2>/dev/null || true
  echo "$tmp/$asset"
fi
