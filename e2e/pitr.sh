#!/usr/bin/env bash
# Point-in-time restore (ADR 0093) on real servers, with the slot's S3
# store (e2e/vm/up.sh starts MinIO as $SLOT-minio): the backups move to
# the bucket, a Postgres database archives its WAL there, and restores to
# a time are checked row by row.
#
#   source /tmp/<slot>/env && e2e/pitr.sh
#
# Covers: turning it on (restart, archiver, first base backup), a quiet
# database reaching the archive by archive_timeout alone, a restore to a
# time between two writes, a restore across a replaced container, the
# window and a restore while the database is stopped (its last commit
# included), off and on again (a time before the gap restores, a time in
# it is refused, a time after it restores from the new base backup), an
# in-place restore, the archiver keeping up under pgbench, and what an
# idle database costs (no uploads, next to no CPU). About 6 minutes.
# PITR_LOAD_S sets the seconds of pgbench (default 30), PITR_LOAD_ROWS the
# rows of the bulk insert after it (default 3 million, about 400 MB of WAL).
set -uo pipefail
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"
: "${E2E_S3_ENDPOINT:?no S3 store in this slot: start it with e2e/vm/up.sh (E2E_S3 not 0)}"
RUN=${E2E_RUN:-$(date +%d%H%M%S)}
P=e2e2-pitr-$RUN
STORAGE=e2e-minio
LOAD_S=${PITR_LOAD_S:-30}
LOAD_ROWS=${PITR_LOAD_ROWS:-3000000}
TMP=$(mktemp -d)

dk_p() { dk "$@" -p "$P" -e "$ENV_NAME"; }
newest_active() { [[ $(api GET "$(svc_path "$P" "$1")/releases" | jq -r 'max_by(.number).status') == active ]]; }
env_server() { api GET "/projects/$P/environments" | jq -r --arg e "$ENV_NAME" '.[] | select(.name == $e) | .server'; }
# on_server CMD... runs CMD on the server the project's environment is on.
on_server() { docker exec "$SERVER" "$@"; }
# container S prints service S's running container: by name, since its
# backup jobs and archiver carry the service's label too.
container() {
	on_server docker ps --format '{{.Names}}' | grep -E "_s$(api GET "$(svc_path "$P" "$1")" | jq -r .id)_r[0-9]+\$" | head -1
}
in_svc() { local s=$1; shift; on_server docker exec "$(container "$s")" "$@"; }
# psql_in S SQL runs SQL in S's database; ids S prints the notes' ids on a line.
psql_in() { in_svc "$1" sh -c "psql -U \"\$POSTGRES_USER\" -d \"\$POSTGRES_DB\" -v ON_ERROR_STOP=1 -tAc \"$2\""; }
ids() { psql_in "$1" "select coalesce(string_agg(id::text, ' ' order by id), '') from notes"; }
note() { psql_in db "insert into notes (id) values ($1)" >/dev/null; }
pitr() { api GET "$(svc_path "$P" db)/backups" | jq -c .pitr; }
# out_has PATTERN CMD...: CMD's output (stdout and stderr) contains PATTERN.
out_has() { local re=$1; shift; { "$@" 2>&1 || true; } | grep -qE -- "$re"; }
utc() { date -u +%FT%TZ; }
# a_moment prints a time with a second of quiet on either side, so a
# restore to it falls between the writes around it.
a_moment() { sleep 2; utc; sleep 2; }
# archived_past T: the window's end is after T.
archived_past() { local th; th=$(pitr | jq -r '.through // empty'); [[ -n $th && $th > $1 ]]; }
# flush ends the current WAL segment and waits for the archive to hold it.
flush() {
	local t
	t=$(utc)
	sleep 1
	psql_in db "select pg_switch_wal()" >/dev/null
	wait_for 60 archived_past "$t"
}
# restored AS TIME WANT...: a restore of db to TIME into service AS holds exactly the notes WANT.
restored() {
	local as=$1 at=$2 out got
	shift 2
	out=$(dk_p backup restore db --to-time "$at" --as "$as" 2>&1) || { fail "restore to $at into $as: $(tail -2 <<<"$out")"; return; }
	wait_for 120 newest_active "$as" || { fail "$as didn't start"; return; }
	got=$(ids "$as")
	if [[ $got == "$*" ]]; then pass "restored to $at: notes $got"; else fail "restored to $at: notes '$got', want '$*'"; fi
}
# archiver_cpu_usec prints the CPU time the archiver's container has used.
archiver_cpu_usec() {
	on_server sh -c "cat /sys/fs/cgroup/system.slice/docker-\$(docker inspect -f '{{.Id}}' $WAL).scope/cpu.stat" | awk '$1 == "usage_usec" {print $2}'
}
# wal_objects counts the WAL segments in the bucket (MinIO keeps an object as a directory).
wal_objects() { docker exec "$SLOT-minio" sh -c "ls -R /data/$E2E_S3_BUCKET" | grep -cE '^[0-9A-F]{24}@'; }

cleanup() {
	rm -rf "$TMP"
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

log "a Postgres database"
ensure_project "$P"
SERVER=$(env_server)
dk_p service create db --template postgres >/dev/null || { fail "db not created"; exit 1; }
wait_for 180 newest_active db || { fail "db didn't start"; exit 1; }
wait_for 30 psql_in db "create table notes (id int primary key, at timestamptz not null default clock_timestamp())" >/dev/null
WAL=$(on_server docker ps -a --format '{{.Names}}' | grep -c '_wal$')
check "no archiver before it is on" test "$WAL" = 0

log "point-in-time restore on"
check "dock backup pitr on" dk_p backup pitr on db --days 7
check "db restarted with archive_mode on" wait_for 120 eval '[[ $(psql_in db "show archive_mode" 2>/dev/null) == on ]]'
check "the first base backup opens the window" wait_for 180 eval '[[ -n $(pitr | jq -r ".from // empty") ]]'
WAL=dock_$(on_server docker ps --format '{{.Names}}' | sed -n 's/^dock_\(.*\)_wal$/\1/p' | head -1)_wal
check "the archiver runs and is healthy" eval '[[ $(pitr | jq -r .archiver.healthy) == true ]]'
check "the window lists what uses the database (nothing)" eval '[[ $(pitr | jq -c .used_by) == "[]" ]]'

log "a quiet database reaches the archive on its own"
note 1
T1=$(a_moment)
note 2
check "a write is archived within archive_timeout, no forced switch" wait_for 100 archived_past "$T1"
restored db-t1 "$T1" 1

log "across a replaced container"
note 3
dk_p restart -s db >/dev/null 2>&1 || fail "dock restart db"
wait_for 120 newest_active db
wait_for 60 psql_in db "select 1" >/dev/null
note 4
T2=$(a_moment)
note 5
check "the archive goes on after the restart" flush
restored db-t2 "$T2" 1 2 3 4

log "a stopped database"
note 6
check "dock service stop db" dk_p service stop db --yes
sleep 2
STOPPED=$(utc)
check "the window shows while it is stopped" wait_for 30 eval '[[ -n $(pitr | jq -r ".through // empty") ]]'
END=$(pitr | jq -r .through)
check "a time after it stopped is refused with the archive's end" out_has "holds WAL only up to" dk_p backup restore db --to-time "$STOPPED" --as db-late
restored db-t3 "$END" 1 2 3 4 5 6
check "dock service start db" dk_p service start db
wait_for 60 psql_in db "select 1" >/dev/null

log "off and on again"
note 7
T3=$(a_moment)
# No flush: Postgres archives its last segment as it stops, and the
# archiver is there to take it until the restart is live.
check "dock backup pitr off" dk_p backup pitr off db
check "db restarted with archive_mode off" wait_for 120 eval '[[ $(psql_in db "show archive_mode" 2>/dev/null) == off ]]'
check "the archiver is gone" wait_for 30 eval '[[ -z $(on_server docker ps -aq --filter name=^$WAL$) ]]'
note 8
TGAP=$(a_moment)
check "the window still shows while it is off" eval '[[ $(pitr | jq -r ".through // empty") > $T3 ]]'
restored db-t4 "$T3" 1 2 3 4 5 6 7
BASES=$(pitr | jq .base_backups)
check "dock backup pitr on again" dk_p backup pitr on db
check "a new base backup" wait_for 180 eval '[[ $(pitr | jq .base_backups) -gt $BASES ]]'
wait_for 60 psql_in db "select 1" >/dev/null
note 9
T4=$(a_moment)
note 10
check "archiving again" flush
check "a time while it was off is refused, naming the gap" out_has "wasn't archived from .* to " dk_p backup restore db --to-time "$TGAP" --as db-gap
restored db-t5 "$T4" 1 2 3 4 5 6 7 8 9

log "in place"
out=$(dk_p backup restore db --to-time "$T4" --in-place --yes 2>&1) || fail "in-place restore: $(tail -2 <<<"$out")"
wait_for 120 newest_active db
wait_for 60 psql_in db "select 1" >/dev/null
check "db itself is back at $T4" eval '[[ $(ids db) == "1 2 3 4 5 6 7 8 9" ]]'
check "a new window opens after the in-place restore" wait_for 180 eval '[[ $(pitr | jq -r .archiver.healthy) == true && -n $(pitr | jq -r ".through // empty") ]]'

log "idle cost: 90 s without writes"
# Before the load, whose vacuuming writes for minutes. What the restore
# and the base backup wrote goes first: a checkpoint and its segment.
psql_in db "checkpoint" >/dev/null
sleep 65
o0=$(wal_objects) c0=$(archiver_cpu_usec)
sleep 90
o1=$(wal_objects) c1=$(archiver_cpu_usec)
mem=$(on_server docker stats --no-stream --format '{{.MemUsage}}' "$WAL")
log "idle: $((o1 - o0)) uploads, archiver CPU $(((c1 - c0) / 1000)) ms in 90 s, memory $mem"
check "an idle database uploads nothing" test "$o1" = "$o0"
check "the idle archiver uses under 0.5% of a core" test $((c1 - c0)) -lt 450000

log "write load: pgbench for ${LOAD_S}s, then ${LOAD_ROWS} rows at once"
out=$(in_svc db sh -c 'pgbench -U "$POSTGRES_USER" -i -s 10 -q "$POSTGRES_DB"' 2>&1) || fail "pgbench -i: $(tail -2 <<<"$out")"
o0=$(wal_objects)
in_svc db sh -c "pgbench -U \"\$POSTGRES_USER\" -c 8 -T $LOAD_S \"\$POSTGRES_DB\"" >"$TMP/pgbench.out" 2>&1 &
# worst is the most segments that waited at once, sampled every 5 s.
worst=0
sample() { local w; w=$(pitr | jq '.archiver.waiting // 0'); ((w > worst)) && worst=$w; return 0; }
for ((i = 0; i < LOAD_S; i += 5)); do sleep 5; sample; done
wait
log "pgbench: $(grep -E "^tps" "$TMP/pgbench.out" || tail -2 "$TMP/pgbench.out"); most segments waiting at once: $worst"
# One statement that writes WAL as fast as the disk takes it.
psql_in db "create table big as select g, md5(g::text) || md5((g + 1)::text) as v from generate_series(1, $LOAD_ROWS) g" >/dev/null &
while kill -0 $! 2>/dev/null; do sleep 2; sample; done
sample
t0=$SECONDS
check "the archive catches up within 60 s of the load" wait_for 60 eval '[[ $(pitr | jq .archiver.waiting) == 0 ]]'
log "caught up after $((SECONDS - t0))s; most segments waiting at once: $worst; $(($(wal_objects) - o0)) segments archived"
check "the archiver stayed healthy" eval '[[ $(pitr | jq -r .archiver.healthy) == true ]]'
T5=$(utc)
check "the load's end is archived" flush
want=$(psql_in db "select (select count(*) from pgbench_history) + (select count(*) from big)")
out=$(dk_p backup restore db --to-time "$T5" --as db-load 2>&1) || fail "restore after the load: $(tail -2 <<<"$out")"
wait_for 120 newest_active db-load
check "the restore after the load holds all $want rows" eval '[[ $(psql_in db-load "select (select count(*) from pgbench_history) + (select count(*) from big)") == "$want" ]]'

((${FAILURES:-0} == 0)) && log "pitr: all passed" || log "pitr: $FAILURES failed"
exit $((${FAILURES:-0} > 0))
