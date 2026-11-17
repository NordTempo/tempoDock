#!/usr/bin/env bash
# Runs the action's cases against a throwaway fake Dock on this machine,
# without GitHub: deploy.sh gets its inputs as the action would pass them,
# and its outputs are read from a GITHUB_OUTPUT file. The workflow
# (.github/workflows/github-action.yml) runs the same cases through
# action.yml on a GitHub-hosted runner.
#
#   local.sh DOCK_BINARY [PORT_BASE]
#
# DOCK_BINARY serves and deploys (a build of this checkout, so it has
# --json-file); the install case also installs the latest release's CLI.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
dock="$(realpath "$1")"
work="$(mktemp -d)"
trap 'kill "$(cat "$work/dock/pid")" 2> /dev/null || true; rm -rf "$work"' EXIT

"$here/fake-dock.sh" "$dock" "$work/dock" "${2:-17470}" > "$work/env"
. "$work/env"
fail=0

# step NAME INPUT=value...: runs deploy.sh with the inputs, sets $code and
# the outputs as out_<name> (dashes as underscores).
step() {
  local name="$1"
  shift
  : > "$work/output"
  echo "== $name"
  code=0
  env -i PATH="$PATH" HOME="$work" RUNNER_TEMP="$work/runner" GITHUB_ACTION_PATH="$here/.." \
    GITHUB_OUTPUT="$work/output" GITHUB_STEP_SUMMARY="$work/summary-$name.md" \
    INPUT_SERVER="$DOCK_TEST_URL" INPUT_TOKEN="$DOCK_TEST_TOKEN" INPUT_PROJECT=shop INPUT_ENVIRONMENT=staging \
    "$@" "$here/../deploy.sh" || code=$?
  unset out_release out_status out_url out_releases out_exit_code
  while IFS='=' read -r k v; do
    printf -v "out_${k//-/_}" '%s' "$v"
  done < "$work/output"
}
check() {
  if eval "$1"; then
    echo "   ok: $1"
  else
    echo "   FAIL: $1 (exit $code, outputs: release=${out_release:-} status=${out_status:-} url=${out_url:-})"
    fail=1
  fi
}
mkdir -p "$work/runner"
cd "$here"

step source INPUT_MODE=source INPUT_PATH=app INPUT_SERVICE=web INPUT_CLI="$dock"
check '[ $code -eq 0 ] && [ "$out_release" = 1 ] && [ "$out_status" = active ]'
check '[ "$out_url" = "$DOCK_TEST_URL/default/shop/staging/web/history/1" ]'

step image INPUT_MODE=image INPUT_IMAGE=busybox:1.37 INPUT_SERVICE=api INPUT_CLI="$dock"
check '[ $code -eq 0 ] && [ "$out_release" = 1 ] && [ "$out_status" = active ]'

step detached INPUT_MODE=source INPUT_PATH=app INPUT_SERVICE=web INPUT_WAIT=false INPUT_CLI="$dock"
check '[ $code -eq 0 ] && [ "$out_release" = 2 ] && [ "$out_status" = queued ]'

# A deploy refused because another runs follows that one, then deploys.
step busy INPUT_MODE=image INPUT_IMAGE=busybox:1.37 INPUT_SERVICE=web INPUT_CLI="$here/busy-dock.sh" \
  REAL_DOCK="$dock" BUSY_LOG="$work/busy.log"
check '[ $code -eq 0 ] && [ "$out_release" = 3 ] && [ "$out_status" = active ] && [ "$(tr "\n" " " < "$work/busy.log")" = "deploy wait " ]'

step apply INPUT_MODE=apply INPUT_PATH=dock.project.toml INPUT_CLI="$dock"
check '[ $code -eq 0 ] && [ "$(jq -r ".[0].service" <<< "$out_releases")" = worker ] && [ "$out_status" = active ]'

# Production is outside the token's grants: refused, exit 3.
step refused INPUT_MODE=image INPUT_IMAGE=busybox:1.37 INPUT_SERVICE=web INPUT_ENVIRONMENT=production INPUT_CLI="$dock"
check '[ $code -eq 3 ] && [ "$out_exit_code" = 3 ] && [ -z "${out_release:-}" ]'

step bad-input INPUT_MODE=image INPUT_CLI="$dock"
check '[ $code -eq 2 ]'

# The installer: the latest release (the fake runs a development build),
# its signature checked.
step install INPUT_MODE=image INPUT_IMAGE=busybox:1.37 INPUT_SERVICE=api
check '[ $code -eq 0 ] && [ "$out_status" = active ] && [ -x "$work/runner/dock-cli/dock" ]'

[ "$fail" -eq 0 ] && echo "all cases passed" || echo "some cases failed"
exit "$fail"
