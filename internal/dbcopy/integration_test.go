//go:build integration

package dbcopy

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The integration tests run the scripts as core runs them: a base and a
// preview database on a network of their own, the dump job joined to it
// with the scratch volume, then the load job. Run with
// `go test -tags integration ./internal/dbcopy`.

const (
	pgImage    = "postgres:17-alpine"
	mysqlImage = "mysql:8.4.13"
	redisImage = "redis:8-alpine"
)

// lab is one test's network, scratch volume and containers, removed at
// its end.
type lab struct {
	t      *testing.T
	prefix string
}

func newLab(t *testing.T) *lab {
	t.Helper()
	l := &lab{t: t, prefix: fmt.Sprintf("dock-copytest-%d", time.Now().UnixNano())}
	l.docker("network", "create", l.prefix)
	l.docker("volume", "create", l.prefix+"-copy")
	t.Cleanup(func() {
		out, _ := exec.Command("docker", "ps", "-aq", "--filter", "name="+l.prefix).Output()
		if ids := strings.Fields(string(out)); len(ids) > 0 {
			_ = exec.Command("docker", append([]string{"rm", "-fv"}, ids...)...).Run()
		}
		_ = exec.Command("docker", "volume", "rm", "-f", l.prefix+"-copy", l.prefix+"-data").Run()
		_ = exec.Command("docker", "network", "rm", l.prefix).Run()
	})
	return l
}

func (l *lab) docker(args ...string) string {
	l.t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		l.t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// start runs a database container named prefix-name on the network.
func (l *lab) start(name, image string, env []string, args ...string) {
	l.t.Helper()
	run := []string{"run", "-d", "--name", l.prefix + "-" + name, "--network", l.prefix, "--network-alias", name}
	for _, e := range env {
		run = append(run, "-e", e)
	}
	l.docker(append(append(run, image), args...)...)
}

// job runs script in image with env and the scratch volume, as RunJob
// would, and returns its output and exit code.
func (l *lab) job(image, script string, env map[string]string, mounts ...string) (string, int) {
	l.t.Helper()
	run := []string{"run", "--rm", "--network", l.prefix, "-v", l.prefix + "-copy:" + Path, "--entrypoint", "/bin/sh"}
	for _, m := range mounts {
		run = append(run, "-v", m)
	}
	for k, v := range env {
		run = append(run, "-e", k+"="+v)
	}
	out, err := exec.Command("docker", append(run, image, "-c", script)...).CombinedOutput()
	if exit, ok := err.(*exec.ExitError); ok {
		return string(out), exit.ExitCode()
	}
	if err != nil {
		l.t.Fatalf("run job: %v\n%s", err, out)
	}
	return string(out), 0
}

// sql runs statements in a container's database client and returns its
// output, rows tab-separated.
func (l *lab) sql(name string, client []string, stmt string) string {
	l.t.Helper()
	return l.docker(append(append([]string{"exec", l.prefix + "-" + name}, client...), stmt)...)
}

// waitFor retries a client until it answers.
func (l *lab) waitFor(name string, client []string) {
	l.t.Helper()
	for range 120 {
		args := append(append([]string{"exec", l.prefix + "-" + name}, client...), "SELECT 1")
		if exec.Command("docker", args...).Run() == nil {
			return
		}
		time.Sleep(time.Second)
	}
	l.t.Fatalf("%s didn't start", name)
}

var psqlBase = []string{"psql", "-U", "postgres", "-d", "app", "-X", "-At", "-F", "\t", "-c"}

// pgSeed is the base's schema and rows: a users table with two sensitive
// columns (one NOT NULL and unique), a partitioned table whose parent is
// marked, a table referencing users, a materialized view over a masked
// column, a trigger that must not fire on the load, and a sequence.
const pgSeed = `
CREATE TABLE users (id serial PRIMARY KEY, email text NOT NULL UNIQUE, phone varchar(20), name text NOT NULL, born date NOT NULL);
CREATE TABLE orders (id serial PRIMARY KEY, user_id int NOT NULL REFERENCES users(id), total numeric NOT NULL);
CREATE TABLE events (id int NOT NULL, at date NOT NULL, ip inet NOT NULL, note text) PARTITION BY RANGE (at);
CREATE TABLE events_2026 PARTITION OF events FOR VALUES FROM ('2026-01-01') TO ('2027-01-01');
CREATE TABLE audit (n int);
CREATE FUNCTION log_order() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN INSERT INTO audit VALUES (1); RETURN NEW; END';
CREATE TRIGGER orders_log AFTER INSERT ON orders FOR EACH ROW EXECUTE FUNCTION log_order();
INSERT INTO users (email, phone, name, born) VALUES ('ada@example.com', '555-1', 'Ada', '1815-12-10'), ('bob@example.com', NULL, 'Bob', '1990-01-01');
INSERT INTO orders (user_id, total) VALUES (1, 10), (2, 20), (1, 5);
INSERT INTO events VALUES (1, '2026-03-01', '10.0.0.1', 'secret note');
CREATE MATERIALIZED VIEW emails AS SELECT email FROM users;
`

// pgMarks marks users.email (NOT NULL, unique), users.phone (nullable),
// users.born (a NOT NULL date) and events.ip on the partitioned parent.
var pgMarks = []Column{{"public", "users", "email"}, {"public", "users", "phone"}, {"public", "users", "born"}, {"public", "events", "ip"}}

func pgLab(t *testing.T) *lab {
	l := newLab(t)
	l.start("base", pgImage, []string{"POSTGRES_PASSWORD=base-pw", "POSTGRES_DB=app"})
	l.start("preview", pgImage, []string{"POSTGRES_PASSWORD=preview-pw", "POSTGRES_DB=app"})
	l.waitFor("base", psqlBase)
	l.waitFor("preview", psqlBase)
	l.docker("exec", l.prefix+"-base", "psql", "-U", "postgres", "-d", "app", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-c", pgSeed)
	return l
}

// pgCopy dumps the base (masking marks) and loads the preview, failing
// the test on a job error.
func (l *lab) pgCopy(marks []Column) Stats {
	l.t.Helper()
	dump, _ := DumpScript(Postgres)
	out, code := l.job(pgImage, dump, DumpEnv(Postgres, Source{Host: "base", Port: 5432, User: "postgres", Password: "base-pw", Database: "app", Masked: marks, MaxBytes: 1 << 30}))
	if code != 0 {
		l.t.Fatalf("dump exit %d:\n%s", code, out)
	}
	stats, err := ParseStats(out)
	if err != nil {
		l.t.Fatalf("%v\n%s", err, out)
	}
	load, _ := LoadScript(Postgres)
	if out, code := l.job(pgImage, load, LoadEnv(Postgres, Target{Host: "preview", Port: 5432, User: "postgres", Password: "preview-pw", Database: "app"})); code != 0 {
		l.t.Fatalf("load exit %d:\n%s", code, out)
	}
	return stats
}

func TestPostgresCopy(t *testing.T) {
	l := pgLab(t)
	stats := l.pgCopy(nil)
	if stats.Tables != 0 || stats.Columns != 0 || stats.Bytes == 0 {
		t.Errorf("stats = %+v, want a size and nothing masked", stats)
	}
	for _, q := range []string{
		"SELECT id, email, phone, name, born FROM users ORDER BY id",
		"SELECT * FROM orders ORDER BY id",
		"SELECT * FROM events",
		"SELECT * FROM emails ORDER BY 1",
	} {
		if base, preview := l.sql("base", psqlBase, q), l.sql("preview", psqlBase, q); base != preview {
			t.Errorf("%s: preview has\n%s\nbase has\n%s", q, preview, base)
		}
	}
	if base, got := l.sql("base", psqlBase, "SELECT count(*) FROM audit"), l.sql("preview", psqlBase, "SELECT count(*) FROM audit"); got != base {
		t.Errorf("audit rows = %s, base has %s; the trigger fired on the load", got, base)
	}
	if got := l.sql("preview", psqlBase, "SELECT nextval('users_id_seq')"); got != "3" {
		t.Errorf("users_id_seq next = %s, want 3", got)
	}
}

func TestPostgresMaskedCopy(t *testing.T) {
	l := pgLab(t)
	stats := l.pgCopy(pgMarks)
	// users (3 columns) and the partition events_2026 (ip).
	if stats.Tables != 2 || stats.Columns != 4 {
		t.Errorf("stats = %+v, want 2 tables and 4 columns masked", stats)
	}
	rows := strings.Split(l.sql("preview", psqlBase, "SELECT id, email, coalesce(phone, '<null>'), name, born FROM users ORDER BY id"), "\n")
	if len(rows) != 2 {
		t.Fatalf("users = %q, want 2 rows", rows)
	}
	for i, want := range []string{"Ada", "Bob"} {
		f := strings.Split(rows[i], "\t")
		if f[3] != want {
			t.Errorf("row %d name = %q, want %q: unmasked columns are copied", i, f[3], want)
		}
		if strings.Contains(f[1], "example.com") || !strings.HasPrefix(f[1], "masked-") {
			t.Errorf("row %d email = %q, want a masked value", i, f[1])
		}
		if f[2] != "<null>" {
			t.Errorf("row %d phone = %q, want NULL", i, f[2])
		}
		if f[4] != "1970-01-01" {
			t.Errorf("row %d born = %q, want the epoch", i, f[4])
		}
	}
	if got := l.sql("preview", psqlBase, "SELECT ip || ' ' || note FROM events"); got != "0.0.0.0/32 secret note" {
		t.Errorf("events = %q, want the ip masked through the partition and the note kept", got)
	}
	if got := l.sql("preview", psqlBase, "SELECT count(*) FROM emails WHERE email LIKE '%example.com'"); got != "0" {
		t.Errorf("the materialized view still holds %s real emails", got)
	}
	if got := l.sql("preview", psqlBase, "SELECT count(*) FROM orders JOIN users ON users.id = orders.user_id"); got != "3" {
		t.Errorf("joined orders = %s, want 3: the foreign keys hold", got)
	}
	// No real value is on the preview's disk, nor in the scratch volume.
	for _, where := range []string{l.prefix + "-preview:/var/lib/postgresql/data"} {
		ctr, dir, _ := strings.Cut(where, ":")
		if out, _ := exec.Command("docker", "exec", ctr, "grep", "-rl", "ada@example.com", dir).CombinedOutput(); strings.TrimSpace(string(out)) != "" {
			t.Errorf("%s holds a sensitive value in %s", ctr, out)
		}
	}
	out, _ := l.job(pgImage, `grep -rl 'ada@example.com\|10.0.0.1' "$D" || true`, map[string]string{"D": Path})
	if strings.TrimSpace(out) != "" {
		t.Errorf("the scratch volume holds sensitive values: %s", out)
	}
}

func TestPostgresMaskedCopyFailsClosed(t *testing.T) {
	l := pgLab(t)
	// A NOT NULL column of a type with no stand-in value is read as NULL,
	// so the load refuses it rather than copy the value.
	l.sql("base", psqlBase, "CREATE TABLE points (p point NOT NULL); INSERT INTO points VALUES ('(1,2)')")
	dump, _ := DumpScript(Postgres)
	out, code := l.job(pgImage, dump, DumpEnv(Postgres, Source{Host: "base", Port: 5432, User: "postgres", Password: "base-pw", Database: "app",
		Masked: []Column{{"public", "points", "p"}}, MaxBytes: 1 << 30}))
	if code != 0 {
		t.Fatalf("dump exit %d:\n%s", code, out)
	}
	load, _ := LoadScript(Postgres)
	out, code = l.job(pgImage, load, LoadEnv(Postgres, Target{Host: "preview", Port: 5432, User: "postgres", Password: "preview-pw", Database: "app"}))
	if code == 0 || !strings.Contains(out, "not-null") {
		t.Fatalf("load exit %d, want a not-null failure:\n%s", code, out)
	}
}

func TestPostgresTooBig(t *testing.T) {
	l := pgLab(t)
	dump, _ := DumpScript(Postgres)
	out, code := l.job(pgImage, dump, DumpEnv(Postgres, Source{Host: "base", Port: 5432, User: "postgres", Password: "base-pw", Database: "app", MaxBytes: 1 << 20}))
	if code != ExitTooBig || !strings.Contains(out, "past the 1 MB") {
		t.Fatalf("exit %d, want %d saying it's too big:\n%s", code, ExitTooBig, out)
	}
}

var mysqlBase = []string{"env", "MYSQL_PWD=base-pw", "mysql", "-uroot", "-N", "-B", "app", "-e"}

func TestMySQLMaskedCopy(t *testing.T) {
	l := newLab(t)
	env := func(pw string) []string { return []string{"MYSQL_ROOT_PASSWORD=" + pw, "MYSQL_DATABASE=app"} }
	l.start("base", mysqlImage, env("base-pw"))
	l.start("preview", mysqlImage, env("preview-pw"))
	l.waitFor("base", mysqlBase)
	previewClient := []string{"env", "MYSQL_PWD=preview-pw", "mysql", "-uroot", "-N", "-B", "app", "-e"}
	l.waitFor("preview", previewClient)
	l.sql("base", mysqlBase, `
CREATE TABLE users (id int PRIMARY KEY AUTO_INCREMENT, email varchar(100) NOT NULL UNIQUE, phone varchar(20), name text NOT NULL, avatar blob, born date NOT NULL, code char(4) NOT NULL);
CREATE TABLE orders (id int PRIMARY KEY, user_id int NOT NULL, note text, FOREIGN KEY (user_id) REFERENCES users(id));
CREATE TABLE audit (n int);
CREATE TRIGGER orders_log AFTER INSERT ON orders FOR EACH ROW INSERT INTO audit VALUES (1);
INSERT INTO users (email, phone, name, avatar, born, code) VALUES ('ada@example.com', '555-1', 'Ada ''the first''\nLovelace', X'00FF10', '1815-12-10', 'ABCD'), ('bob@example.com', NULL, 'Bob', NULL, '1990-01-01', 'WXYZ');
INSERT INTO orders VALUES (1, 1, 'tab\there'), (2, 2, NULL);`)
	dump, _ := DumpScript(MySQL)
	out, code := l.job(mysqlImage, dump, DumpEnv(MySQL, Source{Host: "base", Port: 3306, Password: "base-pw", MaxBytes: 1 << 30,
		Masked: []Column{{"app", "users", "email"}, {"app", "users", "phone"}, {"app", "users", "born"}, {"app", "users", "code"}}}))
	if code != 0 {
		t.Fatalf("dump exit %d:\n%s", code, out)
	}
	if stats, err := ParseStats(out); err != nil || stats.Tables != 1 || stats.Columns != 4 {
		t.Errorf("stats = %+v (%v), want 1 table and 4 columns", stats, err)
	}
	load, _ := LoadScript(MySQL)
	if out, code := l.job(mysqlImage, load, LoadEnv(MySQL, Target{Host: "preview", Port: 3306, Password: "preview-pw"})); code != 0 {
		t.Fatalf("load exit %d:\n%s", code, out)
	}
	got := l.sql("preview", previewClient, "SELECT id, email, COALESCE(phone, '<null>'), name, HEX(avatar), born, code FROM users ORDER BY id")
	rows := strings.Split(got, "\n")
	if len(rows) != 2 {
		t.Fatalf("users = %q, want 2 rows", got)
	}
	f := strings.Split(rows[0], "\t")
	if strings.Contains(got, "example.com") || !strings.HasPrefix(f[1], "masked-") || f[2] != "<null>" || f[5] != "1970-01-01" || len(f[6]) != 4 || f[6] == "ABCD" {
		t.Errorf("users = %q, want email, phone, born and code masked", got)
	}
	if f[3] != `Ada 'the first'\nLovelace` || f[4] != "00FF10" {
		t.Errorf("unmasked = %q %q, want them as in the base", f[3], f[4])
	}
	if got := l.sql("preview", previewClient, "SELECT COUNT(*) FROM orders"); got != "2" {
		t.Errorf("orders = %s, want 2", got)
	}
	if base, got := l.sql("base", mysqlBase, "SELECT COUNT(*) FROM audit"), l.sql("preview", previewClient, "SELECT COUNT(*) FROM audit"); got != base {
		t.Errorf("audit = %s, base has %s; the trigger fired on the load", got, base)
	}
	if got := l.sql("preview", previewClient, "SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema = 'app'"); got != "1" {
		t.Errorf("triggers = %s, want the trigger copied", got)
	}
	out, _ = l.job(mysqlImage, `grep -rl 'ada@example.com' "$D" || true`, map[string]string{"D": Path})
	if strings.TrimSpace(out) != "" {
		t.Errorf("the scratch volume holds sensitive values: %s", out)
	}
}

func TestRedisCopy(t *testing.T) {
	l := newLab(t)
	l.start("base", redisImage, nil, "redis-server", "--requirepass", "base-pw", "--appendonly", "yes")
	cli := []string{"redis-cli", "-a", "base-pw", "--no-auth-warning"}
	for range 60 {
		if out, _ := exec.Command("docker", append(append([]string{"exec", l.prefix + "-base"}, cli...), "PING")...).Output(); strings.TrimSpace(string(out)) == "PONG" {
			break
		}
		time.Sleep(time.Second)
	}
	l.docker(append(append([]string{"exec", l.prefix + "-base"}, cli...), "SET", "greeting", "hello")...)
	dump, _ := DumpScript(Redis)
	out, code := l.job(redisImage, dump, DumpEnv(Redis, Source{Host: "base", Port: 6379, Password: "base-pw", MaxBytes: 1 << 30}))
	if code != 0 {
		t.Fatalf("dump exit %d:\n%s", code, out)
	}
	l.docker("volume", "create", l.prefix+"-data")
	load, _ := LoadScript(Redis)
	if out, code := l.job(redisImage, load, LoadEnv(Redis, Target{DataDir: "/data"}), l.prefix+"-data:/data"); code != 0 {
		t.Fatalf("load exit %d:\n%s", code, out)
	}
	l.docker("run", "-d", "--name", l.prefix+"-preview", "-v", l.prefix+"-data:/data", redisImage, "redis-server", "--appendonly", "yes")
	for range 60 {
		out, _ := exec.Command("docker", "exec", l.prefix+"-preview", "redis-cli", "GET", "greeting").Output()
		if strings.TrimSpace(string(out)) == "hello" {
			return
		}
		time.Sleep(time.Second)
	}
	logs, _ := exec.Command("docker", "logs", l.prefix+"-preview").CombinedOutput()
	t.Fatalf("the preview's Redis has no greeting:\n%s", logs)
}
