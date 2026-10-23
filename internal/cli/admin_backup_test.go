//go:build !dockcli

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/cpbackup"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
)

// controlPlaneDir is a data dir with a database, a secrets key and a link
// key, as `dock serve` leaves it, and a config pointing at it.
func controlPlaneDir(t *testing.T) (dir, cfg string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(dir, "dock.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateOrg(context.Background(), store.CreateOrgParams{Slug: "test", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec("INSERT INTO projects (org_id, name) VALUES (1, 'shop')"); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	if err := os.WriteFile(filepath.Join(dir, cpbackup.SecretKeyFile), bytes.Repeat([]byte{1}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.LoadOrCreateIdentity(filepath.Join(dir, remote.ControlPlaneKeyFile)); err != nil {
		t.Fatal(err)
	}
	return dir, writeConfig(t, dir, "")
}

// `dock admin backup` writes an owner-only archive, never over a file,
// and `dock admin restore` brings it back on another data dir.
func TestAdminBackupAndRestore(t *testing.T) {
	setup(t)
	dir, cfg := controlPlaneDir(t)
	out := filepath.Join(t.TempDir(), "cp.tar.gz")
	code, _, stderr := exec(t, "admin", "backup", "--config", cfg, "-o", out)
	if code != 0 {
		t.Fatalf("backup: exit %d: %s", code, stderr)
	}
	fi, err := os.Stat(out)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("archive: %v, %v", fi.Mode(), err)
	}
	if _, err := os.Stat(out + ".partial"); err == nil {
		t.Error("the partial file is left behind")
	}
	if !strings.Contains(stderr, "every secret") {
		t.Errorf("no warning about the secrets: %s", stderr)
	}
	if code, _, _ := exec(t, "admin", "backup", "--config", cfg, "-o", out); code != int(output.ExitUsage) {
		t.Errorf("backup over an existing file: exit %d", code)
	}

	// The config comes out alone for install.sh, then the data.
	code, stdout, stderr := exec(t, "admin", "restore", "--print-config", out)
	if b, _ := os.ReadFile(cfg); code != 0 || stdout != string(b) {
		t.Fatalf("print-config: exit %d %q %s", code, stdout, stderr)
	}
	newDir := filepath.Join(t.TempDir(), "data")
	if code, _, stderr := exec(t, "admin", "restore", "--config", writeConfig(t, newDir, ""), out); code != 0 {
		t.Fatalf("restore: exit %d: %s", code, stderr)
	}
	for _, f := range []string{cpbackup.SecretKeyFile, remote.ControlPlaneKeyFile} {
		a, _ := os.ReadFile(filepath.Join(dir, f))
		b, err := os.ReadFile(filepath.Join(newDir, f))
		if err != nil || !bytes.Equal(a, b) {
			t.Errorf("%s not restored: %v", f, err)
		}
	}
	st, err := store.Open(context.Background(), filepath.Join(newDir, "dock.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	var action string
	if err := st.DB.QueryRow("SELECT action FROM audit_events ORDER BY id DESC LIMIT 1").Scan(&action); err != nil || action != "control_plane.restore" {
		t.Errorf("restore audit: %q, %v", action, err)
	}
}

// --move needs the daemon stopped, then retires the control plane: dock
// serve refuses to start with the exit code systemd doesn't restart on.
func TestAdminBackupMove(t *testing.T) {
	setup(t)
	dir, cfg := controlPlaneDir(t)
	out := filepath.Join(t.TempDir(), "move.tar.gz")

	unlock, err := cpbackup.Lock(dir) // a running dock serve
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := exec(t, "admin", "backup", "--config", cfg, "--move", "-o", out); code != int(output.ExitConflict) {
		t.Fatalf("--move under a running daemon: exit %d", code)
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("--move wrote an archive under a running daemon")
	}
	unlock()

	if code, _, stderr := exec(t, "admin", "backup", "--config", cfg, "--move", "-o", out); code != 0 {
		t.Fatalf("--move: exit %d: %s", code, stderr)
	}
	code, _, stderr := exec(t, "serve", "--config", cfg)
	if code != int(output.ExitMoved) || !strings.Contains(stderr, "moved to another server") {
		t.Fatalf("serve after --move: exit %d: %s", code, stderr)
	}
	if code, _, _ := exec(t, "admin", "backup", "--config", cfg, "--move", "-o", out+".2"); code == 0 {
		t.Error("moved twice")
	}
}

// `dock node set-control-plane` checks the address and respects the
// config's override.
func TestNodeSetControlPlane(t *testing.T) {
	setup(t)
	dir := t.TempDir()
	link := "server_id = 3\nserver = \"homelab\"\naddrs = [\"100.64.0.10:7443\"]\ncontrol_plane_key = \"sha256:" + strings.Repeat("ab", 32) + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, remote.StateFile), []byte(link), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := writeConfig(t, dir, "")
	if code, _, _ := exec(t, "node", "set-control-plane", "--config", cfg, "not-an-address"); code != int(output.ExitUsage) {
		t.Errorf("bad address: exit %d", code)
	}
	code, stdout, stderr := exec(t, "node", "set-control-plane", "--config", cfg, "100.64.0.20:7443")
	if code != 0 || !strings.Contains(stdout, "homelab") {
		t.Fatalf("exit %d: %s %s", code, stdout, stderr)
	}
	if l, err := remote.LoadLink(dir); err != nil || len(l.Addrs) != 1 || l.Addrs[0] != "100.64.0.20:7443" {
		t.Errorf("link = %+v, %v", l, err)
	}
	override := writeConfig(t, dir, "[node]\ncontrol_plane = \"100.64.0.30:7443\"\n")
	if code, _, _ := exec(t, "node", "set-control-plane", "--config", override, "100.64.0.20:7443"); code != int(output.ExitUsage) {
		t.Errorf("with [node] control_plane set: exit %d", code)
	}
}
