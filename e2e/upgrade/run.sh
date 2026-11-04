#!/usr/bin/env bash
# Upgrade scenario: installs release FROM on a throwaway server (a privileged
# systemd container with its own Docker, see Dockerfile), seeds it with
# realistic data, updates it to TO the way an operator does
# (`dock update apply TO --wait`, ADR 0028) and checks that nothing was lost.
#
#   e2e/upgrade/run.sh v0.14.2 v0.24.0          # everything, then clean up
#   KEEP=1 e2e/upgrade/run.sh v0.14.2 v0.24.0   # leave the server running
#   e2e/upgrade/run.sh v0.14.2 v0.24.0 seed     # one step: up, install, seed,
#                       # snapshot, update, after, verify, failed-update, rollback, down
#
# Steps after `update`: verify compares the "before" and "after" snapshots
# and checks the new version's behaviour; failed-update checks the
# updater's own rollback with a fake release that never comes up; rollback
# goes back to FROM by hand (old binary + the pre-update database).
#
# Needs Docker on the host and `gh` logged in to an account that can read
# NordTempo/tempoDock (its token goes into the server's
# /etc/dock/github-token for the updater). Everything it creates is named
# upg-*; `down` removes it, volumes included.
set -euo pipefail

FROM=${1:?usage: run.sh FROM TO [step]}
TO=${2:?usage: run.sh FROM TO [step]}
STEP=${3:-all}
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NAME=${UPG_NAME:-upg-cp}
WORK=${UPG_WORK:-/tmp/upg}
IMAGE=upg-server:latest
REPO=NordTempo/tempoDock

say() { printf '\n== %s\n' "$*"; }
in() { docker exec -i "$NAME" "$@"; }

# up builds the server image and starts a fresh server with FROM's assets in /root/rel.
up() {
  say "server $NAME"
  docker build -q -t "$IMAGE" "$HERE" >/dev/null
  mkdir -p "$WORK/rel"
  if [[ ! -f "$WORK/rel/$FROM/SHA256SUMS" ]]; then
    rm -rf "$WORK/rel/$FROM" && mkdir -p "$WORK/rel/$FROM"
    gh release download "$FROM" -R "$REPO" -D "$WORK/rel/$FROM" \
      -p dock-linux-amd64 -p "dock-install-$FROM.tar.gz" -p SHA256SUMS
  fi
  (cd "$WORK/rel/$FROM" && grep -E " (dock-linux-amd64|dock-install-$FROM.tar.gz)\$" SHA256SUMS | sha256sum -c --quiet)
  # /var/lib/docker is an anonymous volume so `docker rm -v` takes it along.
  docker run -d --privileged --cgroupns=private --tmpfs /run --tmpfs /run/lock \
    -v /var/lib/docker --name "$NAME" --hostname "$NAME" "$IMAGE" >/dev/null
  for _ in $(seq 60); do in docker info >/dev/null 2>&1 && break; sleep 1; done
  in docker info >/dev/null
  in mkdir -p /root/rel /root/upg
  for f in dock-linux-amd64 "dock-install-$FROM.tar.gz"; do docker cp "$WORK/rel/$FROM/$f" "$NAME:/root/rel/$f"; done
  docker cp "$HERE/server/." "$NAME:/root/upg/"
}

# install runs FROM's install.sh and saves the operator token and admin password.
install() {
  say "install $FROM"
  in bash -c "cd /root/rel && tar -xzf dock-install-$FROM.tar.gz && chmod +x dock-linux-amd64 \
    && DOCK_BINARY=./dock-linux-amd64 DISABLE_TLS=true BASE_DOMAIN=upg.test LISTEN=127.0.0.1:7070 \
       ./deploy/install.sh > /root/upg/install.out 2> /root/upg/install.err"
  in bash /root/upg/lib.sh save_credentials
  gh auth token | in bash -c 'umask 027; cat > /etc/dock/github-token; chown root:dock /etc/dock/github-token'
}

# down removes the server and everything inside it.
down() {
  say "remove $NAME"
  docker rm -fv "$NAME" >/dev/null 2>&1 || true
}

step() { in bash /root/upg/"$1".sh "${@:2}"; }

case "$STEP" in
  up) up ;;
  install) install ;;
  seed) step seed ;;
  snapshot) step snapshot before ;;
  update) step update "$TO" ;;
  after) step snapshot after ;;
  verify) step verify ;;
  failed-update) step failed-update ;;
  rollback) step rollback ;;
  down) down ;;
  all)
    down; up; install
    step seed
    step snapshot before
    step update "$TO"
    step snapshot after
    step verify
    step failed-update
    step rollback
    docker cp "$NAME:/root/upg/evidence" "$WORK/evidence-$FROM-$TO"
    say "evidence in $WORK/evidence-$FROM-$TO"
    [[ -n "${KEEP:-}" ]] || down
    ;;
  *) echo "unknown step $STEP" >&2; exit 2 ;;
esac
