package backup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// A MongoDB backup hands the password to mongodump in its config file,
// as YAML that reads back as the password whatever it holds, and never as
// an argument, where any process on the server could read it.
//
// The real script runs under /bin/sh with stub binaries: `mongodump`
// records its arguments and its config file, `restic` runs the command
// after `--`.
func TestMongoBackupKeepsThePasswordOutOfArguments(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	argv, conf := filepath.Join(dir, "argv"), filepath.Join(dir, "config")
	stub := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stub("mongodump", `for a in "$@"; do printf '%s\n' "$a" >>`+argv+`
  case "$a" in --config=*) cp "${a#--config=}" `+conf+`;; esac
done
`)
	stub("restic", `case "$1" in
cat) exit 0 ;;
backup) while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do shift; done; shift; "$@" >/dev/null
  echo '{"message_type":"summary","snapshot_id":"0123456789abcdef","total_bytes_processed":1}' ;;
snapshots) echo '[]' ;;
esac
`)
	script, err := BackupScript(apitypes.BackupMongoDB)
	if err != nil {
		t.Fatal(err)
	}
	script = strings.ReplaceAll(script, BinDir+"/restic", filepath.Join(bin, "restic"))
	script = strings.ReplaceAll(script, "/tmp/dock-mongo.yaml", filepath.Join(dir, "auth.yaml"))
	password := `it's "quoted" $HOME`
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"DOCK_DB_HOST=db", "DOCK_DB_PORT=27017", "DOCK_DB_USER=root", MongoPassword + "=" + password,
		"RESTIC_REPOSITORY=" + dir, "RESTIC_PASSWORD=p",
		"DOCK_TAG=dock-s7", "DOCK_KEEP_DAILY=7", "DOCK_KEEP_WEEKLY=4", "DOCK_KEEP_MONTHLY=6",
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the job: %v\n%s", err, out)
	}
	args, _ := os.ReadFile(argv)
	if strings.Contains(string(args), "quoted") {
		t.Errorf("the password is in mongodump's arguments:\n%s", args)
	}
	for _, w := range []string{"--host=db", "--port=27017", "--username=root", "--authenticationDatabase=admin", "--archive"} {
		if !strings.Contains(string(args), w+"\n") {
			t.Errorf("mongodump's arguments lack %s:\n%s", w, args)
		}
	}
	// YAML single quotes: a quote is doubled, nothing else is special.
	if got, _ := os.ReadFile(conf); string(got) != `password: 'it''s "quoted" $HOME'`+"\n" {
		t.Errorf("config file = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "auth.yaml")); !os.IsNotExist(err) {
		t.Errorf("the config file is left behind: %v", err)
	}
}
