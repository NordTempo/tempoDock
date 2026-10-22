package registry

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// TokenTTL is how long a pull, delete or catalog token is valid: long
	// enough for one deploy's pull, short enough that a leaked one is
	// soon worthless.
	TokenTTL = 15 * time.Minute
	// PushTokenTTL is how long a push token is valid. It is minted when a
	// build starts but only used when the build ends, and builds can take
	// longer than TokenTTL. It still only covers one repository.
	PushTokenTTL = time.Hour
	// KeyFile is the signing key's file name in the data dir.
	KeyFile = "registry.key"
)

// Access is one grant in a token: Actions on the resource Type/Name, in
// the registry's terms ("repository", "p1/web", ["pull"]).
type Access struct {
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Actions []string `json:"actions"`
}

// Pull grants pulling repo.
func Pull(repo string) Access {
	return Access{Type: "repository", Name: repo, Actions: []string{"pull"}}
}

// Push grants pushing to repo. Pushing needs pull as well: BuildKit checks
// which blobs and manifests the repository already has.
func Push(repo string) Access {
	return Access{Type: "repository", Name: repo, Actions: []string{"pull", "push"}}
}

// deleteAccess grants reading and deleting manifests in repo (pruning).
func deleteAccess(repo string) Access {
	return Access{Type: "repository", Name: repo, Actions: []string{"pull", "delete"}}
}

// catalogAccess grants listing the registry's repositories (pruning).
var catalogAccess = Access{Type: "registry", Name: "catalog", Actions: []string{"*"}}

// Claims are the claims of a registry token, as Distribution reads them.
type Claims struct {
	Issuer    string   `json:"iss"`
	Subject   string   `json:"sub"`
	Audience  string   `json:"aud"`
	Expires   int64    `json:"exp"`
	NotBefore int64    `json:"nbf"`
	IssuedAt  int64    `json:"iat"`
	ID        string   `json:"jti"`
	Access    []Access `json:"access"`
}

// header is a token's JOSE header. Distribution finds the verification key
// by kid in the JWKS the control plane writes for it (WriteConfig).
type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

// Signer mints and verifies registry tokens with the control plane's key.
// It is safe for concurrent use.
type Signer struct {
	key ed25519.PrivateKey
	kid string
	cfg Config
	// now is time.Now, except in tests.
	now func() time.Time
}

// NewSigner returns a Signer for tokens that the registry configured with
// cfg (WriteConfig) accepts.
func NewSigner(key ed25519.PrivateKey, cfg Config) *Signer {
	return &Signer{key: key, kid: keyID(key.Public().(ed25519.PublicKey)), cfg: cfg.withDefaults(), now: time.Now}
}

// LoadOrCreateKey reads the Ed25519 signing key (a 32-byte seed) at path,
// creating a new random one (mode 0600) if the file doesn't exist yet.
func LoadOrCreateKey(path string) (ed25519.PrivateKey, error) {
	seed, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		seed, err = createKey(path)
	}
	if err != nil {
		return nil, fmt.Errorf("registry key: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("registry key %s: want %d bytes, found %d", path, ed25519.SeedSize, len(seed))
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func createKey(path string) ([]byte, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	// O_EXCL: if another process created the key concurrently, use theirs.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(seed); err != nil {
		_ = f.Close()
		return nil, err
	}
	return seed, f.Close()
}

// keyID is the RFC 7638 thumbprint of an Ed25519 public key.
func keyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256([]byte(`{"crv":"Ed25519","kty":"OKP","x":"` + b64(pub) + `"}`))
	return b64(sum[:])
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// Mint returns a token for subject (who it is for, e.g. "server 2", shown
// in the registry's log) granting access for ttl: TokenTTL, or
// PushTokenTTL for a build's push.
func (s *Signer) Mint(subject string, ttl time.Duration, access ...Access) string {
	now := s.now()
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	c := Claims{
		Issuer:    s.cfg.Issuer,
		Subject:   subject,
		Audience:  s.cfg.Service,
		Expires:   now.Add(ttl).Unix(),
		NotBefore: now.Unix(),
		IssuedAt:  now.Unix(),
		ID:        b64(id),
		Access:    access,
	}
	h, _ := json.Marshal(header{Alg: "EdDSA", Typ: "JWT", Kid: s.kid})
	p, _ := json.Marshal(c)
	signing := b64(h) + "." + b64(p)
	return signing + "." + b64(ed25519.Sign(s.key, []byte(signing)))
}

// ErrInvalidToken is returned (wrapped) for a token that isn't one of this
// Signer's or isn't valid now.
var ErrInvalidToken = errors.New("invalid registry token")

// Verify checks a token's signature, issuer, audience and validity period
// the way the registry does (without its minute of clock-skew leeway) and
// returns its claims.
func (s *Signer) Verify(token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, fmt.Errorf("%w: malformed", ErrInvalidToken)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !ed25519.Verify(s.key.Public().(ed25519.PublicKey), []byte(parts[0]+"."+parts[1]), sig) {
		return Claims{}, fmt.Errorf("%w: bad signature", ErrInvalidToken)
	}
	var h header
	if err := decodeSegment(parts[0], &h); err != nil || h.Alg != "EdDSA" || h.Kid != s.kid {
		return Claims{}, fmt.Errorf("%w: unexpected header", ErrInvalidToken)
	}
	var c Claims
	if err := decodeSegment(parts[1], &c); err != nil {
		return Claims{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	now := s.now().Unix()
	switch {
	case c.Issuer != s.cfg.Issuer || c.Audience != s.cfg.Service:
		return Claims{}, fmt.Errorf("%w: issued for another registry", ErrInvalidToken)
	case now >= c.Expires:
		return Claims{}, fmt.Errorf("%w: expired", ErrInvalidToken)
	case now < c.NotBefore:
		return Claims{}, fmt.Errorf("%w: not valid yet", ErrInvalidToken)
	}
	return c, nil
}

func decodeSegment(seg string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// JWKS renders jwks.json, the JSON Web Key Set with the public key the
// registry verifies s's tokens with. It is the same for the same key.
func JWKS(s *Signer) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{"keys": []map[string]string{{
		"kty": "OKP", "crv": "Ed25519", "x": b64(s.key.Public().(ed25519.PublicKey)),
		"kid": s.kid, "alg": "EdDSA", "use": "sig",
	}}})
	return buf.Bytes()
}
