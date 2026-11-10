package data

import "testing"

func TestKeyMatch(t *testing.T) {
	for _, c := range []struct {
		pattern, key string
		want         bool
	}{
		{"session:*", "session:abc", true},
		{"session:*", "sessions", false},
		{"*", "", true},
		{"*token*", "user:1:token:x", true},
		{"user:?", "user:1", true},
		{"user:?", "user:12", false},
		{"user:[0-9]*", "user:42:name", true},
		{"user:[^0-9]*", "user:42", false},
		{"h[ae]llo", "hallo", true},
		{"h[ae]llo", "hillo", false},
		{`a\*b`, "a*b", true},
		{`a\*b`, "axb", false},
		{"a*b*c", "aXbYbZc", true},
		{"a*b*c", "aXbYbZ", false},
		{"[abc", "[abc", true},
	} {
		if got := KeyMatch(c.pattern, c.key); got != c.want {
			t.Errorf("KeyMatch(%q, %q) = %t", c.pattern, c.key, got)
		}
	}
}
