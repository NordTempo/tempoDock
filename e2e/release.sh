#!/usr/bin/env bash
# The release walk-through: each feature used once end to end the way a
# user would, through the CLI, against throwaway servers (e2e/vm):
#
#   make web && make build && make analytics-binary
#   e2e/vm/up.sh 1 && source /tmp/e2e2/env && e2e/release.sh [scenario...]
#
# Scenarios (default: all, in this order):
#   traces     tracing on; an Express app with the stock OpenTelemetry SDK
#              (e2e/apps/node-otel) sends traces; dock traces lists them
#   status     a status page with components, an incident and a maintenance
#              window, the public page, a webhook hook
#   vulns      the app's image is scanned; ignore a finding, export SBOMs
#   data       MySQL and Redis in the Data panel: setup, tables, rows, keys,
#              a key changeset, health
#   diagnose   a service that crashes at start gets a likely cause
#   sleep      a non-production environment sleeps and a request wakes it
#
# Projects are rel-<run>, <run> being E2E_RUN (default: the time). After
# the script, walk the same project in the UI (log in with E2E_ADMIN_EMAIL
# and E2E_ADMIN_PASSWORD at $DOCK_URL): the service overview's
# vulnerability strip, Images, Status pages, the Data tab of mysql and
# cache (commit the pending changeset there, open Health), and the public
# page at the address `dock status-page show rel<run>` prints (map
# *.e2e2.test to the proxy).
set -uo pipefail
source "$(dirname "$0")/lib.sh"
RUN=${E2E_RUN:-$(date +%H%M%S)}
P=rel-$RUN
PE=(-p "$P" -e production)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

# sat CMD: CMD (a string, pipes allowed) succeeds; for check and wait_for.
# Without pipefail: `dk ... | grep -q X` stops reading at the first match,
# and a long listing then dies of SIGPIPE (141), failing a check that matched.
sat() { (set +o pipefail; eval "$1"); }

# web_host prints web's generated hostname, generating it once.
web_host() { generate_domain "$P" web; }

scenario_traces() {
	local store=$E2E_DIR/../bin/dock-analytics-linux-amd64 h n E2E_PROXY=${E2E_PROXY:-}
	[[ -x $store ]] || { fail "no $store; make analytics-binary"; return; }
	# The control plane runs this build's trace store instead of downloading a release's.
	docker cp "$store" "$SLOT-cp":/usr/local/lib/dock-analytics
	docker exec "$SLOT-cp" bash -c 'grep -q "^store_binary" /etc/dock/config.toml || {
		printf "\n[analytics]\nstore_binary = \"/usr/local/lib/dock-analytics\"\n" >>/etc/dock/config.toml; systemctl restart dock; }'
	wait_for 60 curl -fsS "$DOCK_URL/api/v1/health" >/dev/null
	dk tracing on >/dev/null
	wait_for 300 sat "dk tracing --json | jq -e .store_running" >/dev/null ||
		{ fail "the trace store didn't start"; return; }
	dk project create "$P" >/dev/null 2>&1
	dk service create web "${PE[@]}" --health-check-path /healthz >/dev/null 2>&1
	(cd "$E2E_DIR/apps/node-otel" && dk deploy "${PE[@]}" -s web >"$TMP/deploy.log" 2>&1) ||
		{ fail "deploying node-otel: $(tail -3 "$TMP/deploy.log")"; return; }
	h=$(web_host)
	use_proxy_of "$P" # placement may have put it on the node
	for path in / /slow /fail; do http_code "$h" "$path" >/dev/null; done
	# Spans reach the store a few seconds after the request (spool, forwarder).
	if wait_for 60 sat "dk traces ${PE[*]} -s web --errors | grep -q /fail" &&
		dk traces "${PE[@]}" -s web | grep "GET /slow" >/dev/null; then
		pass "dock traces lists the app's requests, and --errors its 500"
	else
		fail "no traces: $(dk traces "${PE[@]}" -s web 2>&1 | head -5)"
	fi
	n=$(dk traces "${PE[@]}" -s web --slowest --limit 1 --json | jq -r '.traces[0].trace')
	check "dock traces show prints the slowest trace's spans" sat "dk traces show $n ${PE[*]} -s web | grep -q 'GET /slow'"
}

scenario_status() {
	local page=rel$RUN id h out
	dk project create "$P" >/dev/null 2>&1
	dk status-page remove "$page" --yes >/dev/null 2>&1 # from an earlier run of $RUN
	dk status-page create "$page" --title "Release $RUN" >/dev/null &&
		dk status-page component add "$page" "Web app" --service "$P/production/web" --group Core >/dev/null &&
		dk status-page component add "$page" Billing --manual --group Core >/dev/null &&
		dk status-page publish "$page" >/dev/null || { fail "making status page $page"; return; }
	# Its address carries a random suffix (ADR 0087): ask for it.
	h=$(dk status-page show "$page" --json | jq -r .address)
	id=$(dk incident open "$page" "Card payments failing" --component Billing:partial -m "Investigating." --json | jq -r .id)
	dk incident update "$id" --state identified -m "A processor outage." >/dev/null
	out=$(http_get "$h" /)
	if grep -q "Card payments failing" <<<"$out" && grep -q "Partial outage" <<<"$out" &&
		! grep "$(date -u '+%-d %b')</span>" <<<"$out" | grep "No incidents" >/dev/null; then
		pass "the public page shows the open incident, today's row included"
	else
		fail "public page: $(head -c 400 <<<"$out")"
	fi
	dk incident resolve "$id" -m "Fixed." >/dev/null
	id=$(dk maintenance schedule "$page" "Database upgrade" --component "Web app" --for 30m -m "Writes pause." --json | jq -r .id)
	check "the window is in progress and Web app reads maintenance" sat "dk status-page show $page --json | jq -e '[.components[] | select(.name == \"Web app\")][0].status == \"maintenance\"'"
	check "completing the window" sat "dk maintenance complete $id | grep -q Completed"
	# The public page is cached for a short while.
	check "the public page is operational again" wait_for 90 body_has "$h" / "All systems operational"
}

scenario_vulns() {
	local id
	wait_for 300 sat "dk vulns ${PE[*]} -s web --json | jq -e '.scan.state == \"scanned\"'" >/dev/null ||
		{ fail "web's image wasn't scanned: $(dk vulns "${PE[@]}" -s web 2>&1 | head -3)"; return; }
	pass "web's live image was scanned"
	id=$(dk vulns "${PE[@]}" -s web --min low --json | jq -r '.findings[0] | "\(.id) \(.package)"')
	read -r vuln pkg <<<"$id"
	if [[ -n ${vuln:-} && $vuln != null ]]; then
		check "ignoring $vuln in $pkg" dk vulns ignore "$vuln" --package "$pkg" --reason "release walk" "${PE[@]}" -s web
		check "the ignore is listed" sat "dk vulns ignores | grep -q $vuln"
	fi
	for f in cyclonedx-json spdx-json; do
		check "SBOM as $f" sat "dk sbom ${PE[*]} -s web --format $f | jq -e '(.components // .packages) | length > 0'"
	done
}

# rel_exec SERVICE CMD... runs CMD in the service's container, on the
# server production was placed on (e2e/vm names containers after servers).
rel_exec() {
	local srv c
	srv=$(api GET "/projects/$P/environments" | jq -r '.[] | select(.name == "production") | .server')
	c=$(docker exec "$srv" docker ps --format '{{.Names}}' | grep -m1 "^dock_p$(api GET "/projects/$P" | jq -r .id)_.*_s$(api GET "$(svc_path "$P" "$1")" | jq -r .id)_")
	shift
	docker exec "$srv" docker exec "$c" "$@"
}

scenario_data() {
	local m=(-s mysql) r=(-s cache)
	dk project create "$P" >/dev/null 2>&1
	dk service create cache --template redis "${PE[@]}" >/dev/null &&
		dk service create mysql --image mysql:8.4 --port 3306 "${PE[@]}" >/dev/null &&
		dk env set "${PE[@]}" -s mysql MYSQL_ROOT_PASSWORD=root-e2e MYSQL_DATABASE=shop MYSQL_USER=shop MYSQL_PASSWORD=shop-e2e >/dev/null &&
		dk volume add data /var/lib/mysql "${PE[@]}" -s mysql >/dev/null &&
		dk deploy "${PE[@]}" -s mysql --image mysql:8.4 >/dev/null 2>&1 &&
		dk release wait "${PE[@]}" -s cache >/dev/null 2>&1 || { fail "creating mysql and cache"; return; }
	check "dock db setup on MySQL" dk db setup --yes "${PE[@]}" "${m[@]}"
	rel_exec mysql mysql -ushop -pshop-e2e shop -e "create table orders (id int primary key auto_increment, customer varchar(64), total decimal(10,2)); insert into orders (customer, total) values ('ada', 12.5), ('bob', 99)" 2>/dev/null
	rel_exec cache sh -c 'redis-cli -a "$REDIS_PASSWORD" --no-auth-warning set feature:dark on' >/dev/null
	sleep 61 # the schema is cached for a minute (data.SchemaTTL)
	check "MySQL's tables list orders" sat "dk db tables ${PE[*]} -s mysql | grep -q shop.orders"
	check "a bare table name reads MySQL rows" sat "dk db rows orders ${PE[*]} -s mysql | grep -q bob"
	check "a read-only query on MySQL" sat "dk db query ${PE[*]} -s mysql 'select count(*) n from orders' | grep -q 2"
	check "Redis keys list feature:dark" sat "dk db keys ${PE[*]} -s cache | grep -q feature:dark"
	echo '{"key_changes":[{"op":"set","db":0,"key":"feature:dark","value":"off","old":{"type":"string","value":"on"}}]}' >"$TMP/kc.json"
	# A token only proposes on production; a person commits it in the UI.
	check "a token proposes a Redis key changeset" sat "dk db apply -f $TMP/kc.json ${PE[*]} -s cache -m 'release walk' --confirm production | grep -q Proposed"
	check "the changeset counts its key change" sat "dk db changes list ${PE[*]} -s cache | grep -q 'pending  [^ ]*  *1 '"
	for s in mysql cache; do
		check "dock db health on $s" dk db health "${PE[@]}" -s "$s"
	done
}

scenario_diagnose() {
	dk project create "$P" >/dev/null 2>&1
	dk service create crashy "${PE[@]}" --image alpine:3.22 --start-command 'sh -c "echo Error: connect ECONNREFUSED 10.0.0.5:5432 >&2; exit 1"' >/dev/null 2>&1
	dk deploy "${PE[@]}" -s crashy --image alpine:3.22 >/dev/null 2>&1 && { fail "crashy's release went live"; return; }
	check "dock diagnose names the exit and the matched line" wait_for 30 sat "dk diagnose ${PE[*]} -s crashy | grep -q 'exited with code 1' && dk diagnose ${PE[*]} -s crashy | grep -q ECONNREFUSED"
}

scenario_sleep() {
	local h code t0 ENV_NAME=staging E2E_PROXY=${E2E_PROXY:-}
	dk environment clone production staging -p "$P" >/dev/null 2>&1
	dk service create web -p "$P" -e staging >/dev/null 2>&1 # when staging was cloned before web existed
	dk promote production staging -p "$P" --service web >/dev/null 2>&1 || { fail "promoting web to staging"; return; }
	h=$(generate_domain "$P" web)
	use_proxy_of "$P" # staging's server
	check "sleep-after on staging" dk env sleep-after staging 30m -p "$P"
	dk release wait -p "$P" -e staging -s web >/dev/null 2>&1
	check "staging sleeps now" dk env sleep staging -p "$P"
	t0=$SECONDS
	code=$(http_code "$h" /)
	if [[ $code == 200 ]]; then pass "a request woke staging in $((SECONDS - t0))s"; else fail "waking staging: HTTP $code"; fi
}

scenarios=("$@")
[[ ${#scenarios[@]} -gt 0 ]] || scenarios=(traces status vulns data diagnose sleep)
log "project $P"
for s in "${scenarios[@]}"; do
	log "scenario $s"
	"scenario_$s"
done
[[ ${FAILURES:-0} -eq 0 ]] && log "all passed" || { log "$FAILURES failed"; exit 1; }
