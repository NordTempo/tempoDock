package core

import (
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"testing"
	"testing/iotest"
)

// maskPlainly is the masking rule spelled out, a value at a time: at each
// place, the longest value that starts there is masked, and the text
// goes on after it.
func maskPlainly(values []string, s string) string {
	var out strings.Builder
	for pos := 0; pos < len(s); {
		longest := 0
		for _, v := range values {
			if len(v) > longest && strings.HasPrefix(s[pos:], v) {
				longest = len(v)
			}
		}
		if longest == 0 {
			out.WriteByte(s[pos])
			pos++
			continue
		}
		out.WriteString(secretMask)
		pos += longest
	}
	return out.String()
}

// prefixPlainly is secretPrefixAt spelled out: the longest tail of s that
// is the start, and not all, of a value.
func prefixPlainly(values []string, s string) int {
	n := 0
	for _, v := range values {
		for k := min(len(s), len(v)-1); k > n; k-- {
			if strings.HasSuffix(s, v[:k]) {
				n = k
			}
		}
	}
	return n
}

// redactAll masks s every way a redactor is used: as a line, through the
// reader in reads of different sizes, and through the live writer.
func redactAll(t *testing.T, red *redactor, s string) map[string]string {
	t.Helper()
	got := map[string]string{}
	got["line"], _ = red.line(s)
	for name, src := range map[string]io.Reader{
		"reader":             strings.NewReader(s),
		"reader, one byte":   iotest.OneByteReader(strings.NewReader(s)),
		"reader, half reads": iotest.HalfReader(strings.NewReader(s)),
	} {
		b, err := io.ReadAll(red.reader(io.NopCloser(src)))
		if err != nil {
			t.Fatal(err)
		}
		got[name] = string(b)
	}
	for _, chunk := range []int{1, 3, len(s) + 1} {
		var out strings.Builder
		w, flush := red.writer(&out)
		for i := 0; i < len(s); i += chunk {
			if _, err := w.Write([]byte(s[i:min(len(s), i+chunk)])); err != nil {
				t.Fatal(err)
			}
		}
		if err := flush(); err != nil {
			t.Fatal(err)
		}
		got[fmt.Sprintf("writer, %d at a time", chunk)] = out.String()
	}
	return got
}

// envOf is an environment with values as its variables' values.
func envOf(values []string) map[string]string {
	env := map[string]string{}
	for i, v := range values {
		env[fmt.Sprintf("K%d", i)] = v
	}
	return env
}

// Values that overlap, start or end one another, or repeat inside
// themselves are masked as the rule says: leftmost first, the longest
// there, then on from its end.
func TestRedactOverlappingValues(t *testing.T) {
	for _, tc := range []struct {
		values   []string
		in, want string
	}{
		// One value starts another: the longer wins where it is whole.
		{[]string{"secret", "secret-longer"}, "a secret-longer b secret-long c", "a ***** b *****-long c"},
		// One value ends another, and sits inside a third.
		{[]string{"abcdefgh", "defghi", "cdefgh"}, "xabcdefghi cdefghi defghi", "x*****i *****i *****"},
		// The earlier start wins over a longer value that starts later,
		// and the later one's rest is left as text.
		{[]string{"abcdef", "cdefghijkl"}, "abcdefghijkl", "*****ghijkl"},
		// A value that starts inside the one masked before it and runs
		// past its end is not masked; the next whole one after it is.
		{[]string{"aaaaaab", "abaaaaaa"}, "aaaaaabaaaaaax", "*****aaaaaax"},
		{[]string{"aaaaaab", "abaaaaaa"}, "aaaaaabaaaaaaaab", "*****aa*****"},
		// A value made of a repeat: each whole copy from the left.
		{[]string{"aaaaaa"}, "aaaaaaaaaaaaaaaaa", "**********aaaaa"},
		{[]string{"ababab", "bababa"}, "abababababab ba", "********** ba"},
		// The same value twice, back to back and at both ends.
		{[]string{"s3cr3t"}, "s3cr3ts3cr3t-s3cr3", "**********-s3cr3"},
		// Too short to mask on its own, also inside a longer value.
		{[]string{"12345", "x12345y", "true"}, "12345 x12345y true x12345", "12345 ***** true x12345"},
		// A multi-line value is masked line by line.
		{[]string{"-----BEGIN KEY-----\nMIIEvQIBADAN\nshort\n-----END KEY-----\n"}, "key: MIIEvQIBADAN short -----END KEY-----", "key: ***** short *****"},
		// Bytes that aren't text.
		{[]string{"\x00\xff\x00\xff\x00\xff", "\xff\xff\xff\xff\xff\xff\xff"}, "a\x00\xff\x00\xff\x00\xff\xff\xff\xff\xff\xff\xff\xffb", "a**********b"},
	} {
		if plain := maskPlainly(newRedactorValues(tc.values), tc.in); plain != tc.want {
			t.Fatalf("%q in %q: the rule gives %q, the test wants %q", tc.values, tc.in, plain, tc.want)
		}
		for how, got := range redactAll(t, newRedactor(envOf(tc.values)), tc.in) {
			if got != tc.want {
				t.Errorf("%q in %q, %s: %q, want %q", tc.values, tc.in, how, got, tc.want)
			}
		}
	}
}

// newRedactorValues are the lines of values a redactor masks.
func newRedactorValues(values []string) []string {
	var out []string
	for _, v := range values {
		for line := range strings.Lines(v) {
			if line = strings.TrimRight(line, "\r\n"); len(line) >= minSecretLen {
				out = append(out, line)
			}
		}
	}
	return out
}

// Over many small sets of values from a two-letter alphabet, where
// nearly every value overlaps another, every way of masking gives what
// the rule spelled out gives, and a live writer holds back the same
// tail. The seed is fixed: the same sets every run.
func TestRedactMatchesTheRule(t *testing.T) {
	rnd := rand.New(rand.NewPCG(1, 2))
	word := func(n int, alphabet string) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = alphabet[rnd.IntN(len(alphabet))]
		}
		return string(b)
	}
	for range 300 {
		values := make([]string, 1+rnd.IntN(6))
		for i := range values {
			values[i] = word(minSecretLen+rnd.IntN(5), "ab")
		}
		red := newRedactor(envOf(values))
		for range 4 {
			text := word(rnd.IntN(60), "abbac")
			want := maskPlainly(values, text)
			for how, got := range redactAll(t, red, text) {
				if got != want {
					t.Fatalf("%q in %q, %s: %q, want %q", values, text, how, got, want)
				}
			}
			if got, want := red.secretPrefixAt([]byte(text)), prefixPlainly(values, text); got != want {
				t.Fatalf("%q: the tail of %q held back is %d, want %d", values, text, got, want)
			}
		}
	}
}
