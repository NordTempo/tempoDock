package backup

import (
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Point-in-time restore for Postgres (ADR 0093): base backups stream the
// data directory into restic (`dock __wal base`), and a restore unpacks
// one and replays the WAL archive to a time inside the job.

// BaseFile is the data directory's tar inside a base backup's snapshot.
const BaseFile = "/pgdata.tar"

// BaseDir is where a base backup job mounts the database's volume,
// read-only.
const BaseDir = "/pgdata"

// BaseTag is the restic tag of a database's base backups: apart from its
// dumps' (Tag), so each has its own retention.
func BaseTag(serviceID int64) string { return Tag(serviceID) + "-base" }

// ForgetFailed is what a job prints when removing the base backups Dock
// picked (DOCK_FORGET) failed: they are kept and stay listed.
const ForgetFailed = "dock: removing old base backups failed; they are kept"

// forget removes the snapshots Dock chose (DOCK_FORGET: IDs separated by
// spaces), unless there are none. It runs after base backups, and after
// a dump while a database with point-in-time restore off still has base
// backups ageing out. Like retention, failing doesn't fail the backup.
const forget = `if [ -n "${DOCK_FORGET:-}" ]; then
  if ! $R forget --quiet --retry-lock ` + RetryLock + ` --prune $DOCK_FORGET >/dev/null; then
    echo "` + ForgetFailed + `" >&2
  fi
fi
`

// BaseScript is a base backup's job script: it runs in the template's
// image with the database's volume at BaseDir and the dock binary mounted, then
// removes the base backups Dock picked and lists what is left of the tag
// (ParseBackup), like a dump's.
func BaseScript() string {
	return prelude + initRepo + `$R backup ` + backupFlags + ` --stdin-filename ` + strings.TrimPrefix(BaseFile, "/") +
		` --stdin-from-command -- ` + node.DockPath + ` __wal base ` + BaseDir + `
` + forget + `echo ` + SnapshotsMarker + `
$R snapshots --json --retry-lock ` + RetryLock + ` --tag "$DOCK_TAG" || true
`
}

// BaseEnv is a base backup job's environment: the repository's, how to
// reach the database (pg_backup_start runs over SQL), its tag and the
// snapshots to forget.
func BaseEnv(src Source, repo Repo, forgetIDs []string) map[string]string {
	env := repo.Env()
	env["DOCK_TAG"] = BaseTag(src.ServiceID)
	env["DOCK_FORGET"] = strings.Join(forgetIDs, " ")
	maps.Copy(env, dbEnv(src))
	return env
}

// RecoveryEnded is what Postgres says when the archive ends before a
// commit after the target time: no transaction committed between the
// two, so replaying to the end is the database at that time (Dock
// checked the archive holds every commit up to it).
const RecoveryEnded = "recovery ended before configured recovery target was reached"

// PITRRestoreScript is the job script of a restore to a time, in the
// template's image with the target volume at its data directory ($PGDATA
// of the image) and the dock binary mounted: empty the volume, unpack the base
// backup, check and list the archive, then run Postgres in the job
// itself with restore_command and the target until it is promoted, and
// shut it down cleanly. The database's own container never sees the
// archive's keys.
const PITRRestoreScript = prelude + `D="$PGDATA"
find "$D" -mindepth 1 -delete
$R dump --retry-lock ` + RetryLock + ` "$DOCK_SNAPSHOT" ` + BaseFile + ` | ` + node.DockPath + ` __wal unpack "$D"
` + node.DockPath + ` __wal index "$DOCK_WAL_START" "$DOCK_WAL_SEGMENT_BYTES" "$DOCK_TARGET_MS" "$DOCK_WAL_INDEX"
mkdir -p "$D/pg_wal/archive_status"
touch "$D/recovery.signal"
chown -R postgres:postgres "$D"
chmod 700 "$D"
LOG=/tmp/dock-recovery.log
recover() {
  docker-entrypoint.sh postgres -c listen_addresses= -c archive_mode=off \
    -c "restore_command=` + node.DockPath + ` __wal fetch %f %p" -c recovery_target_action=promote "$@" >>"$LOG" 2>&1 &
  pid=$!
  until [ "$(psql -X -h /var/run/postgresql -U "$DOCK_PG_USER" -d postgres -Atc 'SELECT pg_is_in_recovery()' 2>/dev/null)" = f ]; do
    if ! kill -0 "$pid" 2>/dev/null; then
      wait "$pid" || true
      return 1
    fi
    sleep 1
  done
  kill -INT "$pid"
  wait "$pid"
}
if ! recover -c "recovery_target_time=$DOCK_TARGET"; then
  if grep -q "` + RecoveryEnded + `" "$LOG"; then
    echo "dock: no transaction committed between the time and the end of the archive; replaying it to the end, which is the same state" >&2
    if ! recover; then
      tail -n 20 "$LOG" >&2
      exit 1
    fi
  else
    tail -n 20 "$LOG" >&2
    exit 1
  fi
fi
grep -E "recovery stopping|consistent recovery state|last completed transaction|selected new timeline" "$LOG" >&2 || true
`

// PITRRestoreEnv is a restore to a time's job environment: the
// repository's, the archive's (walEnv: walarchive.Config.Env), the base
// backup and where its WAL starts, the time, and the superuser that the
// job's checks connect as.
func PITRRestoreEnv(repo Repo, walEnv map[string]string, snapshot, start string, segBytes int64, target time.Time, user string) map[string]string {
	env := repo.Env()
	maps.Copy(env, walEnv)
	env["DOCK_SNAPSHOT"] = snapshot
	env["DOCK_WAL_START"] = start
	env["DOCK_WAL_SEGMENT_BYTES"] = strconv.FormatInt(segBytes, 10)
	env["DOCK_TARGET"] = target.UTC().Format("2006-01-02 15:04:05.000000+00")
	env["DOCK_TARGET_MS"] = strconv.FormatInt(target.UnixMilli(), 10)
	env["DOCK_PG_USER"] = user
	env["DOCK_WAL_INDEX"] = "/tmp/dock-wal.index"
	return env
}
