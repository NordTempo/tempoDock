# rollback.sh rolls the control plane back by hand, as the updater does when
# an update fails (ADR 0028): stop dock, put back the previous binary, units
# and caddy.json from /var/lib/dock-update/previous, put back dock.db from
# the pre-update backup, start. Then checks that the old version is back
# with the data as of the update (snapshot "rolledback" vs "before"), and
# records what the old version does to a volumeless Postgres on restart.
source /root/upg/lib.sh
set +e
R=$EV/rollback.txt
: > $R
B=$(ls -t /var/lib/dock-update/backup-*.tar.gz | head -1)
echo "backup $B" | tee -a $R
tar -tzf "$B" | tee -a $R

systemctl stop dock
install -m 0755 /var/lib/dock-update/previous/dock /usr/local/bin/dock
cp /var/lib/dock-update/previous/units/* /etc/systemd/system/
cp /var/lib/dock-update/previous/caddy.json /etc/dock/caddy.json
systemctl daemon-reload
X=$(mktemp -d)
tar -xzf "$B" -C "$X"
rm -f $DB-wal $DB-shm
install -m 0600 -o dock -g dock "$(find "$X" -name dock.db | head -1)" $DB
rm -rf "$X"
systemctl start dock
wait_api
sleep 5
bash /root/upg/snapshot.sh rolledback > /dev/null 2>&1
cd $EV
{
  echo "version: $(cat rolledback/api-version.json)"
  echo "migrations: $(cat rolledback/migrations.txt) integrity: $(cat rolledback/integrity.txt) install: $(cat rolledback/install.txt)"
  echo "counts diff vs before (stats tables tick on their own):"; diff before/counts.txt rolledback/counts.txt
  echo "variables diff vs before:"; diff before/variables.txt rolledback/variables.txt && echo same
  echo "auth:"; cat rolledback/auth.txt
  echo "routes:"; cat rolledback/routes.txt
  echo "postgres:"; cat rolledback/postgres.txt
  echo "log errors:"; grep -i -E 'level=ERROR|open' rolledback/dock.log | tail -5
} | tee -a $R

say "the old version and a volumeless Postgres"
old=$(docker inspect "$(docker ps -q --filter name=dock_p1_e1_s2_ | head -1)" -f '{{range .Mounts}}{{.Name}}{{end}}')
psql_rows() { docker exec -i "$(docker ps -q --filter name=dock_p1_e1_s2_ | head -1)" psql -qtA -U postgres -c 'SELECT count(*) FROM upg' 2>&1; }
psql_rows > /dev/null
docker exec -i "$(docker ps -q --filter name=dock_p1_e1_s2_ | head -1)" psql -qtA -U postgres \
  -c 'CREATE TABLE IF NOT EXISTS upg (id serial PRIMARY KEY, note text); INSERT INTO upg (note) VALUES ($$after rollback$$)' >> $R 2>&1
echo "before restart: vol=$old rows=$(psql_rows)" | tee -a $R
d restart -p shop -e production -s rawpg >> $R 2>&1
sleep 5
new=$(docker inspect "$(docker ps -q --filter name=dock_p1_e1_s2_ | head -1)" -f '{{range .Mounts}}{{.Name}}{{end}}')
echo "after restart on $(cat rolledback/api-version.json): vol=$new rows=$(psql_rows)" | tee -a $R
if docker volume inspect "$old" >/dev/null 2>&1; then echo "old volume $old kept on disk" | tee -a $R; else echo "old volume $old removed" | tee -a $R; fi
