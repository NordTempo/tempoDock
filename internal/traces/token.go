package traces

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
)

// A service token says which org, project, environment, service and
// release a span comes from, so the ingest never trusts a span's own
// attributes for that. It is "dtr1.<org>.<project>.<env>.<service>.<release>.<mac>",
// the MAC the first 16 bytes of HMAC-SHA256 of everything before it under
// the install's trace key, in hex: checked with no lookup. Dock puts one
// in each release's environment (OTEL_EXPORTER_OTLP_HEADERS), so a new
// release has a new token.

// tokenPrefix starts every token, and names its version.
const tokenPrefix = "dtr1."

// Claims are what a token says.
type Claims struct {
	Org, Project, Env, Service, Release int64
}

// KeyFrom derives the install's trace key from its analytics token, which
// the control plane holds sealed: no other secret to keep. The key goes
// to every server; the analytics token never does.
func KeyFrom(analyticsToken string) []byte {
	m := hmac.New(sha256.New, []byte(analyticsToken))
	m.Write([]byte("dock traces ingest key v1"))
	return m.Sum(nil)
}

// Mint makes the token for c under key.
func Mint(key []byte, c Claims) string {
	body := tokenPrefix + strconv.FormatInt(c.Org, 10) + "." + strconv.FormatInt(c.Project, 10) + "." +
		strconv.FormatInt(c.Env, 10) + "." + strconv.FormatInt(c.Service, 10) + "." + strconv.FormatInt(c.Release, 10)
	return body + "." + hex.EncodeToString(mac(key, body))
}

// errToken is a token that isn't one Dock made with this key.
var errToken = errors.New("not a Dock service token; send the token Dock sets in OTEL_EXPORTER_OTLP_HEADERS")

// Parse checks a token against key and returns its claims.
func Parse(key []byte, tok string) (Claims, error) {
	body, sum, ok := cutLast(tok, ".")
	if !ok || !strings.HasPrefix(body, tokenPrefix) || len(key) == 0 {
		return Claims{}, errToken
	}
	got, err := hex.DecodeString(sum)
	if err != nil || !hmac.Equal(got, mac(key, body)) {
		return Claims{}, errToken
	}
	parts := strings.Split(strings.TrimPrefix(body, tokenPrefix), ".")
	if len(parts) != 5 {
		return Claims{}, errToken
	}
	var ids [5]int64
	for i, p := range parts {
		if ids[i], err = strconv.ParseInt(p, 10, 64); err != nil || ids[i] <= 0 {
			return Claims{}, errToken
		}
	}
	return Claims{Org: ids[0], Project: ids[1], Env: ids[2], Service: ids[3], Release: ids[4]}, nil
}

// mac is the first 16 bytes of HMAC-SHA256(key, body).
func mac(key []byte, body string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(body))
	return m.Sum(nil)[:16]
}

// cutLast cuts s around the last sep.
func cutLast(s, sep string) (string, string, bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}

// Key holds the install's trace key on a server, which the control plane
// hands it while tracing is on (node.AnalyticsConfig); none while it is
// off, and the ingest refuses every span.
type Key struct{ v atomic.Pointer[[]byte] }

// Set replaces the key; nil or empty is none.
func (k *Key) Set(key []byte) {
	if len(key) == 0 {
		k.v.Store(nil)
		return
	}
	c := append([]byte(nil), key...)
	k.v.Store(&c)
}

// Get is the key, nil for none.
func (k *Key) Get() []byte {
	if p := k.v.Load(); p != nil {
		return *p
	}
	return nil
}
