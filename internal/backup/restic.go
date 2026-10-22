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
)

// Repo is a restic repository and how to open it.
type Repo struct {
	// URL is an s3: repository URL; empty is the server's local one.
	URL                                  string
	AccessKeyID, SecretAccessKey, Region string
	Password                             string
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
	return env
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

// Tag is the restic tag of a service's snapshots.
func Tag(serviceID int64) string { return "dock-s" + strconv.FormatInt(serviceID, 10) }

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

// prelude opens (or creates) the repository; pipefail where the shell has it.
const prelude = `set -eu
if (set -o pipefail) 2>/dev/null; then set -o pipefail; fi
R=` + BinDir + `/restic
`

// initRepo creates the repository if it can't be opened. A wrong password
// or an unreachable repository then fails init with restic's own message.
const initRepo = `$R cat config >/dev/null 2>&1 || $R init >/dev/null
`

// retention prunes the service's snapshots and lists what is left, after
// SnapshotsMarker. Failing to prune doesn't fail the backup: the snapshot
// is already written.
const retention = `if ! $R forget --quiet --retry-lock ` + RetryLock + ` --tag "$DOCK_TAG" --group-by tags --keep-daily "$DOCK_KEEP_DAILY" --keep-weekly "$DOCK_KEEP_WEEKLY" --keep-monthly "$DOCK_KEEP_MONTHLY" --prune >/dev/null; then
  echo "dock: retention (forget --prune) failed; older snapshots are kept" >&2
fi
echo ` + SnapshotsMarker + `
$R snapshots --json --retry-lock ` + RetryLock + ` --tag "$DOCK_TAG" || true
`

// SnapshotsMarker precedes the snapshot list in a backup's output.
const SnapshotsMarker = "dock-snapshots:"

// backupFlags are the flags of every backup.
const backupFlags = `--json --quiet --retry-lock ` + RetryLock + ` --host ` + Host + ` --tag "$DOCK_TAG"`

// BackupScript is the job script for src: dump with the database's own
// tool (or read the mounted volumes), stream into restic, apply retention.
// It runs in the database's image (postgres, redis) or the restic image
// (volume).
func BackupScript(kind apitypes.BackupKind) (string, error) {
	var run string
	switch kind {
	case apitypes.BackupPostgres:
		// --stdin-from-command fails the backup when pg_dump fails.
		run = `$R backup ` + backupFlags + ` --stdin-filename ` + strings.TrimPrefix(PostgresFile, "/") + ` --stdin-from-command -- pg_dump -Fc
`
	case apitypes.BackupRedis:
		// redis-cli --rdb fetches a consistent snapshot over the
		// replication protocol. It goes to a file first: written to a
		// pipe, it keeps a trailing EOF marker Redis can't load.
		run = `redis-cli -h "$DOCK_DB_HOST" -p "$DOCK_DB_PORT" --rdb /tmp/dock.rdb >/dev/null
$R backup ` + backupFlags + ` --stdin-filename ` + strings.TrimPrefix(RedisFile, "/") + ` --stdin-from-command -- cat /tmp/dock.rdb
rm -f /tmp/dock.rdb
`
	case apitypes.BackupVolume:
		run = `cd ` + VolumesPath + `
$R backup ` + backupFlags + ` .
`
	default:
		return "", fmt.Errorf("unknown backup kind %q", kind)
	}
	return prelude + initRepo + run + retention, nil
}

// BackupEnv is the job environment for backing up src into repo with
// retention ret.
func BackupEnv(src Source, repo Repo, ret Retention) map[string]string {
	env := repo.Env()
	env["DOCK_TAG"] = Tag(src.ServiceID)
	env["DOCK_KEEP_DAILY"] = strconv.Itoa(ret.Daily)
	env["DOCK_KEEP_WEEKLY"] = strconv.Itoa(ret.Weekly)
	env["DOCK_KEEP_MONTHLY"] = strconv.Itoa(ret.Monthly)
	maps.Copy(env, dbEnv(src))
	return env
}

// dbEnv is how a job reaches the database: libpq's PG* variables for
// pg_dump and pg_restore, REDISCLI_AUTH for redis-cli.
func dbEnv(src Source) map[string]string {
	switch src.Kind {
	case apitypes.BackupPostgres:
		return map[string]string{
			"PGHOST": src.Host, "PGPORT": strconv.Itoa(src.Port), "PGUSER": src.User,
			"PGPASSWORD": src.Password, "PGDATABASE": src.Database,
		}
	case apitypes.BackupRedis:
		return map[string]string{"DOCK_DB_HOST": src.Host, "DOCK_DB_PORT": strconv.Itoa(src.Port), "REDISCLI_AUTH": src.Password}
	}
	return nil
}

// RestoreScript is the job script that restores a snapshot (DOCK_SNAPSHOT)
// into a new service: pg_restore into the running new Postgres, or the
// RDB as the AOF base of the new Redis's volume, mounted at dataDir, before
// it first starts (Redis with appendonly ignores a plain dump.rdb then).
func RestoreScript(kind apitypes.BackupKind, dataDir string) (string, error) {
	dump := `$R dump --retry-lock ` + RetryLock + ` "$DOCK_SNAPSHOT" `
	switch kind {
	case apitypes.BackupPostgres:
		return prelude + dump + PostgresFile + ` | pg_restore --no-owner --no-acl --exit-on-error -d "$PGDATABASE"
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
		return res, errors.New("restic reported no snapshot")
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
