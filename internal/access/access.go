// Package access signs and checks the tokens behind Dock-login access
// protection (ADR 0030). The control plane signs a short-lived grant when a
// signed-in member opens a protected service; the server serving that
// service trades it on the app's own hostname for a session cookie, and
// checks the cookie on every request. Both are signed with the service's
// access key, so a server needs no state to check them.
//
// A token is base64url(payload) "." base64url(HMAC-SHA256(key, payload)),
// payload being "<kind>\n<expiry unix>\n<host>\n<user>". A bound grant
// (Bound) has one more field before the user: the hash of a nonce the
// server left in the visitor's browser, so the grant works there only.
package access

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// KeySize is the length of a service's access key.
	KeySize = 32
	// GrantTTL is how long a grant may take from Dock to the app.
	GrantTTL = time.Minute
	// SessionTTL is how long a visitor stays signed in to a protected
	// service before Dock checks their access again.
	SessionTTL = 12 * time.Hour

	// CallbackPath is where, on the app's hostname, a grant is traded for
	// a session cookie: CallbackPath?grant=<token>&to=<path>.
	CallbackPath = "/.dock/access"
	// NonceCookie holds the nonce a server gives a browser before sending
	// it to Dock to sign in, on plain HTTP; SecureNonceCookie is its name
	// on HTTPS, where the __Host- prefix keeps other hostnames of the
	// same site from setting it.
	NonceCookie       = "dock_access_n"
	SecureNonceCookie = "__Host-" + NonceCookie
	// NonceTTL is how long a browser keeps its nonce: time to log in to
	// Dock on the way.
	NonceTTL = 10 * time.Minute
	// NonceParam carries the nonce's hash (NonceHash) to Dock's /access
	// page beside host and to.
	NonceParam = "n"
	// CookieName is the cookie that holds a visitor's session on a
	// login-protected hostname.
	CookieName = "dock_access"
	// ProbeTTL is how long the session of one uptime probe lasts (ADR
	// 0030, "Uptime probes"): the longest probe timeout and some slack.
	ProbeTTL = time.Minute
	// ProbeUser is who an uptime probe's session names. No person's
	// session does: theirs name an email address.
	ProbeUser = "dock-uptime"
	// UserHeader carries the signed-in visitor's name to the app,
	// replacing any value the client sent.
	UserHeader = "X-Dock-User"
)

// Kind tells grants and sessions apart, so neither passes for the other.
type Kind byte

const (
	// Grant is the one-minute token Dock hands the browser of a server
	// that sent no nonce (one older than bound grants). Newer servers
	// refuse it at the callback.
	Grant Kind = 'g'
	// Bound is a grant that also names the browser it was made for
	// (SignBound, VerifyBound).
	Bound Kind = 'b'
	// Session is the cookie the app's hostname keeps.
	Session Kind = 's'
)

var b64 = base64.RawURLEncoding

// NewKey returns a random access key.
func NewKey() []byte {
	k := make([]byte, KeySize)
	_, _ = rand.Read(k) // never fails (crypto/rand)
	return k
}

// Sign returns a token of kind for user on host, valid until exp. Newlines
// in user are dropped: they separate the payload's fields.
func Sign(key []byte, kind Kind, host, user string, exp time.Time) string {
	return seal(key, kind, host, strings.ReplaceAll(user, "\n", ""), exp)
}

// seal signs a payload of kind for host whose last field is rest.
func seal(key []byte, kind Kind, host, rest string, exp time.Time) string {
	payload := string(kind) + "\n" + strconv.FormatInt(exp.Unix(), 10) + "\n" + host + "\n" + rest
	return b64.EncodeToString([]byte(payload)) + "." + b64.EncodeToString(mac(key, payload))
}

// Verify returns the user a token names if key signed it as kind for host
// and it hasn't expired at now. A bound grant has its own check,
// VerifyBound, and never passes this one.
func Verify(key []byte, token string, kind Kind, host string, now time.Time) (string, bool) {
	if kind == Bound {
		return "", false
	}
	user, _, ok := open(key, token, kind, host, now)
	return user, ok
}

// open checks a token's signature, kind, host and expiry, and returns
// what follows the host in its payload and when it expires (Unix).
func open(key []byte, token string, kind Kind, host string, now time.Time) (rest string, exp int64, ok bool) {
	enc, sig, cut := strings.Cut(token, ".")
	if !cut || len(key) == 0 {
		return "", 0, false
	}
	payload, err := b64.DecodeString(enc)
	if err != nil {
		return "", 0, false
	}
	got, err := b64.DecodeString(sig)
	if err != nil || !hmac.Equal(got, mac(key, string(payload))) {
		return "", 0, false
	}
	f := strings.SplitN(string(payload), "\n", 4)
	if len(f) != 4 || f[0] != string(kind) || f[2] != host {
		return "", 0, false
	}
	exp, err = strconv.ParseInt(f[1], 10, 64)
	if err != nil || now.Unix() >= exp {
		return "", 0, false
	}
	return f[3], exp, true
}

// NewNonce returns the random value a server leaves in a visitor's
// browser (NonceCookie) before sending them to Dock to sign in.
func NewNonce() string {
	n := make([]byte, 16)
	_, _ = rand.Read(n) // never fails (crypto/rand)
	return b64.EncodeToString(n)
}

// ValidNonce reports whether s has the shape of NewNonce's values, so a
// server reuses only a cookie it could have set.
func ValidNonce(s string) bool {
	n, err := b64.DecodeString(s)
	return err == nil && len(n) == 16
}

// NonceHash is what a server tells Dock about a browser's nonce, and
// what a bound grant carries: base64url(SHA-256(nonce)). The nonce itself
// never leaves the app's hostname.
func NonceHash(nonce string) string {
	sum := sha256.Sum256([]byte(nonce))
	return b64.EncodeToString(sum[:])
}

// ValidNonceHash reports whether s has the shape of NonceHash's values.
func ValidNonceHash(s string) bool {
	h, err := b64.DecodeString(s)
	return err == nil && len(h) == sha256.Size
}

// BoundGrant is what a bound grant says once VerifyBound has checked it.
type BoundGrant struct {
	// User is who it signs in.
	User string
	// NonceHash is the NonceHash of the nonce in the browser it was made
	// for; the callback compares it with the browser's cookie (Holds).
	NonceHash string
	// Expires is its expiry, in Unix seconds.
	Expires int64
}

// Holds reports whether nonce, a browser's nonce cookie, is the one the
// grant was made for.
func (g BoundGrant) Holds(nonce string) bool {
	return nonce != "" && hmac.Equal([]byte(NonceHash(nonce)), []byte(g.NonceHash))
}

// SignBound returns a grant for user on host that only the browser
// holding the nonce behind nonceHash (NonceHash) can trade, valid until
// exp. The control plane signs one when the server sent a nonce's hash.
func SignBound(key []byte, host, nonceHash, user string, exp time.Time) string {
	return seal(key, Bound, host, strings.ReplaceAll(nonceHash, "\n", "")+"\n"+strings.ReplaceAll(user, "\n", ""), exp)
}

// VerifyBound returns what a bound grant says if key signed it for host
// and it hasn't expired at now. The caller still has to check the
// browser's nonce (BoundGrant.Holds) and that the grant is unused.
func VerifyBound(key []byte, token, host string, now time.Time) (BoundGrant, bool) {
	rest, exp, ok := open(key, token, Bound, host, now)
	if !ok {
		return BoundGrant{}, false
	}
	n, user, ok := strings.Cut(rest, "\n")
	if !ok || !ValidNonceHash(n) {
		return BoundGrant{}, false
	}
	return BoundGrant{User: user, NonceHash: n, Expires: exp}, true
}

func mac(key []byte, payload string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(payload))
	return m.Sum(nil)
}

// ProbeCookie is the Cookie header that lets one of Dock's uptime probes
// through the login or the password on host: a session naming ProbeUser, bound to host
// like every session and valid for ProbeTTL from now. Only the control
// plane makes one, for a check of a domain of the service key belongs to.
func ProbeCookie(key []byte, host string, now time.Time) string {
	return CookieName + "=" + Sign(key, Session, host, ProbeUser, now.Add(ProbeTTL))
}

// LocalPath is to if it is a path on the same hostname, else "/". Both the
// control plane (building the grant link) and the access callback (sending
// the visitor on) use it, so a sign-in link can't redirect anywhere else.
// Browsers drop tabs and newlines from a Location and read a backslash as
// a slash, so "/\t/evil.com" or "/\\evil.com" would become "//evil.com":
// any of them, or any other control character, sends the visitor to "/".
func LocalPath(to string) string {
	if !strings.HasPrefix(to, "/") || strings.HasPrefix(to, "//") {
		return "/"
	}
	for i := range len(to) {
		if c := to[i]; c < 0x20 || c == 0x7f || c == '\\' {
			return "/"
		}
	}
	if u, err := url.Parse(to); err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "/"
	}
	return to
}
