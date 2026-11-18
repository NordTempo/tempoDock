#!/usr/bin/env bash
# The drill for a lost server (ADR 0098): a control plane backs up to an S3
# bucket (MinIO, next to the slot's servers), the server is removed with
# everything on it, and a fresh one at the same address comes back with the
# two commands of the guide:
#
#   install.sh --restore-from-bucket --yes     (with DOCK_RESTORE_*)
#   dock admin recover --yes
#
#   e2e/vm/up.sh 0 && source /tmp/<slot>/env && e2e/recover.sh
#
# Checks: the fetch and the install go through with nothing asked, dock
# runs in recovery mode, the plan lists the database with its snapshot, the
# run restores it and starts the stateless app from its last release, the
# marker row is back, the app answers through the proxy, the old API token
# still works, recovery mode ended and a backup runs again.
#
# It REMOVES the slot's control plane ($SLOT-cp) and puts a new container
# in its place, so it runs last in a slot, or in a slot of its own: the
# new server has only what the backups held. Services of other scenarios
# on the control plane that were built from source can't start again when
# the platform registry was on the lost server, and the run reports them
# as failed. Nodes are left alone.
#
# The new server runs DOCK_BINARY, or the binary the lost one ran (copied
# off it first), installed with this checkout's deploy/.
#
# RECOVER_VOLUME_APP=1 adds an app with a plain volume holding a marker
# file. Off by default: the recovery restores template databases only until
# ADR 0095's volume restore is on main; turn it on (and make it the
# default) then.
set -uo pipefail
source "$(dirname "$0")/lib.sh"
RUN=${E2E_RUN:-$(date +%d%H%M%S)}
CP=$SLOT-cp CP_IP=$SUBNET_PREFIX.10 NET=$SLOT-net
MINIO=$SLOT-minio MINIO_IP=$SUBNET_PREFIX.30
MINIO_IMAGE=${RECOVER_MINIO_IMAGE:-pgsty/minio:latest}
S3_KEY=recoverdrill S3_SECRET=recoverdrill-secret-$RUN BUCKET=recover-$RUN
S3_URL=http://$MINIO_IP:9000
STORAGE=recover-$RUN P=recover-$RUN
MARKER=marker-$RUN
VOLUME_APP=${RECOVER_VOLUME_APP:-0}
TMP=$(mktemp -d)
trap 'docker rm -fv "$MINIO" >/dev/null 2>&1; rm -rf "$TMP"' EXIT

# The drill removes $CP: only ever the slot's own control plane.
[[ $DOCK_URL == "http://$CP_IP:7070" ]] ||
	{ echo "DOCK_URL ($DOCK_URL) is not $CP's (http://$CP_IP:7070); source /tmp/$SLOT/env" >&2; exit 1; }
docker inspect "$CP" >/dev/null 2>&1 || { echo "no $CP; e2e/vm/up.sh first" >&2; exit 1; }

on() { docker exec "$CP" "$@"; }
dk_p() { dk "$@" -p "$P" -e "$ENV_NAME"; }
newest_active() { [[ $(api GET "$(svc_path "$P" "$1")/releases" | jq -r 'max_by(.number).status') == active ]]; }
env_server() { api GET "/projects/$P/environments" | jq -r --arg e "$ENV_NAME" '.[] | select(.name == $e) | .server'; }
# container S prints the running container of service S on the control plane.
container() {
	local id
	id=$(api GET "$(svc_path "$P" "$1")" | jq -r .id)
	on docker ps -q --filter label=dock.managed=true --filter "label=dock.service=$id" | head -1
}
in_svc() { local s=$1; shift; on docker exec "$(container "$s")" "$@"; }
psql_in() { in_svc db sh -c "psql -U \"\$POSTGRES_USER\" -d \"\$POSTGRES_DB\" -tAc \"$1\""; }
move_done() { [[ $(dk backup status --json | jq -r '.move.running // false') != true ]]; }
db_runs() { [[ -n $(container db) ]]; }
marker_back() { [[ $(psql_in 'select v from recover_marker' 2>/dev/null) == "$MARKER" ]]; }
# allow_minio lets backup and restore jobs on the control plane reach the
# slot's network, as an operator does for a store on the LAN (the guide).
allow_minio() {
	on bash -c "grep -q '^allow_private' /etc/dock/config.toml && sed -i 's|^allow_private.*|allow_private = [\"$SUBNET_PREFIX.0/24\"]|' /etc/dock/config.toml || printf '\n[network]\nallow_private = [\"$SUBNET_PREFIX.0/24\"]\n' >>/etc/dock/config.toml; systemctl restart dock-firewall"
}
# stop ends the drill early: what follows needs what just failed.
stop() { fail "$*"; echo "$FAILURES failed" >&2; exit 1; }

log "run $RUN: $P on $CP, bucket $BUCKET on $MINIO"

# ---- the bucket elsewhere ---------------------------------------------------

docker rm -fv "$MINIO" >/dev/null 2>&1
docker run -d --name "$MINIO" --network "$NET" --ip "$MINIO_IP" \
	-e MINIO_ROOT_USER=$S3_KEY -e MINIO_ROOT_PASSWORD="$S3_SECRET" \
	"$MINIO_IMAGE" server /data --address :9000 >/dev/null || stop "MinIO did not start"
wait_for 60 curl -fsS -o /dev/null "$S3_URL/minio/health/live" 2>/dev/null || stop "MinIO never answered on $S3_URL"
wait_for 20 curl -fsS -o /dev/null -X PUT --aws-sigv4 aws:amz:us-east-1:s3 --user "$S3_KEY:$S3_SECRET" "$S3_URL/$BUCKET" 2>/dev/null ||
	stop "can't create the bucket"
pass "MinIO runs with bucket $BUCKET"

# ---- the server before it is lost -------------------------------------------

allow_minio
printf %s "$S3_SECRET" | dk storage add "$STORAGE" --endpoint "$S3_URL" --bucket "$BUCKET" \
	--access-key-id $S3_KEY --secret-access-key-stdin >/dev/null || stop "storage connection not added"
dk backup move --storage "$STORAGE" --yes >/dev/null || stop "backups not moved to the bucket"
sleep 1
wait_for 300 move_done || stop "the move to the bucket did not finish"
check "backups go to the bucket" test "$(dk backup status --json | jq -r .storage)" = "$STORAGE"

dk project create "$P" >/dev/null || stop "can't create $P"
# The plan covers the control plane's own server: the project lives there.
[[ $(env_server) == "$CP" ]] || dk env move "$ENV_NAME" -p "$P" --to "$CP" --yes >/dev/null 2>&1
check "$P is on $CP" test "$(env_server)" = "$CP"
E2E_PROXY=$CP_IP:80

dk_p service create db --template postgres >/dev/null || stop "db not created"
check "db is active" wait_for 180 newest_active db
sleep 3
psql_in "create table recover_marker (v text); insert into recover_marker values ('$MARKER')" >/dev/null
check "the marker row is in db" test "$(psql_in 'select v from recover_marker')" = "$MARKER"

# From an image on Docker Hub: an image built from source lives in the
# platform registry, which is on the server about to be lost.
dk_p service create web --image nginx:1.29-alpine --port 80 >/dev/null || stop "web not created"
dk_p deploy -s web --image nginx:1.29-alpine >/dev/null 2>&1 || fail "web did not deploy"
HOST=$(generate_domain "$P" web)
check "web answers before the loss" wait_http "$HOST" / 'Welcome to nginx' 60

if [[ $VOLUME_APP == 1 ]]; then
	dk_p service create files --image nginx:1.29-alpine --port 80 >/dev/null || fail "files not created"
	dk_p volume add data /usr/share/nginx/html -s files >/dev/null || fail "files: volume not added"
	dk_p deploy -s files --image nginx:1.29-alpine >/dev/null 2>&1 || fail "files did not deploy"
	FILES_HOST=$(generate_domain "$P" files)
	wait_for 60 newest_active files
	in_svc files sh -c "echo $MARKER >/usr/share/nginx/html/marker.txt"
	check "the marker file is served" wait_http "$FILES_HOST" /marker.txt "$MARKER" 60
	check "files is backed up" dk_p backup run files
fi

check "db is backed up to the bucket" dk_p backup run db
# Last, as a scheduled pass does: the archive knows the backups above.
check "the control plane is backed up to the bucket" dk backup run --control-plane
SETTINGS=$(dk backup status --reveal --json)
PASSWORD=$(jq -r '.password // empty' <<<"$SETTINGS")
FOLDER=$(jq -r '.path // empty' <<<"$SETTINGS")
[[ -n $PASSWORD ]] || stop "dock backup status --reveal shows no repository password"
pass "the repository password is known outside the server (folder $FOLDER)"

# ---- the server is lost -----------------------------------------------------

BIN=${DOCK_BINARY:-}
if [[ -z $BIN ]]; then
	BIN=$TMP/dock
	docker cp "$CP:/usr/local/bin/dock" "$BIN" >/dev/null || stop "can't copy the dock binary off $CP"
fi
MEMORY=$(docker inspect -f '{{.HostConfig.Memory}}' "$CP")
log "removing $CP with its Docker, volumes and state"
docker rm -fv "$CP" >/dev/null || stop "can't remove $CP"
check "nothing answers at the old address" test "$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$DOCK_URL/api/v1/health")" = 000

# ---- a fresh server at the same address -------------------------------------

# As e2e/vm/up.sh starts one.
docker run -d --name "$CP" --hostname "$CP" --privileged --cgroupns=private \
	$([[ $MEMORY == 0 ]] || echo "--memory $MEMORY") --tmpfs /run --tmpfs /run/lock -v /var/lib/docker -v /var/lib/containerd \
	--network "$NET" --ip "$CP_IP" e2e2-vm >/dev/null || stop "the new server did not start"
wait_for 60 on docker info >/dev/null 2>&1 || stop "docker did not start on the new server"
on mkdir -p /root/dock
docker cp "$BIN" "$CP:/root/dock/dock" >/dev/null && docker cp "$E2E_DIR/../deploy" "$CP:/root/dock/" >/dev/null ||
	stop "can't copy dock to the new server"

log "install.sh --restore-from-bucket"
t0=$SECONDS
if docker exec -e DOCK_BINARY=/root/dock/dock -e PUBLIC_IP="$CP_IP" -e LISTEN="$CP_IP:7070" -e NODE_LISTEN="$CP_IP:7443" \
	-e DOCK_RESTORE_S3_ENDPOINT="$S3_URL" -e DOCK_RESTORE_S3_BUCKET="$BUCKET" -e DOCK_RESTORE_S3_PATH="$FOLDER" \
	-e DOCK_RESTORE_S3_ACCESS_KEY_ID=$S3_KEY -e DOCK_RESTORE_S3_SECRET_ACCESS_KEY="$S3_SECRET" \
	-e DOCK_RESTORE_S3_PATH_STYLE=1 -e DOCK_RESTORE_PASSWORD="$PASSWORD" \
	"$CP" /root/dock/deploy/install.sh --restore-from-bucket --yes >"$TMP/install.log" 2>&1 </dev/null; then
	pass "install.sh --restore-from-bucket --yes went through ($((SECONDS - t0))s)"
else
	tail -30 "$TMP/install.log" >&2
	stop "install.sh --restore-from-bucket failed"
fi
check "the install names the next command" grep -q 'dock admin recover' "$TMP/install.log"
check "the fetched archive is named for deleting" grep -q 'holds every secret' "$TMP/install.log"
check "dock runs" wait_for 60 curl -fsS -o /dev/null "$DOCK_URL/api/v1/health"
allow_minio

# ---- recovery mode and the plan ---------------------------------------------

check "the old API token still works" api GET "/projects/$P"
check "no container of the project runs yet" test -z "$(container db)$(container web)"
PLAN=$(on dock admin recover --json) || fail "dock admin recover --json failed: $PLAN"
db_plan() { jq -e --arg p "$P" '.services[] | select(.project == $p and .service == "db") | '"$1" <<<"$PLAN"; }
check "the plan restores db" db_plan '.state == "restore" and .kind != ""'
check "the plan names db's snapshot" db_plan '(.snapshot // "") != ""'
check "the plan reads from the bucket" jq -e --arg b "$BUCKET" '.repository | contains($b)' <<<"$PLAN"
out=$(on dock admin recover 2>&1 </dev/null)
if [[ $? -eq 0 ]] && grep -q 'Nothing changed' <<<"$out"; then
	pass "without --yes and a terminal the plan changes nothing"
else
	fail "dock admin recover (plan only): $out"
fi
check "still no container after the plan" test -z "$(container db)"

# ---- the run ----------------------------------------------------------------

log "dock admin recover --yes"
t0=$SECONDS
on dock admin recover --yes >"$TMP/recover.log" 2>&1 </dev/null
rc=$?
cat "$TMP/recover.log" >&2
if [[ $rc -eq 0 ]]; then pass "dock admin recover --yes exits 0 ($((SECONDS - t0))s)"; else fail "dock admin recover --yes exits $rc"; fi
check "the report says db was restored from its backup" grep -Eq "^restored +[^ ]*/$P/$ENV_NAME/db: backup " "$TMP/recover.log"
check "the report says web started" grep -Eq "^started +[^ ]*/$P/$ENV_NAME/web" "$TMP/recover.log"
check "the run ended recovery mode" grep -q 'Recovery mode ended' "$TMP/recover.log"

check "db runs" wait_for 60 db_runs
check "the marker row is back" wait_for 60 marker_back
check "web answers through the proxy" wait_http "$HOST" / 'Welcome to nginx' 60
if [[ $VOLUME_APP == 1 ]]; then
	check "the report says files was restored" grep -Eq "^restored +[^ ]*/$P/$ENV_NAME/files: backup " "$TMP/recover.log"
	check "the marker file is back" wait_http "$FILES_HOST" /marker.txt "$MARKER" 60
fi

on dock admin recover >"$TMP/after.log" 2>&1 </dev/null
rc=$?
if [[ $rc -eq 5 ]] && grep -q 'not in recovery mode' "$TMP/after.log"; then
	pass "dock admin recover afterwards is a conflict: the mode ended"
else
	fail "dock admin recover after the run exits $rc: $(tail -3 "$TMP/after.log")"
fi
check "a backup of db runs again" dk_p backup run db

dk project archive "$P" --yes >/dev/null 2>&1 || true
if ((${FAILURES:-0})); then
	echo "$FAILURES failed" >&2
	exit 1
fi
echo "all passed" >&2
