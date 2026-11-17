package update

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"golang.org/x/mod/semver"
)

// SigAsset is the Ed25519 signature of a release's SHA256SUMS (ADR 0088):
// one line per signing key, "<key id> <base64 signature>", each over the
// exact bytes of SHA256SUMS. scripts/release/sign writes it; every
// download path checks it against releaseKeys.
const SigAsset = "SHA256SUMS.sig"

// FirstSignedRelease is the first release with a SigAsset. From it on
// (by its vX.Y.Z, so its pre-releases count too) a release without a
// valid signature is refused; an older one, which can only be checked
// against its own SHA256SUMS, is accepted with a warning so rolling back
// or pinning an old CLI still works.
const FirstSignedRelease = "v0.56.0"

// releaseKeys are the public keys Dock releases are signed with: raw
// Ed25519 keys, base64. A valid signature by any of them passes.
// Rotation (ADR 0088): add the new key here one release before the
// workflow signs with it, and keep signing with the old one too until
// the releases that only know it are no longer supported.
var releaseKeys = []string{
	"8uQjZq1kO37ezVv7EzN8eyrqOYXRGK2nsCRL/d3RSdU=", // bc6c45a08a65c136, 2026-11-17
}

// testKeys are keys TrustForTest added.
var (
	testKeysMu sync.Mutex
	testKeys   []ed25519.PublicKey
)

// Errors of VerifySums, for errors.Is.
var (
	// ErrUnsigned is a release from FirstSignedRelease on without a SigAsset.
	ErrUnsigned = errors.New("has no " + SigAsset)
	// ErrBadSignature is a SigAsset that no trusted key's signature in it verifies.
	ErrBadSignature = errors.New("isn't signed by a Dock release key")
)

// SignatureRequired reports whether release version must be signed: it
// is FirstSignedRelease or newer, ignoring pre-release suffixes.
func SignatureRequired(version string) bool {
	core := strings.SplitN(semver.Canonical(version), "-", 2)[0]
	return semver.Compare(core, FirstSignedRelease) >= 0
}

// KeyID names a public key in a SigAsset: the first 8 bytes of its
// SHA-256, hex. install.sh and install-cli.sh compute it the same way.
func KeyID(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:8])
}

// Sign returns priv's SigAsset line for sums, newline included.
func Sign(priv ed25519.PrivateKey, sums []byte) string {
	pub, _ := priv.Public().(ed25519.PublicKey)
	return KeyID(pub) + " " + base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sums)) + "\n"
}

// VerifySums checks release version's raw SHA256SUMS against its
// SigAsset (nil when the release has none). It passes when a trusted
// key's signature in sig verifies and sums lists the version's install
// tarball, so a signed SHA256SUMS can't be passed off as another
// version's. A missing signature passes, reported by signed being
// false, only for versions before FirstSignedRelease.
func VerifySums(version string, sums, sig []byte) (signed bool, err error) {
	if sig == nil {
		if SignatureRequired(version) {
			return false, fmt.Errorf("release %s %w; releases from %s on are signed, so don't install this one: it may have been tampered with", version, ErrUnsigned, FirstSignedRelease)
		}
		return false, nil
	}
	if !signedByTrusted(sums, sig) {
		return false, fmt.Errorf("release %s %w; don't install it: it may have been tampered with", version, ErrBadSignature)
	}
	parsed, err := ParseSums(sums)
	if err != nil {
		return false, err
	}
	if _, ok := parsed[InstallAsset(version)]; !ok {
		return false, fmt.Errorf("release %s's SHA256SUMS doesn't list %s: it belongs to another release; refusing it", version, InstallAsset(version))
	}
	return true, nil
}

// signedByTrusted reports whether a line of sig is a trusted key's valid
// signature of sums. Lines of keys it doesn't know are skipped.
func signedByTrusted(sums, sig []byte) bool {
	keys := trusted()
	sc := bufio.NewScanner(bytes.NewReader(sig))
	for sc.Scan() {
		id, b64, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if !ok {
			continue
		}
		s, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
		if err != nil {
			continue
		}
		for _, k := range keys {
			if KeyID(k) == id && ed25519.Verify(k, sums, s) {
				return true
			}
		}
	}
	return false
}

// trusted decodes releaseKeys, plus any test keys. Decoded per check,
// which is rare, so nothing runs at start-up.
func trusted() []ed25519.PublicKey {
	out := make([]ed25519.PublicKey, 0, len(releaseKeys))
	for _, k := range releaseKeys {
		b, err := base64.StdEncoding.DecodeString(k)
		if err != nil || len(b) != ed25519.PublicKeySize {
			panic("update: bad release key " + k)
		}
		out = append(out, b)
	}
	testKeysMu.Lock()
	defer testKeysMu.Unlock()
	return append(out, testKeys...)
}

// TrustForTest makes signatures by pub count as Dock's until t ends: how
// tests in other packages sign the releases they stage. Never call it
// outside a test.
func TrustForTest(t interface{ Cleanup(func()) }, pub ed25519.PublicKey) {
	testKeysMu.Lock()
	testKeys = append(testKeys, pub)
	testKeysMu.Unlock()
	t.Cleanup(func() {
		testKeysMu.Lock()
		defer testKeysMu.Unlock()
		if i := slices.IndexFunc(testKeys, func(k ed25519.PublicKey) bool { return k.Equal(pub) }); i >= 0 {
			testKeys = slices.Delete(testKeys, i, i+1)
		}
	})
}
