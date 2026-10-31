#!/usr/bin/env bash
# Dock Analytics' outage test (ADR 0037, A1's done-when), on this machine:
# two Dock servers in Docker-in-Docker containers on their own network, so
# nothing touches this host's Docker or any real install.
#
#   cp    the control plane; serves a production site, shop.test
#   lab   a joined server running the analytics store, like a homelab
#
# Page views go to cp's ingest before, during and after lab is off (a
# reboot: its Docker, Caddy, node and store all stop for OFF_FOR seconds).
# Then the store moves from lab to cp with its data. It passes when the
# store has every page view of each phase, the outage's own time window
# holds the page views sent during it, and after the move cp's store has
# them all while lab has no store, data or binary left.
#
#   make build && make analytics-binary
#   e2e/analytics-outage.sh            # N=200 page views a phase, OFF_FOR=120
#
# Needs Docker (privileged containers), caddy on PATH, curl and jq. KEEP=1
# leaves the servers running; the CLI then works with `source $W/env`.
set -euo pipefail
REPO=$(cd "$(dirname "$0")/.." && pwd)
STORE=$REPO/bin/dock-analytics-linux-amd64 # the control plane's store_binary
N=${N:-200}
OFF_FOR=${OFF_FOR:-120}
NET=dock-e2e-analytics
CP=dock-e2e-cp LAB=dock-e2e-lab
HOST=shop.test
W=$(mktemp -d)
export HOME=$W PATH=$REPO/bin:$PATH # the CLI sees only this test's server

say() { printf '==> %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*"; exit 1; }
x() { local c=$1; shift; docker exec "$c" sh -c "$*"; }
ip() { docker inspect -f "{{(index .NetworkSettings.Networks \"$NET\").IPAddress}}" "$1"; }

cleanup() { docker rm -f "$CP" "$LAB" >/dev/null 2>&1; docker network rm "$NET" >/dev/null 2>&1; rm -rf "$W"; }
[[ ${KEEP:-} == 1 ]] || trap cleanup EXIT

# wait_for WHAT SECONDS CMD...: until CMD succeeds.
wait_for() {
	local what=$1 t=$2; shift 2
	for _ in $(seq "$t"); do "$@" >/dev/null 2>&1 && return; sleep 1; done
	fail "$what (after ${t}s)"
}

# server C: a fresh Docker-in-Docker container with dock and caddy.
server() {
	docker rm -f "$1" >/dev/null 2>&1 || true
	docker run -d --privileged --name "$1" --hostname "$1" --network "$NET" \
		-v "$REPO/bin/dock:/usr/local/bin/dock:ro" -v "$(command -v caddy):/usr/local/bin/caddy:ro" \
		-v "$STORE:/usr/local/lib/dock-analytics:ro" \
		docker:28-dind >/dev/null
	wait_for "Docker in $1" 60 x "$1" 'docker info'
}

# configure C ROLE: its config.toml (ROLE cp or node), and Caddy running.
configure() {
	local c=$1 role=$2 api="" link=""
	if [[ $role == cp ]]; then api='listen = "0.0.0.0:7070"' link=$(printf '[node]\nlisten = "%s:7443"' "$(ip "$c")"); fi
	docker exec -i "$c" sh -c 'mkdir -p /etc/dock /run/dock-caddy && cat > /etc/dock/config.toml' <<CONF
data_dir = "/var/lib/dock"
$api
internal_listen = "127.0.0.1:7071"
public_ip = "$(ip "$c")"
base_domain = "$c.test"
[caddy]
admin_socket = "/run/dock-caddy/admin.sock"
http_port = 80
https_port = 443
disable_tls = true
[buildkit]
enabled = false
[analytics]
store_binary = "/usr/local/lib/dock-analytics"
$link
CONF
	x "$c" 'dock admin caddy-config --config /etc/dock/config.toml > /etc/dock/caddy.json'
	start_caddy "$c"
}

# start_caddy C / start_node: what systemd would start on boot.
start_caddy() {
	x "$1" 'nohup caddy run --resume --config /etc/dock/caddy.json >> /var/log/caddy.log 2>&1 &'
	wait_for "Caddy in $1" 20 x "$1" 'test -S /run/dock-caddy/admin.sock'
}
start_node() { x "$LAB" 'nohup dock node --config /etc/dock/config.toml >> /var/log/dock.log 2>&1 &'; }

# lab_is STATUS: lab's status on the control plane.
lab_is() { dock server list --json | jq -e --arg s "$1" '.[] | select(.name == "lab" and .status == $s)'; }

# send PHASE: N page views of /PHASE/<i>, each from another browser.
send() {
	local ok=0
	for i in $(seq "$N"); do
		[[ $(curl -s -o /dev/null -w '%{http_code}' -X POST "http://$(ip "$CP")/_dock/a/e" -H "Host: $HOST" \
			-H "User-Agent: Mozilla/5.0 (X11; Linux x86_64; e2e-$1-$i) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Safari/537.36" \
			--data "{\"k\":1,\"u\":\"http://$HOST/$1/$i\"}") == 202 ]] && ok=$((ok + 1))
	done
	[[ $ok == "$N" ]] || fail "$1: the ingest took $ok of $N page views"
	say "$1: $N page views to cp's ingest"
}

# pageviews PHASE: the store's page views under /PHASE/.
pageviews() { dock analytics stats "$HOST" -p shop --range 24h --filter "page:contains:/$1/" --json | jq '.overview.pageviews'; }
has_all() { for p in "$@"; do [[ $(pageviews "$p") == "$N" ]] || return 1; done; }

say "servers"
docker rm -f "$CP" "$LAB" >/dev/null 2>&1 || true # a KEEP=1 run's
docker network rm "$NET" >/dev/null 2>&1 || true
docker network create "$NET" >/dev/null
server "$CP" & server "$LAB" & wait
configure "$CP" cp
configure "$LAB" node
TOKEN=$(x "$CP" 'dock admin bootstrap --config /etc/dock/config.toml' 2>/dev/null)
x "$CP" 'nohup dock serve --config /etc/dock/config.toml > /var/log/dock.log 2>&1 &'
export DOCK_URL=http://$(ip "$CP"):7070 DOCK_TOKEN=$TOKEN
printf 'export PATH=%q HOME=%q DOCK_URL=%q DOCK_TOKEN=%q\n' "$PATH" "$W" "$DOCK_URL" "$DOCK_TOKEN" > "$W/env"
wait_for "the control plane" 30 dock analytics
join=$(dock server add lab --non-production --json | jq -r .token)
x "$LAB" "printf '%s\n' '$join' | dock node join --config /etc/dock/config.toml --token-file - >/dev/null"
start_node
wait_for "lab to join" 30 lab_is online

say "the site: shop/web on cp, $HOST, counted by a store on lab"
dock project create shop >/dev/null 2>&1
dock service create web -p shop -e production --image nginx:1.29-alpine --port 80 >/dev/null
dock deploy -p shop -e production -s web --image nginx:1.29-alpine >/dev/null 2>&1
dock domain add "$HOST" -p shop -e production -s web --detach >/dev/null 2>&1
wait_for "the site" 180 sh -c "curl -sf -H 'Host: $HOST' http://$(ip "$CP")/ | grep -q nginx"
dock analytics server lab >/dev/null
dock analytics enable "$HOST" >/dev/null 2>&1
wait_for "the store on lab" 180 sh -c 'dock analytics --json | jq -e .store_running'

say "lab up"
send before
wait_for "the first page views in the store" 60 has_all before

say "lab off for ${OFF_FOR}s"
docker stop -t 10 "$LAB" >/dev/null
wait_for "lab offline" 120 lab_is offline
off=$(date -u +%s)
send during
say "cp's spool holds $(x "$CP" 'du -sb /var/lib/dock/analytics/spool | cut -f1') bytes"
sleep "$OFF_FOR"

say "lab back"
docker start "$LAB" >/dev/null
wait_for "Docker in lab" 60 x "$LAB" 'docker info'
start_caddy "$LAB"
start_node
on=$(date -u +%s)
wait_for "lab online" 120 lab_is online
send after
wait_for "every page view in the store" 180 has_all before during after

# The outage's own window holds exactly its page views: they keep the time
# they were collected, not the time they reached the store.
win=$(curl -sf -H "Authorization: Bearer $DOCK_TOKEN" \
	"$DOCK_URL/api/v1/projects/shop/sites/$HOST/stats?from=$((off * 1000))&to=$((on * 1000))" | jq .overview.pageviews)
[[ $win == "$N" ]] || fail "lab's $((on - off))s off hold $win page views, want $N"
say "the outage: $((3 * N)) page views, $N of them while lab was off for $((on - off))s, none missing"

say "the store moves from lab to cp"
dock analytics server >/dev/null
moved() { dock analytics --json | jq -e --arg cp "$CP" '.server == $cp and .store_running and (.moving_to // "") == ""'; }
wait_for "the move" 300 moved
err=$(dock analytics --json | jq -r '.error // ""')
[[ -z $err ]] || fail "the move: $err"
has_all before during after || fail "cp's store is missing page views after the move"
[[ -z $(x "$LAB" 'docker ps -aq --filter name=^dock-analytics$') ]] || fail "lab still has the store's container"
[[ -z $(x "$LAB" 'docker volume ls -q --filter name=^dock-analytics-data$') ]] || fail "lab still has the store's data"
x "$LAB" 'test ! -e /var/lib/dock/analytics/bin' || fail "lab still has the store's binary"
send moved
wait_for "page views after the move" 60 has_all moved
say "PASS: $((4 * N)) page views, $N of them while lab was off for $((on - off))s; the store moved to cp with all of them and left nothing on lab"
