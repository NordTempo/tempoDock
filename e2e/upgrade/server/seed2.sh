# Second half of the seed (run by seed.sh): rows in both Postgres services,
# backups on a MinIO storage connection, an alert channel, analytics, API
# tokens and a logged-in browser session.
source /root/upg/lib.sh

# ctr SERVICE prints the running container of shop/production's SERVICE.
ctr() {
  local id
  id=$(db "SELECT s.id FROM services s JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id
           WHERE p.name = 'shop' AND e.name = 'production' AND s.name = '$1'")
  docker ps -q --filter "label=dock.service=$id" | head -1
}
# psql_in SERVICE SQL runs SQL in shop/production's Postgres SERVICE.
psql_in() {
  docker exec -i "$(ctr "$1")" sh -c 'psql -qtA -v ON_ERROR_STOP=1 -U "${POSTGRES_USER:-postgres}" -d "${POSTGRES_DB:-${POSTGRES_USER:-postgres}}"' <<< "$2"
}

if [[ "${1:-}" != --skip-rows ]]; then
say "rows in both Postgres services"
for s in db rawpg; do
  for _ in $(seq 30); do psql_in $s 'SELECT 1' >/dev/null 2>&1 && break; sleep 2; done
  psql_in $s "CREATE TABLE IF NOT EXISTS upg (id serial PRIMARY KEY, note text);
              INSERT INTO upg (note) SELECT 'row ' || g FROM generate_series(1, 500) g;
              SELECT count(*) FROM upg;"
done
docker inspect "$(ctr rawpg)" --format '{{range .Mounts}}{{.Type}} {{.Name}} {{.Destination}}{{"\n"}}{{end}}' | tee $EV/rawpg-mounts-seed.txt
docker inspect "$(ctr db)" --format '{{range .Mounts}}{{.Type}} {{.Name}} {{.Destination}}{{"\n"}}{{end}}' | tee $EV/db-mounts-seed.txt
fi

if [[ "${1:-}" != --only-rest ]]; then
say "MinIO and backups"
# Restic's jobs run on Docker's default bridge, so MinIO listens on the
# host and is reached at that bridge's gateway.
GW=$(docker network inspect bridge -f '{{(index .IPAM.Config 0).Gateway}}')
# Backup jobs run like apps, which can't reach private addresses: allow
# MinIO's, as an operator would for a LAN store.
sed -i "s|^allow_private = \\[\\]|allow_private = [\"$GW/32\"]|" /etc/dock/config.toml
systemctl restart dock-firewall
docker rm -f upg-minio >/dev/null 2>&1 || true
docker run -d --name upg-minio --network host -e MINIO_ROOT_USER=upgminio -e MINIO_ROOT_PASSWORD=upgminio-secret-42 \
  pgsty/minio:latest server /data --address :9000 >/dev/null
for _ in $(seq 30); do curl -fsS -o /dev/null http://127.0.0.1:9000/minio/health/live 2>/dev/null && break; sleep 1; done; sleep 2
curl -fsS -X PUT --aws-sigv4 aws:amz:us-east-1:s3 --user upgminio:upgminio-secret-42 http://$GW:9000/dock-backups
printf %s upgminio-secret-42 | d storage add minio --endpoint http://$GW:9000 --bucket dock-backups \
  --access-key-id upgminio --secret-access-key-stdin || echo "storage add failed"
d backup run -p shop -e production -s db || echo "local backup failed"
d backup move --storage minio --yes || echo "backup move failed"
for _ in $(seq 60); do [[ "$(d backup status)" == *'Moving the backups'* ]] || break; sleep 2; done
d backup run -p shop -e production -s db || echo "backup run on minio failed"
d backup run -p shop -e production -s rawpg || echo "rawpg has no volume to back up"
d backup status

fi

say "alert channels"
d alerts channel add ops --webhook https://hooks.example.com/dock --secret upg-webhook-secret
d alerts channel add slack --slack https://hooks.slack.example.com/services/T0/B0/x

say "analytics"
d analytics server "$(hostname)" || echo "analytics server failed"
d analytics enable shop.example.com || echo "analytics enable failed"

say "tokens"
d token create upg-admin --scope admin --json | jq -r .secret > $U/admin2.token
d token create upg-shop-deploy --scope deploy --project shop --json | jq -r .secret > $U/shop.token
wc -c $U/admin2.token $U/shop.token

say "browser session"
curl -sS -c $U/cookies.txt -H 'Content-Type: application/json' \
  --data "{\"email\":\"admin@localhost\",\"password\":\"$(cat $U/admin.password)\"}" $DOCK_URL/api/v1/auth/login -o /dev/null -w '%{http_code}\n'
curl -sS -b $U/cookies.txt $DOCK_URL/api/v1/projects -o /dev/null -w 'session GET /projects: %{http_code}\n'
