# Helpers for driving a live Tempo Dock server over its API. Source it:
#   DOCK_URL=http://host:7070 DOCK_TOKEN=... ; source e2e/lib.sh
# Needs curl, jq and tar. All functions fail loudly (set -e friendly).

: "${DOCK_URL:?set DOCK_URL, e.g. http://192.168.10.50:7070}"
: "${DOCK_TOKEN:?set DOCK_TOKEN to an admin API token}"
# Scenarios create and archive projects: never against production by
# accident. The CLI below always runs with DOCK_URL, so no context applies.
case "$DOCK_URL" in
*100.95.4.67*)
	[[ ${E2E_ALLOW_PROD:-} == 1 ]] || { echo "refusing to run e2e against production ($DOCK_URL); set E2E_ALLOW_PROD=1 if you mean it" >&2; exit 1; } ;;
esac
API="${DOCK_URL%/}/api/v1"
E2E_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_NAME="${DOCK_ENV:-production}"

log() { printf '\033[1m[%s] %s\033[0m\n' "$(date +%T)" "$*" >&2; }
fail() { printf '\033[31mFAIL: %s\033[0m\n' "$*" >&2; FAILURES=$((${FAILURES:-0} + 1)); }
pass() { printf '\033[32mok: %s\033[0m\n' "$*" >&2; }

# api METHOD PATH [JSON] prints the body; returns non-zero on HTTP >= 400.
# The status code is left in $API_STATUS (only in the caller's shell when
# not used in a subshell; see api_status).
api() {
	local method=$1 path=$2 body=${3:-} out code
	out=$(mktemp)
	code=$(curl -sS -o "$out" -w '%{http_code}' -X "$method" \
		-H "Authorization: Bearer ${TOKEN_OVERRIDE:-$DOCK_TOKEN}" \
		${body:+-H 'Content-Type: application/json' --data "$body"} "$API$path")
	cat "$out"
	rm -f "$out"
	[[ $code -lt 400 ]]
}

# api_status METHOD PATH [JSON] prints only the HTTP status code.
api_status() {
	local method=$1 path=$2 body=${3:-}
	curl -sS -o /dev/null -w '%{http_code}' -X "$method" \
		-H "Authorization: Bearer ${TOKEN_OVERRIDE:-$DOCK_TOKEN}" \
		${body:+-H 'Content-Type: application/json' --data "$body"} "$API$path"
}

svc_path() { echo "/projects/$1/environments/$ENV_NAME/services/$2"; }

ensure_project() { api GET "/projects/$1" >/dev/null 2>&1 || api POST /projects "{\"name\":\"$1\"}" >/dev/null; }

# ensure_service PROJECT NAME [JSON settings]
ensure_service() {
	local p=$1 s=$2 settings=${3:-'{}'}
	api GET "$(svc_path "$p" "$s")" >/dev/null 2>&1 && return 0
	api POST "/projects/$p/environments/$ENV_NAME/services" "$(jq -c --arg n "$s" '. + {name: $n}' <<<"$settings")" >/dev/null
}

# generate_domain PROJECT SERVICE prints the hostname (reuses an existing one).
generate_domain() {
	local h
	h=$(api GET "$(svc_path "$1" "$2")/domains" | jq -r '[.[] | select(.generated)][0].hostname // empty')
	[[ -n $h ]] || h=$(api POST "$(svc_path "$1" "$2")/domains/generate" | jq -r .domain.hostname)
	echo "$h"
}

# deploy_source PROJECT SERVICE DIR prints the release number.
deploy_source() {
	local p=$1 s=$2 dir=$3
	tar -C "$dir" --exclude=node_modules --exclude=.next -czf - . |
		curl -sS -X POST -H "Authorization: Bearer ${TOKEN_OVERRIDE:-$DOCK_TOKEN}" \
			-H 'Content-Type: application/gzip' --data-binary @- \
			"$API$(svc_path "$p" "$s")/releases/source" | tee /dev/stderr | jq -er .number
}

# deploy_image PROJECT SERVICE IMAGE prints the release number.
deploy_image() {
	api POST "$(svc_path "$1" "$2")/releases" "{\"image\":\"$3\"}" | jq -er .number
}

release() { api GET "$(svc_path "$1" "$2")/releases/$3"; }

# wait_release PROJECT SERVICE NUMBER [TIMEOUT_S] prints the final status.
wait_release() {
	local p=$1 s=$2 n=$3 t=${4:-600} st
	for ((i = 0; i < t; i++)); do
		st=$(release "$p" "$s" "$n" | jq -r .status)
		case $st in active | failed | superseded) echo "$st"; return 0 ;; esac
		sleep 1
	done
	echo timeout
}

# set_vars PROJECT SERVICE JSON_OBJECT [apply] stages service variables.
set_vars() {
	local apply=${4:-false}
	api PATCH "/projects/$1/environments/$ENV_NAME/variables" \
		"$(jq -c --arg s "$2" --argjson a "$apply" '{service: $s, set: ., apply: $a}' <<<"$3")" >/dev/null
}

apply_changes() { api POST "/projects/$1/environments/$ENV_NAME/changes/apply" '{}'; }

# http_get HOST PATH prints the body via the proxy on port 80. With
# E2E_PROXY=ADDR:PORT the request goes there whatever HOST resolves to
# (servers without DNS, like e2e/vm's).
http_get() { curl -sS --max-time 10 ${E2E_PROXY:+--connect-to "$1:80:$E2E_PROXY"} "http://$1$2"; }

# use_proxy_of PROJECT points E2E_PROXY at port 80 of the server running
# PROJECT's environment, from E2E_SERVER_IPS ("name=ip name=ip", written
# by e2e/vm/up.sh). Without E2E_SERVER_IPS it changes nothing.
use_proxy_of() {
	[[ -n ${E2E_SERVER_IPS:-} ]] || return 0
	local srv kv
	srv=$(api GET "/projects/$1/environments" | jq -r --arg e "$ENV_NAME" '.[] | select(.name == $e) | .server')
	for kv in $E2E_SERVER_IPS; do
		[[ ${kv%%=*} == "$srv" ]] && E2E_PROXY=${kv#*=}:80
	done
	return 0
}

# http_code HOST PATH prints only the status code (000 when unreachable).
http_code() { curl -s -o /dev/null -w '%{http_code}' --max-time 10 ${E2E_PROXY:+--connect-to "$1:80:$E2E_PROXY"} "http://$1$2"; }

# wait_http HOST PATH WANT [TIMEOUT_S]: until the body contains WANT.
wait_http() {
	local t=${4:-30}
	for ((i = 0; i < t; i++)); do
		http_get "$1" "$2" 2>/dev/null | grep -q -- "$3" && return 0
		sleep 1
	done
	return 1
}

# dk runs the dock CLI against DOCK_URL with DOCK_TOKEN (or TOKEN_OVERRIDE),
# never a context from the user's config.
dk() { DOCK_TOKEN="${TOKEN_OVERRIDE:-$DOCK_TOKEN}" "${DOCK_BIN:-$E2E_DIR/../bin/dock}" "$@"; }

# check DESC CMD...: pass when CMD succeeds, fail otherwise.
check() {
	local d=$1
	shift
	if "$@" >/dev/null; then pass "$d"; else fail "$d"; fi
}

# wait_for TIMEOUT_S CMD...: until CMD succeeds.
wait_for() {
	local t=$1
	shift
	for ((i = 0; i < t; i++)); do
		"$@" && return 0
		sleep 1
	done
	return 1
}

# body_has HOST PATH WANT: the body via the proxy contains WANT.
body_has() { http_get "$1" "$2" 2>/dev/null | grep -q -- "$3"; }

# code_is HOST PATH CODE: the status code via the proxy is CODE.
code_is() { [[ $(http_code "$1" "$2") == "$3" ]]; }

# ui_login signs in as E2E_ADMIN_EMAIL with E2E_ADMIN_PASSWORD into a
# cookie jar, for what only a UI session may do (approving access requests).
ui_login() {
	UI_JAR=${UI_JAR:-$(mktemp)}
	curl -sS -o /dev/null -c "$UI_JAR" -H 'Content-Type: application/json' -H "Origin: ${DOCK_URL%/}" \
		--data "$(jq -nc --arg e "${E2E_ADMIN_EMAIL:?}" --arg p "${E2E_ADMIN_PASSWORD:?}" '{email: $e, password: $p}')" \
		"$API/auth/login"
}

# ui_api METHOD PATH [JSON] is api with the UI session instead of a token.
ui_api() {
	local method=$1 path=$2 body=${3:-} out code
	out=$(mktemp)
	code=$(curl -sS -o "$out" -w '%{http_code}' -X "$method" -b "$UI_JAR" -H "Origin: ${DOCK_URL%/}" \
		${body:+-H 'Content-Type: application/json' --data "$body"} "$API$path")
	cat "$out"
	rm -f "$out"
	[[ $code -lt 400 ]]
}
