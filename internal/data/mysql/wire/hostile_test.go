package wire

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"testing"
)

// hostile is a fake server: it writes script (packets with their
// sequence numbers) and drains whatever the client sends.
func hostile(t *testing.T, script ...[]byte) net.Conn {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { _, _ = a.Close(), b.Close() })
	go func() { _, _ = io.Copy(io.Discard, b) }()
	go func() {
		for _, p := range script {
			if _, err := b.Write(p); err != nil {
				return
			}
		}
	}()
	return a
}

// packet frames payload as packet seq.
func packet(seq byte, payload []byte) []byte {
	n := len(payload)
	return append([]byte{byte(n), byte(n >> 8), byte(n >> 16), seq}, payload...)
}

// A server that switches to sha256_password with no seed, then sends its
// RSA key, gets an error: the password's XOR divided by zero before,
// which killed the daemon from the KILL QUERY goroutine.
func TestAuthSwitchWithoutSeed(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	hs := []byte("\x0a8.4.3\x00\x01\x00\x00\x00abcdefgh\x00\xff\xff\x2d\x02\x00\xff\xdf\x15\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00ijklmnopqrst\x00caching_sha2_password\x00")
	nc := hostile(t,
		packet(0, hs),
		packet(2, []byte("\xfesha256_password\x00")),
		packet(4, append([]byte{0x01}, pemKey...)),
	)
	if _, err := Connect(nc, Config{User: "u", Password: "p"}); err == nil {
		t.Fatal("logged in")
	}
}

// A result's column count and a packet's continuations are capped before
// anything is allocated for them.
func TestHostileResults(t *testing.T) {
	cont := packet(1, make([]byte, maxPacket))
	for name, script := range map[string][][]byte{
		"2^32 columns": {packet(1, []byte{0xfe, 0, 0, 0, 0, 1, 0, 0, 0})},
		"2^60 columns": {packet(1, []byte{0xfe, 0, 0, 0, 0, 0, 0, 0, 0x10})},
		"endless packet": func() [][]byte {
			var s [][]byte
			for i := range maxLogicalPacket/maxPacket + 2 {
				p := bytes.Clone(cont)
				p[3] = byte(1 + i)
				s = append(s, p)
			}
			return s
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			nc := hostile(t, script...)
			c := &Conn{nc: nc, r: bufio.NewReader(nc)}
			err := c.Query("SELECT 1", func([]Column) error { return nil }, func([][]byte) error { return nil }, nil)
			if err == nil || !c.Broken() {
				t.Fatalf("got %v, broken %t", err, c.Broken())
			}
		})
	}
}
