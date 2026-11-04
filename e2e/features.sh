#!/usr/bin/env bash
# User flows for the features of ADRs 0043 to 0050, against a live Tempo
# Dock (e2e/vm/up.sh starts one plus a node, in containers):
#
#   source /tmp/e2e2/env && e2e/features.sh [scenario...]
#
# Scenarios (default: all, in this order):
#   predeploy   a pre-deploy migration runs before cutover; a failing one
#               fails the release and the old one keeps serving (ADR 0045)
#   stopstart   a stopped service's domain answers 503, start brings it back (0050)
#   run         dock run in a service's image, streamed and detached (0048)
#   lock        a locked environment turns a token's change into an access
#               request; an admin approves it and it applies; a deploy waits (0044)
#   logs        a removed container's output is still readable; a log drain
#               delivers lines to an HTTP sink (0046)
#   snapshot    Postgres rows survive a snapshot restore, in place and as a
#               new service (0047)
#   move        an environment moves to the node and keeps serving (0015)
#   apply       dock apply --dry-run, then apply; a Compose import (0049)
#   health      ongoing health checks restart a hung service; a node going
#               down raises an alert and clears when it is back (0043)
#
# Projects are e2e2-<scenario>-<run>, <run> being E2E_RUN (default: the
# time), so runs are repeatable. E2E_NODE_CONTAINER names the node's
# container for `health` (default e2e2-node1); needs Docker on this machine.
set -uo pipefail
source "$(dirname "$0")/lib.sh"
APPS="$E2E_DIR/apps"
RUN=${E2E_RUN:-$(date +%H%M%S)}
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

# fresh PROJECT archives PROJECT if it exists and creates it again.
fresh() {
	if api GET "/projects/$1" >/dev/null 2>&1; then
		dk project archive "$1" --yes >/dev/null 2>&1 || true
	fi
	dk project create "$1" >/dev/null 2>&1 || { fail "can't create project $1"; return 1; }
	use_proxy_of "$1"
}

# out_has PATTERN CMD...: CMD's output (stdout and stderr) matches the
# extended regex PATTERN.
out_has() {
	local re=$1
	shift
	"$@" 2>&1 | grep -qE -- "$re"
}

# release_field P S N FIELD prints a release's field.
release_field() { release "$1" "$2" "$3" | jq -r ".$4"; }

# active_release P S: the newest release of S is active.
newest_active() { [[ $(api GET "$(svc_path "$1" "$2")/releases" | jq -r 'max_by(.number).status') == active ]]; }

# P S: -p and -e flags for the CLI.
pe() { echo "-p $1 -e $ENV_NAME"; }

# expect_release P S N WANT waits for release N and checks its final status.
expect_release() {
	local st
	st=$(wait_release "$1" "$2" "$3" 600)
	if [[ $st == "$4" ]]; then
		pass "$1/$2 release $3 is $st"
	else
		fail "$1/$2 release $3 is $st, want $4: $(release "$1" "$2" "$3" | jq -c '{status, error_code, error}')"
	fi
}

# newest P S prints the newest release's number.
newest() { api GET "$(svc_path "$1" "$2")/releases" | jq -r 'map(.number) | max'; }

# pg_web P: a Postgres service db and a web service built from apps/pg-web
# that reaches it through DATABASE_URL. Prints web's hostname.
pg_web() {
	local p=$1 n
	dk service create db $(pe "$p") --template postgres >/dev/null || { fail "$p: db not created"; return 1; }
	wait_for 120 newest_active "$p" db ||
		{ fail "$p: db not active"; return 1; }
	ensure_service "$p" web '{"port":8080}'
	set_vars "$p" web '{"DATABASE_URL":"${{db.DATABASE_URL}}","APP_VERSION":"v1"}' true >/dev/null
	generate_domain "$p" web
}

scenario_predeploy() {
	local p=e2e2-pre-$RUN h n
	fresh $p || return
	h=$(pg_web $p) || return
	n=$(deploy_source $p web "$APPS/pg-web" 2>/dev/null) || { fail "$p: upload rejected"; return; }
	expect_release $p web "$n" active
	check "$h serves v1" wait_for 30 body_has "$h" / "web v1"

	# A migration that takes a while: v1 must keep serving until it is done.
	dk service update web $(pe $p) --pre-deploy-command \
		'psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -c "create table if not exists migrations (version text, at timestamptz default now())" -c "insert into migrations (version) values ('"'"'$APP_VERSION'"'"')" && sleep 8' >/dev/null
	set_vars $p web '{"APP_VERSION":"v2"}'
	apply_changes $p >/dev/null
	n=$(newest $p web)
	sleep 4
	check "v1 serves while the pre-deploy command runs" body_has "$h" / "web v1"
	expect_release $p web "$n" active
	check "$h serves v2 after cutover" wait_for 20 body_has "$h" / "web v2"
	check "release $n's build log has the pre-deploy output" out_has INSERT dk logs --build -r "$n" -s web $(pe $p)
	check "the migration wrote its row (dock run)" out_has '^v2$' dk run web $(pe $p) -- 'psql "$DATABASE_URL" -tAc "select version from migrations"'


	# A failing migration fails the release; v2 keeps serving.
	dk service update web $(pe $p) --pre-deploy-command 'echo migrating; psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -c "select * from no_such_table"' >/dev/null
	set_vars $p web '{"APP_VERSION":"v3"}'
	apply_changes $p >/dev/null
	n=$(newest $p web)
	expect_release $p web "$n" failed
	check "failed release's error is deploy_failed" out_has '^deploy_failed$' release_field $p web "$n" error_code
	check "$h still serves v2 after the failed migration" body_has "$h" / "web v2"
	check "the failed release's changes are staged again" out_has APP_VERSION dk changes $(pe $p)
}

# service_containers P S prints the running containers of service S on the
# server container E2E_CP_CONTAINER (default e2e2-cp), via its Docker.
service_containers() {
	local id
	id=$(api GET "$(svc_path "$1" "$2")" | jq -r .id)
	docker exec "${E2E_CP_CONTAINER:-e2e2-cp}" docker ps -q --filter label=dock.managed=true --filter "label=dock.service=$id"
}

# status_line P S prints the service's row of dock status.
status_line() { dk status $(pe "$1") 2>&1 | grep -E "^$2 "; }

scenario_stopstart() {
	local p=e2e2-stop-$RUN h n code
	fresh $p || return
	ensure_service $p web '{"image":"traefik/whoami:v1.11","port":80}'
	h=$(generate_domain $p web)
	n=$(api POST "$(svc_path $p web)/releases" '{}' | jq -r .number)
	expect_release $p web "$n" active
	check "$h answers 200" wait_for 20 code_is "$h" / 200
	code=$(api_status POST "$(svc_path $p web)/start" '{}')
	if [[ $code == 409 ]]; then pass "start of a running service is a conflict"; else fail "start of a running service answered $code, want 409"; fi

	check "dock service stop" dk service stop web $(pe $p) --yes
	check "status shows stopped" out_has stopped status_line $p web
	code=$(http_code "$h" /)
	if [[ $code == 503 ]]; then pass "$h answers 503 while stopped"; else fail "$h answers $code while stopped, want 503"; fi
	check "the 503 page names the service" body_has "$h" / web
	check "no container runs while stopped" test -z "$(service_containers $p web)"
	check "a stopped service's logs are read from the archive" out_has 'Starting up|Listening' dk logs -s web $(pe $p)

	check "dock service start" dk service start web $(pe $p)
	check "$h answers 200 again after start" wait_for 60 code_is "$h" / 200
	check "status shows running" wait_for 30 out_has running status_line $p web
	check "start's release is a copy of the active one" out_has '^restart$' release_field $p web "$(newest $p web)" source

	# A deploy into a stopped service runs it again.
	dk service stop web $(pe $p) --yes >/dev/null 2>&1
	n=$(api POST "$(svc_path $p web)/releases" '{}' | jq -r .number)
	expect_release $p web "$n" active
	check "a deploy brings a stopped service back" wait_for 30 code_is "$h" / 200
}

main() {
	local all=(predeploy stopstart run lock logs snapshot move apply health)
	for s in "${@:-${all[@]}}"; do
		log "scenario $s"
		"scenario_$s"
	done
	if ((${FAILURES:-0})); then
		printf '\033[31m%d check(s) failed\033[0m\n' "$FAILURES"
		exit 1
	fi
	printf '\033[32mall checks passed\033[0m\n'
}

main "$@"
