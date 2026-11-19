#!/usr/bin/env bash
# Restoring volume backups (ADR 0095) and checked backups (ADR 0096) on
# real servers, with the backups in the slot's S3 store (e2e/vm/up.sh
# starts MinIO as $SLOT-minio):
#
#   source /tmp/<slot>/env && e2e/checked.sh
#
# Covers: an app template that keeps its data in a volume (memos) backed
# up and restored in place (the data is back, a file written since is
# gone, a safety snapshot holds what was replaced, the app serves) and
# into a copy (the copy holds the backup's data and serves, the source
# keeps its own); a second volume added since the backup is left alone,
# and a backup of both restores both; a copy that can't start on the
# backup's data is archived as <name>-failed-<restore id> with an alert
# on the source, its name is free, and the next good restore resolves the
# alert; the repository check passes, fails with an alert when a pack is
# missing from the bucket and resolves it once the pack is back; a
# Postgres database's restore drill counts its tables and leaves no
# service or volume behind; the weekly checks' switch. About 2 minutes.
set -uo pipefail
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"
: "${E2E_S3_ENDPOINT:?no S3 store in this slot: start it with e2e/vm/up.sh (E2E_S3 not 0)}"
RUN=${E2E_RUN:-$(date +%d%H%M%S)}
P=e2e2-checked-$RUN
STORAGE=e2e-minio
S=memos
DIR=/var/opt/memos # the memos template's volume
PACKS=/data/$E2E_S3_BUCKET/dock-backups/data
KEEP_PACK=/tmp/e2e-restore-pack-$RUN # in the MinIO container

dk_p() { dk "$@" -p "$P" -e "$ENV_NAME"; }
newest_active() { [[ $(api GET "$(svc_path "$P" "$1")/releases" | jq -r 'max_by(.number).status') == active ]]; }
env_server() { api GET "/projects/$P/environments" | jq -r --arg e "$ENV_NAME" '.[] | select(.name == $e) | .server'; }
# on_server CMD... runs CMD on the server the project's environment is on.
on_server() { docker exec "$SERVER" "$@"; }
backups() { api GET "$(svc_path "$P" "$1")/backups"; }
# newest_backup S prints the snapshot of S's newest backup that succeeded.
newest_backup() { backups "$1" | jq -r '[.runs[] | select(.operation == "backup" and .status == "succeeded")][0].snapshot'; }
# newest_restore S prints S's newest restore as JSON.
newest_restore() { backups "$1" | jq -c '[.runs[] | select(.operation == "restore")][0]'; }
# write S CMD runs CMD in a container of S with its volumes writable. The
# flags go before the --: what follows it is the command.
write() { dk run "$1" -p "$P" -e "$ENV_NAME" --volumes write --no-snapshot -- "$2" >/dev/null 2>&1 || fail "dock run $1 -- $2"; }
# reads S FILE prints FILE from S's volumes.
reads() { dk run "$1" -p "$P" -e "$ENV_NAME" -- "cat $2" 2>/dev/null; }
# holds S FILE WANT: S's FILE reads WANT.
holds() { [[ $(reads "$1" "$2") == "$3" ]]; }
# restore ARGS... restores a backup of $S and waits; its output is in $out.
restore() { out=$(dk_p backup restore "$S" "$@" 2>&1); }
# open_alert TITLE prints the open alert of that title, the platform's with a second argument.
open_alert() { api GET "/alerts${2:+?platform=true}" | jq -ce --arg t "$1" '[.[] | select(.title == $t)][0] // empty'; }
no_alert() { ! open_alert "$@" >/dev/null; }
serves() { [[ $(http_code "$1" /) =~ ^[23] ]]; }
# volumes counts the project's Docker volumes on its server.
volumes() { on_server docker volume ls --format '{{.Name}}' | grep -c "^dock_p${PID}_"; }
psql_in() {
	local ctr
	ctr=$(on_server docker ps --format '{{.Names}}' | grep -E "_s$(api GET "$(svc_path "$P" "$1")" | jq -r .id)_r[0-9]+\$" | head -1)
	on_server docker exec "$ctr" sh -c "psql -U \"\$POSTGRES_USER\" -d \"\$POSTGRES_DB\" -v ON_ERROR_STOP=1 -tAc \"$2\""
}
# pack_back puts the pack the check scenario took out of the bucket back.
pack_back() {
	docker exec "$SLOT-minio" sh -c "[ -d $KEEP_PACK ] && mv $KEEP_PACK/* $PACKS/\$(cat $KEEP_PACK.dir)/ && rmdir $KEEP_PACK && rm -f $KEEP_PACK.dir" 2>/dev/null
}

cleanup() {
	pack_back
	[[ ${KEEP:-} == 1 ]] && return
	dk project archive "$P" --yes >/dev/null 2>&1
}
trap cleanup EXIT

log "backups on the slot's S3 store"
api GET /storage | jq -e --arg n "$STORAGE" 'any(.[]; .name == $n)' >/dev/null ||
	printf %s "$E2E_S3_SECRET" | dk storage add "$STORAGE" --endpoint "$E2E_S3_ENDPOINT" --bucket "$E2E_S3_BUCKET" \
		--access-key-id "$E2E_S3_KEY_ID" --secret-access-key-stdin >/dev/null || { fail "storage connection not added"; exit 1; }
on_storage() { [[ $(api GET /backup | jq -r .repository) == "s3:$E2E_S3_ENDPOINT/$E2E_S3_BUCKET/"* && $(api GET /backup | jq -r '.move.running // false') == false ]]; }
on_storage || dk backup move --storage "$STORAGE" --yes >/dev/null
check "the backups are in the bucket" wait_for 180 on_storage
REPO=$(api GET /backup | jq -r .repository)

log "an app with its data in a volume, and a database"
ensure_project "$P"
PID=$(api GET "/projects/$P" | jq -r .id)
SERVER=$(env_server)
use_proxy_of "$P"
out=$(dk_p template use memos --json 2>&1) || { fail "template use memos: $(tail -2 <<<"$out")"; exit 1; }
dk_p service create db --template postgres >/dev/null || { fail "db not created"; exit 1; }
HOST=$(api GET "$(svc_path "$P" "$S")/domains" | jq -r '.[0].hostname')
check "memos serves" wait_for 60 serves "$HOST"

log "a volume backup, restored in place"
write "$S" "echo one > $DIR/marker.txt"
check "dock backup run" dk_p backup run "$S"
GOOD=$(newest_backup "$S")
check "the backup is in the bucket" eval '[[ $(backups "$S" | jq -r ".runs[0].repository") == "$REPO" ]]'
write "$S" "echo two > $DIR/marker.txt; echo junk > $DIR/junk.txt"
check "without --in-place or --as the answer names both" eval '! restore --snapshot "${GOOD:0:8}" && grep -q -- "--in-place" <<<"$out" && grep -q -- "--as NAME" <<<"$out"'
restore --snapshot "${GOOD:0:8}" --in-place --yes || fail "restore in place: $(tail -2 <<<"$out")"
check "the backup's data is back" holds "$S" "$DIR/marker.txt" one
check "a file written since is gone" eval '! reads "$S" "$DIR/junk.txt" | grep -q junk'
check "memos serves on the restored data" wait_for 60 serves "$HOST"
check "a safety snapshot holds what was replaced" eval \
	'dk_p volume snapshot list -s "$S" --json | jq -e --arg n "before restoring backup ${GOOD:0:8}" "any(.snapshots[]; .trigger ==\"before_restore\" and .status == \"ready\" and .note == \$n)"'
check "the restore is a row of the service's backups, in place" eval '[[ $(newest_restore "$S" | jq -r "[.status, .restored_to] | join(\" \")") == "succeeded $S" ]]'

log "into a copy"
write "$S" "echo three > $DIR/marker.txt"
restore --snapshot "${GOOD:0:8}" --as "$S-old" || fail "restore into a copy: $(tail -2 <<<"$out")"
check "the copy holds the backup's data" holds "$S-old" "$DIR/marker.txt" one
check "the source keeps its own" holds "$S" "$DIR/marker.txt" three
check "the copy has no domain of its own" eval '[[ $(api GET "$(svc_path "$P" "$S-old")/domains" | jq length) == 0 ]]'
COPY_HOST=$(generate_domain "$P" "$S-old")
check "the copy serves" wait_for 60 serves "$COPY_HOST"
check "the source still serves" serves "$HOST"

log "two volumes"
out=$(dk_p volume add files /files -s "$S" --deploy 2>&1) || fail "volume add: $(tail -2 <<<"$out")"
write "$S" "echo later > /files/f.txt"
restore --snapshot "${GOOD:0:8}" --in-place --yes || fail "restore in place: $(tail -2 <<<"$out")"
check "a volume added since the backup is left as it is" holds "$S" /files/f.txt later
check "and the restore's log says so" eval 'dk_p backup log "$(newest_restore "$S" | jq -r .id)" -s "$S" | grep -q "volume files isn.t in the backup"'
check "the volume the backup holds is restored" holds "$S" "$DIR/marker.txt" one
check "dock backup run, both volumes" dk_p backup run "$S"
BOTH=$(newest_backup "$S")
write "$S" "rm -rf /files/* $DIR/marker.txt"
restore --snapshot "${BOTH:0:8}" --in-place --yes || fail "restore in place: $(tail -2 <<<"$out")"
check "both volumes are back" eval 'holds "$S" "$DIR/marker.txt" one && holds "$S" /files/f.txt later'
check "memos serves with both" wait_for 60 serves "$HOST"

log "a copy that can't start on the backup's data"
# A backup memos can't open; the source goes back to good data at once.
write "$S" "cd $DIR && rm -f memos_prod.db-wal memos_prod.db-shm && echo 'not a database' > memos_prod.db"
check "dock backup run, of data memos can't start on" dk_p backup run "$S"
BAD=$(newest_backup "$S")
restore --snapshot "${BOTH:0:8}" --in-place --yes || fail "restore in place: $(tail -2 <<<"$out")"
check "memos serves again" wait_for 60 serves "$HOST"
check "restoring it into a copy fails" eval '! restore --snapshot "${BAD:0:8}" --as "$S-bad"'
FAILED=$S-bad-failed-$(newest_restore "$S" | jq -r .id)
check "the copy is archived as $FAILED" eval \
	'[[ $(newest_restore "$S" | jq -r "[.status, .restored_to] | join(\" \")") == "failed $FAILED" && $(api_status GET "$(svc_path "$P" "$S-bad")") == 404 ]]'
check "the error's hint names the archived copy" eval 'grep -q "dock releases -s $FAILED" <<<"$out" && dk_p releases -s "$FAILED" | grep -q failed'
check "nothing of the failed copy runs" eval \
	'! on_server docker ps --format "{{.Names}}" | grep -q "_s$(dk_p service list --all --json | jq -r --arg n "$FAILED" ".[] | select(.name == \$n) | .id")_"'
TITLE="Restore of $P/$ENV_NAME/$S failed"
check "an alert on the source says what happened and where the copy went" eval \
	'open_alert "$TITLE" | jq -e --arg f "$FAILED" --arg s "$S" ".service == \$s and (.message | contains(\"archived as \" + \$f)) and (.command | startswith(\"dock backup log \"))"'
check "the source is untouched" eval 'serves "$HOST" && holds "$S" "$DIR/marker.txt" one'
restore --snapshot "${BOTH:0:8}" --as "$S-bad" || fail "a good restore under the freed name: $(tail -2 <<<"$out")"
check "the name is free: a good restore into $S-bad" holds "$S-bad" "$DIR/marker.txt" one
check "which resolves the alert" no_alert "$TITLE"

log "the repository check"
check "dock backup check passes" dk backup check
check "the service's backups carry it" eval '[[ $(backups "$S" | jq -r ".check | [.status, .repository] | join(\" \")") == "succeeded $REPO" ]]'
# MinIO keeps an object as a directory: one pack out of the bucket.
docker exec "$SLOT-minio" sh -c "p=\$(ls -d $PACKS/*/* | head -1) && mkdir $KEEP_PACK && basename \$(dirname \$p) > $KEEP_PACK.dir && mv \$p $KEEP_PACK/" || fail "no pack taken out of the bucket"
check "with a pack missing the check fails" eval '! out=$(dk backup check 2>&1) && grep -q "repository check failed" <<<"$out"'
CHECK_TITLE="Backups in $REPO failed their check"
check "and raises the platform's alert" eval 'open_alert "$CHECK_TITLE" platform | jq -e ".command == \"dock backup check\""'
check "the service's backups say so" eval '[[ $(backups "$S" | jq -r .check.status) == failed ]]'
pack_back
check "with the pack back the check passes" dk backup check
check "which resolves the alert" no_alert "$CHECK_TITLE" platform

log "a restore drill"
wait_for 180 newest_active db || fail "db didn't start"
wait_for 30 psql_in db "create table notes (id int primary key); create table tags (id int primary key); insert into notes select generate_series(1, 500)" >/dev/null || fail "no tables in db"
check "dock backup run db" dk_p backup run db
before=$(volumes)
check "dock backup drill" dk_p backup drill db
check "the drill ends" wait_for 180 eval '[[ $(backups db | jq -r ".drill.status // \"none\"") != @(running|none) ]]'
check "it restored the newest snapshot and counted its tables" eval \
	'backups db | jq -e --arg s "$(newest_backup db)" ".drill.status == \"succeeded\" and .drill.snapshot == \$s and (.drill.detail | startswith(\"2 tables in \"))"'
check "its service is gone" eval '[[ $(api_status GET "$(svc_path "$P" db-drill)") == 404 ]] && ! dk_p service list --all --json | jq -e "any(.[]; .name | startswith(\"db-drill\"))"'
check "its volume is gone" eval '[[ $(volumes) == "$before" ]]'
check "its restore is not left in db's backups" eval 'backups db | jq -e "all(.runs[]; .operation != \"restore\")"'
check "db still holds its rows" eval '[[ $(psql_in db "select count(*) from notes") == 500 ]]'

log "the weekly checks' switch"
check "dock backup check --off" dk backup check --off
check "nothing is planned while off" eval 'api GET /backup | jq -e ".verify == false and .next_check_at == null" && backups db | jq -e ".next_check_at == null"'
check "dock backup check --on" dk backup check --on
check "the next check is planned again" eval 'api GET /backup | jq -e ".verify and .next_check_at != null" && backups db | jq -e ".next_check_at != null"'

((${FAILURES:-0} == 0)) && log "checked: all passed" || log "checked: $FAILURES failed"
exit $((${FAILURES:-0} > 0))
