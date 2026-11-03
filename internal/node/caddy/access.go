package caddy

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/access"
)

// accessCookie holds a visitor's session on a login-protected hostname.
const accessCookie = "dock_access"

// AccessHandler is the access check for routes that need a Dock login
// (ADR 0030). The server mounts it on its loopback-only internal listener
// at Options.AccessURL's path, and Caddy asks it before every request to
// such a route (loginAccess), sending the route's key and login URL along.
// So it keeps no state, and works on a node whose control plane is away.
//
//   - A valid session cookie: 204 with the visitor in access.UserHeader.
//   - access.CallbackPath with a valid grant: sets the cookie and
//     redirects to the page the visitor asked for.
//   - Otherwise a browser page load is redirected to Dock's /access page
//     (the login URL), and anything else gets a 401.
func AccessHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		key, err := base64.RawURLEncoding.DecodeString(r.Header.Get(accessKeyHeader))
		uri, uerr := url.ParseRequestURI(r.Header.Get(forwardedURI))
		if err != nil || len(key) == 0 || uerr != nil {
			http.Error(w, "The access check got an incomplete request.", http.StatusBadRequest)
			return
		}
		host, now := hostOnly(r.Host), time.Now()
		if uri.Path == access.CallbackPath {
			signIn(w, r, key, host, uri.Query(), now)
			return
		}
		if c, err := r.Cookie(accessCookie); err == nil {
			if user, ok := access.Verify(key, c.Value, access.Session, host, now); ok {
				w.Header().Set(access.UserHeader, user)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		if pageLoad(r) {
			q := url.Values{"host": {host}, "to": {uri.RequestURI()}}
			redirectTo(w, r.Header.Get(accessLoginHeader)+"?"+q.Encode())
			return
		}
		http.Error(w, "Sign in first: open https://"+host+"/ in a browser.", http.StatusUnauthorized)
	})
}

// signIn trades a grant from Dock for a session cookie on this hostname
// and sends the visitor on to where they were going.
func signIn(w http.ResponseWriter, r *http.Request, key []byte, host string, q url.Values, now time.Time) {
	user, ok := access.Verify(key, q.Get("grant"), access.Grant, host, now)
	if !ok {
		http.Error(w, "This sign-in link has expired or is not for this address. Open the address again to sign in.", http.StatusForbidden)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: accessCookie, Value: access.Sign(key, access.Session, host, user, now.Add(access.SessionTTL)),
		Path: "/", MaxAge: int(access.SessionTTL.Seconds()), HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: r.Header.Get("X-Forwarded-Proto") == "https",
	})
	// The grant stays out of the next page's Referer.
	w.Header().Set("Referrer-Policy", "no-referrer")
	redirectTo(w, access.LocalPath(q.Get("to")))
}

// pageLoad reports whether the request is a browser loading a page, which
// can be sent to sign in; a script's fetch can't follow that.
func pageLoad(r *http.Request) bool {
	if m := r.Header.Get(forwardedMethod); m != http.MethodGet && m != http.MethodHead {
		return false
	}
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// redirectTo answers 302 to location as given (http.Redirect would clean
// the path and could change the query).
func redirectTo(w http.ResponseWriter, location string) {
	w.Header().Set("Location", location)
	w.WriteHeader(http.StatusFound)
}

// hostOnly is a Host header without its port.
func hostOnly(h string) string {
	if i := strings.LastIndexByte(h, ':'); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return strings.ToLower(h)
}
