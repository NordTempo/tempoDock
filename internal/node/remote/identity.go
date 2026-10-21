package remote

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// Both ends authenticate like SSH: each has an Ed25519 key and pins the
// other's fingerprint. There is no CA and nothing expires; the self-signed
// certificate only exists because TLS carries keys in certificates.

// NewIdentity creates a key and a self-signed certificate carrying it.
func NewIdentity(name string) (tls.Certificate, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(100, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv, Leaf: leaf}, nil
}

// Fingerprint identifies the key in cert: "sha256:" and the hex SHA-256 of
// its SubjectPublicKeyInfo. Fingerprints are public; they go in join tokens
// and the servers table.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// peerFingerprint returns the fingerprint of the key the peer presented.
func peerFingerprint(cs tls.ConnectionState) (string, error) {
	if len(cs.PeerCertificates) == 0 {
		return "", errors.New("peer presented no key")
	}
	return Fingerprint(cs.PeerCertificates[0]), nil
}

// controlPlaneTLS is the control plane's side: it requires a key from every
// node and lets authorize decide during the handshake, so a revoked node
// gets a TLS alert rather than a connection.
func controlPlaneTLS(id tls.Certificate, authorize func(fingerprint string) (string, bool)) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{id},
		ClientAuth:   tls.RequireAnyClientCert,
		NextProtos:   []string{ALPN},
		VerifyConnection: func(cs tls.ConnectionState) error {
			if cs.NegotiatedProtocol != ALPN {
				return fmt.Errorf("node does not speak %s", ALPN)
			}
			fp, err := peerFingerprint(cs)
			if err != nil {
				return err
			}
			if _, ok := authorize(fp); !ok {
				return fmt.Errorf("unknown or revoked node key %s", fp)
			}
			return nil
		},
	}
}

// nodeTLS is the node's side: it presents its key and only talks to the
// control plane whose fingerprint it pinned at join.
func nodeTLS(id tls.Certificate, controlPlane string) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{id},
		NextProtos:   []string{ALPN},
		// Certificate chains don't apply; VerifyConnection pins the key.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			fp, err := peerFingerprint(cs)
			if err != nil {
				return err
			}
			if fp != controlPlane {
				return fmt.Errorf("control plane key %s does not match the pinned %s", fp, controlPlane)
			}
			return nil
		},
	}
}
