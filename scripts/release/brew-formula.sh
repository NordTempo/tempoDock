#!/usr/bin/env bash
# Prints the Homebrew formula for the dock CLI of release VERSION, with the
# SHA-256 of each dock-cli build from the release's SHA256SUMS. The release
# workflow runs it on the signed SHA256SUMS it just published and commits
# the result to NordTempo/homebrew-tap as Formula/dock.rb, so
# `brew install nordtempo/tap/dock` installs the same bytes the signature
# covers.
#
#   scripts/release/brew-formula.sh v0.56.0 dist/SHA256SUMS > Formula/dock.rb
set -euo pipefail

version="${1:?usage: scripts/release/brew-formula.sh VERSION SHA256SUMS}"
sums="${2:?usage: scripts/release/brew-formula.sh VERSION SHA256SUMS}"
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "error: $version isn't a release version (vX.Y.Z)" >&2; exit 1; }
base="https://github.com/NordTempo/tempoDock/releases/download/$version"

# sum ASSET prints ASSET's SHA-256 from SHA256SUMS.
sum() {
  local s
  s="$(awk -v n="$1" '$2 == n { print $1 }' "$sums")"
  [[ "$s" =~ ^[0-9a-f]{64}$ ]] || { echo "error: $sums has no SHA-256 for $1" >&2; exit 1; }
  printf '%s\n' "$s"
}

# platform ASSET prints a formula's url and sha256 lines for ASSET.
platform() {
  printf '      url "%s/%s"\n      sha256 "%s"\n' "$base" "$1" "$(sum "$1")"
}

cat <<EOF
# The dock CLI, the client of Tempo Dock: https://dock.nordtempo.com
# Written by Tempo Dock's release workflow (scripts/release/brew-formula.sh)
# from the release's signed SHA256SUMS; changes here are overwritten.
class Dock < Formula
  desc "Deploy apps, databases and agents to servers you own"
  homepage "https://dock.nordtempo.com"
  version "${version#v}"
  license "AGPL-3.0-only"

  on_macos do
    on_arm do
$(platform dock-cli-darwin-arm64)
    end
    on_intel do
$(platform dock-cli-darwin-amd64)
    end
  end

  on_linux do
    on_arm do
$(platform dock-cli-linux-arm64)
    end
    on_intel do
$(platform dock-cli-linux-amd64)
    end
  end

  def install
    # The release's dock-cli builds are bare files, downloaded without the
    # executable bit.
    cli = Dir["dock-cli-*"].first
    chmod 0755, cli
    bin.install cli => "dock"
    generate_completions_from_executable(bin/"dock", "completion")
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/dock --version")
  end
end
EOF
