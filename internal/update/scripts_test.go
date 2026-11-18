package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// The scripts that turn a version into a URL or a file name. Each carries
// the same release_version function.
var versionScripts = []string{"../../deploy/install.sh", "../../scripts/release/install-cli.sh", "../../contrib/github-action/deploy.sh", bootstrapServer}

// shellFunc cuts the function name out of the script at path: a subshell
// body, from "name() (" to the closing parenthesis on its own line.
func shellFunc(t *testing.T, path, name string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fn := regexp.MustCompile(`(?ms)^` + name + `\(\) \(\n.*?^\)\n`).Find(b)
	if fn == nil {
		t.Fatalf("%s has no %s() ( ... ) function", path, name)
	}
	return string(fn)
}

// The install scripts accept exactly the versions IsRelease does, so a
// version that reaches a URL or a file name in a script is one dock would
// take too, and nothing else: no path, space, quote or line break.
func TestScriptsShareVersionGrammar(t *testing.T) {
	versions := []string{
		// Releases, pre-releases, and what e2e and builds between tags stamp.
		"v0.58.0", "v1.2.3", "v10.20.30", "v0.56.0-rc.1", "v0.58.1-nightly.20261117.g3c5dc9a", "v0.0.0-dryrun.12",
		"v0.0.0-fresh", "v0.0.0-dev", "v1.2.3-4-gabc1234", "v1.2.3-4-gabc1234-dirty", "v1.2.3-0", "v1.2.3-a.0.b", "v1.2.3--", "v1.2.3-0a",
		// Not versions.
		"", "latest", "dev", "1.2.3", "V1.2.3", "v1", "v1.2", "v1.2.3.4", "v1..3", "v.2.3", "v1.2.x", "-v1.2.3", "v-1.2.3",
		"v01.2.3", "v1.02.3", "v1.2.03", "v1.2.3-", "v1.2.3-rc.", "v1.2.3-.rc", "v1.2.3-rc..1", "v1.2.3-01", "v1.2.3-rc.01",
		"v1.2.3+build", "v1.2.3-rc_1", "v1.2.3-ü",
		// Versions with something attached.
		"v1.2.3/../x", "v0.1.0/../../../../../other/repo/releases/download/v1", "../v1.2.3", "v1.2.3-rc/1", "v1.2.3 ", " v1.2.3", "v1.2.3 x",
		"v1.2.3\n", "v1.2.3\nv1.2.4", "x\nv1.2.3", "v1.2.3\r", "v1.2.3\t", `v1.2.3"`, "v1.2.3'", `v1.2.3\`, "v1.2.3;id", "v1.2.3$(id)",
		"v1.2.3?x", "v1.2.3#x", "v1.2.3%2f..%2fx", "v1.2.3*",
	}
	fn := shellFunc(t, versionScripts[0], "release_version")
	for _, path := range versionScripts[1:] {
		if shellFunc(t, path, "release_version") != fn {
			t.Errorf("release_version in %s differs from %s's: keep them the same", path, versionScripts[0])
		}
	}
	const loop = `for v do if release_version "$v"; then echo yes; else echo no; fi; done`
	for _, shell := range []string{"sh", "bash"} {
		out, err := exec.Command(shell, append([]string{"-c", fn + loop, shell}, versions...)...).Output()
		if err != nil {
			t.Fatalf("%s: %v", shell, err)
		}
		got := strings.Fields(string(out))
		if len(got) != len(versions) {
			t.Fatalf("%s answered for %d of %d versions", shell, len(got), len(versions))
		}
		for i, v := range versions {
			if (got[i] == "yes") != IsRelease(v) {
				t.Errorf("%s: release_version %q says %s, IsRelease says %t", shell, v, got[i], IsRelease(v))
			}
		}
	}
}

// offline is a directory to run an install script in without the
// network: the stand-in curl (contrib/github-action/test/fake-curl.sh) is
// first on PATH, answers for GitHub from the files under github/ and logs
// what it was asked.
type offline struct{ dir string }

func newOffline(t *testing.T) offline {
	t.Helper()
	if CLIAsset(runtime.GOOS, runtime.GOARCH) == "" {
		t.Skip("the install scripts run on Linux and macOS")
	}
	o := offline{t.TempDir()}
	fake, err := os.ReadFile("../../contrib/github-action/test/fake-curl.sh")
	if err != nil {
		t.Fatal(err)
	}
	o.write(t, "fake/curl", string(fake), 0o755)
	return o
}

// write puts a file under the directory.
func (o offline) write(t *testing.T, name, content string, perm os.FileMode) {
	t.Helper()
	path := filepath.Join(o.dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

// env is a script's whole environment: nothing of the test's own but PATH.
func (o offline) env(extra ...string) []string {
	return append([]string{
		"PATH=" + filepath.Join(o.dir, "fake") + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + o.dir, "DOCK_INSTALL_DIR=" + filepath.Join(o.dir, "bin"),
		"FAKE_GITHUB=" + filepath.Join(o.dir, "github"), "FAKE_CURL_LOG=" + filepath.Join(o.dir, "curl.log"),
	}, extra...)
}

// asked is what curl was asked for, a "<--proto or any> <url>" line each.
func (o offline) asked() string {
	b, _ := os.ReadFile(filepath.Join(o.dir, "curl.log"))
	return string(b)
}

// installed reports whether install-cli.sh put a dock in place.
func (o offline) installed() bool {
	_, err := os.Stat(filepath.Join(o.dir, "bin", "dock"))
	return err == nil
}

// unsignedCLI publishes a release of the CLI as those before
// FirstSignedRelease are: the binary and its SHA256SUMS, no signature.
// latest makes it GitHub's latest release too.
func (o offline) unsignedCLI(t *testing.T, version string, latest bool) {
	t.Helper()
	asset := CLIAsset(runtime.GOOS, runtime.GOARCH)
	bin := "#!/bin/sh\necho dock version " + version + "\n"
	rel := "github/NordTempo/tempoDock/releases/"
	o.write(t, rel+"download/"+version+"/"+asset, bin, 0o644)
	sum := sha256.Sum256([]byte(bin))
	o.write(t, rel+"download/"+version+"/"+SumsAsset, hex.EncodeToString(sum[:])+"  "+asset+"\n", 0o644)
	if latest {
		o.write(t, rel+"latest", "https://github.com/NordTempo/tempoDock/releases/tag/"+version, 0o644)
	}
}

// installCLI runs install-cli.sh with args and the variables in vars.
func (o offline) installCLI(vars []string, args ...string) (string, error) {
	cmd := exec.Command("sh", append([]string{"../../scripts/release/install-cli.sh"}, args...)...)
	cmd.Env = o.env(vars...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// health is a Dock's answer on /api/v1/health, saying it runs version.
func health(version string) string {
	b, _ := json.Marshal(map[string]string{"status": "ok", "version": version})
	return "FAKE_HEALTH=" + string(b)
}

// install-cli.sh downloads nothing for a version that isn't a release
// version, whether a server's answer, the latest release's address or
// DOCK_VERSION names it.
func TestInstallCLITakesOnlyReleaseVersions(t *testing.T) {
	type run struct {
		name string
		vars []string
		args []string
	}
	var runs []run
	for _, v := range []string{"v0.1.0/../../../../../other/repo/releases/download/v1", "v1.2.3/../x", "v1.2.3 x", "v1.2.3\n", `v1.2.3"x`, "v1.2.3'", "v1.2.3.."} {
		runs = append(runs, run{"a server running " + v, []string{health(v)}, []string{"http://dock.test"}})
	}
	for _, v := range []string{"v1.2.3/../x", "v1.2.3\n", "v1.2.3 ", "1.2.3", "../v1.2.3"} {
		runs = append(runs, run{"DOCK_VERSION " + v, []string{"DOCK_VERSION=" + v}, nil})
	}
	runs = append(runs, run{name: "the latest release's address"})
	for _, r := range runs {
		t.Run(r.name, func(t *testing.T) {
			o := newOffline(t)
			o.write(t, "github/NordTempo/tempoDock/releases/latest", "https://github.com/NordTempo/tempoDock/releases/tag/v1.2.3%2F..%2Fx", 0o644)
			out, err := o.installCLI(r.vars, r.args...)
			if err == nil || !strings.Contains(out, "is not a release version") || !strings.Contains(out, "DOCK_VERSION=") {
				t.Errorf("want a refusal that says how to pin a version, got %v: %s", err, out)
			}
			if strings.Contains(o.asked(), "/releases/download/") || o.installed() {
				t.Errorf("it went on to download (installed: %t):\n%s", o.installed(), o.asked())
			}
		})
	}
}

// A release from before signing installs, with a warning, only when
// DOCK_VERSION names it and DOCK_REQUIRE_SIGNATURE isn't 1: never because
// a server runs it or GitHub calls it the latest. Downloads from GitHub
// are https only; a server's own address may be http.
func TestInstallCLIUnsignedOnlyWhenPinned(t *testing.T) {
	const old = "v0.52.0"
	for _, c := range []struct {
		name      string
		vars      []string
		args      []string
		installed bool
		says      string
	}{
		{"the server's version", []string{health(old)}, []string{"http://dock.test"}, false, "DOCK_VERSION=" + old},
		{"the latest release", nil, nil, false, "DOCK_VERSION=" + old},
		{"DOCK_VERSION=latest", []string{"DOCK_VERSION=latest"}, nil, false, "DOCK_VERSION=" + old},
		{"pinned", []string{"DOCK_VERSION=" + old}, nil, true, "warning: release " + old + " predates signed releases"},
		{"pinned, a signature required", []string{"DOCK_VERSION=" + old, "DOCK_REQUIRE_SIGNATURE=1"}, nil, false, "installs signed releases only"},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := newOffline(t)
			o.unsignedCLI(t, old, true)
			out, err := o.installCLI(c.vars, c.args...)
			if (err == nil) != c.installed || o.installed() != c.installed || !strings.Contains(out, c.says) {
				t.Errorf("installed %t (%v), want %t and %q in: %s", o.installed(), err, c.installed, c.says, out)
			}
			for line := range strings.Lines(o.asked()) {
				if github := strings.Contains(line, "github.com"); github != strings.HasPrefix(line, "=https ") {
					t.Errorf("only GitHub is https only: %s", line)
				}
			}
		})
	}
}

// installSh runs one of deploy/install.sh's functions: the script without
// its last line, which calls main (and needs root).
func (o offline) installSh(t *testing.T, vars []string, call ...string) (string, error) {
	t.Helper()
	const path = "../../deploy/install.sh"
	if b, err := os.ReadFile(path); err != nil || !strings.HasSuffix(string(b), "\nmain \"$@\"\n") {
		t.Fatalf("%s no longer ends with its call of main (%v)", path, err)
	}
	cmd := exec.Command("bash", append([]string{"-c", `src="$(sed '$d' "$1")"; shift; eval "$src"; "$@"`, "bash", path}, call...)...)
	cmd.Env = o.env(vars...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// install.sh asks GitHub only about release versions, takes only one for
// an answer, and over https only.
func TestInstallShTakesOnlyReleaseVersions(t *testing.T) {
	const latest = "github/api/repos/NordTempo/tempoDock/releases/latest"
	o := newOffline(t)
	out, err := o.installSh(t, nil, "release_info", "v1.2.3/../x", o.dir)
	if err == nil || !strings.Contains(out, "is not a release version") || o.asked() != "" {
		t.Errorf("a version with a path: %v: %s\nasked: %s", err, out, o.asked())
	}
	o.write(t, latest, `{"url": "https://api.github.com/x", "tag_name": "v1.2.3/../x", "assets": []}`, 0o644)
	if out, err := o.installSh(t, nil, "release_info", "latest", o.dir); err == nil || !strings.Contains(out, "is not a release version") {
		t.Errorf("GitHub answering with a path: %v: %s", err, out)
	}
	o.write(t, latest, `{"url": "https://api.github.com/x", "tag_name": "v0.58.0", "assets": []}`, 0o644)
	if out, err := o.installSh(t, nil, "release_info", "latest", o.dir); err != nil || out != "v0.58.0\n" {
		t.Errorf("the latest release: %v: %q", err, out)
	}
	if want := "=https https://api.github.com/repos/NordTempo/tempoDock/releases/latest\n"; o.asked() != want+want {
		t.Errorf("asked:\n%swant twice: %s", o.asked(), want)
	}
}

// install.sh takes a release from before signing on its SHA256SUMS alone
// only when DOCK_VERSION names it and DOCK_REQUIRE_SIGNATURE isn't 1.
func TestInstallShUnsignedOnlyWhenPinned(t *testing.T) {
	const old = "v0.52.0"
	o := newOffline(t)
	o.write(t, "release/"+SumsAsset, strings.Repeat("0", 64)+"  "+InstallAsset(old)+"\n", 0o644)
	for _, c := range []struct {
		name string
		vars []string
		ok   bool
		says string
	}{
		{"the default version", nil, false, "DOCK_VERSION=" + old},
		{"DOCK_VERSION=latest", []string{"DOCK_VERSION=latest"}, false, "DOCK_VERSION=" + old},
		{"pinned", []string{"DOCK_VERSION=" + old}, true, "warning: release " + old + " predates signed releases"},
		{"pinned, a signature required", []string{"DOCK_VERSION=" + old, "DOCK_REQUIRE_SIGNATURE=1"}, false, "DOCK_REQUIRE_SIGNATURE=0"},
	} {
		out, err := o.installSh(t, c.vars, "verify_sums", filepath.Join(o.dir, "release"), old)
		if (err == nil) != c.ok || !strings.Contains(out, c.says) {
			t.Errorf("%s: passed %t (%v), want %t and %q in: %s", c.name, err == nil, err, c.ok, c.says, out)
		}
	}
}

// install.sh writes this server's address into root's config file, and
// may have it from a service's answer: only an address gets there. An
// answer that is anything else counts as none, and a PUBLIC_IP that is
// anything else stops the install.
func TestInstallShTakesOnlyAnAddress(t *testing.T) {
	o := newOffline(t)
	for answer, want := range map[string]string{
		"203.0.113.7":   "203.0.113.7\n",
		"203.0.113.7\n": "203.0.113.7\n",
		"":              "",
		"203.0.113.7\"\n[backup]\nrestic_image = \"x/y:z\"\n[zz]\nq = \"": "",
		"<html>busy</html>": "",
		"203.0.113.7 # x":   "",
	} {
		if out, err := o.installSh(t, []string{"FAKE_IPIFY=" + answer}, "public_address"); err != nil || out != want {
			t.Errorf("the service answering %q: %q, %v; want %q", answer, out, err, want)
		}
	}
	// With no answer at all the address is left empty, as before.
	if out, err := o.installSh(t, nil, "public_address"); err != nil || out != "" {
		t.Errorf("the service not answering: %q, %v", out, err)
	}
	if out, err := o.installSh(t, []string{"PUBLIC_IP=2001:db8::7"}, "public_address"); err != nil || out != "2001:db8::7\n" {
		t.Errorf("PUBLIC_IP set to an address: %q, %v", out, err)
	}
	if out, err := o.installSh(t, []string{"PUBLIC_IP=203.0.113.7\"\n[zz]"}, "public_address"); err == nil || !strings.Contains(out, "PUBLIC_IP must be") {
		t.Errorf("PUBLIC_IP set to something else: %q, %v", out, err)
	}
}

// install.sh and CI install the same Caddy tarball by the same checksum:
// a version or a checksum changed in one and not the other fails here.
func TestInstallShCaddyMatchesCI(t *testing.T) {
	read := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	ci := regexp.MustCompile(`caddyserver/caddy v([0-9.]+) caddy_[0-9.]+_linux_amd64\.tar\.gz ([0-9a-f]{64})`).FindStringSubmatch(read("../../.github/workflows/ci.yml"))
	if ci == nil {
		t.Fatal("ci.yml no longer installs Caddy by a checksum")
	}
	script := read("../../deploy/install.sh")
	if !strings.Contains(script, `CADDY_VERSION="${CADDY_VERSION:-`+ci[1]+`}"`) {
		t.Errorf("install.sh's default Caddy isn't CI's %s", ci[1])
	}
	if !strings.Contains(script, ci[1]+"/amd64) caddy_sha256="+ci[2]+" ;;") {
		t.Errorf("install.sh has no checksum %s for Caddy %s on amd64, as CI has", ci[2], ci[1])
	}
	if !regexp.MustCompile(regexp.QuoteMeta(ci[1]) + `/arm64\) caddy_sha256=[0-9a-f]{64} ;;`).MatchString(script) {
		t.Errorf("install.sh has no checksum for Caddy %s on arm64", ci[1])
	}
}

// A re-run of install.sh (an update) moves an existing config's [buildkit]
// image to the release's pinned one, and leaves an operator's own choice:
// an image from another repository, or a newer BuildKit.
func TestInstallShRefreshesTheBuildKitImage(t *testing.T) {
	script, err := os.ReadFile("../../deploy/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	pinned := regexp.MustCompile(`(?m)^BUILDKIT_IMAGE="(moby/buildkit:v[0-9.]+@sha256:[0-9a-f]{64})"$`).FindSubmatch(script)
	if pinned == nil {
		t.Fatal("install.sh no longer pins moby/buildkit by digest")
	}
	want := string(pinned[1])
	o := newOffline(t)
	for image, after := range map[string]string{
		"moby/buildkit:v0.20.0":                                   want,
		"moby/buildkit:v0.20.0@sha256:" + strings.Repeat("a", 64): want,
		want: want,
		"moby/buildkit:v9.0.0@sha256:" + strings.Repeat("b", 64): "moby/buildkit:v9.0.0@sha256:" + strings.Repeat("b", 64),
		"moby/buildkit:master":             "moby/buildkit:master",
		"registry.example/buildkit:v0.1.0": "registry.example/buildkit:v0.1.0",
	} {
		conf := "listen = \"127.0.0.1:7070\"\n\n[buildkit]\nenabled = true\nimage = \"" + image + "\"\nmemory_mb = 4096\n\n[network]\npool = \"10.213.0.0/16\"\n"
		o.write(t, "config.toml", conf, 0o644)
		path := filepath.Join(o.dir, "config.toml")
		if out, err := o.installSh(t, nil, "refresh_buildkit_image", path); err != nil {
			t.Fatalf("%s: %v: %s", image, err, out)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if wantConf := strings.Replace(conf, image, after, 1); string(got) != wantConf {
			t.Errorf("%s: the config is now\n%s\nwant\n%s", image, got, wantConf)
		}
	}
}
