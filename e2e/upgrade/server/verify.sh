# verify.sh runs the post-update checks after snapshot.sh made "before" and
# "after": data and credentials compared, then the behaviour of the new
# version with the old data (deploys with sealed values, backups on the
# storage connection, a new custom domain, the volumeless Postgres across
# restart, redeploy and stop/start, defaults). Prints PASS/FAIL lines;
# details go to evidence/verify.txt.
source /root/upg/lib.sh
set +e
cd $EV
R=$EV/verify.txt
: > $R
ok() { echo "PASS $*" | tee -a $R; }
bad() { echo "FAIL $*" | tee -a $R; }
check() { if eval "$2"; then ok "$1"; else bad "$1"; fi; }
P=(-p shop -e production)

# rows SERVICE prints the row count in shop/production's Postgres SERVICE.
rows() {
  local id c
  id=$(db "SELECT s.id FROM services s JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id
           WHERE p.name = 'shop' AND e.name = 'production' AND s.name = '$1'")
  for _ in $(seq 30); do
    c=$(docker ps -q --filter "label=dock.service=$id" | head -1)
    [[ -n "$c" ]] && docker exec -i "$c" sh -c 'psql -qtA -U "${POSTGRES_USER:-postgres}" -d "${POSTGRES_DB:-${POSTGRES_USER:-postgres}}" -c "SELECT count(*) FROM upg"' 2>/dev/null && return
    sleep 2
  done
  echo "none"
}
mounts() { docker inspect "$(docker ps -q --filter "name=dock_p1_e1_s2_" | head -1)" -f '{{range .Mounts}}{{.Name}}{{end}}'; }

say "1. database"
check "migrations at $(cat after/migrations.txt)" '[[ $(cat after/migrations.txt) -ge $(cat before/migrations.txt) ]]'
check "integrity_check ok" '[[ $(cat after/integrity.txt) == ok ]]'
check "foreign_key_check empty" '[[ ! -s after/fk.txt ]]'
lost=$(join <(sort before/counts.txt) <(sort after/counts.txt) | awk '$3 < $2 && $1 !~ /_stats$|^audit/ {print}')
check "no table lost rows ($lost)" '[[ -z "$lost" ]]'
check "domains: same rows, every one verified (0041 grandfathers them as of created_at)" \
  '[[ $(tail -n +2 after/domains.txt | awk -F"|" "\$10 != \"\"" | wc -l) -eq $(tail -n +2 before/domains.txt | wc -l) ]]'
check "domains: id, hostname, service, generated, redirect, status, created_at unchanged" \
  'diff <(tail -n +2 before/domains.txt | cut -d"|" -f1-8) <(tail -n +2 after/domains.txt | cut -d"|" -f1-8)'

say "2. sealed values"
check "install.secrets_format = 2" '[[ $(cut -d"|" -f3 after/install.txt) == 2 ]]'
check "every variable reveals the same value" 'diff -q before/variables.txt after/variables.txt'
check "every variable value is format 2" '! grep -v "|02$" after/variable-formats.txt'
check "no value failed to open in the log" '! grep -i -E "doesn.t open|does not open|reseal.*(fail|error)" after/dock.log'

say "3. domains"
check "routes unchanged" 'diff -q before/routes.txt after/routes.txt'
check "custom domains still active" '[[ $(grep -c "|active|" after/domains.txt) -eq $(grep -c "|active|" before/domains.txt) ]]'
DOCK_TOKEN=$(cat $U/admin2.token) dock domain add new.example.net "${P[@]}" -s web --detach >> $R 2>&1
d domain add new-op.example.net "${P[@]}" -s web --detach >> $R 2>&1
sleep 3
db -header "SELECT hostname, status, status_reason, verified_at IS NOT NULL AS verified, verify_token <> '' AS has_token FROM domains WHERE hostname LIKE 'new%'" | tee -a $R
check "a domain added after the update by an org admin waits for its TXT record" \
  '[[ $(db "SELECT verified_at IS NULL FROM domains WHERE hostname = '"'"'new.example.net'"'"'") == 1 ]]'

say "5. sessions and tokens"
check "operator, admin and project tokens and the session still work" 'diff -q before/auth.txt after/auth.txt'

say "6. defaults"
check "no environment locked" '[[ $(db "SELECT count(*) FROM environments WHERE locked_at IS NOT NULL") == 0 ]]'
check "lock new production environments is off" '[[ $(db "SELECT count(*) FROM orgs WHERE lock_production <> 0") == 0 ]]'
check "no ongoing health checks turned on" '[[ $(db "SELECT count(*) FROM services WHERE health_check_interval_seconds <> 0") == 0 ]]'
check "no service stopped" '[[ $(db "SELECT count(*) FROM services WHERE stopped_at IS NOT NULL") == 0 ]]'
check "no app container recreated by the update" \
  '[[ "$(grep ^dock_p before/containers.txt | cut -d" " -f1)" == "$(grep ^dock_p after/containers.txt | cut -d" " -f1)" ]]'
d deploy "${P[@]}" -s web --image traefik/whoami:v1.10 >> $R 2>&1
check "a new release of web deploys and goes active" \
  '[[ $(db "SELECT status FROM releases r JOIN services s ON s.id = r.service_id WHERE s.name = '"'"'web'"'"' AND s.environment_id = 1 ORDER BY r.id DESC LIMIT 1") == active ]]'
web=$(docker ps -q --filter name=dock_p1_e1_s3_ | head -1)
docker exec "$web" /whoami --help >/dev/null 2>&1
env=$(docker inspect "$web" -f '{{range .Config.Env}}{{println .}}{{end}}')
echo "$env" | grep -E '^(SECRET_KEY|ORG_TOKEN|RAW_PG|DATABASE_URL|LOG_LEVEL)=' | sed 's/=.*PASSWORD.*//' >> $R
check "its container has the sealed values, resolved" \
  'grep -q "^SECRET_KEY=s3cr3t-web-key$" <<< "$env" && grep -q "^ORG_TOKEN=org-secret-1234$" <<< "$env" && grep -q "^RAW_PG=postgres://postgres:rawpg-pass-5678@" <<< "$env" && grep -q "^DATABASE_URL=postgresql://postgres:.*@db:5432/app$" <<< "$env"'
check "routes after the deploy" '[[ $(route shop.example.com) == *" 200" ]]'
d backup run "${P[@]}" -s db >> $R 2>&1
check "a backup to the storage connection works (sealed keys open)" 'tail -1 $R | grep -q "Backed up db"'

say "4. volumeless Postgres (rawpg)"
echo "after update: rows=$(rows rawpg) mount=$(mounts)" | tee -a $R
check "update itself kept its data" '[[ $(rows rawpg) == $(grep -o "rows=[0-9]*" before/postgres.txt | tail -1 | cut -d= -f2) ]]'
before_vol=$(mounts)
d restart "${P[@]}" -s rawpg >> $R 2>&1
echo "after restart: rows=$(rows rawpg) mount=$(mounts)" | tee -a $R
d deploy "${P[@]}" -s rawpg --image postgres:17-alpine >> $R 2>&1
echo "after redeploy: rows=$(rows rawpg) mount=$(mounts)" | tee -a $R
d service stop "${P[@]}" rawpg >> $R 2>&1 || d stop "${P[@]}" -s rawpg >> $R 2>&1
d service start "${P[@]}" rawpg >> $R 2>&1 || d start "${P[@]}" -s rawpg >> $R 2>&1
echo "after stop+start: rows=$(rows rawpg) mount=$(mounts)" | tee -a $R
check "old anonymous volume $before_vol still on disk" 'docker volume inspect "$before_vol" >/dev/null 2>&1'
docker volume ls --format '{{.Name}}' | grep -E '^[0-9a-f]{64}$' | while read -r v; do
  echo "anon volume $v used by: $(docker ps -a -q --filter volume=$v | tr '\n' ' ')"
done | tee -a $R
check "Postgres with a Dock volume kept its rows" '[[ $(rows db) == $(grep -o "rows=[0-9]*" before/postgres.txt | head -1 | cut -d= -f2) ]]'

echo; grep -c ^PASS $R | sed 's/^/passed: /'; grep ^FAIL $R || true
