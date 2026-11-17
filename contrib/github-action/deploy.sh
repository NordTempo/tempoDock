#!/usr/bin/env bash
# The Deploy to Tempo Dock action (action.yml): installs the dock CLI the
# server runs, deploys, follows the release and sets the step's outputs.
#
# Inputs arrive as INPUT_* variables, never interpolated into the script.
# The step exits with dock's exit code (docs/cli.md), so a failed deploy
# (6) or a canceled one (10) fails the job, and the log shows the build as
# it happens. Runs on Linux and macOS runners.
set -uo pipefail

title='Deploy to Tempo Dock'
die() {
  echo "::error title=$title::$*"
  exit 2
}
output() { printf '%s=%s\n' "$1" "$2" >> "${GITHUB_OUTPUT:-/dev/null}"; }
summary() { printf '%s\n' "$@" >> "${GITHUB_STEP_SUMMARY:-/dev/null}"; }

# shown TEXT prints TEXT for a message: printable characters only, on one
# line, at most 80 of them, so a server's answer can't write its own lines
# into the job's log.
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

command -v jq > /dev/null || die "jq is needed and not on this runner; install it in an earlier step"

server="${INPUT_SERVER:-}"
server="${server%/}"
[ -n "$server" ] || die "the server input is required: your Dock's address, like https://dock.example.com"
case "$server" in
  http://* | https://*) ;;
  *) server="https://$server" ;;
esac
[ -n "${INPUT_TOKEN:-}" ] || die 'the token input is required: a Dock token kept in a secret, like token: ${{ secrets.DOCK_TOKEN }}'
echo "::add-mask::$INPUT_TOKEN"

mode="${INPUT_MODE:-source}"
path="${INPUT_PATH:-}"
case "$mode" in
  source) path="${path:-.}" ;;
  image) [ -n "${INPUT_IMAGE:-}" ] || die "mode image needs the image input, like image: ghcr.io/acme/web:\${{ github.sha }}" ;;
  apply) path="${path:-dock.project.toml}" ;;
  *) die "mode is source, image or apply, not $mode" ;;
esac
case "${INPUT_WAIT:-true}" in
  true) wait=true ;;
  false) wait=false ;;
  *) die "wait is true or false, not $INPUT_WAIT" ;;
esac

work="$(mktemp -d "${RUNNER_TEMP:-/tmp}/dock-deploy.XXXXXX")"
trap 'rm -rf "$work"' EXIT

# The CLI: the given binary, or the release the server runs (or the pinned
# one), checked against its signature by the installer this action carries.
# Only signed releases install here (DOCK_REQUIRE_SIGNATURE=1): for a
# server from before signing, the cli input takes a binary.
if [ -n "${INPUT_CLI:-}" ]; then
  dock="$INPUT_CLI"
  [ -x "$dock" ] || die "cli $dock is not an executable file"
else
  version="${INPUT_VERSION:-}"
  from="the version input"
  if [ -z "$version" ]; then
    health="$(curl -fsS --max-time 20 --retry 2 "$server/api/v1/health")" \
      || die "could not reach $server/api/v1/health: check the server input, and that this runner can reach it"
    # The dot keeps a newline at the end of the answer from being dropped.
    version="$(jq -r '((.version // "") | tostring) + "."' <<< "$health")"
    version="${version%.}"
    from="the version $server runs"
    case "$version" in
      v[0-9]*) ;;
      *)
        echo "::warning title=$title::$server runs a development build ($(shown "$version")); installing the latest dock release"
        version=""
        ;;
    esac
  fi
  # A version is a release version, whoever named it, or nothing is fetched.
  [ -z "$version" ] || [ "$version" = latest ] || release_version "$version" \
    || die "\"$(shown "$version")\" ($from) is not a release version like v0.58.0, so no CLI was installed. Pin a release with the version input (version: v0.58.0), or pass a dock binary with the cli input"
  # The signature check needs OpenSSL 3; macOS's own openssl is LibreSSL.
  for d in /opt/homebrew/opt/openssl@3/bin /usr/local/opt/openssl@3/bin; do
    if [ -x "$d/openssl" ]; then
      PATH="$d:$PATH"
      break
    fi
  done
  # Kept for the job's later steps, which find it on PATH.
  bin="${RUNNER_TEMP:-$work}/dock-cli"
  PATH="$bin:$PATH" DOCK_VERSION="$version" DOCK_INSTALL_DIR="$bin" DOCK_REQUIRE_SIGNATURE=1 \
    sh "$GITHUB_ACTION_PATH/install-cli.sh" || {
      echo "::error title=$title::the dock CLI was not installed; the installer's error is above. The action installs signed releases only (v0.56.0 and later): for an older server, pass a dock binary you checked yourself with the cli input"
      exit 1
    }
  echo "$bin" >> "${GITHUB_PATH:-/dev/null}"
  dock="$bin/dock"
fi

export DOCK_URL="$server" DOCK_TOKEN="$INPUT_TOKEN"
unset DOCK_CONTEXT DOCK_PLATFORM_TOKEN

args=()
[ -z "${INPUT_PROJECT:-}" ] || args+=(--project "$INPUT_PROJECT")
[ -z "${INPUT_ENVIRONMENT:-}" ] || args+=(--environment "$INPUT_ENVIRONMENT")
case "$mode" in
  source)
    [ -z "${INPUT_SERVICE:-}" ] || args+=(--service "$INPUT_SERVICE")
    cmd=(deploy "$path")
    ;;
  image)
    [ -z "${INPUT_SERVICE:-}" ] || args+=(--service "$INPUT_SERVICE")
    cmd=(deploy --image "$INPUT_IMAGE")
    ;;
  apply) cmd=(apply "$path" --deploy) ;;
esac
[ "$wait" = true ] || args+=(--detach)

# The result and the error, one JSON value per line (--json-file). An
# older CLI has no --json-file: --json then, without progress.
result="$work/result.json"
# jq filters over the file's values: the error objects, and the result.
errors='map(select(.error? | type == "object") | .error)'
results='map(select(.error? | type != "object"))'
if "$dock" --help 2>&1 | grep -q -- '--json-file'; then
  json=(--json-file "$result")
else
  echo "::warning title=$title::$("$dock" --version 2>/dev/null) has no --json-file, so the log shows no progress until the server runs a newer release"
  json=(--json)
fi

# A deploy already running (deploy_in_progress) is followed to its end,
# then this one is tried again, up to three times in all.
for attempt in 1 2 3; do
  if [ "${json[0]}" = --json ]; then
    "$dock" "${cmd[@]}" "${args[@]}" "${json[@]}" > "$result" 2> "$work/error.json"
    code=$?
    cat "$work/error.json" >> "$result"
    [ "$code" -eq 0 ] || cat "$work/error.json" >&2
  else
    "$dock" "${cmd[@]}" "${args[@]}" "${json[@]}"
    code=$?
  fi
  err_code="$(jq -rs "$errors"' | last | .code // empty' "$result" 2> /dev/null)"
  [ "$code" -eq 5 ] && [ "$err_code" = deploy_in_progress ] && [ "$attempt" -lt 3 ] || break
  echo "Another deploy is running; following it, then deploying again"
  wait_args=()
  [ -z "${INPUT_PROJECT:-}" ] || wait_args+=(--project "$INPUT_PROJECT")
  [ -z "${INPUT_ENVIRONMENT:-}" ] || wait_args+=(--environment "$INPUT_ENVIRONMENT")
  [ -z "${INPUT_SERVICE:-}" ] || wait_args+=(--service "$INPUT_SERVICE")
  "$dock" release wait "${wait_args[@]}" > /dev/null || true
done
output exit-code "$code"

# The releases: one for a deploy, any number for an apply.
releases="$(jq -cs "$results"' | first // null | if . == null then [] elif has("plan") then [.applied[]?.releases[]?] else [.] end | map(del(.events))' "$result" 2> /dev/null)"
releases="${releases:-[]}"
output releases "$releases"
org="$("$dock" whoami --json 2> /dev/null | jq -r '.org // empty')"
url_of() { [ -z "$org" ] || jq -r --arg base "$server/$org" '"\($base)/\(.project)/\(.environment)/\(.service)/history/\(.number)"' <<< "$1"; }
if [ "$(jq length <<< "$releases")" -eq 1 ]; then
  rel="$(jq -c '.[0]' <<< "$releases")"
  output release "$(jq -r .number <<< "$rel")"
  output status "$(jq -r .status <<< "$rel")"
  output url "$(url_of "$rel")"
fi

summary "### $title" "" "| Service | Release | Status |" "|---|---|---|"
jq -c '.[]' <<< "$releases" | while read -r rel; do
  link="$(url_of "$rel")"
  n="$(jq -r .number <<< "$rel")"
  [ -z "$link" ] || n="[$n]($link)"
  summary "| $(jq -r '"\(.project)/\(.environment)/\(.service)"' <<< "$rel") | $n | $(jq -r '.status + (if .error then ": " + .error else "" end)' <<< "$rel") |"
done
if [ "$code" -ne 0 ]; then
  msg="$(jq -rs "$errors"' | last // empty | .message + (if .hint then "\n" + .hint else "" end)' "$result" 2> /dev/null)"
  msg="${msg:-dock exited with $code}"
  summary "" "$msg"
  # An annotation is one line: %0A is its line break.
  echo "::error title=$title (exit $code)::${msg//$'\n'/%0A}"
fi
exit "$code"
