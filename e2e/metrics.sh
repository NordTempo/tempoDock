#!/usr/bin/env bash
# App metrics over OTLP end to end (ADR 0075, phase 6), on throwaway
# servers (e2e/vm): tracing goes on and two stock apps deploy from source
# with nothing but the variables Dock injects, a Node one with the
# OpenTelemetry SDK's auto-instrumentation (e2e/apps/node-otel) and a Go
# one with the SDK's runtime metrics (e2e/apps/go-otel), which later sends
# a counter with 2,000 values of an attribute: a cardinality bomb.
# Passes when the trace store has both apps' runtime metrics and cut the
# Go app's new series at its 2,000 a day, keeping the runtime ones, and
# `dock metrics` charts them and names the attribute to drop.
#
#   make web && make build && make analytics-binary
#   e2e/metrics.sh           # e2e/vm/down.sh afterwards
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

P=(-p metrics -e production)
dock project create metrics >/dev/null 2>&1 || true
# app NAME PORT [VAR=VALUE...] deploys e2e/apps/NAME as a service of its
# name, exporting every 5 s instead of the SDKs' minute.
app() {
	local name=$1 port=$2
	shift 2
	dock service create "$name" "${P[@]}" --port "$port" >/dev/null 2>&1 || true
	dock env set "${P[@]}" -s "$name" OTEL_METRIC_EXPORT_INTERVAL=5000 "$@" >/dev/null
	(cd "e2e/apps/$name" && ../../../bin/dock deploy "${P[@]}" -s "$name" >"/tmp/e2e2/deploy-$name.log" 2>&1) ||
		{ echo "FAIL: deploying $name: $(tail -3 "/tmp/e2e2/deploy-$name.log")" >&2; exit 1; }
}
say "two stock apps: node-otel and go-otel (with its bomb)"
app node-otel 3000
app go-otel 8080 BOMB=1

# The trace store answers on its port on the control plane, with the
# install's analytics token (its container's environment).
TOKEN=$(docker exec e2e2-cp docker inspect dock-traces --format '{{range .Config.Env}}{{println .}}{{end}}' | sed -n 's/^DOCK_ANALYTICS_TOKEN=//p')
names() { # names SERVICE_ID: the service's metrics of the last hour
	local to=$(($(date +%s) + 60))
	docker exec e2e2-cp curl -fsS -H "Authorization: Bearer $TOKEN" \
		-d "{\"org\":1,\"services\":[$1],\"from\":$((to - 3660)),\"to\":$to}" http://127.0.0.1:7080/v1/metrics/names
}
service_id() { dock service show "$1" "${P[@]}" --json | jq -r '.id'; }
node=$(service_id node-otel) go=$(service_id go-otel)

node_runtime() { names "$node" | jq -e '[.metrics[].name] | any(startswith("v8js.") or startswith("nodejs."))'; }
go_runtime() { names "$go" | jq -e '[.metrics[].name] | index("go.memory.used")'; }
go_cut() { names "$go" | jq -e '[.metrics[].cut // 0] | add > 0'; }
wait_for "node-otel's runtime metrics" 180 node_runtime
say "ok: node-otel sends its runtime metrics: $(names "$node" | jq -c '[.metrics[].name]')"
wait_for "go-otel's runtime metrics" 180 go_runtime
wait_for "the bomb cut" 180 go_cut
out=$(names "$go")
jq -e '([.metrics[].series] | add) == 2000 and (.metrics[] | select(.name == "go.memory.used") | .series > 0)' <<<"$out" >/dev/null ||
	{ echo "FAIL: go-otel's metrics past the cap: $out" >&2; exit 1; }
say "ok: go-otel's bomb was cut at 2,000 series, its runtime metrics kept: $(jq -c '[.metrics[] | select(.cut) | {name, series, cut}]' <<<"$out")"
# `dock metrics` reads the same through the API: Dock's runtime charts,
# and the attribute to drop.
dock metrics "${P[@]}" -s go-otel --range 1h --json |
	jq -e '(.charts | map(.name) | index("go.memory.used")) and (.metrics[] | select(.name == "bomb.hits") | .cut_by == "user.id")' >/dev/null ||
	{ echo "FAIL: dock metrics for go-otel: $(dock metrics "${P[@]}" -s go-otel --range 1h 2>&1 | head -20)" >&2; exit 1; }
say "ok: dock metrics shows Dock's runtime charts and names user.id to drop"
say "passed"
