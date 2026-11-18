// Package backup builds the restic jobs that back up and restore
// stateful services (ADR 0019), and computes their schedule. It is pure:
// core runs the scripts as node jobs and records the results.
//
// Every job runs `/bin/sh -c <script>` with its parameters in environment
// variables, never spliced into the script, so names and passwords need no
// quoting. restic is mounted read-only at BinDir from a per-server volume.
package backup

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

const (
	// BinDir is where jobs find the restic binary (read-only).
	BinDir = "/dock-restic"
	// LocalVolumePrefix starts the name of the volume holding a server's
	// local repository (not off-box); core adds the install.
	LocalVolumePrefix = "dock_backups_"
	// LocalPath is where jobs mount the local repository.
	LocalPath = "/repo"
	// VolumesPath is where volume backups mount a service's volumes.
	VolumesPath = "/volumes"
	// Host is the restic host name of every snapshot, so retention groups
	// by tag alone whichever server took it.
	Host = "dock"
	// RetryLock is how long restic waits for another job's lock (prune
	// takes an exclusive one).
	RetryLock = "10m"
)

// Snapshot file names inside database snapshots.
const (
	PostgresFile = "/postgres.dump"
	RedisFile    = "/redis.rdb"
	MySQLFile    = "/mysql.sql"
)

// A Postgres snapshot's one file holds the server's roles ahead of the
// dump, since restic stores one stream per snapshot and a dump that names
// a role (a policy TO tenant) only restores where the role exists:
//
//	DOCKPG1 <length of the roles SQL, 12 digits>\n   (PostgresHeaderLen bytes)
//	<pg_dumpall --roles-only --no-role-passwords>
//	<pg_dump -Fc>
//
// A stream without the header is a snapshot from before roles were saved:
// pg_dump's output alone, which starts with PGDMP.
const (
	// PostgresMagic starts the header of a Postgres snapshot's stream.
	PostgresMagic = "DOCKPG1"
	// PostgresHeaderLen is the header's length in bytes, its newline
	// included: `tail -c +22` of the stream is the roles and the dump.
	PostgresHeaderLen = len(PostgresMagic) + 1 + len(pgLenZeros) + 1
	// pgLenZeros is as many zeros as the header's length has digits:
	// 1<digits> - 1<zeros> reads the length as decimal in a shell,
	// whatever its leading zeros.
	pgLenZeros = "000000000000"
	// pgLenPattern matches the header's digits in a shell case.
	pgLenPattern = "[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]"
	// pgRolesFile holds the roles SQL in a backup or restore job; small,
	// unlike the dump, which only ever streams.
	pgRolesFile = "/tmp/dock-roles.sql"
	// pgHeadFile holds the first PostgresHeaderLen bytes of the stream in
	// a restore job.
	pgHeadFile = "/tmp/dock-pg-head"
)

// pgRoles dumps the server's roles, without passwords, into pgRolesFile
// before a backup, so the header can say how long they are. A failure
// fails the job (set -e) before anything is saved.
const pgRoles = `pg_dumpall --roles-only --no-role-passwords -l "$PGDATABASE" > ` + pgRolesFile + `
`

// pgStream is the command restic backs up the output of: the header, the
// roles, then pg_dump, which streams. It runs under sh -c in single
// quotes, so it holds none; each step failing fails it, and the backup.
const pgStream = `printf "` + PostgresMagic + ` %012d\n" "$(wc -c < ` + pgRolesFile + `)" && cat ` + pgRolesFile + ` && exec pg_dump -Fc`

// pgUnpack restores a Postgres snapshot's stream from its stdin, in one
// pass. dd with bs=1 takes the header and the roles byte for byte and
// leaves the rest in the pipe for pg_restore, which head doesn't promise
// (busybox's reads ahead). With the header, the roles SQL runs through
// psql without ON_ERROR_STOP, so a role that exists already (the new
// service's owner) is skipped and the missing ones are created, without
// passwords. The job connects as the template's owner, POSTGRES_USER of
// the official image: a superuser, which creating roles needs. Without
// the header the bytes read are put back in front of the stream, and it
// restores as it always did.
const pgUnpack = `{
  dd bs=1 count=21 of=` + pgHeadFile + ` 2>/dev/null
  H="$(tr -cd 'A-Z0-9 ' < ` + pgHeadFile + `)"
  case "$H" in
  "` + PostgresMagic + ` "` + pgLenPattern + `)
    N="${H#` + PostgresMagic + ` }"
    dd bs=1 count="$((1$N - 1` + pgLenZeros + `))" of=` + pgRolesFile + ` 2>/dev/null
    psql -X -q -d "$PGDATABASE" -f ` + pgRolesFile + ` 2>&1 >/dev/null | { grep -v "already exists" || true; } >&2
    pg_restore --no-owner --no-acl --exit-on-error -d "$PGDATABASE" ;;
  *)
    { cat ` + pgHeadFile + `; cat; } | pg_restore --no-owner --no-acl --exit-on-error -d "$PGDATABASE" ;;
  esac
}
`

// mysqlTools picks the client and dump tools of a MySQL or MariaDB image
// (MariaDB's are mariadb and mariadb-dump) and how they reach the server;
// the password is MYSQL_PWD, which both read.
const mysqlTools = `CLIENT="$(command -v mariadb || command -v mysql)"
DUMP="$(command -v mariadb-dump || command -v mysqldump)"
AT="-h $DOCK_DB_HOST -P $DOCK_DB_PORT -u root"
`

// mysqlNames makes the script's arguments ("$@") the database names in
// $DBS, one a line as the client prints them. The names are the
// tenant's: each stays one argument whatever it holds (spaces aren't
// split, nothing is matched against files), so no part of a name is read
// as an option of the dump tool, and a name that starts with a dash,
// which the tool would read as one, stops the job. dbcopy has the same.
const mysqlNames = `set -f
IFS='
'
set -- $DBS
unset IFS
set +f
for db in "$@"; do
  case "$db" in -*) echo "dock: the database \"$db\" has a name that starts with a dash, which the dump tool reads as an option; rename it" >&2; exit 1 ;; esac
done
`

// Repo is a restic repository and how to open it.
type Repo struct {
	// URL is an s3: repository URL; empty is the server's local one.
	URL                                  string
	AccessKeyID, SecretAccessKey, Region string
	// PathStyle addresses the bucket in the URL's path; false puts it in
	// the host name (restic's s3.bucket-lookup).
	PathStyle bool
	Password  string
}

// Local reports whether r is a server's local repository (not off-box).
func (r Repo) Local() bool { return r.URL == "" }

// Env is the restic environment for r.
func (r Repo) Env() map[string]string {
	env := map[string]string{"RESTIC_PASSWORD": r.Password, "RESTIC_REPOSITORY": LocalPath, "RESTIC_CACHE_DIR": "/tmp/restic-cache"}
	if r.Local() {
		return env
	}
	env["RESTIC_REPOSITORY"] = r.URL
	if r.AccessKeyID != "" {
		env["AWS_ACCESS_KEY_ID"] = r.AccessKeyID
		env["AWS_SECRET_ACCESS_KEY"] = r.SecretAccessKey
	}
	if r.Region != "" {
		env["AWS_DEFAULT_REGION"] = r.Region
	}
	lookup := "dns"
	if r.PathStyle {
		lookup = "path"
	}
	env["DOCK_RESTIC_OPTS"] = "-o s3.bucket-lookup=" + lookup
	return env
}

// S3URL is the restic URL of the repository in folder path of bucket at
// endpoint: s3:<endpoint>/<bucket>/<path>.
func S3URL(endpoint, bucket, path string) string {
	u := "s3:" + strings.TrimRight(endpoint, "/") + "/" + bucket
	if p := strings.Trim(path, "/"); p != "" {
		u += "/" + p
	}
	return u
}

// ParseS3URL splits an s3:<endpoint>/<bucket>[/path] repository URL. An
// endpoint without a scheme is https, as restic takes it.
func ParseS3URL(u string) (endpoint, bucket, path string, err error) {
	rest, ok := strings.CutPrefix(u, "s3:")
	if !ok || rest == "" {
		return "", "", "", fmt.Errorf("%q is not an s3: repository URL", u)
	}
	scheme := "https://"
	for _, s := range []string{"https://", "http://"} {
		if r, ok := strings.CutPrefix(rest, s); ok {
			scheme, rest = s, r
			break
		}
	}
	host, rest, _ := strings.Cut(rest, "/")
	bucket, path, _ = strings.Cut(rest, "/")
	if host == "" || bucket == "" {
		return "", "", "", fmt.Errorf("%q names no bucket: s3:https://<endpoint>/<bucket>[/path]", u)
	}
	return scheme + host, bucket, strings.Trim(path, "/"), nil
}

// ValidPath checks a folder in a bucket for a repository: relative,
// slash-separated names of letters, digits, dots, dashes and underscores.
func ValidPath(p string) error {
	if p == "" || len(p) > 200 {
		return errors.New("the folder must be 1 to 200 characters, like dock-backups")
	}
	for part := range strings.SplitSeq(p, "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsFunc(part, func(r rune) bool {
			return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.'
		}) {
			return fmt.Errorf("%q is not a folder: names of letters, digits, dots, dashes and underscores, separated by slashes", p)
		}
	}
	return nil
}

// ValidateURL checks a repository URL: empty (local) or s3:<endpoint>/<bucket>[/path].
func ValidateURL(u string) error {
	if u == "" {
		return nil
	}
	rest, ok := strings.CutPrefix(u, "s3:")
	if !ok || rest == "" || strings.ContainsAny(u, " \t\n") {
		return errors.New("the repository must be an S3 URL like s3:https://s3.eu-central-1.amazonaws.com/bucket/dock (or empty for local repositories)")
	}
	return nil
}

// Retention is how many snapshots forget keeps per service.
type Retention struct {
	Daily, Weekly, Monthly int
}

// Tag is the restic tag of a service's scheduled snapshots.
func Tag(serviceID int64) string { return "dock-s" + strconv.FormatInt(serviceID, 10) }

// ManualTag is the restic tag of the snapshots of a service's manual
// backups, which have their own retention (KeepManual, ADR 0095): with
// Tag the daily rule would remove an earlier backup of the same day.
func ManualTag(serviceID int64) string { return Tag(serviceID) + "-manual" }

// KeepManual is how many manual backups of a service are kept.
const KeepManual = 5

// Source is what one backup reads.
type Source struct {
	Kind apitypes.BackupKind
	// ServiceID tags the snapshots.
	ServiceID int64
	// Host and Port reach the database on the environment network
	// (postgres, redis).
	Host string
	Port int
	// User, Password and Database are the owner's credentials (postgres;
	// redis uses Password only).
	User, Password, Database string
}

// Reaching a repository: restic retries a backend that doesn't answer
// (connection refused, no route, DNS) for a long time without a word, so
// a job behind the app firewall pointed at a loopback or private endpoint
// hung with no log line. Every job first reads the repository's config
// under reachTimeout, which a reachable repository answers in a second
// (or with "doesn't exist" for a new one); only that probe has a
// deadline, the work after it runs as long as its data takes.
const (
	// reachTimeout is how long, in seconds, the first read of a
	// repository may take.
	reachTimeout = "60"
	// UnreachableExit is a job's exit code when a repository didn't
	// answer within reachTimeout; its last output line says which. The
	// prelude spells it out (unreachableExit) to stay a constant.
	UnreachableExit = 75
	unreachableExit = "75"
)

// prelude sets up the shell (pipefail where it has it) and checks the
// job's repository answers, leaving restic's exit code of reading its
// config in $rc (0: it exists). $R is restic with the repository's
// options (DOCK_RESTIC_OPTS: the bucket addressing), unquoted on use so
// they split into arguments. reach REPOSITORY PASSWORD fails the job with
// UnreachableExit when the probe is cut off: timeout's 124, or the
// killed restic's 137 where busybox's timeout execs it in place.
const prelude = `set -eu
if (set -o pipefail) 2>/dev/null; then set -o pipefail; fi
R="` + BinDir + `/restic ${DOCK_RESTIC_OPTS:-}"
T=""
if command -v timeout >/dev/null 2>&1; then T="timeout -s KILL ` + reachTimeout + `"; fi
reach() {
  rc=0
  RESTIC_REPOSITORY="$1" RESTIC_PASSWORD="$2" $T $R cat config </dev/null >/dev/null 2>&1 || rc=$?
  case $rc in 124|137)
    echo "dock: can't reach the backup storage at ${1#s3:} from a backup job (no answer in ` + reachTimeout + `s). Jobs run in a container behind the app firewall: a loopback endpoint never works and a private one must be in [network] allow_private. Fix the storage endpoint and run it again." >&2
    exit ` + unreachableExit + ` ;;
  esac
}
reach "$RESTIC_REPOSITORY" "$RESTIC_PASSWORD"
`

// initRepo creates the repository if its config couldn't be read (the
// prelude's $rc). A wrong password then fails init with restic's own
// message.
const initRepo = `[ "$rc" = 0 ] || $R init >/dev/null
`

// retention prunes the service's snapshots and lists what is left, after
// SnapshotsMarker: the scheduled ones (DOCK_TAG) by the daily, weekly and
// monthly rule, and a service's manual ones (DOCK_MANUAL_TAG, when set)
// down to the last KeepManual. Failing to prune doesn't fail the backup:
// the snapshot is already written.
const retention = `if ! $R forget --quiet --retry-lock ` + RetryLock + ` --tag "$DOCK_TAG" --group-by tags --keep-daily "$DOCK_KEEP_DAILY" --keep-weekly "$DOCK_KEEP_WEEKLY" --keep-monthly "$DOCK_KEEP_MONTHLY" --prune >/dev/null; then
  echo "dock: retention (forget --prune) failed; older snapshots are kept" >&2
fi
if [ -n "${DOCK_MANUAL_TAG:-}" ]; then
  if ! $R forget --quiet --retry-lock ` + RetryLock + ` --tag "$DOCK_MANUAL_TAG" --group-by tags --keep-last ` + keepManual + ` --prune >/dev/null; then
    echo "dock: retention of manual backups (forget --prune) failed; older ones are kept" >&2
  fi
fi
echo ` + SnapshotsMarker + `
$R snapshots --json --retry-lock ` + RetryLock + ` --tag "$DOCK_TAG" ${DOCK_MANUAL_TAG:+--tag "$DOCK_MANUAL_TAG"} || true
`

// keepManual is KeepManual for the script, spelt out to stay a constant.
const keepManual = "5"

// SnapshotsMarker precedes the snapshot list in a backup's output.
const SnapshotsMarker = "dock-snapshots:"

// backupFlags are the flags of every backup. A manual backup of a service
// is tagged DOCK_SNAPSHOT_TAG (ManualTag) instead of DOCK_TAG.
const backupFlags = `--json --quiet --retry-lock ` + RetryLock + ` --host ` + Host + ` --tag "${DOCK_SNAPSHOT_TAG:-$DOCK_TAG}"`

// BackupScript is the job script for src: dump with the database's own
// tool (or read the mounted volumes), stream into restic, apply retention.
// It runs in the database's image (postgres, redis) or the restic image
// (volume).
func BackupScript(kind apitypes.BackupKind) (string, error) {
	var run string
	switch kind {
	case apitypes.BackupPostgres:
		// --stdin-from-command fails the backup when its command fails:
		// pgStream exits non-zero if the roles or pg_dump do.
		run = pgRoles + `$R backup ` + backupFlags + ` --stdin-filename ` + strings.TrimPrefix(PostgresFile, "/") + ` --stdin-from-command -- sh -c '` + pgStream + `' sh
rm -f ` + pgRolesFile + `
`
	case apitypes.BackupRedis:
		// redis-cli --rdb fetches a consistent snapshot over the
		// replication protocol. It goes to a file first: written to a
		// pipe, it keeps a trailing EOF marker Redis can't load.
		run = `redis-cli -h "$DOCK_DB_HOST" -p "$DOCK_DB_PORT" --rdb /tmp/dock.rdb >/dev/null
$R backup ` + backupFlags + ` --stdin-filename ` + strings.TrimPrefix(RedisFile, "/") + ` --stdin-from-command -- cat /tmp/dock.rdb
rm -f /tmp/dock.rdb
`
	case apitypes.BackupMySQL:
		// Every database but the server's own: a restore goes into a new
		// server with its own users and root password, which a dump of
		// the mysql schema would overwrite.
		run = mysqlTools + `DBS="$($CLIENT $AT -N -e 'SHOW DATABASES' | grep -Ev '^(information_schema|performance_schema|mysql|sys)$')"
[ -n "$DBS" ] || { echo "dock: the server has no databases to back up" >&2; exit 1; }
` + mysqlNames + `$R backup ` + backupFlags + ` --stdin-filename ` + strings.TrimPrefix(MySQLFile, "/") + ` --stdin-from-command -- $DUMP $AT --single-transaction --routines --triggers --events --databases "$@"
`
	case apitypes.BackupVolume:
		run = `cd ` + VolumesPath + `
$R backup ` + backupFlags + ` .
`
	default:
		return "", fmt.Errorf("unknown backup kind %q", kind)
	}
	return prelude + initRepo + run + forget + retention, nil
}

// Restic names of the control plane's own backups (ADR 0021).
const (
	// ControlPlaneTag tags every control plane snapshot.
	ControlPlaneTag = "dock-control-plane"
	// ControlPlaneFile is the archive's file name inside a snapshot:
	// `restic dump --tag dock-control-plane latest /dock-control-plane.tar`.
	ControlPlaneFile = "dock-control-plane.tar"
)

// ControlPlaneScript is the job script of a control plane backup: the
// archive (internal/cpbackup) arrives on stdin and is stored as one file,
// then retention runs as for services. It runs in the restic image.
func ControlPlaneScript() string {
	return prelude + initRepo + `$R backup ` + backupFlags + ` --stdin --stdin-filename ` + ControlPlaneFile + `
` + retention
}

// ControlPlaneEnv is the job environment for a control plane backup into
// repo with retention ret.
func ControlPlaneEnv(repo Repo, ret Retention) map[string]string {
	env := repo.Env()
	env["DOCK_TAG"] = ControlPlaneTag
	env["DOCK_KEEP_DAILY"] = strconv.Itoa(ret.Daily)
	env["DOCK_KEEP_WEEKLY"] = strconv.Itoa(ret.Weekly)
	env["DOCK_KEEP_MONTHLY"] = strconv.Itoa(ret.Monthly)
	return env
}

// BackupEnv is the job environment for backing up src into repo with
// retention ret; manual tags the snapshot as a manual backup's
// (ManualTag). A caller sets DOCK_FORGET on it for base backups to
// remove along (BaseEnv).
func BackupEnv(src Source, repo Repo, ret Retention, manual bool) map[string]string {
	env := repo.Env()
	env["DOCK_TAG"] = Tag(src.ServiceID)
	env["DOCK_MANUAL_TAG"] = ManualTag(src.ServiceID)
	if manual {
		env["DOCK_SNAPSHOT_TAG"] = ManualTag(src.ServiceID)
	}
	env["DOCK_KEEP_DAILY"] = strconv.Itoa(ret.Daily)
	env["DOCK_KEEP_WEEKLY"] = strconv.Itoa(ret.Weekly)
	env["DOCK_KEEP_MONTHLY"] = strconv.Itoa(ret.Monthly)
	maps.Copy(env, dbEnv(src))
	return env
}

// dbEnv is how a job reaches the database: libpq's PG* variables for
// pg_dump and pg_restore, REDISCLI_AUTH for redis-cli, MYSQL_PWD for the
// MySQL and MariaDB tools.
func dbEnv(src Source) map[string]string {
	switch src.Kind {
	case apitypes.BackupPostgres:
		return map[string]string{
			"PGHOST": src.Host, "PGPORT": strconv.Itoa(src.Port), "PGUSER": src.User,
			"PGPASSWORD": src.Password, "PGDATABASE": src.Database,
		}
	case apitypes.BackupRedis:
		return map[string]string{"DOCK_DB_HOST": src.Host, "DOCK_DB_PORT": strconv.Itoa(src.Port), "REDISCLI_AUTH": src.Password}
	case apitypes.BackupMySQL:
		return map[string]string{"DOCK_DB_HOST": src.Host, "DOCK_DB_PORT": strconv.Itoa(src.Port), "MYSQL_PWD": src.Password}
	}
	return nil
}

// RestoreScript is the job script that restores a snapshot (DOCK_SNAPSHOT)
// into a new service: pg_restore into the running new Postgres, the dump
// into the running new MySQL or MariaDB, or the
// RDB as the AOF base of the new Redis's volume, mounted at dataDir, before
// it first starts (Redis with appendonly ignores a plain dump.rdb then).
func RestoreScript(kind apitypes.BackupKind, dataDir string) (string, error) {
	dump := `$R dump --retry-lock ` + RetryLock + ` "$DOCK_SNAPSHOT" `
	switch kind {
	case apitypes.BackupPostgres:
		return prelude + dump + PostgresFile + ` | ` + pgUnpack, nil
	case apitypes.BackupMySQL:
		return prelude + mysqlTools + dump + MySQLFile + ` | $CLIENT $AT
`, nil
	case apitypes.BackupRedis:
		dir := dataDir + "/appendonlydir"
		return prelude + `mkdir -p ` + dir + `
` + dump + RedisFile + ` > ` + dir + `/appendonly.aof.1.base.rdb
printf 'file appendonly.aof.1.base.rdb seq 1 type b\n' > ` + dir + `/appendonly.aof.manifest
`, nil
	}
	return "", fmt.Errorf("%s backups can't be restored into a new service", kind)
}

// VolumeRestoreScript is the job script that restores a volume backup
// (DOCK_SNAPSHOT) into a service's volumes, mounted under VolumesPath by
// name as for the backup (ADR 0095). It restores exactly the volumes the
// backup holds: each is emptied, then filled. A volume the backup holds
// that isn't mounted stops it before anything is touched; a mounted one
// the backup doesn't hold (added since) is left as it is, and the log
// says so. A backup of VolumesPath's "." holds each volume as a folder
// at its root. It runs in the restic image.
const VolumeRestoreScript = prelude + `names="$($R ls --retry-lock ` + RetryLock + ` "$DOCK_SNAPSHOT" / | sed -n 's|^/\([^/]*\)$|\1|p' | tr '\n' ' ')"
[ -n "$names" ] || { echo "dock: the backup holds no volumes, so nothing was restored" >&2; exit 1; }
for v in $names; do
  [ -d "` + VolumesPath + `/$v" ] || { echo "dock: the backup holds the volume $v, which the service no longer has; add a volume named $v back and restore again. Nothing was changed." >&2; exit 1; }
done
for d in ` + VolumesPath + `/*/; do
  v="$(basename "$d")"
  case " $names " in *" $v "*) ;; *) echo "dock: the volume $v isn't in the backup (added since) and is left as it is" ;; esac
done
for v in $names; do
  find "` + VolumesPath + `/$v" -mindepth 1 -delete
done
$R restore --retry-lock ` + RetryLock + ` "$DOCK_SNAPSHOT" --target ` + VolumesPath + `
echo "dock: restored $names"
`

// RestoreEnv is the job environment for restoring snapshot from repo into
// target (the new service; postgres only, redis writes its volume).
func RestoreEnv(snapshot string, target Source, repo Repo) map[string]string {
	env := repo.Env()
	env["DOCK_SNAPSHOT"] = snapshot
	maps.Copy(env, dbEnv(target))
	return env
}

// InstallScript copies the restic binary of the restic image into the
// volume mounted at BinDir (read-write for this job only).
const InstallScript = `set -eu
cp /usr/bin/restic ` + BinDir + `/restic.tmp
mv ` + BinDir + `/restic.tmp ` + BinDir + `/restic
`

// Result is what a backup's output says.
type Result struct {
	SnapshotID string
	// Bytes is the size of what was backed up (the dump, or the files).
	Bytes int64
	// Kept are the service's snapshot IDs left after retention; nil if
	// they couldn't be listed.
	Kept []string
}

// summary is restic's backup summary message (--json).
type summary struct {
	MessageType string `json:"message_type"`
	SnapshotID  string `json:"snapshot_id"`
	TotalBytes  int64  `json:"total_bytes_processed"`
}

// ParseBackup reads a backup job's output: restic's summary line and the
// snapshot list after SnapshotsMarker.
func ParseBackup(output string) (Result, error) {
	var res Result
	afterMarker := false
	sc := bufio.NewScanner(strings.NewReader(output))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == SnapshotsMarker:
			afterMarker = true
		case strings.HasPrefix(line, `{"message_type":"summary"`):
			var s summary
			if json.Unmarshal([]byte(line), &s) == nil && s.SnapshotID != "" {
				res.SnapshotID, res.Bytes = s.SnapshotID, s.TotalBytes
			}
		case afterMarker && strings.HasPrefix(line, "["):
			var snaps []struct {
				ID string `json:"id"`
			}
			if json.Unmarshal([]byte(line), &snaps) == nil {
				res.Kept = make([]string, 0, len(snaps))
				for _, s := range snaps {
					res.Kept = append(res.Kept, s.ID)
				}
			}
		}
	}
	if res.SnapshotID == "" {
		return res, errors.New("the backup didn't finish, so nothing was saved; its log shows why")
	}
	return res, nil
}

// ErrorText picks the lines of a failed job's output worth showing: the
// last few that aren't JSON progress, at most 2 KB.
func ErrorText(output string) string {
	var keep []string
	for line := range strings.Lines(output) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "{") || strings.HasPrefix(line, "[") || line == SnapshotsMarker {
			continue
		}
		keep = append(keep, line)
	}
	if len(keep) > 6 {
		keep = keep[len(keep)-6:]
	}
	s := strings.Join(keep, "; ")
	if len(s) > 2048 {
		s = s[len(s)-2048:]
	}
	return s
}

// NextDue is when a daily backup at hour (in loc) is next due: the latest
// occurrence of hour at or before now if nothing happened since (since:
// the last scheduled attempt, or when the service was created), else the
// next occurrence. A result at or before now means "due now".
func NextDue(hour int, since, now time.Time, loc *time.Location) time.Time {
	n := now.In(loc)
	at := time.Date(n.Year(), n.Month(), n.Day(), hour, 0, 0, 0, loc)
	if at.After(n) {
		at = at.AddDate(0, 0, -1)
	}
	if since.Before(at) {
		return at
	}
	return at.AddDate(0, 0, 1)
}

// Moving backups to another repository (ADR 0039): restic copy from each
// source repository into the target, then one check and listing of the
// target, then each source emptied.

// MoveMarker precedes a snapshot listing in a move job's output.
const MoveMarker = "dock-move-snapshots:"

// listSnapshots prints a repository's snapshots after MoveMarker as one
// "original" (when the snapshot is a copy) and "id" token each, in that
// order: restic's JSON lists original before id, and nothing else in it
// has those keys. Kept this small, thousands of snapshots fit in a job's
// output (node.MaxJobOutput), which the full JSON doesn't.
const listSnapshots = `echo ` + MoveMarker + `
$R snapshots --json --retry-lock ` + RetryLock + ` | grep -oE '"(original|id)":"[0-9a-f]{64}"' || true
`

// CopyScript copies every snapshot of the repository RESTIC_FROM_REPOSITORY
// (password RESTIC_FROM_PASSWORD) into the target (the job's repository),
// creating the target with the source's chunking parameters if it doesn't
// exist, so their data deduplicates, then lists the source (ParseMoved).
// Snapshots already copied are skipped. It runs in the restic image.
const CopyScript = prelude + `target=$rc
reach "$RESTIC_FROM_REPOSITORY" "$RESTIC_FROM_PASSWORD"
if [ "$target" != 0 ]; then
  $R init --from-repo "$RESTIC_FROM_REPOSITORY" --copy-chunker-params >/dev/null
fi
$R copy --quiet --retry-lock ` + RetryLock + ` --from-repo "$RESTIC_FROM_REPOSITORY"
export RESTIC_REPOSITORY="$RESTIC_FROM_REPOSITORY" RESTIC_PASSWORD="$RESTIC_FROM_PASSWORD"
` + listSnapshots

// CheckScript checks the target's structure and lists its snapshots
// (ParseMoved).
const CheckScript = prelude + `$R check --retry-lock ` + RetryLock + ` >&2
` + listSnapshots

// VerifySubset is how much of a repository's pack data the weekly check
// reads back and re-hashes (ADR 0096): a random sample of a fixed size,
// so the cost stays the same as a repository grows, and a repository
// smaller than it is read in full.
const VerifySubset = "1G"

// VerifyScript is the weekly repository check (ADR 0096): the whole
// structure and VerifySubset of the data. restic's check takes a shared
// lock, so a backup meanwhile still runs. It runs in the restic image.
const VerifyScript = prelude + `$R check --retry-lock ` + RetryLock + ` --read-data-subset=` + VerifySubset + `
`

// ClearLocalScript empties the local repository mounted at LocalPath,
// once its snapshots were copied elsewhere. The volume stays.
const ClearLocalScript = `set -eu
find ` + LocalPath + ` -mindepth 1 -delete
`

// CopyEnv is the job environment for copying from into to: to is the
// job's repository, from the source (the same password: Dock uses one for
// every repository; mounted at LocalPath when local).
func CopyEnv(from, to Repo) map[string]string {
	env := to.Env()
	env["RESTIC_FROM_REPOSITORY"] = LocalPath
	if !from.Local() {
		env["RESTIC_FROM_REPOSITORY"] = from.URL
	}
	env["RESTIC_FROM_PASSWORD"] = from.Password
	return env
}

// ParseMoved reads the snapshot listing of a move job (CopyScript's of
// the source, CheckScript's of the target): each snapshot's ID mapped to
// the snapshot it is a copy of (restic's "original"), or to itself. A
// target's copy of a source snapshot shares the source's value.
func ParseMoved(output string) (map[string]string, error) {
	_, list, ok := strings.Cut(output, MoveMarker)
	if !ok {
		return nil, errors.New("the job listed no snapshots")
	}
	out := map[string]string{}
	original := ""
	for line := range strings.Lines(list) {
		key, val, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		val = strings.Trim(val, `"`)
		switch key {
		case `"original"`:
			original = val
		case `"id"`:
			out[val] = cmp.Or(original, val)
			original = ""
		}
	}
	return out, nil
}
