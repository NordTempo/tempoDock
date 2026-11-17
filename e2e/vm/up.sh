#!/usr/bin/env bash
# Starts throwaway "servers" for e2e runs and installs Tempo Dock on them
# with deploy/install.sh, as on real machines: a control plane plus
# NODES nodes that join it. Each server is a privileged Debian 13
# container running systemd and its own Docker (e2e/vm/Dockerfile) on the
# $SLOT-net network.
#
#   e2e/vm/up.sh [NODES]     # default 1 node; DOCK_BINARY defaults to bin/dock
#   source /tmp/e2e2/env     # DOCK_URL, DOCK_TOKEN, E2E_PROXY, E2E_SERVER_IPS
#   e2e/vm/down.sh           # removes everything up.sh made
#
# E2E_RELEASE=vX.Y.Z installs that published release instead of a local
# build: its binary and install tarball, checked against its SHA256SUMS.
# E2E_SLOT names a second, independent set of servers (default e2e2): its
# containers are $SLOT-*, its state /tmp/$SLOT. Give it its own E2E_SUBNET
# (e2e2 has 10.250.250.0/24) so it never touches another slot's servers;
# e2e/nightly.sh uses E2E_SLOT=e2e-nightly.
#
# E2E_TLS=1 installs with TLS (Caddy on 80 and 443, certificates from a
# real CA) instead of plain HTTP, for wildcard certificates (ADR 0090);
# set [caddy] acme_directory to Let's Encrypt's staging directory first.
#
# E2E_VM_MEMORY caps each server container's memory (docker --memory, e.g.
# 5g), so a runaway test can't take the host down; unset means no cap.
#
# Re-running with the servers up re-installs the binary (an upgrade).
# Everything is named $SLOT-*; nothing else on the host is touched.
set -euo pipefail
cd "$(dirname "$0")/../.."

NODES=${1:-1}
SLOT=${E2E_SLOT:-e2e2}
BIN=${DOCK_BINARY:-bin/dock}
DEPLOY=deploy
NET=$SLOT-net
SUBNET=${E2E_SUBNET:-10.250.250.0/24}
PREFIX=${SUBNET%.*}
BASE_DOMAIN=${BASE_DOMAIN:-$SLOT.test}
STATE=/tmp/$SLOT
TLS_OFF=true
[[ ${E2E_TLS:-} != 1 ]] || TLS_OFF=false
mkdir -p "$STATE"

say() { printf '\033[1m==> %s\033[0m\n' "$*"; }

if [[ -n ${E2E_RELEASE:-} ]]; then
	# The version goes into a URL and paths: a release version, nothing else.
	[[ $E2E_RELEASE =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] ||
		{ echo "E2E_RELEASE is a release version like v0.58.0, not this" >&2; exit 1; }
	rel=${XDG_CACHE_HOME:-$HOME/.cache}/dock-e2e-releases/$E2E_RELEASE
	if [[ ! -f $rel/ok ]]; then
		rm -rf "$rel" && mkdir -p "$rel"
		for f in dock-linux-amd64 "dock-install-$E2E_RELEASE.tar.gz" SHA256SUMS; do
			curl --proto '=https' --proto-redir '=https' -fsSL -o "$rel/$f" "https://github.com/NordTempo/tempoDock/releases/download/$E2E_RELEASE/$f"
		done
		(cd "$rel" && grep -E " (dock-linux-amd64|dock-install-$E2E_RELEASE.tar.gz)\$" SHA256SUMS | sha256sum -c --quiet)
		chmod +x "$rel/dock-linux-amd64"
		tar -xzf "$rel/dock-install-$E2E_RELEASE.tar.gz" -C "$rel"
		touch "$rel/ok"
	fi
	BIN=$rel/dock-linux-amd64 DEPLOY=$rel/deploy
fi
[[ -x $BIN ]] || { echo "no dock binary at $BIN; make web && make build" >&2; exit 1; }

docker image inspect e2e2-vm >/dev/null 2>&1 || docker build -t e2e2-vm e2e/vm
docker network inspect "$NET" >/dev/null 2>&1 || docker network create --subnet "$SUBNET" "$NET" >/dev/null

# start NAME IP starts a server container unless it runs. Docker's data is
# an anonymous volume, removed by down.sh's `docker rm -v`.
start() {
	docker inspect "$1" >/dev/null 2>&1 && return 0
	docker run -d --name "$1" --hostname "$1" --privileged --cgroupns=private \
		${E2E_VM_MEMORY:+--memory "$E2E_VM_MEMORY"} --tmpfs /run --tmpfs /run/lock -v /var/lib/docker -v /var/lib/containerd \
		--network "$NET" --ip "$2" e2e2-vm >/dev/null
}

# copy NAME puts the binary and deploy/ in /root/dock on the server.
copy() {
	docker exec "$1" rm -rf /root/dock/deploy
	docker exec "$1" mkdir -p /root/dock
	docker cp "$BIN" "$1:/root/dock/dock"
	docker cp "$DEPLOY" "$1:/root/dock/"
}

wait_docker() {
	for _ in $(seq 60); do docker exec "$1" docker info >/dev/null 2>&1 && return 0; sleep 1; done
	echo "docker did not start on $1" >&2; return 1
}

CP_IP=$PREFIX.10
CP=$SLOT-cp
say "control plane $CP ($CP_IP)"
start "$CP" "$CP_IP"
wait_docker "$CP"
copy "$CP"
# Apps may reach $SLOT-net (log sinks, registries, S3 on the host side).
docker exec -e DOCK_BINARY=/root/dock/dock -e DISABLE_TLS=$TLS_OFF -e BASE_DOMAIN="$BASE_DOMAIN" \
	-e PUBLIC_IP="$CP_IP" -e LISTEN="$CP_IP:7070" -e NODE_LISTEN="$CP_IP:7443" -e REGISTRY=1 \
	"$CP" /root/dock/deploy/install.sh >"$STATE/install-cp.log" 2>&1 ||
	{ tail -30 "$STATE/install-cp.log"; exit 1; }
token=$(sed -n 's/.*admin token: //p' "$STATE/install-cp.log" | tail -1)
if [[ -n $token ]]; then
	echo "$token" >"$STATE/token"
	# admin@localhost's password, for UI-session-only actions (approvals).
	sed -n 's/.*with password \([^ ]*\) .*/\1/p' "$STATE/install-cp.log" >"$STATE/admin-password"
fi
token=$(cat "$STATE/token")
# An offline node raises its alert after a minute instead of five (health scenario).
docker exec "$CP" bash -c "grep -q server_offline_minutes /etc/dock/config.toml || printf '\n[alerts]\nserver_offline_minutes = 1\n' >>/etc/dock/config.toml"
docker exec "$CP" bash -c "grep -q allow_private /etc/dock/config.toml && sed -i 's|^allow_private.*|allow_private = [\"$SUBNET\"]|' /etc/dock/config.toml || printf '\n[network]\nallow_private = [\"$SUBNET\"]\n' >>/etc/dock/config.toml; systemctl restart dock-firewall dock"

export DOCK_URL=http://$CP_IP:7070 DOCK_TOKEN=$token
# The CLI only ever talks to this control plane (DOCK_URL wins over any
# context in the user's config); never to production.
[[ $DOCK_URL != *100.95.4.67* || ${E2E_ALLOW_PROD:-} == 1 ]] || { echo "refusing: $DOCK_URL is production" >&2; exit 1; }
for _ in $(seq 60); do curl -fsS "$DOCK_URL/api/v1/health" >/dev/null 2>&1 && break; sleep 1; done
cat >"$STATE/env" <<EOF
export DOCK_URL=$DOCK_URL DOCK_TOKEN=$token E2E_PROXY=$CP_IP:80 E2E_BASE_DOMAIN=$BASE_DOMAIN E2E_SLOT=$SLOT E2E_SUBNET=$SUBNET
export E2E_ADMIN_EMAIL=admin@localhost E2E_ADMIN_PASSWORD=$(cat "$STATE/admin-password")
EOF

servers="$CP=$CP_IP"
for i in $(seq "$NODES"); do
	name=$SLOT-node$i ip=$PREFIX.$((10 + i))
	say "node $name ($ip)"
	start "$name" "$ip"
	wait_docker "$name"
	copy "$name"
	join=""
	if ! docker exec "$name" test -f /var/lib/dock/node.toml; then
		join=$("$BIN" server add "$name" --production --non-production --builder --json | jq -r '.token // .join_token')
	fi
	docker exec -e DOCK_BINARY=/root/dock/dock -e DISABLE_TLS=$TLS_OFF -e BASE_DOMAIN="node$i.$BASE_DOMAIN" \
		-e PUBLIC_IP="$ip" ${join:+-e NODE_JOIN_TOKEN="$join"} \
		"$name" /root/dock/deploy/install.sh --node >"$STATE/install-$name.log" 2>&1 ||
		{ tail -30 "$STATE/install-$name.log"; exit 1; }
	docker exec "$name" bash -c "grep -q allow_private /etc/dock/config.toml && sed -i 's|^allow_private.*|allow_private = [\"$SUBNET\"]|' /etc/dock/config.toml || printf '\n[network]\nallow_private = [\"$SUBNET\"]\n' >>/etc/dock/config.toml; systemctl restart dock-firewall"
	servers+=" $name=$ip"
done
echo "export E2E_SERVER_IPS=\"$servers\"" >>"$STATE/env"
"$BIN" server list
say "ready: source $STATE/env"
