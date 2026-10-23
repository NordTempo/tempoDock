package access

import (
	"strings"
	"testing"
	"time"
)

func TestVerify(t *testing.T) {
	key, now := NewKey(), time.Unix(1_800_000_000, 0)
	tok := Sign(key, Grant, "app.example.com", "ada@example.com", now.Add(GrantTTL))
	if u, ok := Verify(key, tok, Grant, "app.example.com", now); !ok || u != "ada@example.com" {
		t.Fatalf("valid grant: %q %v", u, ok)
	}
	enc, sig, _ := strings.Cut(tok, ".")
	forged := b64.EncodeToString([]byte("g\n1900000000\napp.example.com\nada@example.com")) + "." + sig
	for name, tc := range map[string]struct {
		key   []byte
		token string
		kind  Kind
		host  string
		at    time.Time
	}{
		"other key":      {NewKey(), tok, Grant, "app.example.com", now},
		"no key":         {nil, tok, Grant, "app.example.com", now},
		"as a session":   {key, tok, Session, "app.example.com", now},
		"other host":     {key, tok, Grant, "evil.example.com", now},
		"expired":        {key, tok, Grant, "app.example.com", now.Add(GrantTTL)},
		"edited payload": {key, forged, Grant, "app.example.com", now},
		"no signature":   {key, enc, Grant, "app.example.com", now},
		"garbage":        {key, "a.b", Grant, "app.example.com", now},
	} {
		if u, ok := Verify(tc.key, tc.token, tc.kind, tc.host, tc.at); ok {
			t.Errorf("%s: accepted as %q", name, u)
		}
	}
}
