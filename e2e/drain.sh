#!/usr/bin/env bash
# A whole server drained under load (ADR 0063, 0091): two projects with
# Postgres, Redis, a plain volume and stateless apps run on one node, a
# hammer per app sends GETs and POSTs through that node's proxy, and
# `dock server drain` moves everything off it.
#
#   source /tmp/<slot>/env && e2e/drain.sh [SERVER]
#
# Needs e2e/vm's servers with at least two nodes (`e2e/vm/up.sh 2`).
# SERVER is a server's name, or nodeN for $SLOT-nodeN (default node1):
# e2e/nightly.sh runs it as drain/node2, after everything else, so the
# other scenarios' environments on that node are drained along. Checks: the drain succeeds, no request
# fails (held, not refused), every acknowledged POST is a row on the new
# server and no other row appeared, Redis keys and volume files arrived,
# nothing of the projects runs on the drained server, and the server takes
# placements again afterwards (so the slot can be reused). DRAIN_DB_MB=N
# grows the database by about N MB first.
set -uo pipefail
source "$(dirname "$0")/lib.sh"
APPS="$E2E_DIR/apps"
RUN=${E2E_RUN:-$(date +%d%H%M%S)}
SRC=${1:-node1}
[[ $SRC != node[0-9]* ]] || SRC=$SLOT-$SRC
TMP=$(mktemp -d)
trap 'kill $(jobs -p) 2>/dev/null; rm -rf "$TMP"' EXIT
HAMMER="${HAMMER:-$TMP/hammer}"
[[ -x $HAMMER ]] || (cd "$E2E_DIR/.." && go build -o "$HAMMER" ./e2e/hammer) || { echo "can't build the hammer" >&2; exit 1; }

dk_p() { local p=$1; shift; dk "$@" -p "$p" -e "$ENV_NAME"; }
newest_active() { [[ $(api GET "$(svc_path "$1" "$2")/releases" | jq -r 'max_by(.number).status') == active ]]; }
env_server() { api GET "/projects/$1/environments" | jq -r --arg e "$ENV_NAME" '.[] | select(.name == $e) | .server'; }
ip_of() { local kv; for kv in $E2E_SERVER_IPS; do [[ ${kv%%=*} == "$1" ]] && echo "${kv#*=}"; done; }
# container P S prints the running container of service S on its server.
container() {
	local id
	id=$(api GET "$(svc_path "$1" "$2")" | jq -r .id)
	docker exec "$(env_server "$1")" docker ps -q --filter label=dock.managed=true --filter "label=dock.service=$id" | head -1
}
in_svc() { local p=$1 s=$2; shift 2; docker exec "$(env_server "$p")" docker exec "$(container "$p" "$s")" "$@"; }
psql_in() { in_svc "$1" db sh -c "psql -U \"\$POSTGRES_USER\" -d \"\$POSTGRES_DB\" -tAc \"$2\""; }
redis_in() { in_svc "$1" cache sh -c "redis-cli -a \"\$REDIS_PASSWORD\" --no-auth-warning $2"; }
wait_for() { local t=$1; shift; for ((i = 0; i < t; i++)); do "$@" && return 0; sleep 1; done; return 1; }
check() { local d=$1; shift; if "$@" >/dev/null 2>&1; then pass "$d"; else fail "$d"; fi; }
release_ok() { local n; n=$(deploy_source "$1" "$2" "$3" 2>/dev/null) && [[ $(wait_release "$1" "$2" "$n" 600) == active ]]; }

[[ -n $(ip_of "$SRC") ]] || { echo "no server $SRC in E2E_SERVER_IPS" >&2; exit 1; }
SRC_IP=$(ip_of "$SRC")
SHOP=drain-$RUN-shop CACHE=drain-$RUN-cache
log "run $RUN: $SHOP and $CACHE on $SRC"

# Both projects start on SRC: an empty environment moves there first.
for p in $SHOP $CACHE; do
	dk project create $p >/dev/null || { fail "can't create $p"; exit 1; }
	[[ $(env_server $p) == "$SRC" ]] || dk env move "$ENV_NAME" -p $p --to "$SRC" --yes >/dev/null 2>&1
	check "$p starts on $SRC" test "$(env_server $p)" = "$SRC"
done

# shop: Postgres and an API writing rows to it.
dk_p $SHOP service create db --template postgres >/dev/null || fail "$SHOP: db not created"
check "$SHOP/db is active" wait_for 180 newest_active $SHOP db
ensure_service $SHOP api '{"health_check_path":"/healthz","port":3000}'
set_vars $SHOP api '{"DATABASE_URL":"${{db.DATABASE_URL}}"}'
API_HOST=$(generate_domain $SHOP api)
check "$SHOP/api deploys" release_ok $SHOP api "$APPS/node-pg-api"

# cache: Redis, a static site, and nginx serving a file from a volume.
dk_p $CACHE service create cache --template redis >/dev/null || fail "$CACHE: cache not created"
check "$CACHE/cache is active" wait_for 180 newest_active $CACHE cache
ensure_service $CACHE site '{}'
SITE_HOST=$(generate_domain $CACHE site)
check "$CACHE/site deploys" release_ok $CACHE site "$APPS/static-site"
ensure_service $CACHE files '{"image":"nginx:1.27-alpine","port":80}'
api POST "$(svc_path $CACHE files)/volumes" '{"name":"files","mount_path":"/usr/share/nginx/html"}' >/dev/null || fail "$CACHE/files: volume not added"
FILES_HOST=$(generate_domain $CACHE files)
n=$(api POST "$(svc_path $CACHE files)/releases" '{}' | jq -r .number)
check "$CACHE/files is active" test "$(wait_release $CACHE files "$n" 300)" = active

# Data to find on the other side.
seed=$(head -c 300000 /dev/urandom | base64 -w0)
sum=$(printf %s "$seed" | sha256sum | cut -d' ' -f1)
printf %s "$seed" | docker exec -i "$SRC" docker exec -i "$(container $CACHE files)" sh -c 'cat >/usr/share/nginx/html/blob.txt && echo pong >/usr/share/nginx/html/ping.txt'
for i in $(seq 200); do echo "SET k$i v$i"; done | docker exec -i "$SRC" docker exec -i "$(container $CACHE cache)" sh -c 'redis-cli -a "$REDIS_PASSWORD" --no-auth-warning >/dev/null'
keys=$(redis_in $CACHE DBSIZE)
check "redis has 200 keys (has $keys)" test "$keys" = 200
E2E_PROXY=$SRC_IP:80
check "api answers on $SRC" wait_for 60 code_is "$API_HOST" /healthz 200
check "files answers on $SRC" wait_for 60 code_is "$FILES_HOST" /ping.txt 200
check "site answers on $SRC" wait_for 60 code_is "$SITE_HOST" / 200
# DRAIN_DB_MB makes the database that big first, so the live passes and
# the final copy have real work (0: just the API's rows).
if ((${DRAIN_DB_MB:-0} > 0)); then
	psql_in $SHOP "create table big as select g, repeat(md5(g::text), 30) as pad from generate_series(1, ${DRAIN_DB_MB} * 1000) g" >/dev/null
	# Settled, as a database that has run a while: vacuumed (hint bits and
	# the visibility map written) and checkpointed. DRAIN_DB_FRESH=1 skips
	# it, so autovacuum rewrites the new table during the move.
	[[ -n ${DRAIN_DB_FRESH:-} ]] || psql_in $SHOP "vacuum (freeze, analyze) big" >/dev/null
	[[ -n ${DRAIN_DB_FRESH:-} ]] || psql_in $SHOP "checkpoint" >/dev/null
	log "db is $(psql_in $SHOP "select pg_size_pretty(pg_database_size(current_database()))")"
fi
big0=$(psql_in $SHOP "select count(*) from big" 2>/dev/null)
rows0=$(psql_in $SHOP "select count(*) from items")
log "items before the drain: $rows0"

# A hammer per app, all through SRC's proxy: it serves, holds, then forwards.
# A request may wait out a whole hold (--max-hold, 30 s), so the
# hammers' timeout is longer than that; slowest= shows the real holds.
hammer() { "$HAMMER" -url "http://$2$3" -connect "$SRC_IP:80" -c 4 -every 20ms -d 30m -timeout 35s ${4:+-method $4} >"$TMP/$1.out" & echo $! >"$TMP/$1.pid"; }
hammer api-get "$API_HOST" /healthz
hammer api-post "$API_HOST" "/items?n=hammer" POST
hammer site "$SITE_HOST" /
hammer files "$FILES_HOST" /ping.txt
sleep 5

out=$(dk server drain "$SRC" --yes 2>&1 | while IFS= read -r l; do echo "$(date +%T.%3N) $l"; done; exit "${PIPESTATUS[0]}")
st=$?
log "drain output:"$'\n'"$out"
check "the drain finished" test $st -eq 0
status=$(dk server drain "$SRC" --status --json 2>/dev/null)
check "the drain succeeded" test "$(jq -r .status <<<"$status")" = succeeded
# A paused drain would refuse the next run's: leave none behind.
[[ $(jq -r .status <<<"$status") != paused ]] || dk server drain "$SRC" --cancel --yes >/dev/null 2>&1
check "the drain moved every environment, both projects' included" jq -e --arg a $SHOP --arg b $CACHE \
	'all(.items[]; .status == "moved") and ([.items[] | select(.project == $a or .project == $b)] | length) == 2' <<<"$status"
sleep 10
for h in api-get api-post site files; do kill -INT "$(cat "$TMP/$h.pid")"; done
wait
for p in $SHOP $CACHE; do
	api GET "/projects/$p/environments/$ENV_NAME/move" | jq -r '"\(.project): held \(.downtime_from) -> \(.downtime_to)"' >&2
done
for h in api-get api-post site files; do
	sed "s/^/  $h: /" "$TMP/$h.out" >&2
	if grep -q ' failed=0 ' "$TMP/$h.out"; then pass "no failed $h request"; else fail "$h requests failed: $(head -1 "$TMP/$h.out")"; fi
done

# Every acknowledged write arrived, and nothing else.
acked=$(sed -n 's/^  body "ok" x\([0-9]*\) .*/\1/p' "$TMP/api-post.out")
for p in $SHOP $CACHE; do
	check "$p left $SRC (on $(env_server $p))" test "$(env_server $p)" != "$SRC"
done
rows1=$(psql_in $SHOP "select count(*) from items")
if [[ $rows1 == $((rows0 + ${acked:-0})) ]]; then pass "all ${acked:-0} acknowledged POSTs are rows ($rows1)"; else fail "rows $rows0 -> $rows1, but ${acked:-?} POSTs were acknowledged"; fi
keys=$(redis_in $CACHE DBSIZE)
[[ -z $big0 ]] || check "the big table kept its $big0 rows" test "$(psql_in $SHOP "select count(*) from big")" = "$big0"
check "redis kept its 200 keys (has $keys)" test "$keys" = 200
check "redis kept k137" test "$(redis_in $CACHE "GET k137")" = v137
got=$(in_svc $CACHE files sha256sum /usr/share/nginx/html/blob.txt | cut -d' ' -f1)
check "the volume's file arrived intact" test "$got" = "$sum"
check "nothing of $SHOP runs on $SRC" test -z "$(docker exec "$SRC" docker ps -q --filter label=dock.managed=true --filter "label=dock.project=$(api GET /projects/$SHOP | jq -r .id)")"
check "nothing of $CACHE runs on $SRC" test -z "$(docker exec "$SRC" docker ps -q --filter label=dock.managed=true --filter "label=dock.project=$(api GET /projects/$CACHE | jq -r .id)")"

# The new servers serve directly too, and take writes.
for pair in "$SHOP api $API_HOST /healthz" "$CACHE site $SITE_HOST /" "$CACHE files $FILES_HOST /ping.txt"; do
	read -r p s h path <<<"$pair"
	E2E_PROXY=$(ip_of "$(env_server $p)"):80
	check "$p/$s answers on $(env_server $p)" wait_for 30 code_is "$h" "$path" 200
done
E2E_PROXY=$(ip_of "$(env_server $SHOP)"):80
curl -sS -o /dev/null -X POST --connect-to "$API_HOST:80:$E2E_PROXY" "http://$API_HOST/items?n=after"
check "a write after the drain lands" test "$(psql_in $SHOP "select count(*) from items")" = $((rows1 + 1))
check "$SRC keeps forwards for the moved services" test "$(dk server drain "$SRC" --status --json 2>/dev/null | jq '.forwards | length')" -ge 3

# Leave the slot reusable: SRC takes placements again; the projects go.
dk server update "$SRC" --enable-placement >/dev/null || fail "can't enable placement on $SRC again"
[[ -n ${E2E_KEEP:-} ]] || for p in $SHOP $CACHE; do dk project archive $p --yes >/dev/null 2>&1; done

if ((${FAILURES:-0} > 0)); then printf '\033[31m%d failure(s)\033[0m\n' "$FAILURES"; exit 1; fi
printf '\033[32mdrain passed\033[0m\n'
