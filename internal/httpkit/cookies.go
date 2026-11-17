package httpkit

import (
	"net/http"
	"time"
)

// A server's own cookies take the __Host- prefix over HTTPS. A browser
// keeps such a cookie only host-only, Secure and on Path=/, so an app on
// a sibling hostname (the server at dock.example.com, apps at
// *.example.com) can't set one with Domain=example.com for the server to
// read before its own. Plain HTTP (a tailnet address, `make dev`) can't
// use the prefix and keeps the bare names.
//
// Both servers name their cookies here, so neither can drift from the
// other: Dock's API (internal/api) and tempoBase's (tbhttp).

// HostPrefix is the cookie name prefix browsers lock to the host.
const HostPrefix = "__Host-"

// CookieName is the name one of a server's cookies has for a client:
// prefixed over HTTPS (secure), bare over plain HTTP.
func CookieName(name string, secure bool) string {
	if secure {
		return HostPrefix + name
	}
	return name
}

// SessionSecret is the session cookie's value as a client sent it; get is
// http.Request.Cookie or huma.ReadCookie. legacy says it came under the
// bare name over HTTPS, which only a session from before the prefix may:
// the server refuses any other there, so nobody is signed out by the
// change and no newer session can be set from a sibling hostname.
func SessionSecret(get func(string) (*http.Cookie, error), name string, secure bool) (secret string, legacy bool) {
	if ck, err := get(CookieName(name, secure)); err == nil && ck.Value != "" {
		return ck.Value, false
	}
	if !secure {
		return "", false
	}
	if ck, err := get(name); err == nil && ck.Value != "" {
		return ck.Value, true
	}
	return "", false
}

// SessionCookies set (secret) or clear ("") the session cookie: HttpOnly,
// SameSite=Strict, on Path=/. Over HTTPS they also clear the bare-name
// one a browser may hold from before the prefix.
func SessionCookies(name, secret string, expires time.Time, secure bool) []http.Cookie {
	ck := http.Cookie{
		Name: CookieName(name, secure), Value: secret, Path: "/", Expires: expires,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secure,
	}
	if secret == "" {
		ck.Expires, ck.MaxAge = time.Time{}, -1
	}
	out := []http.Cookie{ck}
	if secure {
		out = append(out, http.Cookie{Name: name, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: true})
	}
	return out
}
