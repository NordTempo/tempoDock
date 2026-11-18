#!/usr/bin/env bash
# Restoring a volume backup (ADR 0095) on real servers: a service that
# isn't a template database, with a file in its volume.
#
#   source /tmp/<slot>/env && e2e/restore.sh
#
# Covers: a backup of the volume, an in-place restore that brings a
# changed file back and removes one written since, the safety snapshot it
# took (named on the restore's run, and restoring it undoes the restore),
# and a restore into a copy (--as) that leaves the service as it is. It
# uses whatever repository the slot's backups go to. About 2 minutes.
set -uo pipefail
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"
RUN=${E2E_RUN:-$(date +%d%H%M%S)}
P=e2e2-restore-$RUN
DIR=/usr/share/nginx/html

dk_p() { dk "$@" -p "$P" -e "$ENV_NAME"; }
newest_active() { [[ $(api GET "$(svc_path "$P" "$1")/releases" | jq -r 'max_by(.number).status') == active ]]; }
env_server() { api GET "/projects/$P/environments" | jq -r --arg e "$ENV_NAME" '.[] | select(.name == $e) | .server'; }
# container S prints service S's running container: by name, since its
# backup and restore jobs carry the service's label too.
container() {
	docker exec "$SERVER" docker ps --format '{{.Names}}' | grep -E "_s$(api GET "$(svc_path "$P" "$1")" | jq -r .id)_r[0-9]+\$" | head -1
}
in_svc() { local s=$1; shift; docker exec "$SERVER" docker exec "$(container "$s")" "$@"; }
# holds S FILE WANT: FILE in S's volume reads WANT ("" for no such file).
holds() { [[ $(in_svc "$1" sh -c "cat $DIR/$2 2>/dev/null") == "$3" ]]; }
runs() { api GET "$(svc_path "$P" files)/backups"; }
# out_has PATTERN CMD...: CMD's output (stdout and stderr) contains PATTERN.
out_has() { local re=$1; shift; { "$@" 2>&1 || true; } | grep -qE -- "$re"; }

cleanup() {
	[[ ${KEEP:-} == 1 ]] && return
	dk project archive "$P" --yes >/dev/null 2>&1
}
trap cleanup EXIT

log "a service with a file in its volume"
ensure_project "$P"
SERVER=$(env_server)
ensure_service "$P" files '{"image":"nginx:1.27-alpine","port":80}'
api POST "$(svc_path "$P" files)/volumes" '{"name":"files","mount_path":"'$DIR'"}' >/dev/null || { fail "volume not added"; exit 1; }
n=$(api POST "$(svc_path "$P" files)/releases" '{}' | jq -r .number)
[[ $(wait_release "$P" files "$n" 300) == active ]] || { fail "files didn't start"; exit 1; }
in_svc files sh -c "echo one >$DIR/note.txt"
check "the file is written" holds files note.txt one

log "a backup of the volume"
out=$(dk_p backup run files 2>&1) || fail "dock backup run: $(tail -3 <<<"$out")"
SNAP=$(runs | jq -r '[.runs[] | select(.operation == "backup" and .status == "succeeded")][0].snapshot // empty')
[[ -n $SNAP ]] || { fail "no succeeded backup: $(runs | jq -c '.runs')"; exit 1; }
pass "backup ${SNAP:0:8}"
check "the list names the volume a restore replaces" eval '[[ $(runs | jq -c .volumes) == "[\"files\"]" ]]'

log "restore in place"
in_svc files sh -c "echo two >$DIR/note.txt && echo new >$DIR/later.txt"
check "the file changed after the backup" holds files note.txt two
check "a restore names how it goes (neither --in-place nor --as is refused)" out_has "in-place" dk_p backup restore files --snapshot "${SNAP:0:8}"
out=$(dk_p backup restore files --snapshot "${SNAP:0:8}" --in-place --yes 2>&1) || fail "restore in place: $(tail -3 <<<"$out")"
check "files runs again" wait_for 120 newest_active files
check "the file is back as backed up" wait_for 60 holds files note.txt one
check "a file written since the backup is gone" holds files later.txt ""
RESTORE=$(runs | jq -c '[.runs[] | select(.operation == "restore")][0]')
check "the restore succeeded, in place" eval '[[ $(jq -r "[.status, .restored_to] | join(\" \")" <<<"$RESTORE") == "succeeded files" ]]'
SAFETY=$(jq -r '.safety_snapshot // empty' <<<"$RESTORE")
check "the restore names its safety snapshot ($SAFETY)" eval '[[ $SAFETY =~ ^[0-9]+$ ]]'
check "that snapshot was taken before the restore and is ready" eval \
	'[[ $(api GET "$(svc_path "$P" files)/snapshots" | jq -r --argjson id "${SAFETY:-0}" ".snapshots[] | select(.id == \$id) | [.trigger, .status] | join(\" \")") == "before_restore ready" ]]'

log "the safety snapshot undoes it"
if [[ $SAFETY =~ ^[0-9]+$ ]]; then
	check "restore the safety snapshot in place" dk_p volume snapshot restore "$SAFETY" -s files --in-place --yes
	check "files runs again" wait_for 120 newest_active files
	check "the file is as it was before the restore" wait_for 60 holds files note.txt two
	check "the later file is back" holds files later.txt new
else
	fail "no safety snapshot to restore"
fi

log "restore into a copy"
out=$(dk_p backup restore files --snapshot "${SNAP:0:8}" --as files-copy 2>&1) || fail "restore into a copy: $(tail -3 <<<"$out")"
check "files-copy runs" wait_for 180 newest_active files-copy
check "the copy holds the backup's file" wait_for 60 holds files-copy note.txt one
check "the copy has no later file" holds files-copy later.txt ""
check "files itself is untouched" holds files note.txt two
check "the copy has no domains" eval '[[ $(api GET "$(svc_path "$P" files-copy)/domains" | jq length) == 0 ]]'
check "the restore into the copy is a run of files" eval \
	'[[ $(runs | jq -r "[.runs[] | select(.operation == \"restore\")][0] | [.status, .restored_to] | join(\" \")") == "succeeded files-copy" ]]'
check "no restore alert is open" eval \
	'[[ $(api GET /alerts | jq --arg p "$P" "[(if type == \"array\" then . else .alerts end)[]? | select(.project == \$p and .resolved_at == null and (.title | startswith(\"Restore of\")))] | length") == 0 ]]'

((${FAILURES:-0} == 0)) && log "restore: all passed" || log "restore: $FAILURES failed"
exit $((${FAILURES:-0} > 0))
