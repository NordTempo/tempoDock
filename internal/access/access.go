// Package access signs and checks the tokens behind Dock-login access
// protection (ADR 0030). The control plane signs a short-lived grant when a
// signed-in member opens a protected service; the server serving that
// service trades it on the app's own hostname for a session cookie, and
// checks the cookie on every request. Both are signed with the service's
// access key, so a server needs no state to check them.
//
// A token is base64url(payload) "." base64url(HMAC-SHA256(key, payload)),
// payload being "<kind>\n<expiry unix>\n<host>\n<user>".
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
	// Grant is the one-minute token Dock hands the browser.
	Grant Kind = 'g'
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
	user = strings.ReplaceAll(user, "\n", "")
	payload := string(kind) + "\n" + strconv.FormatInt(exp.Unix(), 10) + "\n" + host + "\n" + user
	return b64.EncodeToString([]byte(payload)) + "." + b64.EncodeToString(mac(key, payload))
}

// Verify returns the user a token names if key signed it as kind for host
// and it hasn't expired at now.
func Verify(key []byte, token string, kind Kind, host string, now time.Time) (string, bool) {
	enc, sig, ok := strings.Cut(token, ".")
	if !ok || len(key) == 0 {
		return "", false
	}
	payload, err := b64.DecodeString(enc)
	if err != nil {
		return "", false
	}
	got, err := b64.DecodeString(sig)
	if err != nil || !hmac.Equal(got, mac(key, string(payload))) {
		return "", false
	}
	f := strings.SplitN(string(payload), "\n", 4)
	if len(f) != 4 || f[0] != string(kind) || f[2] != host {
		return "", false
	}
	exp, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil || now.Unix() >= exp {
		return "", false
	}
	return f[3], true
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
