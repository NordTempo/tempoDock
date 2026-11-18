package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The scripts the site serves at /install.sh and /cli.sh: one script, set
// to run the release's install.sh or its install-cli.sh.
const (
	bootstrapServer = "../../site/public/install.sh"
	bootstrapCLI    = "../../site/public/cli.sh"
)

var (
	bootstrapKeysRe    = regexp.MustCompile(`(?m)^release_keys='[^']*'$`)
	bootstrapMinimumRe = regexp.MustCompile(`(?m)^minimum=(\S+)$`)
)

// bootstrapMinimum is the oldest release the site's script takes as the
// latest.
func bootstrapMinimum(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(bootstrapServer)
	if err != nil {
		t.Fatal(err)
	}
	m := bootstrapMinimumRe.FindSubmatch(b)
	if m == nil {
		t.Fatalf("%s: no minimum release", bootstrapServer)
	}
	return string(m[1])
}

// cli.sh is install.sh with one line changed, so a fix to one is a fix to
// both: sed 's/^script=install\.sh$/script=install-cli.sh/' site/public/install.sh > site/public/cli.sh
func TestBootstrapsAreOneScript(t *testing.T) {
	server, err := os.ReadFile(bootstrapServer)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := os.ReadFile(bootstrapCLI)
	if err != nil {
		t.Fatal(err)
	}
	const line = "\nscript=install.sh\n"
	if bytes.Count(server, []byte(line)) != 1 {
		t.Fatalf("%s: want one %q line", bootstrapServer, strings.TrimSpace(line))
	}
	if want := bytes.Replace(server, []byte(line), []byte("\nscript=install-cli.sh\n"), 1); !bytes.Equal(cli, want) {
		t.Errorf("%s differs from %s by more than its script= line: make it again with the sed in this test's comment", bootstrapCLI, bootstrapServer)
	}
	// Every release the script accepts as the latest is a signed one.
	if min := bootstrapMinimum(t); !IsRelease(min) || !SignatureRequired(min) {
		t.Errorf("minimum %s must be a release from %s on", min, FirstSignedRelease)
	}
}

// signedScripts publishes release version as GitHub's latest: the two
// install scripts (each prints its name, arguments, $SEEN and what is left
// on stdin, and exits 7), SHA256SUMS and its signature by key.
func (o offline) signedScripts(t *testing.T, version string, key ed25519.PrivateKey) {
	t.Helper()
	rel := "github/NordTempo/tempoDock/releases/"
	sums := string(sumsFor(version))
	for _, name := range []string{"install.sh", "install-cli.sh"} {
		body := "echo \"ran " + name + " [$*] SEEN=${SEEN:-} stdin=[$(cat)]\"\nexit 7\n"
		o.write(t, rel+"download/"+version+"/"+name, body, 0o644)
		h := sha256.Sum256([]byte(body))
		sums += hex.EncodeToString(h[:]) + "  " + name + "\n"
	}
	o.write(t, rel+"download/"+version+"/"+SumsAsset, sums, 0o644)
	o.write(t, rel+"download/"+version+"/"+SigAsset, Sign(key, []byte(sums)), 0o644)
	o.write(t, rel+"latest", "https://github.com/NordTempo/tempoDock/releases/tag/"+version, 0o644)
}

// bootstrap pipes the site's script at path into shell, as `curl | shell
// -s -- args` does. With a key, the script trusts that key instead of
// Dock's: the script has no setting for it, so the test changes the line.
func (o offline) bootstrap(t *testing.T, path, shell string, key ed25519.PrivateKey, vars []string, args ...string) (string, int) {
	t.Helper()
	script, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if key != nil {
		pub, _ := key.Public().(ed25519.PublicKey)
		line := fmt.Sprintf("release_keys='%s %s'", KeyID(pub), base64.StdEncoding.EncodeToString(pub))
		if !bootstrapKeysRe.Match(script) {
			t.Fatalf("%s: no release keys", path)
		}
		script = bootstrapKeysRe.ReplaceAllLiteral(script, []byte(line))
	}
	cmd := exec.Command(shell, append([]string{"-s", "--"}, args...)...)
	cmd.Stdin = bytes.NewReader(script)
	cmd.Env = o.env(vars...)
	out, err := cmd.CombinedOutput()
	if err != nil && cmd.ProcessState == nil {
		t.Fatal(err)
	}
	return string(out), cmd.ProcessState.ExitCode()
}

// needOpenSSL3 skips a test that has the scripts check a signature where
// they can't: they need openssl pkeyutl -rawin.
func needOpenSSL3(t *testing.T) {
	t.Helper()
	if out, _ := exec.Command("openssl", "pkeyutl", "-help").CombinedOutput(); !strings.Contains(string(out), "-rawin") {
		t.Skip("the scripts check signatures with OpenSSL 3")
	}
}

// The site's script runs the latest release's script once its signature
// and checksum hold, with the arguments, the environment and the exit
// status passed through, and nothing of itself left on the script's stdin.
func TestBootstrapRunsTheVerifiedScript(t *testing.T) {
	needOpenSSL3(t)
	_, key, _ := ed25519.GenerateKey(nil)
	for _, c := range []struct{ path, shell, want string }{
		{bootstrapServer, "bash", "ran install.sh [--node --yes] SEEN=me stdin=[]\n"},
		{bootstrapCLI, "sh", "ran install-cli.sh [https://dock.example.com] SEEN=me stdin=[]\n"},
	} {
		o := newOffline(t)
		o.signedScripts(t, bootstrapMinimum(t), key)
		args := strings.Fields(strings.Trim(regexp.MustCompile(`\[.*?\]`).FindString(c.want), "[]"))
		if out, code := o.bootstrap(t, c.path, c.shell, key, []string{"SEEN=me"}, args...); out != c.want || code != 7 {
			t.Errorf("%s: exit %d, want 7, and %q, got %q", c.path, code, c.want, out)
		}
		for line := range strings.Lines(o.asked()) {
			if !strings.HasPrefix(line, "=https https://github.com/NordTempo/tempoDock/releases/") {
				t.Errorf("%s asked for more than the release, or not over https only: %s", c.path, line)
			}
		}
	}
}

// The site's script runs nothing it can't vouch for: a script or a
// SHA256SUMS changed after signing, another release's SHA256SUMS, a
// release signed with a key that isn't Dock's, one without a signature,
// and an older release than its minimum handed out as the latest.
func TestBootstrapRefuses(t *testing.T) {
	needOpenSSL3(t)
	_, key, _ := ed25519.GenerateKey(nil)
	good := bootstrapMinimum(t)
	dir := "github/NordTempo/tempoDock/releases/download/" + good + "/"
	for _, c := range []struct {
		name    string
		version string
		key     ed25519.PrivateKey // the key the script trusts; nil is Dock's
		change  func(o offline)
		says    string
	}{
		{"a changed script", good, key, func(o offline) { o.write(t, dir+"install.sh", "echo ran something else\n", 0o644) }, "install.sh does not match"},
		{"a changed SHA256SUMS", good, key, func(o offline) {
			b, _ := os.ReadFile(filepath.Join(o.dir, dir+SumsAsset))
			o.write(t, dir+SumsAsset, string(b)+strings.Repeat("0", 64)+"  extra\n", 0o644)
		}, "isn't signed by a Dock release key"},
		{"another release's SHA256SUMS", good, key, func(o offline) {
			old := sumsFor(FirstSignedRelease)
			o.write(t, dir+SumsAsset, string(old), 0o644)
			o.write(t, dir+SigAsset, Sign(key, old), 0o644)
		}, "belongs to another release"},
		{"a key that isn't Dock's", good, nil, func(offline) {}, "isn't signed by a Dock release key"},
		{"no signature", good, key, func(o offline) { o.write(t, dir+SigAsset, "", 0o644) }, "has no signature"},
		{"an older release as the latest", FirstSignedRelease, key, func(offline) {}, "older than " + good},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := newOffline(t)
			o.signedScripts(t, c.version, key)
			c.change(o)
			out, code := o.bootstrap(t, bootstrapServer, "bash", c.key, nil)
			if code != 1 || !strings.Contains(out, c.says) || strings.Contains(out, "ran ") {
				t.Errorf("exit %d, want 1 and %q, with nothing run: %s", code, c.says, out)
			}
			if c.version != good && strings.Contains(o.asked(), "/releases/download/") {
				t.Errorf("it downloaded from a release below its minimum:\n%s", o.asked())
			}
		})
	}
}

// Without OpenSSL 3 the signature can't be checked. The server's script
// stops unless DOCK_REQUIRE_SIGNATURE=0, as a piped install.sh does; the
// CLI's warns and goes on unless DOCK_REQUIRE_SIGNATURE=1, as
// install-cli.sh does.
func TestBootstrapWithoutOpenSSL3(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	for _, c := range []struct {
		path, shell string
		vars        []string
		ran         bool
		says        string
	}{
		{bootstrapServer, "bash", nil, false, "it needs OpenSSL 3"},
		{bootstrapServer, "bash", []string{"DOCK_REQUIRE_SIGNATURE=0"}, true, "warning: can't check install.sh's signature"},
		{bootstrapCLI, "sh", nil, true, "warning: can't check install-cli.sh's signature"},
		{bootstrapCLI, "sh", []string{"DOCK_REQUIRE_SIGNATURE=1"}, false, "it needs OpenSSL 3"},
	} {
		o := newOffline(t)
		// An openssl that knows no -rawin, as LibreSSL and OpenSSL 1.1.
		o.write(t, "fake/openssl", "#!/bin/sh\nexit 1\n", 0o755)
		o.signedScripts(t, bootstrapMinimum(t), key)
		out, code := o.bootstrap(t, c.path, c.shell, key, c.vars)
		if ran := strings.Contains(out, "ran "); ran != c.ran || !strings.Contains(out, c.says) || (code == 7) != c.ran {
			t.Errorf("%s %v: ran %t (exit %d), want %t and %q: %s", c.path, c.vars, ran, code, c.ran, c.says, out)
		}
	}
}
