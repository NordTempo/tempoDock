#!/usr/bin/env bash
# Dock Analytics' errors, alerts and session replay (ADR 0037, the
# done-whens of A2 and A3), on this machine: one Dock server in a
# Docker-in-Docker container serving a production site, shop.test, with
# the analytics store next to it. A real browser (headless Chromium) loads
# the site's pages with Dock's script.
#
#   A2: a thrown error on the site appears grouped within seconds and
#       alerts once, however often it is thrown again.
#   A3: a sampled page load is recorded and replays: its chunks reach the
#       store, the replay lists with the error on its timeline, what was
#       typed into an input is masked, and watching it is audited.
#   A4: a public dashboard is served through Caddy at
#       stats-<site>.<base domain>, shows errors only when allowed, and is
#       gone at once when turned off.
#
#   make web && make build && make analytics-binary
#   e2e/analytics-features.sh
#
# Needs Docker (privileged containers), caddy on PATH, curl, jq and a
# Chromium (CHROME, default Playwright's cached headless shell). KEEP=1
# leaves the server running; the CLI then works with `source $W/env`.
set -euo pipefail
REPO=$(cd "$(dirname "$0")/.." && pwd)
STORE=$REPO/bin/dock-analytics-linux-amd64
CHROME=${CHROME:-$(ls -d ~/.cache/ms-playwright/chromium_headless_shell-*/chrome-*/chrome-headless-shell 2>/dev/null | tail -1)}
NET=dock-e2e-features
CP=dock-e2e-features-cp
HOST=shop.test
W=$(mktemp -d)
export HOME=$W PATH=$REPO/bin:$PATH

say() { printf '==> %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*"; exit 1; }
x() { local c=$1; shift; docker exec "$c" sh -c "$*"; }
ip() { docker inspect -f "{{(index .NetworkSettings.Networks \"$NET\").IPAddress}}" "$1"; }
cleanup() { docker rm -f "$CP" >/dev/null 2>&1; docker network rm "$NET" >/dev/null 2>&1; rm -rf "$W"; }
[[ ${KEEP:-} == 1 ]] || trap cleanup EXIT
[[ -x $CHROME ]] || fail "no Chromium; set CHROME"

wait_for() {
	local what=$1 t=$2; shift 2
	for _ in $(seq "$t"); do "$@" >/dev/null 2>&1 && return; sleep 1; done
	fail "$what (after ${t}s)"
}

say "the server"
docker rm -f "$CP" >/dev/null 2>&1 || true
docker network rm "$NET" >/dev/null 2>&1 || true
docker network create "$NET" >/dev/null
docker run -d --privileged --name "$CP" --hostname "$CP" --network "$NET" \
	-v "$REPO/bin/dock:/usr/local/bin/dock:ro" -v "$(command -v caddy):/usr/local/bin/caddy:ro" \
	-v "$STORE:/usr/local/lib/dock-analytics:ro" docker:28-dind >/dev/null
wait_for "Docker" 60 x "$CP" 'docker info'
docker exec -i "$CP" sh -c 'mkdir -p /etc/dock /run/dock-caddy && cat > /etc/dock/config.toml' <<CONF
data_dir = "/var/lib/dock"
listen = "0.0.0.0:7070"
internal_listen = "127.0.0.1:7071"
public_ip = "$(ip "$CP")"
base_domain = "$CP.test"
[caddy]
admin_socket = "/run/dock-caddy/admin.sock"
http_port = 80
https_port = 443
disable_tls = true
[buildkit]
enabled = false
[analytics]
store_binary = "/usr/local/lib/dock-analytics"
CONF
x "$CP" 'dock admin caddy-config --config /etc/dock/config.toml > /etc/dock/caddy.json'
x "$CP" 'nohup caddy run --resume --config /etc/dock/caddy.json >> /var/log/caddy.log 2>&1 &'
wait_for "Caddy" 20 x "$CP" 'test -S /run/dock-caddy/admin.sock'
TOKEN=$(x "$CP" 'dock admin bootstrap --config /etc/dock/config.toml' 2>/dev/null)
x "$CP" 'nohup dock serve --config /etc/dock/config.toml > /var/log/dock.log 2>&1 &'
export DOCK_URL=http://$(ip "$CP"):7070 DOCK_TOKEN=$TOKEN
printf 'export PATH=%q HOME=%q DOCK_URL=%q DOCK_TOKEN=%q\n' "$PATH" "$W" "$DOCK_URL" "$DOCK_TOKEN" > "$W/env"
wait_for "the control plane" 30 dock analytics

say "the site: shop/web, $HOST, recording every page load"
dock project create shop >/dev/null 2>&1
dock service create web -p shop -e production --image nginx:1.29-alpine --port 80 >/dev/null
dock deploy -p shop -e production -s web --image nginx:1.29-alpine >/dev/null 2>&1
dock domain add "$HOST" -p shop -e production -s web --detach >/dev/null 2>&1
wait_for "the site" 180 sh -c "curl -sf -H 'Host: $HOST' http://$(ip "$CP")/ | grep -q nginx"
dock analytics enable "$HOST" >/dev/null 2>&1
dock analytics site set "$HOST" -p shop --replay on --replay-sample 100 >/dev/null
wait_for "the store" 180 sh -c 'dock analytics --json | jq -e .store_running'

# A page with Dock's script, a form and a button that throws.
web=$(x "$CP" "docker ps --format '{{.Names}}' | grep -m1 '^dock_p'")
[[ -n $web ]] || fail "no container of the site"
docker exec -i "$CP" docker exec -i "$web" sh -c 'cat > /usr/share/nginx/html/form.html' <<'HTML'
<!doctype html><html><head><title>Checkout</title><script src="/_dock/a/s.js" defer></script></head>
<body><h1>Checkout</h1><p>Card holder</p>
<input id="name" type="text"><button id="pay">Pay</button>
<script>
  // Type a secret, then throw: what a visitor would do.
  setTimeout(function () {
    var i = document.getElementById("name");
    i.focus();
    i.value = "Ola Secretname";
    i.dispatchEvent(new Event("input", { bubbles: true }));
  }, 1500);
  setTimeout(function () { throw new TypeError("price is undefined"); }, 2500);
</script></body></html>
HTML

# browse N: a real browser loads the page N times, each its own visit.
browse() {
	for i in $(seq "$1"); do
		"$CHROME" --no-sandbox --headless --disable-gpu --user-agent="Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 e2e-$i" \
			--host-resolver-rules="MAP $HOST $(ip "$CP")" --virtual-time-budget=15000 --run-all-compositor-stages-before-draw \
			--dump-dom "http://$HOST/form.html" >/dev/null 2>&1 || true
	done
}

say "A2: a thrown error appears grouped, and alerts once"
t0=$(date +%s)
browse 1
groups() { dock analytics errors "$HOST" -p shop --range 24h --json | jq -r '.groups | length'; }
wait_for "the error in the store" 60 sh -c "[ \$(dock analytics errors $HOST -p shop --range 24h --json | jq '.groups | length') = 1 ]"
say "grouped after $(($(date +%s) - t0))s: $(dock analytics errors "$HOST" -p shop --range 24h --json | jq -r '.groups[0] | "\(.type): \(.message) at \(.frame)"')"
alerts() { dock alerts --json | jq '[.[] | select(.kind == "new_error")] | length'; }
wait_for "the new error alert" 30 sh -c "[ \$(dock alerts --json | jq '[.[] | select(.kind == \"new_error\")] | length') = 1 ]"
browse 3
wait_for "four of it" 30 sh -c "[ \$(dock analytics errors $HOST -p shop --range 24h --json | jq '.groups[0].count') = 4 ]"
[[ $(groups) == 1 ]] || fail "the same error made $(groups) groups"
[[ $(alerts) == 1 ]] || fail "the same error alerted $(alerts) times"
say "PASS A2: one group of 4, one alert"

say "A3: the page loads replay, inputs masked"
wait_for "the replays" 60 sh -c "[ \$(dock analytics replays $HOST -p shop --range 24h --json | jq '.replays | length') -ge 1 ]"
replay=$(dock analytics replays "$HOST" -p shop --range 24h --json | jq -r '.replays[-1].id')
detail=$(dock analytics replays "$HOST" -p shop "$replay" --json)
echo "$detail" | jq -e '[.timeline[] | select(.kind == "error")] | length >= 1' >/dev/null || fail "no error on the replay's timeline: $detail"
rec=$W/recording.ndjson
curl -sf -H "Authorization: Bearer $DOCK_TOKEN" "$DOCK_URL/api/v1/projects/shop/sites/$HOST/replays/$replay/events" --compressed > "$rec"
grep -q '"type":2' "$rec" || fail "the recording has no full snapshot"
grep -q 'Checkout' "$rec" || fail "the recording doesn't have the page's text"
! grep -q 'Secretname' "$rec" || fail "what was typed is in the recording"
grep -q '\*\*\*\*' "$rec" || fail "the masked input isn't in the recording"
dock audit -p shop --json 2>/dev/null | jq -e '[.[] | select(.action == "analytics.replay_view")] | length >= 1' >/dev/null || fail "watching the replay wasn't audited"
say "PASS A3: replay $replay, $(wc -c < "$rec") bytes of events, the error on its timeline, the input masked, the view audited"

say "A4: the public dashboard"
pub="stats-shop-test.$CP.test"
page() { curl -s -H "Host: $pub" "http://$(ip "$CP")/"; }
dock analytics site set "$HOST" -p shop --public on >/dev/null
wait_for "the public dashboard" 30 sh -c "curl -sf -H 'Host: $pub' http://$(ip "$CP")/ | grep -q '<h1>$HOST</h1>'"
p=$(page)
grep -q 'Visitors' <<<"$p" || fail "no visitors on the public dashboard: $p"
! grep -q 'TypeError' <<<"$p" || fail "the public dashboard shows errors the site didn't allow"
dock analytics site public "$HOST" -p shop --json | jq -e '.addresses[0].hostname == "'"$pub"'"' >/dev/null || fail "dock analytics site public doesn't list $pub"
dock analytics site set "$HOST" -p shop --public-errors on >/dev/null
# Changing a setting takes effect at once, cached page or not.
grep -q 'TypeError' <<<"$(page)" || fail "the public dashboard doesn't show errors the site allowed"
dock analytics site set "$HOST" -p shop --public off >/dev/null
[[ $(curl -s -o /dev/null -w '%{http_code}' -H "Host: $pub" "http://$(ip "$CP")/") != 200 ]] || fail "the public dashboard is still served once turned off"
say "PASS A4: served at $pub, errors only when allowed, gone when off"
