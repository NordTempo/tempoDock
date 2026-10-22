package remote

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Both ends authenticate like SSH: each has an Ed25519 key and pins the
// other's fingerprint. There is no CA and nothing expires; the self-signed
// certificate only exists because TLS carries keys in certificates.

// Key files in the data dir (ADR 0014 section 2), 0600.
const (
	// ControlPlaneKeyFile is the control plane's key, created by the first
	// `dock serve` (LoadOrCreateIdentity).
	ControlPlaneKeyFile = "link.key"
	// NodeKeyFile is a node's key, created by Join.
	NodeKeyFile = "node.key"
	// nextKeyFile holds a node's new key during a rotation, until the
	// control plane accepted it.
	nextKeyFile = "node.key.next"
)

// Identity is one end's Ed25519 key, with the self-signed certificate that
// presents it in TLS.
type Identity struct {
	cert tls.Certificate
}

// NewIdentity creates a key in memory.
func NewIdentity() (Identity, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Identity{}, err
	}
	return identityFor(priv)
}

// identityFor wraps priv in a certificate. Only the key matters: the
// fingerprint is of the public key, so a fresh certificate on every load
// is the same identity.
func identityFor(priv ed25519.PrivateKey) (Identity, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return Identity{}, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "dock"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(100, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, priv.Public(), priv)
	if err != nil {
		return Identity{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return Identity{}, err
	}
	return Identity{tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv, Leaf: leaf}}, nil
}

// Fingerprint identifies the key: "sha256:" and the hex SHA-256 of its
// SubjectPublicKeyInfo. Fingerprints are public; they go in join tokens
// and the servers table.
func (id Identity) Fingerprint() string { return fingerprint(id.cert.Leaf) }

func fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// fingerprintBytes parses a fingerprint back into its 32 bytes.
func fingerprintBytes(fp string) ([]byte, error) {
	b, err := hex.DecodeString(strings.TrimPrefix(fp, "sha256:"))
	if err != nil || len(b) != sha256.Size || !strings.HasPrefix(fp, "sha256:") {
		return nil, fmt.Errorf("invalid key fingerprint %q", fp)
	}
	return b, nil
}

// LoadOrCreateIdentity loads the key at path, creating it (0600) if the
// file doesn't exist. The control plane uses it for ControlPlaneKeyFile.
func LoadOrCreateIdentity(path string) (Identity, error) {
	id, err := loadIdentity(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return id, err
	}
	if id, err = NewIdentity(); err != nil {
		return Identity{}, err
	}
	return id, id.save(path)
}

// loadIdentity reads a key file. Like ssh, it refuses a key that other
// users can read.
func loadIdentity(path string) (Identity, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return Identity{}, err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return Identity{}, fmt.Errorf("key file %s is readable by other users; run chmod 600 %s", path, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Identity{}, err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "PRIVATE KEY" {
		return Identity{}, fmt.Errorf("key file %s: no PEM private key", path)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return Identity{}, fmt.Errorf("key file %s: %w", path, err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return Identity{}, fmt.Errorf("key file %s: not an Ed25519 key", path)
	}
	return identityFor(priv)
}

// save writes the key to path, 0600, replacing it atomically.
func (id Identity) save(path string) error {
	der, err := x509.MarshalPKCS8PrivateKey(id.cert.PrivateKey)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// writeFileAtomic writes data to path with mode 0600 through a temporary
// file, so a crash never leaves a half-written key or link file.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*") // 0600
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// peerFingerprint returns the fingerprint of the key the peer presented.
func peerFingerprint(cs tls.ConnectionState) (string, error) {
	if len(cs.PeerCertificates) == 0 {
		return "", errors.New("peer presented no key")
	}
	return fingerprint(cs.PeerCertificates[0]), nil
}

// controlPlaneTLS is the control plane's side: it requires a key from every
// node and lets verify decide during the handshake, so an unknown or
// revoked node gets a TLS alert rather than a connection.
func controlPlaneTLS(id Identity, verify func(cs tls.ConnectionState, fingerprint string) error) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{id.cert},
		ClientAuth:   tls.RequireAnyClientCert,
		NextProtos:   append(append([]string(nil), acceptedALPN...), joinALPN),
		VerifyConnection: func(cs tls.ConnectionState) error {
			fp, err := peerFingerprint(cs)
			if err != nil {
				return err
			}
			return verify(cs, fp)
		},
	}
}

// nodeTLS is the node's side: it presents its key and only talks to the
// control plane whose fingerprint it pinned at join. The pin is checked in
// the handshake, before the node sends a single byte of its own.
func nodeTLS(id Identity, controlPlane, proto string) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{id.cert},
		NextProtos:   []string{proto},
		// Certificate chains don't apply; VerifyConnection pins the key.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			fp, err := peerFingerprint(cs)
			if err != nil {
				return err
			}
			if fp != controlPlane {
				return &pinError{got: fp, want: controlPlane}
			}
			return nil
		},
	}
}

// pinError is a control plane that presented a different key than the
// pinned one: an impostor, or a control plane whose key was replaced.
type pinError struct{ got, want string }

func (e *pinError) Error() string {
	return fmt.Sprintf("control plane key %s does not match the pinned %s; if the control plane was reinstalled, join again with a new token", e.got, e.want)
}
