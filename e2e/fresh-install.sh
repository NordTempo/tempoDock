#!/usr/bin/env bash
# The first five minutes on a new server, as the README tells a stranger:
# on bare Debian and Ubuntu machines (no Docker, no iptables) it pipes
# deploy/install.sh into `sudo bash`, opens the console, signs
# a CLI in with a device code, deploys a template without flags it
# shouldn't need, adds a domain, installs again over the running app, and
# uninstalls (keeping data, then purging). Each step is timed; the install's messages are checked too.
#
#   make web && make build && e2e/fresh-install.sh               # every distribution
#   e2e/fresh-install.sh ubuntu:24.04                             # one
#   E2E_RELEASE_DIR=dist DOCK_TRUSTED_KEYS=... e2e/fresh-install.sh   # a built release
#
# The release comes from E2E_RELEASE_DIR (a directory release: what `make
# release-local` or e2e/nightly.sh build), else one is made here from
# bin/dock and deploy/, signed with a throwaway key the install is told to
# trust (DOCK_TRUSTED_KEYS), so the signature check runs as for a real one.
# The CLI's own installer (install-cli.sh) installs the latest published
# release from GitHub.
#
# Servers are privileged systemd containers (e2e/vm/bare.Dockerfile) in
# slot E2E_FRESH_SLOT (default e2e-fresh, subnet 10.250.255.0/24; set
# E2E_FRESH_SUBNET with another slot), removed at the end unless
# E2E_KEEP=1. Needs Docker, Go (for the throwaway signature) and openssl.
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

SLOT=${E2E_FRESH_SLOT:-e2e-fresh}
SUBNET=${E2E_FRESH_SUBNET:-10.250.255.0/24}
PREFIX=${SUBNET%.*}
NET=$SLOT-net
STATE=/tmp/$SLOT
IMAGES=("$@")
((${#IMAGES[@]})) || IMAGES=(debian:12 debian:13 ubuntu:22.04 ubuntu:24.04)
FAILURES=0

log() { printf '\033[1m[%s] %s\033[0m\n' "$(date +%T)" "$*" >&2; }
fail() { printf '\033[31mFAIL: %s\033[0m\n' "$*" >&2; FAILURES=$((FAILURES + 1)); }
pass() { printf '\033[32mok: %s\033[0m\n' "$*" >&2; }

# step NAME CMD... runs CMD, timed, as one ok/FAIL line.
step() {
	local name=$1 t0=$SECONDS
	shift
	if "$@"; then pass "$name ($((SECONDS - t0))s)"; else fail "$name ($((SECONDS - t0))s)"; return 1; fi
}

# release_dir prints a directory release of bin/dock and deploy/, signed
# with a key made for this run; its public half goes to $STATE/trusted.
release_dir() {
	local v=v0.0.0-fresh out=$STATE/release stage
	[[ -x bin/dock ]] || { echo "no bin/dock; make web && make build" >&2; return 1; }
	rm -rf "$out" && mkdir -p "$out"
	cp bin/dock "$out/dock-linux-amd64"
	stage=$(mktemp -d)
	cp -R deploy "$stage/deploy"
	rm -rf "$stage/deploy/tempobase"
	printf '%s\n' "$v" >"$stage/deploy/VERSION"
	tar --owner=0 --group=0 --numeric-owner -C "$stage" -czf "$out/dock-install-$v.tar.gz" deploy
	rm -rf "$stage"
	(cd "$out" && sha256sum -- * >SHA256SUMS)
	(umask 077 && openssl genpkey -algorithm ed25519 -out "$STATE/key.pem")
	openssl pkey -in "$STATE/key.pem" -pubout -outform DER | tail -c 32 | base64 >"$STATE/trusted"
	RELEASE_SIGNING_KEY=$(cat "$STATE/key.pem") TRUSTED_KEYS=$(cat "$STATE/trusted") \
		go run ./scripts/release/sign "$out" "$v" >/dev/null || return 1
	echo "$out"
}

mkdir -p "$STATE"
if [[ -n ${E2E_RELEASE_DIR:-} ]]; then
	REL=$(cd "$E2E_RELEASE_DIR" && pwd)
	TRUSTED=${DOCK_TRUSTED_KEYS:-}
else
	REL=$(release_dir) || exit 1
	TRUSTED=$(cat "$STATE/trusted")
fi
docker network inspect "$NET" >/dev/null 2>&1 || docker network create --subnet "$SUBNET" "$NET" >/dev/null

# The server under test, its address and its name for DNS.
C="" IP="" NAME=""
on() { docker exec "$C" "$@"; }
# laptop CMD... runs the dock CLI on the server against the console, as a
# laptop would through `ssh -L`, with a config of its own.
laptop() { docker exec -e XDG_CONFIG_HOME=/root/laptop "$C" dock "$@"; }

# install [ARGS...] pipes deploy/install.sh into bash on the server, as
# `curl -fsSL .../install.sh | sudo bash -s -- ARGS`, output in $STATE/$NAME-install.log.
install() {
	docker exec -i -e DOCK_RELEASE_DIR=/root/release -e DOCK_TRUSTED_KEYS="$TRUSTED" \
		-e PUBLIC_IP="$IP" -e DISABLE_TLS=true -e BASE_DOMAIN="$NAME.test" -e DOCK_ADMIN_EMAIL=you@example.com \
		"$C" bash -s -- "$@" <deploy/install.sh >"$STATE/$NAME-install.log" 2>&1 ||
		{ tail -20 "$STATE/$NAME-install.log" >&2; return 1; }
}

# has PATTERN: the last install's output has PATTERN (grep -E).
has() { grep -Eq -- "$1" "$STATE/$NAME-install.log" || { echo "install output lacks: $1" >&2; return 1; }; }

first_install() {
	install --yes || return 1
	has '==> apt: iptables, docker-ce' && has 'Console   http://localhost:7070' &&
		has "ssh -L 7070:127.0.0.1:7070 root@$IP" && has 'Sign in   you@example.com with password [^ ]+ ' &&
		has 'CLI       curl -fsSL https://dock.nordtempo.com/cli.sh \| sh -s -- http://localhost:7070' &&
		has "under $NAME.test" && has 'admin token: dock_' || return 1
	! grep -q "can't check the release's signature" "$STATE/$NAME-install.log" || { echo "signature not checked" >&2; return 1; }
	PASSWORD=$(sed -n 's/.*with password \([^ ]*\) .*/\1/p' "$STATE/$NAME-install.log")
	on docker version --format '{{.Server.Version}}' >/dev/null
}

# rerun installs again over the running app: nothing new is created and
# the app keeps serving.
rerun() {
	install --yes && has 'with the admin account from the first install' && ! has 'admin token:' 2>/dev/null &&
		[[ $(curl -s -o /dev/null -w '%{http_code}' --max-time 5 --connect-to "${APP#http://}:80:$IP:80" "$APP/") == 200 ]]
}

console() {
	on curl -fsS http://localhost:7070/ | grep -q '<html' && on curl -fsS http://localhost:7070/api/v1/health | grep -q '"ok"'
}

# cli_installer runs install-cli.sh for this server, with no terminal: it
# installs the latest published CLI and prints the login line.
cli_installer() {
	local out
	out=$(docker exec -i -e DOCK_VERSION=latest -e DOCK_INSTALL_DIR=/root/laptop/bin "$C" sh -s -- http://localhost:7070 \
		<scripts/release/install-cli.sh 2>&1) || { echo "$out" >&2; return 1; }
	grep -q 'Installed dock version v' <<<"$out" && grep -q 'Sign in with: dock login http://localhost:7070' <<<"$out" ||
		{ echo "$out" >&2; return 1; }
}

# device_login runs `dock login` and approves its code from a console
# session, as a person would in the browser.
device_login() {
	local code="" body
	docker exec -d -e XDG_CONFIG_HOME=/root/laptop "$C" sh -c 'dock login http://localhost:7070 --no-browser >/root/login.out 2>&1'
	for _ in $(seq 20); do
		code=$(on sed -n 's/.*enter the code \([A-Z0-9-]*\).*/\1/p' /root/login.out 2>/dev/null)
		[[ -n $code ]] && break
		sleep 0.5
	done
	[[ -n $code ]] || { echo "no code from dock login" >&2; return 1; }
	body=$(printf '{"email":"you@example.com","password":"%s"}' "$PASSWORD")
	on curl -fsS -o /dev/null -c /root/jar -H 'Content-Type: application/json' -H 'Origin: http://localhost:7070' \
		--data "$body" http://localhost:7070/api/v1/auth/login || return 1
	on curl -fsS -o /dev/null -b /root/jar -H 'Content-Type: application/json' -H 'Origin: http://localhost:7070' \
		--data "{\"user_code\":\"$code\"}" http://localhost:7070/api/v1/auth/cli/approve || return 1
	for _ in $(seq 20); do
		on grep -q 'Logged in as you@example.com' /root/login.out && return 0
		sleep 0.5
	done
	on cat /root/login.out >&2
	return 1
}

# template deploys a template with no --environment (the project's only
# one) and checks the URL it prints serves through Caddy.
template() {
	local out url host
	laptop project create fresh >/dev/null || return 1
	out=$(laptop template use memos -p fresh 2>&1) || { echo "$out" >&2; return 1; }
	url=$(grep -Eo "http://[a-z0-9.-]+\.$NAME\.test" <<<"$out" | head -1)
	[[ -n $url ]] || { echo "no URL printed: $out" >&2; return 1; }
	host=${url#http://}
	for _ in $(seq 60); do
		[[ $(curl -s -o /dev/null -w '%{http_code}' --max-time 5 --connect-to "$host:80:$IP:80" "$url/") == 200 ]] && APP=$url && return 0
		sleep 1
	done
	echo "$url doesn't answer 200" >&2
	return 1
}

domain() {
	local out
	out=$(laptop domain add "app.$NAME.test" -p fresh -s memos --detach 2>&1) || { echo "$out" >&2; return 1; }
	grep -Eq "A +app\.$NAME\.test +.+ +$IP" <<<"$out" || { echo "no DNS record: $out" >&2; return 1; }
}

uninstall_keep() {
	install --uninstall || return 1
	! on test -e /usr/local/bin/dock && ! on systemctl is-active -q dock && on test -f /var/lib/dock/dock.db &&
		[[ -z $(on docker ps -aq --filter label=dock.managed) ]]
}

reinstall() {
	install --yes && laptop project list | grep -q fresh
}

purge() {
	install --uninstall --purge && ! on test -e /var/lib/dock && ! on id dock >/dev/null 2>&1
}

# bare_image IMAGE TAG makes e2e-bare:TAG, the distribution with systemd.
# One built in the last week is used as it is: every build asks Docker Hub
# for the base, anonymously, and a night of scenarios has spent its pull
# limit by then (429). Past a week it is built again, and when that fails
# the old one still serves.
bare_image() {
	local created
	created=$(docker image inspect -f '{{.Created}}' "e2e-bare:$2" 2>/dev/null) &&
		(($(date +%s) - $(date -d "$created" +%s) < 7 * 86400)) && return 0
	docker build -q -f e2e/vm/bare.Dockerfile --build-arg BASE="$1" -t "e2e-bare:$2" e2e/vm >/dev/null && return 0
	[[ -n $created ]] && log "$1: using the bare image of $created"
}

i=0
for image in "${IMAGES[@]}"; do
	i=$((i + 1))
	tag=${image//[:\/]/-}
	NAME=$tag C=$SLOT-$tag IP=$PREFIX.$((10 + i))
	log "$image ($C, $IP)"
	bare_image "$image" "$tag" || { fail "$image: build the bare image"; continue; }
	docker rm -f -v "$C" >/dev/null 2>&1
	docker run -d --name "$C" --hostname "$C" --privileged --cgroupns=private \
		${E2E_VM_MEMORY:+--memory "$E2E_VM_MEMORY"} --tmpfs /run --tmpfs /run/lock -v /var/lib/docker -v /var/lib/containerd \
		--network "$NET" --ip "$IP" "e2e-bare:$tag" >/dev/null || { fail "$image: start"; continue; }
	# Booted, as a server is before anyone logs in: systemd's boot work
	# (tmpfiles empties /tmp, units start) must not race the install.
	docker exec "$C" systemctl is-system-running --wait >/dev/null 2>&1
	docker cp "$REL" "$C:/root/release"
	PASSWORD="" APP=""
	step "$image: install, piped, Docker and iptables included" first_install &&
		step "$image: console answers" console &&
		step "$image: install-cli.sh with the server's address" cli_installer &&
		step "$image: dock login with a device code" device_login &&
		step "$image: template without --environment, URL printed and served" template &&
		step "$image: domain add prints the DNS record" domain &&
		step "$image: install again changes nothing, the app keeps serving" rerun &&
		step "$image: uninstall keeps the data" uninstall_keep &&
		step "$image: install again brings the projects back" reinstall &&
		step "$image: uninstall --purge leaves nothing" purge
	[[ ${E2E_KEEP:-} == 1 ]] || docker rm -f -v "$C" >/dev/null
done
[[ ${E2E_KEEP:-} == 1 ]] || docker network rm "$NET" >/dev/null 2>&1

((FAILURES == 0)) && log "all passed" || log "$FAILURES failed (logs in $STATE)"
((FAILURES == 0))
