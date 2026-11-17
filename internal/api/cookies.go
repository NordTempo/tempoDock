package api

import (
	"net"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/httpkit"
)

// Dock's own cookies (the session, a pending sign-in, a single sign-on's
// or GitHub install's browser binding) take the __Host- prefix over
// HTTPS, so an app on a sibling hostname can't set one for Dock to read
// before its own (httpkit.CookieName, shared with tempoBase).
//
// The session also authenticates from its bare name over HTTPS, but only
// for sessions signed in before the prefix (core.AuthenticateSession's
// legacy), so nobody is signed out mid-session. Short-lived cookies
// don't: a sign-in in flight during the upgrade starts again.

// client is how a request arrived: over HTTPS (for the cookies' Secure
// flag and prefix) and from which IP (for rate limits). Behind a reverse
// proxy the IP is the proxy's.
type client struct {
	secure bool
	ip     string
}

func clientOf(ctx huma.Context) client {
	return newClient(ctx.RemoteAddr(), ctx.TLS() != nil || ctx.Header("X-Forwarded-Proto") == "https")
}

// requestClient is clientOf for the plain pages outside Huma.
func requestClient(r *http.Request) client {
	return newClient(r.RemoteAddr, overHTTPS(r))
}

// overHTTPS reports whether a request arrived over HTTPS: on TLS, or
// through Caddy at Dock's public address, which says so.
func overHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func newClient(addr string, secure bool) client {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		addr = host
	}
	return client{secure: secure, ip: addr}
}

// origin is the scheme and host the client reached Dock at.
func (c client) origin(host string) string {
	if c.secure {
		return "https://" + host
	}
	return "http://" + host
}

// cookieName is the name one of Dock's cookies has for this client.
func (c client) cookieName(name string) string { return httpkit.CookieName(name, c.secure) }

// cookie sets (value) or clears ("") one of Dock's cookies: HttpOnly,
// sent only under path over plain HTTP (the prefix needs Path=/).
func (c client) cookie(name, value, path string, expires time.Time, sameSite http.SameSite) http.Cookie {
	ck := http.Cookie{
		Name: c.cookieName(name), Value: value, Path: path, Expires: expires,
		HttpOnly: true, SameSite: sameSite, Secure: c.secure,
	}
	if c.secure {
		ck.Path = "/"
	}
	if value == "" {
		ck.Expires, ck.MaxAge = time.Time{}, -1
	}
	return ck
}

// read is the value of one of Dock's cookies as this client names it, ""
// without one. get is huma.ReadCookie's or http.Request.Cookie's.
func (c client) read(get func(string) (*http.Cookie, error), name string) string {
	if ck, err := get(c.cookieName(name)); err == nil {
		return ck.Value
	}
	return ""
}

// session is the session cookie's secret, and legacy when it came under
// the bare name over HTTPS (a session from before the prefix).
func (c client) session(get func(string) (*http.Cookie, error)) (secret string, legacy bool) {
	return httpkit.SessionSecret(get, sessionCookie, c.secure)
}

// sessionCookies set (secret) or clear ("") the session cookie and, over
// HTTPS, clear the bare-name one a browser may hold from before the
// prefix.
func (c client) sessionCookies(secret string, expires time.Time) []http.Cookie {
	return httpkit.SessionCookies(sessionCookie, secret, expires, c.secure)
}

// humaCookies is get for a Huma request.
func humaCookies(ctx huma.Context) func(string) (*http.Cookie, error) {
	return func(name string) (*http.Cookie, error) { return huma.ReadCookie(ctx, name) }
}
