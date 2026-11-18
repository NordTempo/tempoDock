package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/NordTempo/tempoDock/internal/update"
)

// install-cli.sh's ssh-keygen check passes a SHA256SUMS signed by a key it
// trusts, and nothing else: not a changed file, and not another key's
// signature. It runs the script's own function, so the signer and the
// script can't drift apart.
func TestSSHSignatureVerifiesInTheInstallScript(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	script, err := os.ReadFile("../install-cli.sh")
	if err != nil {
		t.Fatal(err)
	}
	fn := regexp.MustCompile(`(?ms)^ssh_verify_sums\(\) \{\n.*?^\}\n`).Find(script)
	if fn == nil {
		t.Fatal("install-cli.sh no longer has ssh_verify_sums")
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	_, other, _ := ed25519.GenerateKey(nil)
	keys := update.KeyID(pub) + " " + base64.StdEncoding.EncodeToString(pub)
	sums := []byte("0000  dock-install-v1.2.3.tar.gz\n")

	verify := func(sums []byte, sig string) error {
		dir := t.TempDir()
		for name, content := range map[string][]byte{update.SumsAsset: sums, sshSigAsset: []byte(sig)} {
			if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command("sh", "-c", `release_keys="$1"; `+string(fn)+` ssh_verify_sums "$2"`, "sh", keys, dir)
		out, err := cmd.CombinedOutput()
		t.Logf("%s", out)
		return err
	}
	if err := verify(sums, signSSH(priv, sums)); err != nil {
		t.Errorf("a release signed by a trusted key: %v", err)
	}
	// During a rotation the file holds a line per key.
	if err := verify(sums, signSSH(other, sums)+signSSH(priv, sums)); err != nil {
		t.Errorf("signed by two keys, one trusted: %v", err)
	}
	if err := verify(append(sums, "1111  extra\n"...), signSSH(priv, sums)); err == nil {
		t.Error("a SHA256SUMS changed after signing passed")
	}
	// Another key's signature, filed under the trusted key's id.
	forged := regexp.MustCompile(`^\S+`).ReplaceAllString(signSSH(other, sums), update.KeyID(pub))
	if err := verify(sums, forged); err == nil {
		t.Error("another key's signature passed")
	}
}
