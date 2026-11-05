# snapshot.sh LABEL records the install's state under evidence/LABEL: every
# table's row count, the domains and links the 0041 migration rebuilds, the
# secrets format, revealed variable values, routes through Caddy, rows in
# both Postgres services, their containers and mounts, and whether the
# saved session and tokens still work. verify.sh compares two snapshots.
source /root/upg/lib.sh
set +e
L=${1:?label}
O=$EV/$L
rm -rf "$O" && mkdir -p "$O"

dock version > "$O/version.txt" 2>&1
curl -sS $DOCK_URL/api/v1/version > "$O/api-version.json"
counts > "$O/counts.txt"
db "PRAGMA user_version" > "$O/migrations.txt"
db "PRAGMA integrity_check" > "$O/integrity.txt"
db "PRAGMA foreign_key_check" > "$O/fk.txt"
db -header "SELECT * FROM domains ORDER BY id" > "$O/domains.txt"
db -header "SELECT * FROM cloudflare_records ORDER BY 1" > "$O/cloudflare_records.txt" 2>&1
db "SELECT * FROM install" > "$O/install.txt"
db ".schema" > "$O/schema.sql"
# The first byte of each sealed variable value is its format (ADR 0041).
db "SELECT id, key, hex(substr(value, 1, 1)) FROM variables ORDER BY id" > "$O/variable-formats.txt"

# Revealed values, as the API shows them to an admin.
for pe in shop/production shop/staging blog/production; do
  api GET "/projects/${pe%/*}/environments/${pe#*/}/variables?reveal=true" \
    | jq -r --arg pe "$pe" '(.items? // .)[] | "\($pe) \(.service // "-") \(.key)=\(.value)"' 2>&1
done | sort > "$O/variables.txt"
d org env list > "$O/org-variables.txt" 2>&1
api GET /storage > "$O/storage.json"
api GET /alerts/channels > "$O/alert-channels.json"

for pe in shop/production shop/staging blog/production; do
  d status -p "${pe%/*}" -e "${pe#*/}"
  d domain list -p "${pe%/*}" -e "${pe#*/}" --json 2>/dev/null
done > "$O/status.txt" 2>&1
for h in shop.example.com www.shop.example.com blog.example.org web-shop.upg.test; do
  printf '%s %s\n' "$h" "$(route "$h" | grep -E -o 'Hostname: [0-9a-f]+|[0-9]{3}$' | tr '\n' ' ')"
done > "$O/routes.txt"

# Rows in both Postgres services and where their data lives.
for s in db rawpg; do
  id=$(db "SELECT s.id FROM services s JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id
           WHERE p.name = 'shop' AND e.name = 'production' AND s.name = '$s'")
  c=$(docker ps -q --filter "label=dock.service=$id" | head -1)
  rows=$(docker exec -i "$c" sh -c 'psql -qtA -U "${POSTGRES_USER:-postgres}" -d "${POSTGRES_DB:-${POSTGRES_USER:-postgres}}" -c "SELECT count(*) FROM upg"' 2>&1)
  printf '%s container=%s rows=%s\n' "$s" "$(docker inspect -f '{{.Name}} {{.Id}} started={{.State.StartedAt}}' "$c")" "$rows"
  docker inspect "$c" -f '{{range .Mounts}}  {{.Type}} {{.Name}} {{.Destination}}{{"\n"}}{{end}}'
done > "$O/postgres.txt"
docker ps -a --format '{{.Names}} {{.Status}} {{.Image}}' | sort > "$O/containers.txt"
docker volume ls --format '{{.Name}}' | sort > "$O/volumes.txt"

# Does each credential still work?
{
  printf 'operator token: %s\n' "$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $(cat $U/admin.token)" $DOCK_URL/api/v1/projects)"
  printf 'admin token:    %s\n' "$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $(cat $U/admin2.token)" $DOCK_URL/api/v1/projects)"
  printf 'shop token:     %s (blog: %s)\n' \
    "$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $(cat $U/shop.token)" $DOCK_URL/api/v1/projects/shop/environments/production/variables)" \
    "$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $(cat $U/shop.token)" $DOCK_URL/api/v1/projects/blog/environments/production/variables)"
  printf 'session cookie: %s\n' "$(curl -sS -o /dev/null -w '%{http_code}' -b $U/cookies.txt $DOCK_URL/api/v1/projects)"
} > "$O/auth.txt"

journalctl -u dock --no-pager -o cat > "$O/dock.log" 2>&1
d backup status > "$O/backup-status.txt" 2>&1
d backup list -p shop -e production -s db > "$O/backup-list.txt" 2>&1
echo "snapshot $L in $O"
head -50 "$O/auth.txt" "$O/routes.txt" "$O/postgres.txt" "$O/install.txt" "$O/integrity.txt" "$O/fk.txt"
