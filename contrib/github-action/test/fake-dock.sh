#!/usr/bin/env bash
# Starts a throwaway Dock for the action's tests: `dock serve` on the fake
# runtime (no Docker, Caddy or BuildKit), listening on 127.0.0.1 only, with
# its state in DIR. Then bootstraps it and creates the project shop with
# an environment staging, and a token that may deploy there and nowhere
# else: what the guide tells teams to make.
#
#   fake-dock.sh DOCK_BINARY DIR [PORT_BASE]
#
# Prints KEY=value lines for the test to read (DOCK_TEST_URL, the admin's
# DOCK_TEST_ADMIN and the deploy token DOCK_TEST_TOKEN) and leaves the
# daemon running; its pid is in DIR/pid, its log in DIR/daemon.log. Stop it
# with: kill "$(cat DIR/pid)". PORT_BASE (default 17470) is the first of
# the eleven ports it takes.
set -euo pipefail

dock="$(realpath "$1")"
dir="$(realpath -m "$2")"
base="${3:-17470}"
mkdir -p "$dir/data"
url="http://127.0.0.1:$base"

# The fake runtime runs an in-memory analytics store: it only needs a
# binary to name.
printf fake > "$dir/dock-analytics"
chmod +x "$dir/dock-analytics"
cat > "$dir/config.toml" << EOF
data_dir = "$dir/data"
listen = "127.0.0.1:$base"
internal_listen = "127.0.0.1:$((base + 1))"
public_listen = "127.0.0.1:$((base + 2))"
public_ip = "127.0.0.1"
base_domain = "localhost"

[caddy]
admin_socket = "$dir/data/caddy-admin.sock"
http_port = $((base + 3))
https_port = $((base + 4))
disable_tls = true
traffic_listen = "127.0.0.1:$((base + 8))"

[analytics]
store_binary = "$dir/dock-analytics"
ingest_listen = "127.0.0.1:$((base + 5))"
stats_listen = "127.0.0.1:$((base + 6))"

[hooks]
listen = "127.0.0.1:$((base + 7))"

[traces]
ingest_listen = "127.0.0.1:$((base + 9))"

[status]
listen = "127.0.0.1:$((base + 10))"
EOF

DOCK_FAKE_RUNTIME=1 HOME="$dir" "$dock" serve --config "$dir/config.toml" > "$dir/daemon.log" 2>&1 &
echo $! > "$dir/pid"
for _ in $(seq 150); do
  curl -fsS -o /dev/null "$url/api/v1/health" 2> /dev/null && break
  sleep 0.1
done
curl -fsS -o /dev/null "$url/api/v1/health" || {
  cat "$dir/daemon.log" >&2
  echo "the fake Dock did not start" >&2
  exit 1
}

admin="$(HOME="$dir" "$dock" admin bootstrap --config "$dir/config.toml" 2> /dev/null)"
as_admin() { HOME="$dir" DOCK_URL="$url" DOCK_TOKEN="$admin" "$dock" "$@"; }
as_admin project create shop --json > /dev/null
as_admin environment create staging -p shop --json > /dev/null
token="$(as_admin token create github-shop-staging --scope deploy --project shop --environment staging --json | jq -r .secret)"

echo "DOCK_TEST_URL=$url"
echo "DOCK_TEST_ADMIN=$admin"
echo "DOCK_TEST_TOKEN=$token"
