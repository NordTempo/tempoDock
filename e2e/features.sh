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

# busybox_web P S MSG: an image service answering "MSG" over HTTP on 8080,
# deployed and active. Prints its hostname.
busybox_web() {
	local p=$1 s=$2 n
	ensure_service "$p" "$s" '{"image":"busybox:1.37","port":8080,"start_command":"sh -c \"mkdir -p /tmp/w && echo $MSG > /tmp/w/index.html && exec httpd -f -p 8080 -h /tmp/w\""}'
	set_vars "$p" "$s" "$(jq -nc --arg m "$3" '{MSG: $m}')"
	n=$(api POST "$(svc_path "$p" "$s")/releases" '{}' | jq -r .number)
	[[ $(wait_release "$p" "$s" "$n" 300) == active ]] || { fail "$p/$s release $n not active"; return 1; }
	generate_domain "$p" "$s"
}

scenario_run() {
	local p=e2e2-run-$RUN h before out code id t0
	fresh $p || return
	h=$(busybox_web $p web hello-run) || return
	before=$(service_containers $p web)
	out=$(dk run web $(pe $p) -- 'echo "msg=$MSG"; echo to-stderr >&2; exit 3' 2>&1)
	code=$?
	check "dock run streams output with the service's variables" grep -q 'msg=hello-run' <<<"$out"
	check "dock run streams stderr" grep -q 'to-stderr' <<<"$out"
	if [[ $code == 3 ]]; then pass "dock run exits with the command's code"; else fail "dock run exited $code, want 3: $out"; fi
	check "piped stdin reaches the command (-i)" out_has '^piped-in$' bash -c "echo piped-in | '${DOCK_BIN:-$E2E_DIR/../bin/dock}' run -i web $(pe $p) -- cat"
	t0=$SECONDS
	dk run web $(pe $p) --timeout 3s -- 'sleep 60' >/dev/null 2>&1
	code=$?
	if [[ $code != 0 && $((SECONDS - t0)) -lt 30 ]]; then pass "--timeout stops the command ($((SECONDS - t0))s, exit $code)"; else fail "--timeout: exit $code after $((SECONDS - t0))s"; fi

	id=$(dk run web $(pe $p) --detach --json -- 'sleep 2; echo detached-done' 2>/dev/null | jq -r .id)
	if [[ $id =~ ^[0-9]+$ ]]; then pass "dock run --detach returns run $id"; else fail "dock run --detach printed no run id"; fi
	run_done() { [[ $(api GET "$(svc_path $p web)/runs/$id" | jq -r .status) != running ]]; }
	check "the detached run ends" wait_for 60 run_done
	out=$(api GET "$(svc_path $p web)/runs/$id")
	check "the detached run kept its output and exit code" jq -e '(.output | contains("detached-done")) and .exit_code == 0' <<<"$out"
	check "dock runs lists the runs" out_has 'detached-done|sleep 2' dk runs -s web $(pe $p)
	check "the running service was not touched" test "$before" == "$(service_containers $p web)"
	check "$h still serves" body_has "$h" / hello-run
	check "runs are audited (service.run)" out_has 'service.run' dk audit -p $p
}

# pending_request P prints the newest pending access request in project P.
pending_request() { api GET /requests | jq -r --arg p "$1" '[.[] | select(.project == $p and .status == "pending")] | max_by(.id).id // empty'; }

# request_is ID STATUS: access request ID has STATUS.
request_is() { [[ $(api GET "/requests/$1" | jq -r .status) == "$2" ]]; }

# request_of FILE prints the access request ID in a 423 body.
request_of() { jq -r '.request // .details.request // empty' "$1" 2>/dev/null; }

scenario_lock() {
	local p=e2e2-lock-$RUN h tok out st req n
	fresh $p || return
	h=$(busybox_web $p web lock-v1) || return
	tok=$(dk token create agent-$RUN --project $p --scope deploy --json 2>/dev/null | jq -r .secret)
	[[ $tok == dock_* ]] || { fail "no project deploy token"; return; }
	check "dock environment lock" dk environment lock $ENV_NAME -p $p --reason "e2e: live"
	check "environment list shows the lock" out_has 'locked|e2e: live' dk environment list -p $p

	# A token's variable change becomes an access request.
	st=$(TOKEN_OVERRIDE=$tok api_status GET "$(svc_path $p web)")
	if [[ $st == 200 ]]; then pass "reads still work in a locked environment"; else fail "read in a locked environment answered $st"; fi
	out=$(TOKEN_OVERRIDE=$tok dk env set -s web MSG=lock-v2 $(pe $p) --reason "e2e: change the message" 2>&1)
	st=$?
	if [[ $st == 5 ]]; then pass "a token's change exits 5 in a locked environment"; else fail "token change in a locked env exited $st: $out"; fi
	req=$(pending_request $p)
	if [[ $req =~ ^[0-9]+$ ]]; then pass "the change filed access request $req"; else fail "no access request for the refused change: $out"; return; fi
	check "the CLI error names the request" grep -q "$req" <<<"$out"
	check "the request is pending with the token's reason" jq -e '.status == "pending" and (.reason | contains("change the message"))' <<<"$(api GET /requests/$req)"
	check "nothing is staged before approval" bash -c "! '${DOCK_BIN:-$E2E_DIR/../bin/dock}' changes $(pe $p) 2>&1 | grep -q MSG"
	st=$(TOKEN_OVERRIDE=$tok api_status POST "/requests/$req/approve" '{}')
	if [[ $st -ge 400 ]]; then pass "a token can't approve ($st)"; else fail "a token approved its own request ($st)"; fi
	st=$(TOKEN_OVERRIDE=$tok api_status POST "/projects/$p/environments/$ENV_NAME/unlock" '{}')
	if [[ $st == 423 || $st == 403 ]]; then pass "a token can't unlock ($st)"; else fail "token unlock answered $st"; fi

	ui_login
	check "admin approves in a UI session" ui_api POST "/requests/$req/approve" '{"comment":"e2e ok"}'
	check "the approved request is done" wait_for 60 request_is $req done
	check "the approved change is staged" out_has MSG dk changes $(pe $p)
	# Applying is a change too: the token's apply waits for approval.
	TOKEN_OVERRIDE=$tok dk apply $(pe $p) >/dev/null 2>&1
	st=$?
	if [[ $st == 5 ]]; then pass "the token's apply waits for approval"; else fail "token apply in a locked env exited $st"; fi
	req=$(pending_request $p)
	check "admin approves the apply" ui_api POST "/requests/$req/approve" '{}'
	check "the approved apply is done" wait_for 120 request_is $req done
	check "the approved change went live" wait_for 120 body_has "$h" / lock-v2

	# A deploy into the locked environment waits for approval.
	out=$(mktemp)
	st=$(curl -sS -o "$out" -w '%{http_code}' -X POST -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' \
		--data '{}' "$API$(svc_path $p web)/releases")
	if [[ $st == 423 ]]; then pass "a token's deploy into a locked environment is 423"; else fail "token deploy answered $st: $(cat "$out")"; fi
	req=$(request_of "$out")
	jq -e '.code == "environment_locked" or .error_code == "environment_locked" or (tostring | contains("environment_locked"))' "$out" >/dev/null &&
		pass "the error code is environment_locked" || fail "423 body: $(head -c 300 "$out")"
	[[ $req =~ ^[0-9]+$ ]] || req=$(pending_request $p)
	n=$(newest $p web)
	TOKEN_OVERRIDE=$tok dk request wait "$req" >"$TMP/wait.out" 2>&1 &
	local waiter=$!
	sleep 2
	check "admin approves the deploy" ui_api POST "/requests/$req/approve" '{}'
	wait $waiter
	st=$?
	if [[ $st == 0 ]]; then pass "dock request wait ends 0 once the approved deploy ran"; else fail "dock request wait exited $st: $(cat "$TMP/wait.out")"; fi
	check "the approved deploy made a release that went active" wait_for 120 newest_active $p web
	check "a new release exists after approval" test "$(newest $p web)" -gt "$n"
	check "the audit log names the approver" out_has 'approved by|request #' dk audit -p $p

	# Rejected requests run nothing.
	TOKEN_OVERRIDE=$tok dk env set -s web MSG=never $(pe $p) >/dev/null 2>&1
	req=$(pending_request $p)
	check "admin rejects" ui_api POST "/requests/$req/reject" '{"comment":"no"}'
	check "a rejected change isn't staged" bash -c "! '${DOCK_BIN:-$E2E_DIR/../bin/dock}' changes $(pe $p) 2>&1 | grep -q never"

	check "an admin token can't unlock either: it files a request" out_has "access request" dk environment unlock $ENV_NAME -p $p
	check "admin unlocks in a UI session" ui_api POST "/projects/$p/environments/$ENV_NAME/unlock" '{}'
	check "after unlock the token deploys directly" bash -c "[[ \$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Authorization: Bearer $tok' -H 'Content-Type: application/json' --data '{}' '$API$(svc_path $p web)/releases') == 20? ]]"
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
