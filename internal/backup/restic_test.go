package backup

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

func TestBackupScript(t *testing.T) {
	for kind, want := range map[apitypes.BackupKind][]string{
		apitypes.BackupPostgres: {"--stdin-from-command -- pg_dump -Fc", "--stdin-filename postgres.dump"},
		apitypes.BackupRedis:    {`redis-cli -h "$DOCK_DB_HOST" -p "$DOCK_DB_PORT" --rdb /tmp/dock.rdb`, "--stdin-filename redis.rdb", "-- cat /tmp/dock.rdb"},
		apitypes.BackupVolume:   {"cd /volumes", "backup --json"},
	} {
		s, err := BackupScript(kind)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range append(want,
			"set -eu", "R=/dock-restic/restic", "$R init", `--tag "$DOCK_TAG"`, "--host dock", "--retry-lock 10m",
			`forget --quiet --retry-lock 10m --tag "$DOCK_TAG" --group-by tags --keep-daily "$DOCK_KEEP_DAILY"`, "--prune",
			"echo "+SnapshotsMarker, `snapshots --json --retry-lock 10m --tag "$DOCK_TAG"`) {
			if !strings.Contains(s, w) {
				t.Errorf("%s script lacks %q:\n%s", kind, w, s)
			}
		}
	}
	if _, err := BackupScript("mysql"); err == nil {
		t.Error("unknown kind: no error")
	}
}

func TestBackupEnv(t *testing.T) {
	src := Source{Kind: apitypes.BackupPostgres, ServiceID: 7, Host: "db", Port: 5432, User: "postgres", Password: "pw", Database: "app"}
	env := BackupEnv(src, Repo{Password: "repo-pw"}, Retention{Daily: 7, Weekly: 4, Monthly: 6})
	for k, v := range map[string]string{
		"RESTIC_REPOSITORY": LocalPath, "RESTIC_PASSWORD": "repo-pw", "DOCK_TAG": "dock-s7",
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

	s3 := Repo{URL: "s3:https://s3.example.com/b/dock", AccessKeyID: "id", SecretAccessKey: "secret", Region: "eu", Password: "p"}
	env = BackupEnv(Source{Kind: apitypes.BackupRedis, ServiceID: 3, Host: "cache", Port: 6379, Password: "rp"}, s3, Retention{Daily: 1})
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
	for _, w := range []string{`dump --retry-lock 10m "$DOCK_SNAPSHOT" /postgres.dump | pg_restore --no-owner --no-acl --exit-on-error -d "$PGDATABASE"`, "set -o pipefail"} {
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
