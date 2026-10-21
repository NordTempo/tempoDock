// Package secrets encrypts values at rest (variable values, resolved release
// config) with AES-256-GCM. The key lives in its own file outside the
// database, so a leaked DB file alone reveals no secrets.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// KeySize is the length of the key file in bytes (AES-256).
const KeySize = 32

// version prefixes every ciphertext so the format can change later.
const version byte = 1

// Box seals and opens values with one key.
type Box struct {
	aead cipher.AEAD
}

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
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext as version || nonce || ciphertext.
func (b *Box) Seal(plaintext []byte) ([]byte, error) {
	out := make([]byte, 1+b.aead.NonceSize(), 1+b.aead.NonceSize()+len(plaintext)+b.aead.Overhead())
	out[0] = version
	if _, err := rand.Read(out[1:]); err != nil {
		return nil, err
	}
	return b.aead.Seal(out, out[1:], plaintext, nil), nil
}

// Open decrypts a value produced by Seal.
func (b *Box) Open(sealed []byte) ([]byte, error) {
	ns := b.aead.NonceSize()
	if len(sealed) < 1+ns || sealed[0] != version {
		return nil, errors.New("secrets: malformed ciphertext")
	}
	pt, err := b.aead.Open(nil, sealed[1:1+ns], sealed[1+ns:], nil)
	if err != nil {
		return nil, errors.New("secrets: decryption failed (wrong key or corrupted value)")
	}
	return pt, nil
}
