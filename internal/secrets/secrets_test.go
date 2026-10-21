package secrets

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRoundtripAndKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "secret.key")
	box, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || info.Size() != KeySize {
		t.Fatalf("key file mode %v size %d", info.Mode().Perm(), info.Size())
	}

	sealed, err := box.Seal([]byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("hunter2")) {
		t.Fatal("ciphertext contains plaintext")
	}

	// A second load reuses the same key.
	again, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := again.Open(sealed)
	if err != nil || string(got) != "hunter2" {
		t.Fatalf("open = %q, %v", got, err)
	}

	other, _ := New(bytes.Repeat([]byte{1}, KeySize))
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("opened with the wrong key")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := box.Open(sealed); err == nil {
		t.Fatal("opened tampered ciphertext")
	}
}
