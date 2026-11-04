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
	{ "$@" 2>&1 || true; } | grep -qE -- "$re"
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

# env_server P prints the server P's environment runs on.
env_server() { api GET "/projects/$1/environments" | jq -r --arg e "$ENV_NAME" '.[] | select(.name == $e) | .server'; }

# service_containers P S prints the running containers of service S, via
# the Docker of its server's container (e2e/vm names them as the servers).
service_containers() {
	local id
	id=$(api GET "$(svc_path "$1" "$2")" | jq -r .id)
	docker exec "$(env_server "$1")" docker ps -q --filter label=dock.managed=true --filter "label=dock.service=$id"
}

# logs_kept P S N: release N's removed container's output was archived.
logs_kept() { [[ $(release "$1" "$2" "$3" | jq -r .logs_kept) == true ]]; }

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
	ensure_service $p site '{}'
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

	# A source upload into the locked environment is kept and built on approval.
	local sh
	sh=$(generate_domain $p site)
	TOKEN_OVERRIDE=$tok deploy_source $p site "$APPS/static-site" >/dev/null 2>&1 &&
		fail "a token's source deploy into a locked env was accepted" || pass "a token's source deploy waits"
	req=$(pending_request $p)
	if [[ $req =~ ^[0-9]+$ ]]; then pass "the upload filed request $req"; else fail "no request for the source upload"; fi
	check "nothing was built before approval" test "$(api GET "$(svc_path $p site)/releases" | jq length)" == 0
	check "admin approves the upload" ui_api POST "/requests/$req/approve" '{}'
	check "the approved upload was built and went active" wait_for 300 newest_active $p site
	check "$sh serves the uploaded site" wait_for 30 body_has "$sh" / "static-site"

	check "an admin token can't unlock either: it files a request" out_has "access request" dk environment unlock $ENV_NAME -p $p
	check "admin unlocks in a UI session" ui_api POST "/projects/$p/environments/$ENV_NAME/unlock" '{}'
	check "after unlock the token deploys directly" bash -c "[[ \$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Authorization: Bearer $tok' -H 'Content-Type: application/json' --data '{}' '$API$(svc_path $p web)/releases') == 20? ]]"
}

# sink_start starts e2e2-sink on e2e2-net: an HTTP server that appends
# every POST body to a file and returns them all on GET /got. Prints its URL.
SINK_IP=${E2E_SINK_IP:-10.250.250.20}
sink_start() {
	docker rm -fv e2e2-sink >/dev/null 2>&1
	docker run -d --name e2e2-sink --network e2e2-net --ip "$SINK_IP" python:3.13-alpine python -c '
import http.server
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        open("/tmp/got", "ab").write(self.rfile.read(n) + b"\n")
        self.send_response(204); self.end_headers()
    def do_GET(self):
        self.send_response(200); self.end_headers()
        try: self.wfile.write(open("/tmp/got", "rb").read())
        except FileNotFoundError: pass
http.server.ThreadingHTTPServer(("", 8080), H).serve_forever()' >/dev/null
	wait_for 30 curl -fsS -o /dev/null "http://$SINK_IP:8080/got"
	echo "http://$SINK_IP:8080/ingest"
}

# sink_has PATTERN: the sink received a line matching PATTERN.
sink_has() { curl -fsS "http://$SINK_IP:8080/got" | grep -qE -- "$1"; }

scenario_logs() {
	local p=e2e2-logs-$RUN n1 n2 url
	fresh $p || return
	ensure_service $p worker '{"image":"busybox:1.37","port":8080,"start_command":"sh -c \"echo boot-$MSG; (while true; do echo tick-$MSG; sleep 1; done) & mkdir -p /tmp/w && exec httpd -f -p 8080 -h /tmp/w\""}'
	set_vars $p worker '{"MSG":"hist1"}'
	n1=$(api POST "$(svc_path $p worker)/releases" '{}' | jq -r .number)
	expect_release $p worker "$n1" active
	sleep 3
	set_vars $p worker '{"MSG":"hist2"}' true
	n2=$(newest $p worker)
	expect_release $p worker "$n2" active
	check "release $n1's logs are archived" wait_for 90 logs_kept $p worker "$n1"
	check "release $n1's container is gone" test "$(service_containers $p worker | wc -l)" -eq 1
	check "dock logs -r $n1 reads the removed container's output" out_has 'boot-hist1' dk logs -s worker -r "$n1" -n 0 $(pe $p)
	check "release $n1's history has its ticks" out_has 'tick-hist1' dk logs -s worker -r "$n1" $(pe $p)
	check "the active release's logs are its own" bash -c "'${DOCK_BIN:-$E2E_DIR/../bin/dock}' logs -s worker $(pe $p) 2>&1 | grep -q boot-hist2 && ! '${DOCK_BIN:-$E2E_DIR/../bin/dock}' logs -s worker $(pe $p) 2>&1 | grep -q boot-hist1"

	# A drain to an HTTP sink.
	url=$(sink_start) || { fail "log sink didn't start"; return; }
	check "dock logs drain add http" dk logs drain add http "$url" --name sink --header X-E2E=yes $(pe $p)
	check "dock logs drain list shows it" out_has sink dk logs drain list $(pe $p)
	check "the sink receives the running service's lines" wait_for 60 sink_has 'tick-hist2'
	check "drained lines carry project and service" sink_has "\"service\": ?\"worker\".*|\"project\": ?\"$p\""
	set_vars $p worker '{"MSG":"hist3"}' true
	check "a new release's lines are drained too" wait_for 120 sink_has 'boot-hist3'
	local id
	id=$(api GET "/projects/$p/environments/$ENV_NAME/log-drains" | jq -r '.[0].id')
	check "drain test delivers" api POST "/projects/$p/environments/$ENV_NAME/log-drains/$id/test" '{}'
	check "dock logs drain remove" dk logs drain remove sink --yes $(pe $p)
	docker rm -fv e2e2-sink >/dev/null 2>&1
}

# psql_in P S SQL runs SQL in service S's own database through dock run,
# printing unaligned tuples.
psql_in() { dk run "$2" -p "$1" -e "$ENV_NAME" -- "psql \"\$DATABASE_URL\" -tAc \"$3\"" 2>/dev/null; }

scenario_snapshot() {
	local p=e2e2-snap-$RUN sid out
	fresh $p || return
	dk service create db $(pe $p) --template postgres >/dev/null || { fail "$p: db not created"; return; }
	check "db is active" wait_for 180 newest_active $p db
	sleep 3
	psql_in $p db "create table t (n int); insert into t select generate_series(1, 3)" >/dev/null
	check "three rows before the snapshot" out_has '^3$' psql_in $p db "select count(*) from t"
	out=$(dk volume snapshot -s db $(pe $p) --note "e2e before" --json 2>/dev/null)
	sid=$(jq -r '.id // .snapshot.id' <<<"$out")
	if [[ $sid =~ ^[0-9]+$ ]]; then pass "snapshot $sid taken ($(jq -r '.size_bytes // empty' <<<"$out") bytes, paused $(jq -r '.paused_ms // empty' <<<"$out") ms)"; else fail "no snapshot: $out"; return; fi
	check "the snapshot is listed" out_has "e2e before" dk volume snapshot list -s db $(pe $p)
	psql_in $p db "delete from t; insert into t values (99)" >/dev/null
	check "the data changed after the snapshot" out_has '^1$' psql_in $p db "select count(*) from t"

	check "restore into a new service" dk volume snapshot restore "$sid" -s db --as db-old $(pe $p) --yes
	check "db-old is active" wait_for 180 newest_active $p db-old
	check "db-old has the snapshot's three rows" wait_for 60 out_has '^3$' psql_in $p db-old "select count(*) from t"
	check "db itself was not touched" out_has '^99$' psql_in $p db "select n from t"

	check "restore in place" dk volume snapshot restore "$sid" -s db --in-place $(pe $p) --yes
	check "db is active after the in-place restore" wait_for 180 newest_active $p db
	check "db has the snapshot's three rows again" wait_for 60 out_has '^3$' psql_in $p db "select count(*) from t"
	check "the in-place restore left a safety snapshot" test "$(api GET "$(svc_path $p db)/snapshots" | jq 'if type == "array" then . else .snapshots end | length')" -ge 2
}

scenario_apply() {
	local p=e2e2-apply-$RUN h f=$TMP/project.toml out who
	fresh $p || return
	h=$(busybox_web $p web apply-v1) || return
	check "dock project export" bash -c "'${DOCK_BIN:-$E2E_DIR/../bin/dock}' project export $p > $f"
	check "the export round-trips: its plan changes nothing" bash -c "! '${DOCK_BIN:-$E2E_DIR/../bin/dock}' apply $f --plan 2>&1 | grep -qE '^ +[+~-] '"
	# The file adds a service and changes web's start command.
	cp "$f" "$f.orig"
	printf '\n  [[environments.services]]\n    name = %s\n    image = %s\n    port = 80\n' "'who'" "'traefik/whoami:v1.11'" >>"$f"
	sed -i 's|echo \$MSG >|echo applied-\$MSG >|' "$f"
	out=$(dk apply "$f" --dry-run 2>&1)
	check "the dry run shows the new service" grep -qE '\+.*who' <<<"$out"
	check "the dry run shows the changed start command" grep -qE '~.*web.*start_command' <<<"$out"
	check "the dry run stages nothing" bash -c "! '${DOCK_BIN:-$E2E_DIR/../bin/dock}' changes $(pe $p) 2>&1 | grep -qE 'who|start_command'"
	check "the dry run creates no service" test "$(api_status GET "$(svc_path $p who)")" == 404
	check "--plan --json is machine-readable" bash -c "'${DOCK_BIN:-$E2E_DIR/../bin/dock}' apply $f --plan --json 2>/dev/null | jq -e . >/dev/null"
	check "dock apply FILE stages it" dk apply "$f"
	check "the staged changes list it" out_has 'start_command|who' dk changes $(pe $p)
	check "dock apply deploys the staged changes" dk apply $(pe $p)
	check "who is active" wait_for 180 newest_active $p who
	check "web runs the new start command" wait_for 120 body_has "$h" / applied-apply-v1
	who=$(generate_domain $p who)
	check "$who serves" wait_for 30 code_is "$who" / 200
	check "applying the file again plans nothing" bash -c "! '${DOCK_BIN:-$E2E_DIR/../bin/dock}' apply $f --plan 2>&1 | grep -qE '^ +[+~-] '"
	# Removing who from the file removes nothing unless --prune.
	sed 's|echo \$MSG >|echo applied-\$MSG >|' "$f.orig" >"$f"
	check "the file without who is valid" dk apply "$f" --plan
	check "without --prune nothing is removed" bash -c "! '${DOCK_BIN:-$E2E_DIR/../bin/dock}' apply $f --plan 2>&1 | grep -qE '^ +- '"
	check "--prune plans who's removal" out_has '^ +-.*who' dk apply "$f" --plan --prune

	# A Compose stack: web plus Postgres.
	local c=e2e2-compose-$RUN wh
	out=$(dk import compose "$APPS/compose-stack/docker-compose.yml" -p $c 2>&1)
	check "compose plan maps postgres to the template" grep -qE '\+.*db.*template postgres' <<<"$out"
	check "compose plan rewrites web's DATABASE_URL to the template's" grep -q 'db.DATABASE_URL' <<<"$out"
	check "compose plan warns about the host port" grep -q "8080:8080" <<<"$out"
	check "the plan created nothing" test "$(api_status GET "/projects/$c")" == 404
	check "dock import compose --apply --yes" dk import compose "$APPS/compose-stack/docker-compose.yml" -p $c --apply --yes
	use_proxy_of $c
	check "compose db is active" wait_for 240 newest_active $c db
	check "compose web is active" wait_for 240 newest_active $c web
	wh=$(api GET "$(svc_path $c web)/domains" | jq -r '.[0].hostname')
	check "compose web reaches its database" wait_for 60 body_has "$wh" / 'hello-compose db-reachable'
	check "compose db has a volume" test "$(api GET "$(svc_path $c db)/volumes" | jq length)" -ge 1
}

# svc_status P S FIELD prints a field of the service's status.
svc_status() { api GET "$(svc_path "$1" "$2")" | jq -r ".status.$3"; }
status_is() { [[ $(svc_status "$1" "$2" "$3") == "$4" ]]; }

# alert_open KIND [PROJECT|SERVER]: an open alert of KIND (naming it) exists.
alert_open() { api GET /alerts | jq -e --arg k "$1" --arg w "${2:-}" '[.[]? // .alerts[]? | select(.kind == $k and .resolved_at == null and ($w == "" or .project == $w or .server == $w))] | length > 0' >/dev/null; }
alert_closed() { ! alert_open "$@"; }

scenario_health() {
	local p=e2e2-health-$RUN h ctr srv n
	fresh $p || return
	ensure_service $p web '{"image":"busybox:1.37","port":8080,"health_check_path":"/health",
		"health_check_interval_seconds":2,"health_check_retries":2,"health_check_probe_timeout_seconds":1,"health_check_start_period_seconds":3,
		"start_command":"sh -c \"mkdir -p /tmp/w && echo ok > /tmp/w/health && echo up > /tmp/w/index.html && exec httpd -f -p 8080 -h /tmp/w\""}'
	n=$(api POST "$(svc_path $p web)/releases" '{}' | jq -r .number)
	expect_release $p web "$n" active
	h=$(generate_domain $p web)
	check "settings took the ongoing check" jq -e '.health_check_interval_seconds == 2' <<<"$(api GET "$(svc_path $p web)")"
	check "the service reads healthy" wait_for 30 status_is $p web health healthy
	srv=$(env_server $p)
	ctr=$(service_containers $p web)
	# The app breaks without crashing: its health path starts failing.
	docker exec "$srv" docker exec "$ctr" rm /tmp/w/health
	check "the service reads unhealthy" wait_for 60 status_is $p web health unhealthy
	check "an unhealthy alert opens" wait_for 30 alert_open unhealthy $p
	check "the health check restarts the container in place" wait_for 60 bash -c "[[ \$(curl -s -H 'Authorization: Bearer $DOCK_TOKEN' '$API$(svc_path $p web)' | jq -r '.status.health_restarts // 0') -ge 1 ]]"
	check "same container after the restart" test "$ctr" == "$(service_containers $p web)"
	check "healthy again after the restart" wait_for 60 status_is $p web health healthy
	check "the unhealthy alert resolves" wait_for 60 alert_closed unhealthy $p
	check "health restarts aren't counted as crashes" status_is $p web restart_count 0
	check "$h serves" body_has "$h" / up
}

# server_is NAME STATUS: dock server list says NAME is STATUS.
server_is() { [[ $(api GET /servers | jq -r --arg n "$1" '.[] | select(.name == $n) | .status') == "$2" ]]; }

scenario_nodedown() {
	local p=e2e2-down-$RUN node=${E2E_NODE_CONTAINER:-e2e2-node1} h n
	fresh $p || return
	# Its staging environment goes on the node: pinned before it exists.
	local ENV_NAME=staging
	check "pin the project's non-production to $node" api PUT "/projects/$p/placements/non_production" "{\"server\":\"$node\"}"
	check "create staging" dk environment create staging -p $p
	if [[ $(env_server $p) != "$node" ]]; then fail "staging is on $(env_server $p), not $node"; return; fi
	use_proxy_of $p
	h=$(busybox_web $p web node-up) || return
	check "$h serves from the node" body_has "$h" / node-up
	log "stopping $node"
	docker stop "$node" >/dev/null
	check "the node reads offline" wait_for 60 server_is "$node" offline
	check "the service status says its server is offline" wait_for 30 status_is $p web server_offline true
	check "a server offline alert opens (1 minute here)" wait_for 180 alert_open server_offline "$node"
	log "starting $node"
	docker start "$node" >/dev/null
	check "the node is back online" wait_for 180 server_is "$node" online
	check "$h serves again after the node's reboot" wait_for 120 body_has "$h" / node-up
	check "the offline alert resolves" wait_for 120 alert_closed server_offline "$node"
	n=$(api POST "$(svc_path $p web)/releases" '{}' | jq -r .number)
	expect_release $p web "$n" active
}

# on_node P: put P's staging environment on the node (pinned before it
# exists) and use it as ENV_NAME in the caller (declare `local ENV_NAME`).
on_node() {
	local node=${E2E_NODE_CONTAINER:-e2e2-node1}
	ENV_NAME=staging
	api PUT "/projects/$1/placements/non_production" "{\"server\":\"$node\"}" >/dev/null &&
		dk environment create staging -p "$1" >/dev/null 2>&1 &&
		[[ $(env_server "$1") == "$node" ]] || { fail "$1: staging is not on $node"; return 1; }
	use_proxy_of "$1"
}

# Stop and start on the node: its Caddy must answer 503 too (node protocol 17).
scenario_stopnode() {
	local p=e2e2-stopn-$RUN h code ENV_NAME
	fresh $p || return
	on_node $p || return
	h=$(busybox_web $p web on-node) || return
	check "$h serves from the node" body_has "$h" / on-node
	check "dock service stop on the node" dk service stop web $(pe $p) --yes
	code=$(http_code "$h" /)
	if [[ $code == 503 ]]; then pass "the node answers 503 while stopped"; else fail "the node answers $code while stopped, want 503"; fi
	check "no container runs on the node while stopped" test -z "$(service_containers $p web)"
	check "a stopped service's logs read without error on the node" dk logs -s web -n 0 $(pe $p)
	check "dock service start on the node" dk service start web $(pe $p)
	check "$h serves again" wait_for 60 body_has "$h" / on-node
}

# A backup on the node that runs well past the node link's 30 s call
# timeout (ADR 0045 lifted it for jobs). E2E_BACKUP_ROWS sizes it.
scenario_backup() {
	local p=e2e2-bak-$RUN rows=${E2E_BACKUP_ROWS:-20000000} t0 took out ENV_NAME
	fresh $p || return
	on_node $p || return
	dk service create db $(pe $p) --template postgres >/dev/null || { fail "$p: db not created"; return; }
	check "db is active on the node" wait_for 180 newest_active $p db
	sleep 3
	log "filling db with $rows rows"
	dk run db $(pe $p) --timeout 30m -- "psql \"\$DATABASE_URL\" -v ON_ERROR_STOP=1 -c \"create table big as select g, md5(random()::text) || md5(random()::text) as v from generate_series(1, $rows) g\"" >/dev/null 2>&1 ||
		{ fail "filling the database failed"; return; }
	log "database size: $(psql_in $p db "select pg_size_pretty(pg_database_size(current_database()))")"
	t0=$SECONDS
	out=$(dk backup run db $(pe $p) 2>&1)
	local rc=$?
	took=$((SECONDS - t0))
	if [[ $rc -eq 0 ]] && grep -qiE 'done|complete|✓' <<<"$out"; then pass "the backup on the node finished in ${took}s"; else fail "backup on the node after ${took}s: $(tail -5 <<<"$out")"; fi
	((took > 30)) && pass "the backup ran past 30 s ($took s)" || log "note: the backup took only ${took}s; raise E2E_BACKUP_ROWS"
	check "dock backup list shows it succeeded" out_has 'succeeded|done|ok' dk backup list -s db $(pe $p)
}

main() {
	local all=(predeploy stopstart stopnode run lock logs snapshot apply health nodedown backup)
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
