#!/usr/bin/env bash
# Tracing over OTLP end to end, on throwaway servers (e2e/vm): a control
# plane and a node installed with deploy/install.sh. Tracing goes on, and
# a service on each server sends a span every two seconds the way an
# OpenTelemetry SDK does: OTLP/HTTP JSON to $OTEL_EXPORTER_OTLP_TRACES_ENDPOINT
# with the token in its path, injected by Dock.
# Passes when the trace store has spans of both services: dock-otlp in the
# containers' hosts file, the app firewall, Caddy's route, each server's
# ingest and spool, the forwarders and the trace store all work.
#
#   make web && make build && make analytics-binary
#   e2e/traces.sh            # e2e/vm/down.sh afterwards
set -euo pipefail
cd "$(dirname "$0")/.."
STORE=$PWD/bin/dock-analytics-linux-amd64
[[ -x $STORE ]] || { echo "no store binary; make analytics-binary" >&2; exit 1; }

say() { printf '\033[1m==> %s\033[0m\n' "$*"; }
wait_for() {
	local what=$1 secs=$2
	shift 2
	for _ in $(seq "$secs"); do "$@" >/dev/null 2>&1 && return 0; sleep 1; done
	echo "FAIL: $what within ${secs}s" >&2
	exit 1
}

e2e/vm/up.sh 1
# shellcheck disable=SC1091
source /tmp/e2e2/env
dock() { ./bin/dock "$@"; }

say "the control plane runs the store binary this build made"
docker cp "$STORE" e2e2-cp:/usr/local/lib/dock-analytics
docker exec e2e2-cp bash -c 'grep -q "^store_binary" /etc/dock/config.toml ||
	{ grep -q "^\[analytics\]" /etc/dock/config.toml &&
		sed -i "/^\[analytics\]/a store_binary = \"/usr/local/lib/dock-analytics\"" /etc/dock/config.toml ||
		printf "\n[analytics]\nstore_binary = \"/usr/local/lib/dock-analytics\"\n" >>/etc/dock/config.toml; }
	systemctl restart dock'
wait_for "the control plane" 60 curl -fsS "$DOCK_URL/api/v1/health"

say "tracing on"
dock tracing on >/dev/null
wait_for "the trace store" 300 sh -c './bin/dock tracing --json | jq -e .store_running'

# The sender: nginx for the health check, and a loop posting a span with
# a new trace ID every two seconds.
SEND='(while true; do
  id=$(head -c16 /dev/urandom | od -An -tx1 | tr -d " \n")
  now=$(date +%s)
  body="{\"resourceSpans\":[{\"scopeSpans\":[{\"spans\":[{\"traceId\":\"$id\",\"spanId\":\"0102030405060708\",\"name\":\"GET /e2e\",\"kind\":2,\"startTimeUnixNano\":\"${now}000000000\",\"endTimeUnixNano\":\"${now}005000000\",\"attributes\":[{\"key\":\"http.route\",\"value\":{\"stringValue\":\"/e2e\"}},{\"key\":\"db.password\",\"value\":{\"stringValue\":\"hunter2\"}}]}]}]}]}"
  wget -q -O- --header "Content-Type: application/json" --post-data "$body" "$OTEL_EXPORTER_OTLP_TRACES_ENDPOINT" >/dev/null 2>&1
  sleep 2
done) & exec nginx -g "daemon off;"'

# send PROJECT SERVER deploys the sender in PROJECT's production
# environment, moved to SERVER first unless placement put it there.
send() {
	dock project create "$1" >/dev/null 2>&1 || true
	local out
	out=$(dock env move -p "$1" -e production --to "$2" --yes 2>&1) || grep -q "already" <<<"$out" ||
		{ echo "FAIL: moving $1 to $2: $out" >&2; exit 1; }
	dock service create sender -p "$1" -e production --image nginx:1.29-alpine --port 80 --start-command "$SEND" >/dev/null 2>&1 || true
	dock deploy -p "$1" -e production -s sender --image nginx:1.29-alpine >/dev/null
}
say "a sender on the control plane (shop) and on the node (lab)"
send shop e2e2-cp
send lab e2e2-node1

# The trace store answers on its port on the control plane, with the
# install's analytics token (its container's environment).
TOKEN=$(docker exec e2e2-cp docker inspect dock-traces --format '{{range .Config.Env}}{{println .}}{{end}}' | sed -n 's/^DOCK_ANALYTICS_TOKEN=//p')
spans() { # spans SERVICE_ID: how many of the service's traces the store has in the last 10 minutes
	local to from
	to=$(($(date +%s) * 1000000 + 60000000)) from=$((to - 660000000))
	docker exec e2e2-cp curl -fsS -H "Authorization: Bearer $TOKEN" \
		-d "{\"org\":1,\"services\":[$1],\"from\":$from,\"to\":$to,\"limit\":200}" http://127.0.0.1:7080/v1/traces |
		jq '.traces | length'
}
service_id() { dock service show sender -p "$1" -e production --json | jq -r '.id'; }
shop=$(service_id shop) lab=$(service_id lab)
for name in shop lab; do
	id=${!name}
	for _ in $(seq 120); do
		n=$(spans "$id" 2>/dev/null || echo 0)
		[[ $n -gt 0 ]] && break
		sleep 1
	done
	[[ $n -gt 0 ]] || { echo "FAIL: no spans from $name's sender (service $id)" >&2; exit 1; }
	say "ok: the trace store has $n traces of $name's sender"
done

# What was stored is masked and attributed by the token, not the span.
first=$(docker exec e2e2-cp curl -fsS -H "Authorization: Bearer $TOKEN" \
	-d "{\"org\":1,\"services\":[$shop],\"from\":0,\"to\":$(($(date +%s) * 1000000 + 60000000)),\"limit\":1}" http://127.0.0.1:7080/v1/traces)
trace=$(jq -r '.traces[0].trace' <<<"$first") at=$(jq -r '.traces[0].start' <<<"$first")
detail=$(docker exec e2e2-cp curl -fsS -H "Authorization: Bearer $TOKEN" -d "{\"org\":1,\"trace\":\"$trace\",\"at\":$at}" http://127.0.0.1:7080/v1/trace)
jq -e '.spans[0].attrs["db.password"] == "*****" and .spans[0].route == "/e2e" and .spans[0].entry' <<<"$detail" >/dev/null ||
	{ echo "FAIL: the stored span: $detail" >&2; exit 1; }
say "ok: stored spans are masked and attributed"
say "passed"
