#!/usr/bin/env bash
# Starts throwaway "servers" for e2e runs and installs Tempo Dock on them
# with deploy/install.sh, as on real machines: a control plane plus
# NODES nodes that join it. Each server is a privileged Debian 13
# container running systemd and its own Docker (e2e/vm/Dockerfile) on the
# e2e2-net network.
#
#   e2e/vm/up.sh [NODES]     # default 1 node; DOCK_BINARY defaults to bin/dock
#   source /tmp/e2e2/env     # DOCK_URL, DOCK_TOKEN, E2E_PROXY, E2E_NODE_PROXY
#   e2e/vm/down.sh           # removes everything up.sh made
#
# Re-running with the servers up re-installs the binary (an upgrade).
# Everything is named e2e2-*; nothing else on the host is touched.
set -euo pipefail
cd "$(dirname "$0")/../.."

NODES=${1:-1}
BIN=${DOCK_BINARY:-bin/dock}
NET=e2e2-net
SUBNET=${E2E_SUBNET:-10.250.250.0/24}
PREFIX=${SUBNET%.*}
BASE_DOMAIN=${BASE_DOMAIN:-e2e2.test}
STATE=/tmp/e2e2
mkdir -p "$STATE"

say() { printf '\033[1m==> %s\033[0m\n' "$*"; }
[[ -x $BIN ]] || { echo "no dock binary at $BIN; make web && make build" >&2; exit 1; }

docker image inspect e2e2-vm >/dev/null 2>&1 || docker build -t e2e2-vm e2e/vm
docker network inspect $NET >/dev/null 2>&1 || docker network create --subnet "$SUBNET" $NET >/dev/null

# start NAME IP starts a server container unless it runs. Docker's data is
# an anonymous volume, removed by down.sh's `docker rm -v`.
start() {
	docker inspect "$1" >/dev/null 2>&1 && return 0
	docker run -d --name "$1" --hostname "$1" --privileged --cgroupns=private \
		--tmpfs /run --tmpfs /run/lock -v /var/lib/docker -v /var/lib/containerd \
		--network $NET --ip "$2" e2e2-vm >/dev/null
}

# copy NAME puts the binary and deploy/ in /root/dock on the server.
copy() {
	docker exec "$1" mkdir -p /root/dock
	docker cp "$BIN" "$1:/root/dock/dock"
	docker cp deploy "$1:/root/dock/"
}

wait_docker() {
	for _ in $(seq 60); do docker exec "$1" docker info >/dev/null 2>&1 && return 0; sleep 1; done
	echo "docker did not start on $1" >&2; return 1
}

CP_IP=$PREFIX.10
say "control plane e2e2-cp ($CP_IP)"
start e2e2-cp "$CP_IP"
wait_docker e2e2-cp
copy e2e2-cp
# Apps may reach e2e2-net (log sinks, registries, S3 on the host side).
docker exec -e DOCK_BINARY=/root/dock/dock -e DISABLE_TLS=true -e BASE_DOMAIN="$BASE_DOMAIN" \
	-e PUBLIC_IP="$CP_IP" -e LISTEN="$CP_IP:7070" -e NODE_LISTEN="$CP_IP:7443" -e REGISTRY=1 \
	e2e2-cp /root/dock/deploy/install.sh >"$STATE/install-cp.log" 2>&1 ||
	{ tail -30 "$STATE/install-cp.log"; exit 1; }
token=$(sed -n 's/.*admin token: //p' "$STATE/install-cp.log" | tail -1)
if [[ -n $token ]]; then
	echo "$token" >"$STATE/token"
	# admin@localhost's password, for UI-session-only actions (approvals).
	sed -n 's/.*with password \([^ ]*\) .*/\1/p' "$STATE/install-cp.log" >"$STATE/admin-password"
fi
token=$(cat "$STATE/token")
docker exec e2e2-cp bash -c "grep -q allow_private /etc/dock/config.toml && sed -i 's|^allow_private.*|allow_private = [\"$SUBNET\"]|' /etc/dock/config.toml || printf '\n[network]\nallow_private = [\"$SUBNET\"]\n' >>/etc/dock/config.toml; systemctl restart dock-firewall dock"

export DOCK_URL=http://$CP_IP:7070 DOCK_TOKEN=$token
for _ in $(seq 60); do curl -fsS "$DOCK_URL/api/v1/health" >/dev/null 2>&1 && break; sleep 1; done
cat >"$STATE/env" <<EOF
export DOCK_URL=$DOCK_URL DOCK_TOKEN=$token E2E_PROXY=$CP_IP:80 E2E_BASE_DOMAIN=$BASE_DOMAIN
export E2E_ADMIN_EMAIL=admin@localhost E2E_ADMIN_PASSWORD=$(cat "$STATE/admin-password")
EOF

servers="e2e2-cp=$CP_IP"
for i in $(seq "$NODES"); do
	name=e2e2-node$i ip=$PREFIX.$((10 + i))
	say "node $name ($ip)"
	start "$name" "$ip"
	wait_docker "$name"
	copy "$name"
	join=""
	if ! docker exec "$name" test -f /var/lib/dock/node.toml; then
		join=$("$BIN" server add "$name" --production --non-production --builder --json | jq -r '.token // .join_token')
	fi
	docker exec -e DOCK_BINARY=/root/dock/dock -e DISABLE_TLS=true -e BASE_DOMAIN="node$i.$BASE_DOMAIN" \
		-e PUBLIC_IP="$ip" ${join:+-e NODE_JOIN_TOKEN="$join"} \
		"$name" /root/dock/deploy/install.sh --node >"$STATE/install-$name.log" 2>&1 ||
		{ tail -30 "$STATE/install-$name.log"; exit 1; }
	docker exec "$name" bash -c "grep -q allow_private /etc/dock/config.toml && sed -i 's|^allow_private.*|allow_private = [\"$SUBNET\"]|' /etc/dock/config.toml || printf '\n[network]\nallow_private = [\"$SUBNET\"]\n' >>/etc/dock/config.toml; systemctl restart dock-firewall"
	servers+=" $name=$ip"
done
echo "export E2E_SERVER_IPS=\"$servers\"" >>"$STATE/env"
"$BIN" server list
say "ready: source $STATE/env"
