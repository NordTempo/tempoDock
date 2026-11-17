# Helpers for the upgrade scenario's server-side steps (seed.sh, verify.sh,
# ...), run as root inside the test server. Sourced; `bash lib.sh FUNC`
# runs one function.
set -euo pipefail

U=/root/upg
EV=$U/evidence
DB=/var/lib/dock/dock.db
mkdir -p "$EV"
# Only ever the test server's own daemon: never a real install's.
export DOCK_URL=http://127.0.0.1:7070

# guard refuses to run against production or the homelab, whatever the
# environment or CLI config says.
guard() {
  local bad
  for bad in 100.95.4.67 192.168.10.50; do
    [[ "$DOCK_URL" != *"$bad"* ]] || { echo "refusing: $DOCK_URL is a real install" >&2; exit 1; }
  done
  [[ "$(hostname)" == upg-* ]] || { echo "refusing: not an upg-* test server" >&2; exit 1; }
  if [[ -f ~/.config/dock/config.json ]] && grep -q -E '100\.95\.4\.67|192\.168\.10\.50|"prod"' ~/.config/dock/config.json; then
    echo "refusing: a CLI config with real contexts is present" >&2; exit 1
  fi
}
guard

say() { printf '\n== %s\n' "$*"; }
die() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

# save_credentials keeps install.sh's operator token and admin password.
save_credentials() {
  grep -o 'dock_[A-Za-z0-9_-]*' $U/install.out | head -1 > $U/admin.token
  grep -o 'password [^ ]*' $U/install.err | awk '{print $2}' > $U/admin.password
}

# d runs the dock CLI as the operator.
d() { DOCK_TOKEN="$(cat $U/admin.token)" dock "$@"; }

# api METHOD PATH [JSON] calls the API as the operator.
api() {
  curl -sS -X "$1" -H "Authorization: Bearer $(cat $U/admin.token)" \
    -H 'Content-Type: application/json' ${3:+--data "$3"} "$DOCK_URL/api/v1$2"
}

# db runs SQL against the control plane database, read only.
db() { sqlite3 -readonly "$DB" "$@"; }

# counts prints every table's row count, one "table count" per line.
counts() {
  local t
  for t in $(db "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name"); do
    printf '%s %s\n' "$t" "$(db "SELECT count(*) FROM \"$t\"")"
  done
}

# route HOST curls HOST through Caddy on :80 and prints the status and body.
route() { curl -sS -m 10 -o - -w ' %{http_code}' -H "Host: $1" http://127.0.0.1/ 2>&1 | tail -c 300; }

# wait_api waits up to 120 s for the API to answer.
wait_api() {
  local i
  for i in $(seq 120); do curl -fsS -o /dev/null $DOCK_URL/api/v1/version 2>/dev/null && return 0; sleep 1; done
  die "API not up"
}

[[ "${BASH_SOURCE[0]}" != "$0" ]] || "$@"
