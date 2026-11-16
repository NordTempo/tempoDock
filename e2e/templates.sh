#!/usr/bin/env bash
# Deploys app templates (ADR 0082) on throwaway servers, one at a time, and
# checks each one serves: its services' first releases go live and its
# domain answers without a 5xx. Each template gets its own project,
# deleted afterwards with its volumes. Run it for the templates a change
# touched (a bump, a new template) before it merges:
#
#   make web && make build && e2e/vm/up.sh 1 && source /tmp/e2e2/env
#   e2e/templates.sh [template...]     # default: every template
#
# Required inputs get placeholder values: e2e@example.com for emails, a
# random password for secrets, the default otherwise.
set -uo pipefail
source "$(dirname "$0")/lib.sh"
command -v jq >/dev/null || { echo "needs jq" >&2; exit 1; }

names=("$@")
if [[ ${#names[@]} -eq 0 ]]; then
	mapfile -t names < <(dk template list --json | jq -r '.[].name')
fi

# inputs NAME prints the --set flags for a template's required inputs.
inputs() {
	dk template show "$1" --json | jq -r '.inputs[]? | select((.optional // false) | not) |
		if .kind == "email" then "--set=\(.key)=e2e@example.com"
		elif .kind == "secret" then "--set=\(.key)=E2e-\(now | floor)-pw"
		elif (.default // "") != "" then empty
		else "--set=\(.key)=e2e" end'
}

for name in "${names[@]}"; do
	P="tpl-$name"
	P=${P:0:32}
	log "$name"
	dk project create "$P" >/dev/null 2>&1
	mapfile -t sets < <(inputs "$name")
	start=$(date +%s)
	if ! out=$(dk template use "$name" -p "$P" -e production "${sets[@]}" --json 2>&1); then
		fail "$name: template use: $(tail -3 <<<"$out")"
	else
		took=$(($(date +%s) - start))
		bad=$(jq -r '.releases[] | select(.status != "active") | "\(.service) r\(.number) \(.status): \(.error // "")"' <<<"$out")
		failed=$(jq -r '.failed[]?' <<<"$out")
		main=$(jq -r '.services[0]' <<<"$out")
		if [[ -n $bad || -n $failed ]]; then
			fail "$name: ${bad}${failed}"
			dk logs -p "$P" -e production -s "$main" --since 10m 2>/dev/null | tail -20 >&2
		else
			host=$(api GET "$(svc_path "$P" "$main")/domains" | jq -r '.[0].hostname // empty')
			use_proxy_of "$P"
			code=000
			if [[ -n $host ]]; then
				for _ in $(seq 60); do
					code=$(http_code "$host" /)
					[[ $code =~ ^[234] ]] && break
					sleep 2
				done
			fi
			if [[ -z $host || $code =~ ^[234] ]]; then
				pass "$name: live in ${took}s${host:+, $host answers $code}"
			else
				fail "$name: live, but $host answers $code"
				dk logs -p "$P" -e production -s "$main" --since 10m 2>/dev/null | tail -20 >&2
			fi
		fi
	fi
	[[ ${E2E_KEEP:-} == 1 ]] && continue
	dk project archive "$P" --yes >/dev/null 2>&1 || dk project archive "$P" >/dev/null 2>&1
	dk project delete "$P" --confirm "$P" >/dev/null 2>&1 || fail "$name: deleting $P"
done

if [[ ${FAILURES:-0} -gt 0 ]]; then
	echo "$FAILURES failed" >&2
	exit 1
fi
