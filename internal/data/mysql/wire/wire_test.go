package wire

import (
	"bytes"
	"testing"
)

// FuzzParse feeds arbitrary server bytes to the packet parsers: a broken
// or hostile server must get an error, never a panic.
func FuzzParse(f *testing.F) {
	f.Add([]byte("\x0a8.4.3\x00\x01\x00\x00\x00abcdefgh\x00\xff\xff\x2d\x02\x00\xff\xdf\x15\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00ijklmnopqrst\x00caching_sha2_password\x00"))
	f.Add([]byte("\x03def\x03app\x01t\x01t\x02id\x02id\x0c\x3f\x00\x14\x00\x00\x00\x08\x03\x42\x00\x00\x00"))
	f.Add([]byte("\x011\xfb\x03abc"))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = parseHandshake(b)
		_, _ = parseColumn(b)
		_ = parseRow(b, make([][]byte, 3))
		_, _, _ = lenencString(b)
	})
}

func TestLenenc(t *testing.T) {
	for _, n := range []uint64{0, 250, 251, 1 << 16, 1<<24 - 1, 1 << 24, 1 << 40} {
		b := appendLenencInt(nil, n)
		got, m, ok := lenencInt(b)
		if !ok || got != n || m != len(b) {
			t.Errorf("%d: got %d (%d bytes, ok %t), encoded %x", n, got, m, ok, b)
		}
	}
	row := appendLenencString(appendLenencString([]byte{0xfb}, "a"), string(bytes.Repeat([]byte("x"), 300)))
	vals := make([][]byte, 3)
	if err := parseRow(row, vals); err != nil || vals[0] != nil || string(vals[1]) != "a" || len(vals[2]) != 300 {
		t.Fatalf("row: %q %v", vals, err)
	}
}
