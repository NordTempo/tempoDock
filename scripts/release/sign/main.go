// Command sign signs a release's SHA256SUMS (ADR 0088): the release
// workflow runs it once SHA256SUMS lists every asset, before publishing.
//
//	RELEASE_SIGNING_KEY="$(cat key.pem)" go run ./scripts/release/sign dist v0.56.0
//
// RELEASE_SIGNING_KEY holds one or more Ed25519 private keys as PKCS#8
// PEM blocks (openssl genpkey -algorithm ed25519); during a key rotation
// it holds both. It writes DIR/SHA256SUMS.sig with a line per key (and
// the same signatures for ssh-keygen in DIR/SHA256SUMS.sshsig), then
// checks the result the way dock does, against the keys built into this
// checkout's internal/update: a signing key dock doesn't trust fails here,
// before anything is published. Nothing about the keys is printed but
// their ids.
//
// For a release you sign yourself (ADR 0088's amendment; e2e/nightly.sh),
// TRUSTED_KEYS holds the public keys your servers' [updates] trusted_keys
// list, base64, separated by spaces or newlines: the check counts them too.
package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NordTempo/tempoDock/internal/update"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "sign:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: sign DIR VERSION, with the keys in RELEASE_SIGNING_KEY")
	}
	dir, version := args[0], args[1]
	keys, err := parseKeys(os.Getenv("RELEASE_SIGNING_KEY"))
	if err != nil {
		return err
	}
	var extra []ed25519.PublicKey
	for _, k := range strings.Fields(os.Getenv("TRUSTED_KEYS")) {
		b, err := base64.StdEncoding.DecodeString(k)
		if err != nil || len(b) != ed25519.PublicKeySize {
			return fmt.Errorf("TRUSTED_KEYS: %q isn't a base64 Ed25519 public key", k)
		}
		extra = append(extra, b)
	}
	update.TrustExtra(extra)
	sums, err := os.ReadFile(filepath.Join(dir, update.SumsAsset))
	if err != nil {
		return err
	}
	var sig, sshSig strings.Builder
	for _, k := range keys {
		sig.WriteString(update.Sign(k, sums))
		sshSig.WriteString(signSSH(k, sums))
	}
	if signed, err := update.VerifySums(version, sums, []byte(sig.String())); err != nil || !signed {
		return fmt.Errorf("the signature doesn't verify with the keys in internal/update and TRUSTED_KEYS (is the signing key one of them?): %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, update.SigAsset), []byte(sig.String()), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, sshSigAsset), []byte(sshSig.String()), 0o644); err != nil {
		return err
	}
	for _, k := range keys {
		pub, _ := k.Public().(ed25519.PublicKey)
		fmt.Printf("signed %s with key %s\n", filepath.Join(dir, update.SumsAsset), update.KeyID(pub))
	}
	return nil
}

// parseKeys reads the PEM blocks in s as Ed25519 private keys.
func parseKeys(s string) ([]ed25519.PrivateKey, error) {
	var keys []ed25519.PrivateKey
	rest := []byte(s)
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
		if err != nil {
			return nil, fmt.Errorf("RELEASE_SIGNING_KEY: %w", err)
		}
		ek, ok := k.(ed25519.PrivateKey)
		if !ok {
			return nil, errors.New("RELEASE_SIGNING_KEY holds a key that isn't Ed25519")
		}
		keys = append(keys, ek)
	}
	if len(keys) == 0 {
		return nil, errors.New("RELEASE_SIGNING_KEY holds no PEM private key; set the secret in the release environment")
	}
	return keys, nil
}
