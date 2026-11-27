package backup

import (
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

func TestBackupScript(t *testing.T) {
	for kind, want := range map[apitypes.BackupKind][]string{
		apitypes.BackupPostgres: {`pg_dumpall --roles-only --no-role-passwords -l "$PGDATABASE" > /tmp/dock-roles.sql`, "--stdin-filename postgres.dump",
			`--stdin-from-command -- sh -c 'printf "DOCKPG1 %012d\n" "$(wc -c < /tmp/dock-roles.sql)" && cat /tmp/dock-roles.sql && exec pg_dump -Fc' sh`},
		apitypes.BackupRedis:  {`redis-cli -h "$DOCK_DB_HOST" -p "$DOCK_DB_PORT" --rdb /tmp/dock.rdb`, "--stdin-filename redis.rdb", "-- cat /tmp/dock.rdb"},
		apitypes.BackupVolume: {"cd /volumes", "backup --json"},
		apitypes.BackupMySQL: {`CLIENT="$(command -v mariadb || command -v mysql)"`, "grep -Ev '^(information_schema|performance_schema|mysql|sys)$'",
			"--stdin-filename mysql.sql", "--stdin-from-command -- $DUMP $AT --single-transaction --routines --triggers --events --databases \"$@\""},
	} {
		s, err := BackupScript(kind)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range append(want,
			"set -eu", `R="/dock-restic/restic ${DOCK_RESTIC_OPTS:-}"`, "$R init", `--tag "${DOCK_SNAPSHOT_TAG:-$DOCK_TAG}"`, "--host dock", "--retry-lock 10m",
			`--tag "$DOCK_MANUAL_TAG" --group-by tags --keep-last 5 --prune`, `${DOCK_MANUAL_TAG:+--tag "$DOCK_MANUAL_TAG"}`,
			`forget --quiet --retry-lock 10m --tag "$DOCK_TAG" --group-by tags --keep-daily "$DOCK_KEEP_DAILY"`, "--prune",
			"echo "+SnapshotsMarker, `snapshots --json --retry-lock 10m --tag "$DOCK_TAG"`) {
			if !strings.Contains(s, w) {
				t.Errorf("%s script lacks %q:\n%s", kind, w, s)
			}
		}
	}
	if _, err := BackupScript("nope"); err == nil {
		t.Error("unknown kind: no error")
	}
}

func TestBackupEnv(t *testing.T) {
	src := Source{Kind: apitypes.BackupPostgres, ServiceID: 7, Host: "db", Port: 5432, User: "postgres", Password: "pw", Database: "app"}
	env := BackupEnv(src, Repo{Password: "repo-pw"}, Retention{Daily: 7, Weekly: 4, Monthly: 6}, false)
	for k, v := range map[string]string{
		"RESTIC_REPOSITORY": LocalPath, "RESTIC_PASSWORD": "repo-pw", "DOCK_TAG": "dock-s7", "DOCK_MANUAL_TAG": "dock-s7-manual",
		"DOCK_KEEP_DAILY": "7", "DOCK_KEEP_WEEKLY": "4", "DOCK_KEEP_MONTHLY": "6",
		"PGHOST": "db", "PGPORT": "5432", "PGUSER": "postgres", "PGPASSWORD": "pw", "PGDATABASE": "app",
	} {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if _, ok := env["AWS_ACCESS_KEY_ID"]; ok {
		t.Error("local repository got S3 credentials")
	}
	if _, ok := env["DOCK_SNAPSHOT_TAG"]; ok {
		t.Error("a scheduled backup is tagged as a manual one")
	}
	if manual := BackupEnv(src, Repo{}, Retention{}, true); manual["DOCK_SNAPSHOT_TAG"] != "dock-s7-manual" || manual["DOCK_TAG"] != "dock-s7" {
		t.Errorf("manual backup env = %v", manual)
	}

	s3 := Repo{URL: "s3:https://s3.example.com/b/dock", AccessKeyID: "id", SecretAccessKey: "secret", Region: "eu", Password: "p"}
	env = BackupEnv(Source{Kind: apitypes.BackupRedis, ServiceID: 3, Host: "cache", Port: 6379, Password: "rp"}, s3, Retention{Daily: 1}, false)
	for k, v := range map[string]string{
		"RESTIC_REPOSITORY": s3.URL, "AWS_ACCESS_KEY_ID": "id", "AWS_SECRET_ACCESS_KEY": "secret", "AWS_DEFAULT_REGION": "eu",
		"DOCK_DB_HOST": "cache", "DOCK_DB_PORT": "6379", "REDISCLI_AUTH": "rp",
	} {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if _, ok := env["PGPASSWORD"]; ok {
		t.Error("redis backup got Postgres variables")
	}
}

func TestRestoreScript(t *testing.T) {
	pg, err := RestoreScript(apitypes.BackupPostgres, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		`dump --retry-lock 10m "$DOCK_SNAPSHOT" /postgres.dump | {`, "set -o pipefail",
		"dd bs=1 count=" + strconv.Itoa(PostgresHeaderLen) + " of=/tmp/dock-pg-head",
		`psql -X -q -d "$PGDATABASE" -f /tmp/dock-roles.sql`,
		// With the header, and without it (an older snapshot).
		`    pg_restore --no-owner --no-acl --exit-on-error -d "$PGDATABASE" ;;`,
		`{ cat /tmp/dock-pg-head; cat; } | pg_restore --no-owner --no-acl --exit-on-error -d "$PGDATABASE" ;;`,
	} {
		if !strings.Contains(pg, w) {
			t.Errorf("postgres restore lacks %q:\n%s", w, pg)
		}
	}
	redis, err := RestoreScript(apitypes.BackupRedis, "/data")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		"mkdir -p /data/appendonlydir",
		`"$DOCK_SNAPSHOT" /redis.rdb > /data/appendonlydir/appendonly.aof.1.base.rdb`,
		`printf 'file appendonly.aof.1.base.rdb seq 1 type b\n' > /data/appendonlydir/appendonly.aof.manifest`,
	} {
		if !strings.Contains(redis, w) {
			t.Errorf("redis restore lacks %q:\n%s", w, redis)
		}
	}
	my, err := RestoreScript(apitypes.BackupMySQL, "")
	if err != nil || !strings.Contains(my, `"$DOCK_SNAPSHOT" /mysql.sql | $CLIENT $AT`) || !strings.Contains(my, "set -o pipefail") {
		t.Errorf("mysql restore: %v\n%s", err, my)
	}
	if _, err := RestoreScript(apitypes.BackupVolume, ""); err == nil {
		t.Error("volume restore: no error")
	}
	env := RestoreEnv("abc123", Source{Kind: apitypes.BackupPostgres, Host: "db-restored", Port: 5432, User: "postgres", Password: "new", Database: "app"}, Repo{Password: "p"})
	if env["DOCK_SNAPSHOT"] != "abc123" || env["PGHOST"] != "db-restored" || env["PGPASSWORD"] != "new" {
		t.Errorf("restore env = %v", env)
	}
}

func TestParseBackup(t *testing.T) {
	out := `subprocess pg_dump: some notice
{"message_type":"summary","files_new":1,"total_bytes_processed":23166,"snapshot_id":"4cf53263e99c"}
` + SnapshotsMarker + `
[{"time":"2026-10-22T10:16:41Z","id":"4cf53263e99c","short_id":"4cf53263"},{"id":"0011aa"}]
`
	res, err := ParseBackup(out)
	if err != nil {
		t.Fatal(err)
	}
	if res.SnapshotID != "4cf53263e99c" || res.Bytes != 23166 || !slices.Equal(res.Kept, []string{"4cf53263e99c", "0011aa"}) {
		t.Errorf("result = %+v", res)
	}

	// A failed snapshot listing leaves Kept nil: nothing is marked pruned.
	res, err = ParseBackup(`{"message_type":"summary","snapshot_id":"ab"}` + "\n" + SnapshotsMarker + "\nFatal: repository locked\n")
	if err != nil || res.Kept != nil {
		t.Errorf("no list: %+v, %v", res, err)
	}
	if _, err := ParseBackup("Fatal: wrong password or no key found\n"); err == nil {
		t.Error("no summary: no error")
	}
}

func TestErrorText(t *testing.T) {
	out := `{"message_type":"status"}
pg_dump: error: connection to server at "db" failed
Fatal: unable to save snapshot: exit status 1
`
	got := ErrorText(out)
	if got != `pg_dump: error: connection to server at "db" failed; Fatal: unable to save snapshot: exit status 1` {
		t.Errorf("ErrorText = %q", got)
	}
}

func TestValidateURL(t *testing.T) {
	for u, ok := range map[string]bool{
		"": true, "s3:https://s3.eu-central-1.amazonaws.com/bucket/dock": true, "s3:s3.amazonaws.com/bucket": true,
		"s3:": false, "/var/backups": false, "sftp:host:/x": false, "s3:https://x/b c": false,
	} {
		if err := ValidateURL(u); (err == nil) != ok {
			t.Errorf("ValidateURL(%q) = %v, want ok %v", u, err, ok)
		}
	}
}

func TestNextDue(t *testing.T) {
	oslo, err := time.LoadLocation("Europe/Oslo")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, oslo)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct {
		name       string
		since, now string
		want       string
	}{
		{"created after today's hour: tomorrow", "2026-10-22 09:00", "2026-10-22 10:00", "2026-10-23 03:00"},
		{"created before today's hour, now past it: due now", "2026-10-21 12:00", "2026-10-22 10:00", "2026-10-22 03:00"},
		{"backed up today: tomorrow", "2026-10-22 03:00", "2026-10-22 10:00", "2026-10-23 03:00"},
		{"before today's hour, backed up yesterday: today", "2026-10-21 03:00", "2026-10-22 02:00", "2026-10-22 03:00"},
		{"daemon down for days: due now, once", "2026-10-18 03:00", "2026-10-22 10:00", "2026-10-22 03:00"},
		{"DST ends (25 h day): still 03:00", "2026-10-24 03:00", "2026-10-25 01:00", "2026-10-25 03:00"},
	} {
		got := NextDue(3, at(c.since), at(c.now), oslo)
		if !got.Equal(at(c.want)) {
			t.Errorf("%s: NextDue = %s, want %s", c.name, got, at(c.want))
		}
	}
}

// A repository URL splits into endpoint, bucket and folder, and builds
// back the same; an endpoint without a scheme is https.
func TestS3URL(t *testing.T) {
	for u, want := range map[string][3]string{
		"s3:https://hel1.your-objectstorage.com/backups/dock": {"https://hel1.your-objectstorage.com", "backups", "dock"},
		"s3:http://127.0.0.1:9000/b":                          {"http://127.0.0.1:9000", "b", ""},
		"s3:s3.amazonaws.com/b/a/b/":                          {"https://s3.amazonaws.com", "b", "a/b"},
	} {
		e, b, p, err := ParseS3URL(u)
		if err != nil || [3]string{e, b, p} != want {
			t.Errorf("ParseS3URL(%q) = %q %q %q, %v", u, e, b, p, err)
		}
	}
	if u := S3URL("https://hel1.your-objectstorage.com/", "backups", "/dock/"); u != "s3:https://hel1.your-objectstorage.com/backups/dock" {
		t.Errorf("S3URL = %q", u)
	}
	if _, _, _, err := ParseS3URL("s3:https://host"); err == nil {
		t.Error("a URL without a bucket parsed")
	}
	for _, bad := range []string{"", "a//b", "../x", "a b"} {
		if ValidPath(bad) == nil {
			t.Errorf("ValidPath(%q) passed", bad)
		}
	}
}

// A move job's listing maps each snapshot to what it is a copy of: its
// original, or itself.
func TestParseMoved(t *testing.T) {
	out := "copied\n" + MoveMarker + "\n" +
		`"original":"` + strings.Repeat("1", 64) + `"` + "\n" + `"id":"` + strings.Repeat("a", 64) + `"` + "\n" +
		`"id":"` + strings.Repeat("b", 64) + `"` + "\n"
	got, err := ParseMoved(out)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{strings.Repeat("a", 64): strings.Repeat("1", 64), strings.Repeat("b", 64): strings.Repeat("b", 64)}
	if !maps.Equal(got, want) {
		t.Fatalf("ParseMoved = %v", got)
	}
	if _, err := ParseMoved("Fatal: wrong password"); err == nil {
		t.Error("output without a listing parsed")
	}
}

// Bucket addressing reaches restic as an option in DOCK_RESTIC_OPTS; a
// local repository has none.
func TestRepoEnvAddressing(t *testing.T) {
	if env := (Repo{URL: "s3:https://e/b", PathStyle: true}).Env(); env["DOCK_RESTIC_OPTS"] != "-o s3.bucket-lookup=path" {
		t.Errorf("path style: %q", env["DOCK_RESTIC_OPTS"])
	}
	if env := (Repo{URL: "s3:https://e/b"}).Env(); env["DOCK_RESTIC_OPTS"] != "-o s3.bucket-lookup=dns" {
		t.Errorf("host style: %q", env["DOCK_RESTIC_OPTS"])
	}
	if env := (Repo{}).Env(); env["DOCK_RESTIC_OPTS"] != "" {
		t.Errorf("local: %q", env["DOCK_RESTIC_OPTS"])
	}
}

// The prelude fails a job whose repository doesn't answer with
// UnreachableExit and a message naming the endpoint, lets one that
// answers (even "doesn't exist", restic's exit 10) go on to init, and
// removes an existing one's stale locks, going on when that fails.
// restic is a stub; the probe's deadline is cut to a second.
func TestPreludeReach(t *testing.T) {
	for name, tc := range map[string]struct {
		stub     string
		wantExit int
		wantOut  string
	}{
		"unreachable":   {stub: "sleep 30", wantExit: UnreachableExit, wantOut: "can't reach the backup storage at http://127.0.0.1:9000/bucket from a backup job"},
		"new":           {stub: `[ "$1" = cat ] && exit 10; echo "ran $1" >&2`, wantOut: "ran init\n"},
		"exists":        {stub: `[ "$1" = cat ] && exit 0; echo "ran $1" >&2`, wantOut: "ran unlock\n"},
		"unlock failed": {stub: `[ "$1" = cat ] && exit 0; echo "ran $1" >&2; exit 1`, wantOut: "ran unlock\n"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "restic"), []byte("#!/bin/sh\n"+tc.stub+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			script := strings.NewReplacer(BinDir, dir, "timeout -s KILL "+reachTimeout, "timeout -s KILL 0.1").Replace(prelude + initRepo)
			cmd := exec.Command("sh", "-c", script)
			cmd.Env = append(os.Environ(), "RESTIC_REPOSITORY=s3:http://127.0.0.1:9000/bucket", "RESTIC_PASSWORD=pw")
			out, err := cmd.CombinedOutput()
			code := 0
			if ee, ok := errors.AsType[*exec.ExitError](err); ok {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != tc.wantExit || strconv.Itoa(UnreachableExit) != unreachableExit || !strings.Contains(string(out), tc.wantOut) || (tc.wantExit == 0 && string(out) != tc.wantOut) {
				t.Fatalf("exit %d, output %q; want exit %d with %q", code, out, tc.wantExit, tc.wantOut)
			}
		})
	}
}
