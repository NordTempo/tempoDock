#!/usr/bin/env bash
# A dock for local.sh's retry case: its first deploy is refused as
# deploy_in_progress (exit 5, the error in --json-file, or on stderr with
# --json as an older CLI does) and logged to BUSY_LOG with every `release
# wait`; everything else runs REAL_DOCK.
set -euo pipefail
if [ "${1:-}" = deploy ] && [ ! -e "$BUSY_LOG" ]; then
  echo deploy >> "$BUSY_LOG"
  err='{"error":{"code":"deploy_in_progress","exitCode":5,"message":"release 2 of shop/staging/web is already deploying (building); wait for it to finish"}}'
  while [ $# -gt 0 ]; do
    case "$1" in
      --json-file) printf '%s\n' "$err" > "$2" ;;
      --json) printf '%s\n' "$err" >&2 ;;
    esac
    shift
  done
  exit 5
fi
[ "${1:-} ${2:-}" != "release wait" ] || echo wait >> "$BUSY_LOG"
exec "$REAL_DOCK" "$@"
