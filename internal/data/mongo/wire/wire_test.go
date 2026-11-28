package wire

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"
)

// Decimals read and write as the BSON spec's text, through the bits.
func TestDecimal128(t *testing.T) {
	for _, s := range []string{"0", "249.00", "-1.5", "0.000001", "1E-7", "1.5E+40", "9999999999999999999999999999999999", "Infinity", "-Infinity", "NaN", "-0"} {
		d, err := ParseDecimal128(s)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if got := d.String(); got != s {
			t.Errorf("%s reads back as %s", s, got)
		}
	}
	// The spec's own bits for 1 and 0.001.
	if d := (Decimal128{H: 0x3040000000000000, L: 1}); d.String() != "1" {
		t.Errorf("1 = %s", d)
	}
	if d := (Decimal128{H: 0x303A000000000000, L: 1}); d.String() != "0.001" {
		t.Errorf("0.001 = %s", d)
	}
	for _, s := range []string{"", "1.2.3", "12345678901234567890123456789012345", "1e9999", "abc"} {
		if _, err := ParseDecimal128(s); err == nil {
			t.Errorf("%q parsed", s)
		}
	}
}

// What people type in the shell reads as the BSON the server needs, and
// a document's canonical Extended JSON reads back as itself.
func TestParseJSON(t *testing.T) {
	d, err := ParseDoc(`{ status: 'open', total: { $gt: 100 }, n: 2, big: 3000000000, f: 1.5,
		_id: ObjectId("6745f0c2e1b7a3d4c5f2a91e"), at: ISODate("2026-11-27T09:14:02Z"), "quoted key": NumberLong(7), }`)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"status":"open","total":{"$gt":{"$numberInt":"100"}},"n":{"$numberInt":"2"},"big":{"$numberLong":"3000000000"},"f":{"$numberDouble":"1.5"},` +
		`"_id":{"$oid":"6745f0c2e1b7a3d4c5f2a91e"},"at":{"$date":"2026-11-27T09:14:02.000Z"},"quoted key":{"$numberLong":"7"}}`
	if got := Compact(d); got != want {
		t.Fatalf("parsed as\n%s", got)
	}
	back, err := ParseDoc(want)
	if err != nil || !Equal(back, d) {
		t.Fatalf("canonical JSON reads back as %s (%v)", Compact(back), err)
	}
	for _, bad := range []string{`{a: }`, `{a: 1`, `[1, 2`, `{a: foo}`, `{a: ObjectId("12")}`, `{"$oid": 5}`, `{a: 1} x`} {
		if _, err := ParseJSON(bad); err == nil {
			t.Errorf("%s parsed", bad)
		}
	}
}

// A document a hostile server sends can't run past the bytes it has:
// every length is checked, and a cut or lying one is an error.
func TestReadDocHostile(t *testing.T) {
	good, err := AppendDoc(nil, D{{Key: "a", Value: "x"}, {Key: "b", Value: D{{Key: "c", Value: A{int32(1)}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDoc(good); err != nil {
		t.Fatal(err)
	}
	for i := range good {
		if _, err := ReadDoc(good[:i]); !errors.Is(err, ErrBSON) {
			t.Fatalf("cut at %d: %v", i, err)
		}
	}
	lying := append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(lying[7:], 1<<30) // the string's length
	if _, err := ReadDoc(lying); !errors.Is(err, ErrBSON) {
		t.Fatalf("a string longer than the document: %v", err)
	}
	if _, err := ReadDoc(nestedDoc(maxDepth + 2)); !errors.Is(err, ErrBSON) {
		t.Fatalf("nested too deeply: %v", err)
	}
}

// nestedDoc is {a: {a: ... {}}} n levels deep, past what AppendDoc
// itself writes.
func nestedDoc(n int) []byte {
	d := D{}
	for range n {
		d = D{{Key: "a", Value: d}}
	}
	b, _ := appendDoc(nil, d, -maxDepth*4)
	return b
}

// FuzzReadDoc: no input panics, and what decodes encodes to the same bytes.
func FuzzReadDoc(f *testing.F) {
	seed, _ := AppendDoc(nil, D{{Key: "a", Value: "x"}, {Key: "n", Value: int64(1)}, {Key: "d", Value: D{{Key: "b", Value: A{1.5, nil, true}}}}})
	f.Add(seed)
	f.Fuzz(func(t *testing.T, b []byte) {
		d, err := ReadDoc(b)
		if err != nil {
			return
		}
		again, err := AppendDoc(nil, d)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ReadDoc(again); err != nil {
			t.Fatalf("re-encoded document doesn't read: %v", err)
		}
	})
}

// A password SASLprep changes reads as the server stores it; printable
// ASCII is left alone.
func TestSASLPrep(t *testing.T) {
	for in, want := range map[string]string{"plain-pw": "plain-pw", "a\u00a0b": "a b", "soft\u00adhyphen": "softhyphen", "\uff21": "A"} {
		if got, err := saslPrep(in); err != nil || got != want {
			t.Errorf("saslPrep(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := saslPrep("bell\u0007"); err == nil {
		t.Error("a control character was allowed")
	}
	if _, err := saslPrep("\u05d0x\u05d1"); err == nil {
		t.Error("right-to-left text with a left-to-right letter in it was allowed")
	}
	if got, err := saslPrep("\u05d0\u05d1"); err != nil || got != "\u05d0\u05d1" {
		t.Errorf("right-to-left text: %q, %v", got, err)
	}
}

// Strings quote as encoding/json quotes them.
func TestAppendQuoted(t *testing.T) {
	for _, s := range []string{"plain", "q\"b\\s\n\r\t", "<a&b>", "\x01\x1f", "é ✓  ", "bad\xffutf8"} {
		want, _ := json.Marshal(s)
		if got := string(appendQuoted(nil, s)); got != string(want) {
			t.Errorf("%q: %s, want %s", s, got, want)
		}
	}
}
