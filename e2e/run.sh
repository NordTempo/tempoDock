#!/usr/bin/env bash
# Real-world scenarios against a live Tempo Dock server.
#
#   DOCK_URL=http://192.168.10.50:7070 DOCK_TOKEN=<admin token> e2e/run.sh [scenario...]
#
# Scenarios (default: apps pg zerodowntime failures perms):
#   apps          Node (Railpack), Go (Dockerfile), Python, static, Next.js from source
#   pg            Postgres with a volume + API using ${{postgres.*}} references;
#                 data survives a stateful redeploy, never two postgres at once
#   zerodowntime  hammer an app during redeploy, restart, config apply, rollback
#   failures      health check fail, crash at start, OOM, build error, build over
#                 the BuildKit cap, missing image, bad and oversized uploads
#   perms         a project-scoped deploy token can't escape its project
#   daemon        stop / SIGKILL the daemon mid-deploy (needs DOCK_SSH=host with sudo,
#                 or e2e/vm's servers: E2E_SERVER_IPS set, the control plane's container)
#   cleanup       archive every e2e-* project
#
# Apps are reached through the proxy on port 80 at their generated hostnames,
# so the machine running this must resolve the server's base domain.
# Projects are named e2e-<run>-*, <run> being E2E_RUN (default: a timestamp,
# printed at the start). Set E2E_RUN to reuse an earlier run's projects, e.g.
# to run daemon after apps. Archived projects keep their names, so a run's
# projects can't be reused after cleanup.
set -uo pipefail
source "$(dirname "$0")/lib.sh"
APPS="$E2E_DIR/apps"
HAMMER="${HAMMER:-$(mktemp -d)/hammer}"

build_hammer() { [[ -x $HAMMER ]] || (cd "$E2E_DIR/.." && go build -o "$HAMMER" ./e2e/hammer); }

# expect_release P S N WANT: wait for release N and check its final status.
expect_release() {
	local st
	st=$(wait_release "$1" "$2" "$3" 900)
	if [[ $st == "$4" ]]; then
		pass "$1/$2 release $3 is $st"
	else
		fail "$1/$2 release $3 is $st, want $4: $(release "$1" "$2" "$3" | jq -c '{error_code, error}')"
	fi
}

# hammer_start URL / hammer_stop NAME: zero failed requests or it's a FAIL.
hammer_start() { "$HAMMER" -url "$1" ${E2E_PROXY:+-connect "$E2E_PROXY"} -c 8 -every 20ms -d 30m >"$TMPDIR_E2E/hammer.out" & HAMMER_PID=$!; sleep 1; }
hammer_stop() {
	kill -INT "$HAMMER_PID"; wait "$HAMMER_PID"
	local line; line=$(head -1 "$TMPDIR_E2E/hammer.out")
	if [[ $line == *" failed=0 "* ]]; then pass "$1: $line"; else fail "$1: $(cat "$TMPDIR_E2E/hammer.out")"; fi
}

# source_app P S DIR WANT [settings]: deploy DIR and check the body has WANT.
source_app() {
	local p=$1 s=$2 dir=$3 want=$4 settings=${5:-'{}'} h n
	ensure_project "$p"; use_proxy_of "$p"; ensure_service "$p" "$s" "$settings"
	h=$(generate_domain "$p" "$s")
	local t0=$SECONDS
	n=$(deploy_source "$p" "$s" "$dir" 2>/dev/null) || { fail "$p/$s upload rejected"; return; }
	expect_release "$p" "$s" "$n" active
	log "$p/$s release $n took $((SECONDS - t0))s"
	if wait_http "$h" / "$want" 20; then pass "$h serves '$want'"; else fail "$h does not serve '$want'"; fi
}

scenario_apps() {
	source_app $E2E-node web "$APPS/node-express" "node-express" '{"health_check_path":"/healthz"}'
	source_app $E2E-go api "$APPS/go-lab" "go-lab" '{"health_check_path":"/healthz"}'
	source_app $E2E-py api "$APPS/python-fastapi" "python-fastapi" '{"health_check_path":"/healthz"}'
	source_app $E2E-static site "$APPS/static-site" "static-site v1"
	source_app $E2E-next web "$APPS/nextjs" "nextjs v1"
}

scenario_pg() {
	local p=$E2E-pg h n body
	ensure_project $p; use_proxy_of $p
	ensure_service $p postgres '{"image":"postgres:17-alpine","port":5432}'
	set_vars $p postgres '{"POSTGRES_USER":"app","POSTGRES_PASSWORD":"e2e-not-a-secret","POSTGRES_DB":"app",
		"DATABASE_URL":"postgresql://${{postgres.POSTGRES_USER}}:${{postgres.POSTGRES_PASSWORD}}@${{postgres.DOCK_PRIVATE_DOMAIN}}:5432/${{postgres.POSTGRES_DB}}"}'
	api GET "$(svc_path $p postgres)/volumes" | jq -e 'length > 0' >/dev/null ||
		api POST "$(svc_path $p postgres)/volumes" '{"name":"pgdata","mount_path":"/var/lib/postgresql/data"}' >/dev/null
	n=$(api POST "$(svc_path $p postgres)/releases" '{}' | jq -r .number)
	expect_release $p postgres "$n" active
	ensure_service $p api '{"health_check_path":"/healthz"}'
	set_vars $p api '{"DATABASE_URL":"${{postgres.DATABASE_URL}}"}'
	h=$(generate_domain $p api)
	n=$(deploy_source $p api "$APPS/node-pg-api" 2>/dev/null)
	expect_release $p api "$n" active
	wait_http "$h" / '"db_host":"postgres"' 20 || fail "api can't reach postgres by service name: $(http_get "$h" /)"
	curl -sS -X POST ${E2E_PROXY:+--connect-to "$h:80:$E2E_PROXY"} "http://$h/items?n=persist-$$" >/dev/null
	before=$(http_get "$h" / | jq .n)

	# Stateful redeploy: count running postgres containers every 100ms.
	local count="$TMPDIR_E2E/pgcount"
	(while :; do curl -s --max-time 1 ${E2E_PROXY:+--connect-to "$h:80:$E2E_PROXY"} -o /dev/null -w '%{http_code}\n' "http://$h/healthz"; sleep 0.1; done) >"$count" &
	local watcher=$!
	n=$(api POST "$(svc_path $p postgres)/releases" '{}' | jq -r .number)
	expect_release $p postgres "$n" active
	release $p postgres "$n" | jq -e '[.events[].message] | any(test("Stopping release"))' >/dev/null &&
		pass "postgres redeploy stopped the old container first" || fail "postgres redeploy did not use recreate"
	sleep 3; kill "$watcher"
	log "api health during postgres recreate: $(sort "$count" | uniq -c | tr '\n' ' ')"
	wait_http "$h" / '"n":' 20
	body=$(http_get "$h" /)
	if [[ $(jq .n <<<"$body") == "$before" ]]; then pass "postgres data survived the redeploy ($before rows)"; else fail "rows before=$before after: $body"; fi
	if [[ -n ${DOCK_SSH:-} ]]; then
		local running; running=$(ssh "$DOCK_SSH" "docker ps -q --filter label=dock.service=$(api GET "$(svc_path $p postgres)" | jq .id) | wc -l")
		[[ $running == 1 ]] && pass "exactly one postgres container" || fail "$running postgres containers running"
	fi

	# Changing a referenced variable must reach the services referencing it.
	local applied
	applied=$(api PATCH "/projects/$p/environments/$ENV_NAME/variables" "$(jq -nc --arg n "$RANDOM" '{service: "postgres", apply: true,
		set: {DATABASE_URL: ("postgresql://${{postgres.POSTGRES_USER}}:${{postgres.POSTGRES_PASSWORD}}@${{postgres.DOCK_PRIVATE_DOMAIN}}:5432/${{postgres.POSTGRES_DB}}?application_name=e2e" + $n)}}')" |
		jq -c '[.applied.releases[].service]')
	[[ $applied == *'"api"'* ]] && pass "referencing service redeployed: $applied" ||
		fail "changed postgres.DATABASE_URL; only $applied redeployed, api keeps the stale value"
}

scenario_zerodowntime() {
	local p=$E2E-zd s=web h n first
	ensure_project $p; use_proxy_of $p; ensure_service $p $s '{"health_check_path":"/healthz"}'
	h=$(generate_domain $p $s)
	first=$(deploy_source $p $s "$APPS/go-lab" 2>/dev/null)
	expect_release $p $s "$first" active
	wait_http "$h" / go-lab 20

	hammer_start "http://$h/"
	n=$(deploy_source $p $s "$APPS/go-lab" 2>/dev/null); expect_release $p $s "$n" active
	sleep 2; hammer_stop "redeploy"

	hammer_start "http://$h/"
	n=$(api POST "$(svc_path $p $s)/restart" | jq -r .number); expect_release $p $s "$n" active
	sleep 2; hammer_stop "restart"

	hammer_start "http://$h/"
	n=$(api PATCH "/projects/$p/environments/$ENV_NAME/variables" '{"service":"web","set":{"VERSION":"v2"},"apply":true}' | jq -r '.applied.releases[0].number')
	expect_release $p $s "$n" active
	wait_http "$h" / "go-lab v2" 10 || fail "config apply did not reach traffic"
	sleep 2; hammer_stop "config apply"

	# Rollback to the first release (cached image): time to traffic switch.
	hammer_start "http://$h/"
	local t0; t0=$(date +%s%N)
	n=$(api POST "$(svc_path $p $s)/releases/$first/rollback" | jq -r .number)
	until http_get "$h" / 2>/dev/null | grep -q "go-lab v1"; do sleep 0.05; done
	local ms=$((($(date +%s%N) - t0) / 1000000))
	expect_release $p $s "$n" active
	release $p $s "$n" | jq -r '.events[] | "    \(.created_at[11:23]) \(.message)"' >&2
	if ((ms <= 10000)); then pass "rollback served v1 after ${ms}ms"; else fail "rollback took ${ms}ms"; fi
	sleep 2; hammer_stop "rollback"

	log "deploy again while the previous container is still draining"
	n=$(api POST "$(svc_path $p $s)/restart" | jq -r .number); expect_release $p $s "$n" active
	api PATCH "/projects/$p/environments/$ENV_NAME/variables" '{"service":"web","unset":["VERSION"]}' >/dev/null
}

# fail_config P S JSON_SET WANT_RE: apply variables (and whatever else is
# staged), expect a failed release whose "error_code: error" matches WANT_RE
# while the old one keeps serving. A failed release stages its changes
# again; they are discarded.
fail_config() {
	local p=$1 s=$2 set=$3 want=$4 n got env="/projects/$1/environments/$ENV_NAME"
	n=$(api PATCH "$env/variables" "$(jq -c --arg s "$s" '{service: $s, set: ., apply: true}' <<<"$set")" | jq -r '.applied.releases[0].number')
	expect_release "$p" "$s" "$n" failed
	got=$(release "$p" "$s" "$n" | jq -r '"\(.error_code): \(.error)"')
	[[ $got =~ $want ]] && pass "failed with $got" || fail "want /$want/, got $got"
	if api GET "$env/changes" | jq -e '.count > 0' >/dev/null && api POST "$env/changes/discard" | jq -e '.count == 0' >/dev/null; then
		pass "the failed changes were staged again, and discarded"
	else
		fail "restaged changes: $(api GET "$env/changes" | jq -c .)"
	fi
}

scenario_failures() {
	local p=$E2E-fail s=lab h n
	ensure_project $p; use_proxy_of $p; ensure_service $p $s '{"health_check_path":"/healthz","health_check_timeout_seconds":15}'
	h=$(generate_domain $p $s)
	n=$(deploy_source $p $s "$APPS/go-lab" 2>/dev/null); expect_release $p $s "$n" active
	wait_http "$h" / go-lab 20
	hammer_start "http://$h/"

	log "health check failure"; fail_config $p $s '{"FAIL_HEALTH":"1"}' health_check_failed
	log "crash at start"; fail_config $p $s '{"EXIT_AT_START":"3"}' 'health_check_failed: .*exited with code 3'
	log "OOM at start"
	api PATCH "$(svc_path $p $s)" '{"memory_mb":32}' >/dev/null
	fail_config $p $s '{"ALLOC_MB":"128"}' 'health_check_failed: .*out of memory'

	log "build error"
	n=$(deploy_source $p $s "$APPS/build-broken" 2>/dev/null); expect_release $p $s "$n" failed
	log "build over the BuildKit memory cap"
	local oom="$TMPDIR_E2E/build-oom"
	cp -r "$APPS/build-oom" "$oom"; date +%s%N >"$oom/nonce"
	n=$(deploy_source $p $s "$oom" 2>/dev/null); expect_release $p $s "$n" failed
	release $p $s "$n" | jq -e '.error | test("out of memory")' >/dev/null &&
		pass "over-cap build failed as out of memory" || fail "over-cap build: $(release $p $s "$n" | jq -c '{error_code, error}')"
	log "image that doesn't exist"
	n=$(deploy_image $p $s ghcr.io/nordtempo/e2e-does-not-exist:nope); expect_release $p $s "$n" failed

	log "upload that isn't a tarball"
	local code
	code=$(head -c 4096 /dev/urandom | curl -sS -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $DOCK_TOKEN" \
		-H 'Content-Type: application/gzip' --data-binary @- "$API$(svc_path $p $s)/releases/source")
	[[ $code == 400 ]] && pass "garbage upload rejected with 400" || fail "garbage upload got $code"
	log "upload over the size limit (600 MB)"
	code=$(head -c 600M /dev/urandom | curl -sS -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $DOCK_TOKEN" \
			-H 'Content-Type: application/gzip' --data-binary @- "$API$(svc_path $p $s)/releases/source")
	[[ $code == 413 ]] && pass "oversized upload rejected with 413" || fail "oversized upload got $code"

	hammer_stop "old release served through every failure"
}

scenario_perms() {
	local ct secret
	ensure_project $E2E-node; ensure_service $E2E-node web '{"health_check_path":"/healthz"}'
	ensure_project $E2E-go; ensure_service $E2E-go api
	ct=$(api POST /tokens "$(jq -nc --arg p "$E2E-node" '{name: "e2e-scoped-agent", scope: "deploy", project: $p, expires_in_days: 1}')")
	secret=$(jq -r .secret <<<"$ct")
	expect_code() {
		local want=$1 desc=$2 got; shift 2
		got=$(TOKEN_OVERRIDE=$secret api_status "$@")
		[[ $got == "$want" ]] && pass "$got $desc" || fail "$desc: got $got, want $want"
	}
	expect_code 200 "read own project" GET /projects/$E2E-node/overview
	expect_code 403 "read another project" GET /projects/$E2E-go/overview
	expect_code 403 "deploy to another project" POST "$(svc_path $E2E-go api)/releases" '{"image":"traefik/whoami"}'
	expect_code 403 "set variables in another project" PATCH "/projects/$E2E-go/environments/$ENV_NAME/variables" '{"service":"api","set":{"X":"1"}}'
	expect_code 403 "archive own service" POST "$(svc_path $E2E-node web)/archive"
	expect_code 403 "reveal secret values" GET "/projects/$E2E-node/environments/$ENV_NAME/variables?reveal=true"
	expect_code 403 "create a token" POST /tokens '{"name":"escalate","scope":"admin"}'
	expect_code 403 "list tokens" GET /tokens
	expect_code 403 "create a user" POST /users '{"email":"x@example.com","name":"x","password":"aaaaaaaaaaaaaaaa","role":"admin"}'
	expect_code 403 "create a project" POST /projects '{"name":"e2e-escape"}'
	local n; n=$(TOKEN_OVERRIDE=$secret deploy_source $E2E-node web "$APPS/node-express" 2>/dev/null)
	expect_release $E2E-node web "$n" active
	api DELETE "/tokens/$(jq -r .token.id <<<"$ct")" >/dev/null
	expect_code 401 "revoked token" GET /me
}

# daemon_ssh CMD runs CMD on the control plane: over ssh with DOCK_SSH,
# else in e2e/vm's control plane container.
daemon_ssh() {
	if [[ -n ${DOCK_SSH:-} ]]; then ssh "$DOCK_SSH" "$@"; else docker exec "$SLOT-cp" bash -c "$*"; fi
}
wait_api() { for _ in $(seq 1 30); do curl -sf --max-time 2 "$API/health" >/dev/null && return; sleep 1; done; fail "API did not come back"; }

release_is() { [[ $(release "$1" "$2" "$3" | jq -r .status) == "$4" ]]; }

scenario_daemon() {
	[[ -n ${DOCK_SSH:-}${E2E_SERVER_IPS:-} ]] || { fail "the daemon scenario needs DOCK_SSH=<ssh host with passwordless sudo> or e2e/vm's servers"; return; }
	local p=$E2E-node s=web h n tmp
	ensure_project $p; use_proxy_of $p; ensure_service $p $s '{"health_check_path":"/healthz"}'
	h=$(generate_domain $p $s)
	wait_http "$h" / node-express 5 || { n=$(deploy_source $p $s "$APPS/node-express" 2>/dev/null); expect_release $p $s "$n" active; }

	log "daemon stopped: apps keep serving"
	hammer_start "http://$h/"
	daemon_ssh 'sudo systemctl stop dock'
	sleep 15
	daemon_ssh 'sudo systemctl start dock'; wait_api
	hammer_stop "serving while the daemon was down"

	log "SIGKILL during build"
	# The build has a step that outlasts the kill: with everything else
	# cached it finishes in a few seconds, and the release would go active.
	tmp=$(mktemp -d); cp -r "$APPS/node-express/." "$tmp/"; echo "v-killed" >"$tmp/VERSION"
	jq '.scripts.build = "sleep 120"' "$APPS/node-express/package.json" >"$tmp/package.json"
	hammer_start "http://$h/"
	# On a node the project's server is linked again a moment after the API
	# answers: until then a deploy is refused.
	n=; for _ in $(seq 1 30); do n=$(deploy_source $p $s "$tmp" 2>/dev/null) && break; sleep 1; done
	[[ -n $n ]] || { fail "the deploy to kill was never accepted"; hammer_stop "serving"; return; }
	wait_for 120 release_is $p $s "$n" building || fail "release $n never started building"
	sleep 3
	daemon_ssh 'sudo systemctl kill -s KILL dock'; sleep 3; wait_api
	expect_release $p $s "$n" failed
	local leftovers; leftovers=$(daemon_ssh 'sudo find /var/lib/dock/uploads /var/lib/dock/builds/work -mindepth 1 -maxdepth 1 | wc -l')
	[[ $leftovers == 0 ]] && pass "no upload/build leftovers" || fail "$leftovers upload/build leftovers after crash recovery"
	hammer_stop "serving through a crash during build"

	log "SIGKILL during health check"
	hammer_start "http://$h/"
	n=$(api PATCH "$(svc_path $p $s)" '{"health_check_path":"/nope","apply":true}' | jq -r '.applied.releases[0].number')
	for _ in $(seq 1 600); do [[ $(release $p $s "$n" | jq -r .status) == health_checking ]] && break; sleep 0.2; done
	daemon_ssh 'sudo systemctl kill -s KILL dock'; sleep 3; wait_api
	expect_release $p $s "$n" failed
	[[ $(daemon_ssh "docker ps -aq --filter label=dock.release=$(release $p $s "$n" | jq .id) | wc -l") == 0 ]] &&
		pass "recovery removed the half-deployed container" || fail "container of release $n left behind"
	api GET "/projects/$p/environments/$ENV_NAME/changes" | jq -e 'any(.settings[]; .field == "health_check_path")' >/dev/null &&
		pass "the interrupted change was staged again" || fail "interrupted change not restaged"
	api POST "/projects/$p/environments/$ENV_NAME/changes/discard" >/dev/null
	sleep 2; hammer_stop "serving through a crash during health check"
}

scenario_cleanup() {
	local p
	for p in $(api GET /projects | jq -r '.[].name | select(startswith("e2e-"))'); do
		log "archiving $p"
		api POST "/projects/$p/archive" >/dev/null || fail "archive $p"
	done
}

E2E_RUN=${E2E_RUN:-$(date +%d%H%M)}
E2E="e2e-$E2E_RUN"
log "run $E2E_RUN (projects $E2E-*)"
TMPDIR_E2E=$(mktemp -d)
trap 'rm -rf "$TMPDIR_E2E"' EXIT
build_hammer
for s in "${@:-apps pg zerodowntime failures perms}"; do
	for one in $s; do
		log "=== $one"
		"scenario_$one"
	done
done
if ((${FAILURES:-0} > 0)); then
	printf '\033[31m%d failure(s)\033[0m\n' "$FAILURES"
	exit 1
fi
printf '\033[32mall scenarios passed\033[0m\n'
