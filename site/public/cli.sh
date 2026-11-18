#!/bin/sh
# What https://dock.nordtempo.com/install.sh and /cli.sh serve (site/public):
#
#   curl -fsSL https://dock.nordtempo.com/install.sh | sudo bash
#   curl -fsSL https://dock.nordtempo.com/cli.sh | sh -s -- https://dock.example.com
#
# It runs the latest release's install.sh (deploy/install.sh, under bash) or
# install-cli.sh (scripts/release/install-cli.sh, under sh) once that script
# is known to be Dock's:
#
#   1. the latest release is $minimum or newer, so an old release can't be
#      handed out as the latest;
#   2. the release's SHA256SUMS is signed by a Dock release key (ADR 0088)
#      and is that release's own;
#   3. the script matches its line in SHA256SUMS.
#
# Arguments and the environment go to the script as they are, DOCK_VERSION
# included: the latest script installs the release it names. The script's
# exit status is this one's.
#
# The signature check needs OpenSSL 3 or, for a release that has a
# SHA256SUMS.sshsig, ssh-keygen (OpenSSH 8.1 or later, which macOS ships
# beside its LibreSSL). With neither, install.sh stops unless
# DOCK_REQUIRE_SIGNATURE=0, and cli.sh warns and goes on unless
# DOCK_REQUIRE_SIGNATURE=1, as the scripts themselves do.
#
# install.sh and cli.sh are the same file but for the next line
# (TestBootstrapsAreOneScript). All of it is functions called on the last
# line, so a download cut short runs nothing.
set -eu

script=install-cli.sh
repo=NordTempo/tempoDock
# Dock's release signing keys, as in internal/update/sign.go: "<key id>
# <raw Ed25519 public key, base64>" per line.
release_keys='bc6c45a08a65c136 8uQjZq1kO37ezVv7EzN8eyrqOYXRGK2nsCRL/d3RSdU='
# The oldest release taken as the latest: raise it with releases that fix
# the install scripts. Never below the first signed release (v0.56.0).
minimum=v0.60.2

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

# at_least VERSION MINIMUM: is VERSION (vX.Y.Z, pre-releases as their
# X.Y.Z) MINIMUM or newer?
at_least() {
  printf '%s %s\n' "${1#v}" "${2#v}" | awk '{
    split($1, a, /[.-]/); split($2, b, /[.-]/)
    for (i = 1; i <= 3; i++) { if (a[i] + 0 > b[i] + 0) exit 0; if (a[i] + 0 < b[i] + 0) exit 1 }
    exit 0 }'
}

# ssh_verify_sums DIR: is DIR/SHA256SUMS signed by a release key in
# DIR/SHA256SUMS.sshsig ("<key id> <SSHSIG, base64>" per line, namespace
# dock-release)? With ssh-keygen, for where there is no OpenSSL 3. The
# same function as install-cli.sh's.
ssh_verify_sums() {
  echo "$release_keys" | while read -r id key; do
    line="$(grep "^$id " "$1/SHA256SUMS.sshsig")" || continue
    {
      echo '-----BEGIN SSH SIGNATURE-----'
      printf '%s\n' "${line#* }" | fold -w 70
      echo '-----END SSH SIGNATURE-----'
    } > "$1/sig.ssh"
    sshkey="$({ printf '\000\000\000\013ssh-ed25519\000\000\000\040'; printf '%s' "$key" | openssl base64 -d -A; } | openssl base64 -A)" || continue
    printf 'dock-release namespaces="dock-release" ssh-ed25519 %s\n' "$sshkey" > "$1/allowed_signers"
    if ssh-keygen -Y verify -f "$1/allowed_signers" -I dock-release -n dock-release -s "$1/sig.ssh" < "$1/SHA256SUMS" >/dev/null 2>&1; then
      echo ok
      break
    fi
  done | grep -q ok
}

# verify_sums DIR VERSION REQUIRE checks DIR/SHA256SUMS against
# DIR/SHA256SUMS.sig with openssl, as dock and the release's scripts do, or
# without OpenSSL 3 against DIR/SHA256SUMS.sshsig (empty when the release
# has none) with ssh-keygen. REQUIRE=1 dies when neither check can run
# here; otherwise that is a warning.
verify_sums() {
  [ -s "$1/SHA256SUMS.sig" ] || die "release $2 has no signature; don't install it: it may have been tampered with"
  grep -q "  dock-install-$2.tar.gz\$" "$1/SHA256SUMS" || die "SHA256SUMS belongs to another release than $2"
  if ! openssl pkeyutl -help 2>&1 | grep -q -- -rawin; then
    if [ -s "$1/SHA256SUMS.sshsig" ] && ssh-keygen -\? 2>&1 | grep -q -- '-Y verify'; then
      ssh_verify_sums "$1" || die "release $2's SHA256SUMS isn't signed by a Dock release key; don't install it: it may have been tampered with"
      return 0
    fi
    [ "$3" != 1 ] || die "can't check the release's signature: it needs OpenSSL 3 (openssl pkeyutl -rawin). Install it, or set DOCK_REQUIRE_SIGNATURE=0 to take $script on the release's SHA256SUMS alone"
    warn "can't check $script's signature without OpenSSL 3; checked against the release's SHA256SUMS only"
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

# main ARGS... finds the latest release, checks its copy of $script and
# runs it with ARGS.
main() {
  case "$script" in
    install.sh) run=bash require="${DOCK_REQUIRE_SIGNATURE:-1}" ;;
    *) run=sh require="${DOCK_REQUIRE_SIGNATURE:-0}" ;;
  esac
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  trap 'exit 1' HUP INT TERM

  # .../releases/latest redirects to .../releases/tag/<version>.
  url="$(github -I -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest")" \
    || die "could not find the latest release of $repo"
  version="${url##*/}"
  # The one place a version becomes part of a URL.
  release_version "$version" \
    || die "\"$(shown "$version")\" (the latest release of $repo) is not a release version like $minimum, so nothing was downloaded"
  at_least "$version" "$minimum" \
    || die "the latest release of $repo came back as $version, older than $minimum, so nothing was downloaded. Try again; if it stays, report it: https://github.com/$repo/security"
  base="https://github.com/$repo/releases/download/$version"
  for name in "$script" SHA256SUMS SHA256SUMS.sig; do
    github -o "$tmp/$name" "$base/$name" || die "could not download $name of release $version from $repo"
  done
  # A release from before the ssh-keygen signature has none: a 404 (curl's
  # exit 22) leaves the file empty and verify_sums decides.
  rc=0
  github -o "$tmp/SHA256SUMS.sshsig" "$base/SHA256SUMS.sshsig" 2>/dev/null || rc=$?
  case $rc in
    0) ;;
    22) : > "$tmp/SHA256SUMS.sshsig" ;;
    *) die "could not download SHA256SUMS.sshsig of release $version from $repo" ;;
  esac
  verify_sums "$tmp" "$version" "$require"

  awk -v n="$script" '$2 == n' "$tmp/SHA256SUMS" > "$tmp/sum"
  [ -s "$tmp/sum" ] || die "release $version's SHA256SUMS has no entry for $script"
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$tmp" && sha256sum -c sum >/dev/null 2>&1) || die "$script does not match release $version's SHA256SUMS; it was not run"
  else
    (cd "$tmp" && shasum -a 256 -c sum >/dev/null 2>&1) || die "$script does not match release $version's SHA256SUMS; it was not run"
  fi

  rc=0
  "$run" "$tmp/$script" "$@" || rc=$?
  exit "$rc"
}

main "$@"
