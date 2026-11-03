package secrets

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

var (
	rowA = Bind("variables.value", "1", "0", "TOKEN")
	rowB = Bind("variables.value", "2", "0", "TOKEN")
)

// sealUnbound seals the way Seal did before ADR 0041 (format 1).
func sealUnbound(t *testing.T, b *Box, plaintext []byte) []byte {
	t.Helper()
	out := make([]byte, 1+b.aead.NonceSize())
	out[0] = unbound
	if _, err := rand.Read(out[1:]); err != nil {
		t.Fatal(err)
	}
	return b.aead.Seal(out, out[1:], plaintext, nil)
}

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

	sealed, err := box.Seal([]byte("hunter2"), rowA)
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
	got, err := again.Open(sealed, rowA)
	if err != nil || string(got) != "hunter2" {
		t.Fatalf("open = %q, %v", got, err)
	}

	other, _ := New(bytes.Repeat([]byte{1}, KeySize))
	if _, err := other.Open(sealed, rowA); err == nil {
		t.Fatal("opened with the wrong key")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := box.Open(sealed, rowA); err == nil {
		t.Fatal("opened tampered ciphertext")
	}
}

// A bound value opens only where it was sealed: not in another row, not
// in another column, and not as an unbound value either.
func TestBoundToItsPlace(t *testing.T) {
	box, _ := New(bytes.Repeat([]byte{7}, KeySize))
	sealed, err := box.Seal([]byte("hunter2"), rowA)
	if err != nil {
		t.Fatal(err)
	}
	for name, aad := range map[string]AAD{
		"another row":    rowB,
		"another column": Bind("variables.other", "1", "0", "TOKEN"),
		"a shifted key":  Bind("variables.value", "1", "0TOKEN"),
	} {
		if _, err := box.Open(sealed, aad); err == nil {
			t.Errorf("opened in %s", name)
		}
	}
	downgraded := bytes.Clone(sealed)
	downgraded[0] = unbound
	if _, err := box.Open(downgraded, rowA); err == nil {
		t.Error("opened as an unbound value")
	}
	if _, err := box.Seal([]byte("x"), AAD{}); err == nil {
		t.Error("sealed without an AAD")
	}
}

// Values sealed before ADR 0041 open anywhere until RefuseUnbound.
func TestUnboundValues(t *testing.T) {
	box, _ := New(bytes.Repeat([]byte{7}, KeySize))
	old := sealUnbound(t, box, []byte("hunter2"))
	if got, err := box.Open(old, rowB); err != nil || string(got) != "hunter2" {
		t.Fatalf("open = %q, %v", got, err)
	}
	box.RefuseUnbound()
	if _, err := box.Open(old, rowA); err == nil {
		t.Fatal("opened an unbound value after RefuseUnbound")
	}
}

// Reseal binds an unbound value, always to the same bytes for one value
// and place, and leaves a bound one as it is.
func TestReseal(t *testing.T) {
	box, _ := New(bytes.Repeat([]byte{7}, KeySize))
	box.RefuseUnbound() // Reseal still reads unbound values
	old := sealUnbound(t, box, []byte("hunter2"))
	sealed, err := box.Reseal(old, rowA)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := box.Open(sealed, rowA); err != nil || string(got) != "hunter2" {
		t.Fatalf("open = %q, %v", got, err)
	}
	if _, err := box.Open(sealed, rowB); err == nil {
		t.Fatal("a resealed value opened in another row")
	}
	if again, _ := box.Reseal(old, rowA); !bytes.Equal(again, sealed) {
		t.Fatal("resealing one value twice gave different bytes")
	}
	if atB, _ := box.Reseal(old, rowB); bytes.Equal(atB[1:13], sealed[1:13]) {
		t.Fatal("one value resealed at two places shares a nonce")
	}
	if same, err := box.Reseal(sealed, rowA); err != nil || !bytes.Equal(same, sealed) {
		t.Fatalf("reseal of a bound value = %x, %v", same, err)
	}
	if _, err := box.Reseal(sealed, rowB); err == nil {
		t.Fatal("resealed a bound value at another place")
	}
}
