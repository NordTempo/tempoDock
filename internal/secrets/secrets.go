// Package secrets encrypts values at rest (variable values, resolved release
// config, connection credentials) with AES-256-GCM. The key lives in its own
// file outside the database, so a leaked DB file alone reveals no secrets.
//
// Every value is bound to where it is stored (ADR 0041): its AAD names the
// column and the row, so a value moved to another row or column doesn't
// open. Values sealed before that (format 1, unbound) still open until
// the host has resealed them (ResealColumn) and called RefuseUnbound.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
)

// KeySize is the length of the key file in bytes (AES-256).
const KeySize = 32

// The first byte of every sealed value is its format.
const (
	unbound byte = 1 // version || nonce || ciphertext, no associated data
	bound   byte = 2 // the same, with the AAD as associated data
)

// Box seals and opens values with one key.
type Box struct {
	aead cipher.AEAD
	// nonceKey derives Reseal's nonces; it is derived from the key and
	// used for nothing else.
	nonceKey []byte
	// refuseUnbound is set once the host has resealed its values.
	refuseUnbound atomic.Bool
}

// AAD names where a sealed value is stored: a column and the row's key.
// Make one with Bind or Column.AAD; the zero AAD seals and opens nothing.
type AAD struct{ b []byte }

// Bind is the AAD of the value in column ("table.column") of the row named
// by key, its parts in a fixed order: Bind("org_variables.value", "1",
// "API_KEY"). Parts are length-prefixed, so no two keys encode alike.
func Bind(column string, key ...string) AAD {
	b := appendPart(nil, column)
	for _, k := range key {
		b = appendPart(b, k)
	}
	return AAD{b: b}
}

func appendPart(b []byte, s string) []byte {
	b = binary.AppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}

// String is the AAD for messages and tests; it isn't the encoding.
func (a AAD) String() string { return fmt.Sprintf("%q", a.b) }

var (
	errNoAAD     = errors.New("secrets: no AAD given (a bug: every value is bound to where it is stored)")
	errMalformed = errors.New("secrets: malformed ciphertext")
	errUnbound   = errors.New("secrets: the value was sealed before values were bound to where they are stored, and they have all been resealed since; set it again")
	errOpen      = errors.New("secrets: decryption failed: the key is wrong, or the value was corrupted or moved from where it was sealed; set it again")
)

// LoadOrCreate reads the key at path, creating a new random key (mode 0600)
// if the file doesn't exist yet.
func LoadOrCreate(path string) (*Box, error) {
	key, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		key, err = create(path)
	}
	if err != nil {
		return nil, fmt.Errorf("secrets key: %w", err)
	}
	return New(key)
}

func create(path string) ([]byte, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
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
	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		return nil, err
	}
	return key, f.Close()
}

// New returns a Box for a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("tempo dock secrets: reseal nonce"))
	return &Box{aead: aead, nonceKey: mac.Sum(nil)}, nil
}

// RefuseUnbound makes Open refuse unbound values from now on. A host calls
// it once every stored value is bound (ResealColumn), so an old value
// pasted into another row doesn't open there either.
func (b *Box) RefuseUnbound() { b.refuseUnbound.Store(true) }

// Seal encrypts plaintext for the place aad names, as bound || nonce ||
// ciphertext.
func (b *Box) Seal(plaintext []byte, aad AAD) ([]byte, error) {
	if len(aad.b) == 0 {
		return nil, errNoAAD
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.seal(nonce, plaintext, aad), nil
}

func (b *Box) seal(nonce, plaintext []byte, aad AAD) []byte {
	ns := len(nonce)
	out := make([]byte, 1+ns, 1+ns+len(plaintext)+b.aead.Overhead())
	out[0] = bound
	copy(out[1:], nonce)
	return b.aead.Seal(out, out[1:1+ns], plaintext, aad.b)
}

// Open decrypts a value sealed for the place aad names. An unbound value
// opens anywhere until RefuseUnbound.
func (b *Box) Open(sealed []byte, aad AAD) ([]byte, error) {
	if len(sealed) > 0 && sealed[0] == unbound && b.refuseUnbound.Load() {
		return nil, errUnbound
	}
	return b.open(sealed, aad)
}

func (b *Box) open(sealed []byte, aad AAD) ([]byte, error) {
	if len(aad.b) == 0 {
		return nil, errNoAAD
	}
	ns := b.aead.NonceSize()
	if len(sealed) < 1+ns {
		return nil, errMalformed
	}
	var ad []byte
	switch sealed[0] {
	case bound:
		ad = aad.b
	case unbound:
	default:
		return nil, errMalformed
	}
	pt, err := b.aead.Open(nil, sealed[1:1+ns], sealed[1+ns:], ad)
	if err != nil {
		return nil, errOpen
	}
	return pt, nil
}

// Reseal returns sealed bound to aad. An unbound value is opened and
// sealed again with a nonce derived from it and aad, so resealing one
// value at one place always gives the same bytes: a migration that is
// interrupted and runs again, or two processes running it at once, agree,
// and so do two stored copies of one value (a release's record of the
// variable change it applied). A bound value is returned as it is once it
// opens at aad.
func (b *Box) Reseal(sealed []byte, aad AAD) ([]byte, error) {
	pt, err := b.open(sealed, aad)
	if err != nil || sealed[0] == bound {
		return sealed, err
	}
	mac := hmac.New(sha256.New, b.nonceKey)
	mac.Write(aad.b) // self-delimiting, so aad || sealed is unambiguous
	mac.Write(sealed)
	return b.seal(mac.Sum(nil)[:b.aead.NonceSize()], pt, aad), nil
}
