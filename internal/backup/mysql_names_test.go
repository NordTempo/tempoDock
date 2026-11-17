package backup

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// A MySQL server's database names are its tenant's: the backup job hands
// each to the dump tool as one argument, whatever it holds, so no part of
// a name is read as an option; and a name that starts with a dash, which
// the tool would read as one, stops the job before anything is dumped.
//
// The real script runs under /bin/sh with stub binaries: `mysql` answers
// SHOW DATABASES, `mysqldump` records its arguments, `restic` runs the
// command after `--`.
func TestMySQLBackupPassesDatabaseNamesWhole(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	bin, repo := filepath.Join(dir, "bin"), filepath.Join(dir, "repo")
	for _, d := range []string{bin, repo} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The shared local repository's config, as restic init writes it.
	config := filepath.Join(repo, "config")
	if err := os.WriteFile(config, []byte("restic repository config (every org's backups need it)"), 0o600); err != nil {
		t.Fatal(err)
	}
	argv := filepath.Join(dir, "mysqldump.argv")
	stub := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	hostile := "x --result-file=" + config
	names := func(list ...string) {
		stub("mysql", "printf '%s\\n' information_schema "+`"`+strings.Join(list, `" "`)+`"`+" mysql\n")
	}
	names("app", hostile, "with space")
	// mysqldump: record the arguments, and honour --result-file as the
	// real one does (it truncates the file and writes the dump there).
	stub("mysqldump", `for a in "$@"; do printf '%s\n' "$a" >>`+argv+`
  case "$a" in --result-file=*) echo "-- MySQL dump" >"${a#--result-file=}";; esac
done
`)
	stub("restic", `case "$1" in
cat) exit 0 ;;
backup) while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do shift; done; shift; "$@" >/dev/null
  echo '{"message_type":"summary","snapshot_id":"0123456789abcdef","total_bytes_processed":1}' ;;
snapshots) echo '[]' ;;
esac
`)
	script, err := BackupScript(apitypes.BackupMySQL)
	if err != nil {
		t.Fatal(err)
	}
	script = strings.ReplaceAll(script, BinDir+"/restic", filepath.Join(bin, "restic"))

	run := func() (string, error) {
		cmd := exec.Command("sh", "-c", script)
		cmd.Env = []string{
			"PATH=" + bin + ":/usr/bin:/bin",
			"DOCK_DB_HOST=db", "DOCK_DB_PORT=3306", "MYSQL_PWD=root-password",
			"RESTIC_REPOSITORY=" + repo, "RESTIC_PASSWORD=install-wide-password",
			"DOCK_TAG=dock-s7", "DOCK_KEEP_DAILY=7", "DOCK_KEEP_WEEKLY=4", "DOCK_KEEP_MONTHLY=6",
		}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	if out, err := run(); err != nil {
		t.Fatalf("the job: %v\n%s", err, out)
	}
	got, _ := os.ReadFile(argv)
	args := strings.Split(strings.TrimSpace(string(got)), "\n")
	at := slices.Index(args, "--databases")
	if at < 0 || !slices.Equal(args[at+1:], []string{"app", hostile, "with space"}) {
		t.Fatalf("the dump tool's arguments after --databases: %q", args)
	}
	if after, _ := os.ReadFile(config); !strings.HasPrefix(string(after), "restic repository config") {
		t.Fatalf("the repository's config was written over: %q", after)
	}

	// A name the tool would read as an option: nothing is dumped.
	_ = os.Remove(argv)
	names("app", "--result-file="+config)
	out, err := run()
	if err == nil || !strings.Contains(out, "starts with a dash") {
		t.Fatalf("a database named like an option: %v\n%s", err, out)
	}
	if _, err := os.Stat(argv); !os.IsNotExist(err) {
		t.Fatalf("the dump tool ran: %v", err)
	}
}
