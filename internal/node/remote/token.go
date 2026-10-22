package remote

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// Join tokens (ADR 0014 section 2): "dockjoin1_" and the base64url (no
// padding) of
//
//	32 bytes  the control plane's key fingerprint (SHA-256 of its SPKI)
//	32 bytes  the one-time secret
//	rest      the control plane's node addresses, host:port, comma separated
//
// The control plane stores only the secret's hash, with the server name,
// role, expiry and a used flag, so a leaked database can't join nodes.
const (
	joinTokenPrefix = "dockjoin1_"
	secretSize      = 32
	// JoinTokenTTL is how long a join token stays valid. Core stores the
	// expiry; Store.Join enforces it.
	JoinTokenTTL = time.Hour
)

// JoinToken is a parsed join token.
type JoinToken struct {
	// Addrs are where the node dials the control plane, tried in order.
	Addrs []string
	// ControlPlane is the fingerprint of the control plane's key.
	ControlPlane string
	Secret       [secretSize]byte
}

// String encodes the token for `dock node join <token>`.
func (t JoinToken) String() string {
	fp, _ := fingerprintBytes(t.ControlPlane) // valid for minted and parsed tokens
	payload := make([]byte, 0, len(fp)+secretSize+64)
	payload = append(payload, fp...)
	payload = append(payload, t.Secret[:]...)
	payload = append(payload, strings.Join(t.Addrs, ",")...)
	return joinTokenPrefix + base64.RawURLEncoding.EncodeToString(payload)
}

// SecretHash is what the control plane stores and looks the token up by.
func (t JoinToken) SecretHash() string { return hashSecret(t.Secret[:]) }

func hashSecret(secret []byte) string {
	sum := sha256.Sum256(secret)
	return hex.EncodeToString(sum[:])
}

// newJoinToken mints a token with a fresh secret.
func newJoinToken(controlPlane string, addrs []string) (JoinToken, error) {
	if len(addrs) == 0 {
		return JoinToken{}, errors.New("no node addresses configured: set the address nodes dial in the [nodes] config")
	}
	for _, a := range addrs {
		if err := checkAddr(a); err != nil {
			return JoinToken{}, err
		}
	}
	if _, err := fingerprintBytes(controlPlane); err != nil {
		return JoinToken{}, err
	}
	t := JoinToken{Addrs: addrs, ControlPlane: controlPlane}
	_, _ = rand.Read(t.Secret[:]) // never fails
	return t, nil
}

// ParseJoinToken decodes a token from `dock server join`.
func ParseJoinToken(s string) (JoinToken, error) {
	invalid := errors.New("invalid join token: copy the whole `dock node join` command printed by `dock server join`")
	rest, ok := strings.CutPrefix(strings.TrimSpace(s), joinTokenPrefix)
	if !ok {
		return JoinToken{}, invalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil || len(payload) <= sha256.Size+secretSize {
		return JoinToken{}, invalid
	}
	t := JoinToken{ControlPlane: "sha256:" + hex.EncodeToString(payload[:sha256.Size])}
	copy(t.Secret[:], payload[sha256.Size:])
	t.Addrs = strings.Split(string(payload[sha256.Size+secretSize:]), ",")
	for _, a := range t.Addrs {
		if checkAddr(a) != nil {
			return JoinToken{}, invalid
		}
	}
	return t, nil
}

func checkAddr(a string) error {
	if _, port, err := net.SplitHostPort(a); err != nil || port == "" || strings.Contains(a, ",") {
		return fmt.Errorf("node address %q must be host:port", a)
	}
	return nil
}
