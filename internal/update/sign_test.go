package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// trustedTestKey is a new key that counts as Dock's until t ends.
func trustedTestKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	TrustForTest(t, pub)
	return priv
}

// sumsFor is a SHA256SUMS of release v: its install tarball and a binary.
func sumsFor(v string) []byte {
	var b strings.Builder
	for _, name := range []string{InstallAsset(v), BinaryAsset("amd64")} {
		h := sha256.Sum256([]byte(name))
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(h[:]), name)
	}
	return []byte(b.String())
}

func TestVerifySums(t *testing.T) {
	key := trustedTestKey(t)
	_, stranger, _ := ed25519.GenerateKey(nil)
	sums := sumsFor("v0.56.0")
	sig := []byte(Sign(key, sums))
	for _, c := range []struct {
		name, version string
		sums, sig     []byte
		signed        bool
		err           error
	}{
		{"signed", "v0.56.0", sums, sig, true, nil},
		{"another key's lines are skipped", "v0.56.0", sums, append([]byte(Sign(stranger, sums)+"junk\n"), sig...), true, nil},
		{"tampered sums", "v0.56.0", append(sums, "00  extra\n"...), sig, false, ErrBadSignature},
		{"signed by a stranger", "v0.56.0", sums, []byte(Sign(stranger, sums)), false, ErrBadSignature},
		{"unsigned", "v0.56.0", sums, nil, false, ErrUnsigned},
		{"unsigned pre-release of a signed version", "v0.56.0-rc.1", sumsFor("v0.56.0-rc.1"), nil, false, ErrUnsigned},
		{"unsigned, before signing", "v0.55.3", sumsFor("v0.55.3"), nil, false, nil},
	} {
		signed, err := VerifySums(c.version, c.sums, c.sig)
		if signed != c.signed || (c.err == nil) != (err == nil) || (c.err != nil && !errors.Is(err, c.err)) {
			t.Errorf("%s: signed %t, err %v; want %t, %v", c.name, signed, err, c.signed, c.err)
		}
	}
	// A signed SHA256SUMS can't be passed off as another release's.
	if _, err := VerifySums("v0.57.0", sums, sig); err == nil || !strings.Contains(err.Error(), "another release") {
		t.Errorf("v0.56.0's sums as v0.57.0: %v", err)
	}
}

// A release signed only by a key in [updates] trusted_keys verifies once
// TrustExtra has it, and is refused without it; a bad signature by that
// key is refused either way.
func TestTrustExtra(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(nil)
	t.Cleanup(func() { TrustExtra(nil) })
	sums := sumsFor("v0.56.1-nightly.20261117.gabcdef0")
	sig := []byte(Sign(key, sums))
	bad := []byte(Sign(key, append(sums, "00  extra\n"...)))
	if _, err := VerifySums("v0.56.1-nightly.20261117.gabcdef0", sums, sig); !errors.Is(err, ErrBadSignature) {
		t.Errorf("without the key: %v, want ErrBadSignature", err)
	}
	TrustExtra([]ed25519.PublicKey{pub})
	if signed, err := VerifySums("v0.56.1-nightly.20261117.gabcdef0", sums, sig); !signed || err != nil {
		t.Errorf("with the key: %t, %v", signed, err)
	}
	if _, err := VerifySums("v0.56.1-nightly.20261117.gabcdef0", sums, bad); !errors.Is(err, ErrBadSignature) {
		t.Errorf("a bad signature with the key: %v, want ErrBadSignature", err)
	}
}

// Rotation (ADR 0088): while both keys sign, a binary that knows either
// one verifies; once only the new key signs, a binary that knows only the
// old key refuses the release.
func TestKeyRotation(t *testing.T) {
	oldPub, oldKey, _ := ed25519.GenerateKey(nil)
	newPub, newKey, _ := ed25519.GenerateKey(nil)
	trust := func(keys ...ed25519.PublicKey) {
		releaseKeys = nil
		for _, k := range keys {
			releaseKeys = append(releaseKeys, base64.StdEncoding.EncodeToString(k))
		}
	}
	saved := releaseKeys
	t.Cleanup(func() { releaseKeys = saved })

	sums := sumsFor("v1.0.0")
	both := []byte(Sign(oldKey, sums) + Sign(newKey, sums))
	for name, keys := range map[string][]ed25519.PublicKey{"old binary": {oldPub}, "new binary": {oldPub, newPub}, "newest binary": {newPub}} {
		trust(keys...)
		if signed, err := VerifySums("v1.0.0", sums, both); !signed || err != nil {
			t.Errorf("%s, signed by both: %t, %v", name, signed, err)
		}
	}
	trust(oldPub)
	if _, err := VerifySums("v1.0.0", sums, []byte(Sign(newKey, sums))); !errors.Is(err, ErrBadSignature) {
		t.Errorf("old binary, signed by the new key only: %v, want ErrBadSignature", err)
	}
}

// The GitHub Action (contrib/github-action) carries its own copy of
// install-cli.sh, so it checks a release with the keys it was pinned
// with rather than the release's own script: the copy stays the same.
func TestActionInstallerIsInstallCLI(t *testing.T) {
	want, err := os.ReadFile("../../scripts/release/install-cli.sh")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("../../contrib/github-action/install-cli.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("contrib/github-action/install-cli.sh differs from scripts/release/install-cli.sh: cp scripts/release/install-cli.sh contrib/github-action/")
	}
}

// The install scripts check signatures with the same keys and the same
// first signed release as dock.
func TestScriptsShareKeys(t *testing.T) {
	var want []string
	for _, k := range releaseKeys {
		b, err := base64.StdEncoding.DecodeString(k)
		if err != nil || len(b) != ed25519.PublicKeySize {
			t.Fatalf("release key %q isn't a base64 Ed25519 key", k)
		}
		want = append(want, KeyID(b)+" "+k)
	}
	keysRe := regexp.MustCompile(`(?m)^(?:release_keys|RELEASE_KEYS)='([^']*)'$`)
	firstRe := regexp.MustCompile(`(?m)^(?:first_signed|FIRST_SIGNED)=(\S+)$`)
	for _, path := range []string{"../../deploy/install.sh", "../../scripts/release/install-cli.sh"} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		keys, first := keysRe.FindSubmatch(b), firstRe.FindSubmatch(b)
		if keys == nil || first == nil {
			t.Fatalf("%s: no release keys or first signed release", path)
		}
		if got := strings.Split(string(keys[1]), "\n"); !slices.Equal(got, want) {
			t.Errorf("%s: keys %q, want %q (internal/update/sign.go)", path, got, want)
		}
		if string(first[1]) != FirstSignedRelease {
			t.Errorf("%s: first signed release %s, want %s", path, first[1], FirstSignedRelease)
		}
	}
}
