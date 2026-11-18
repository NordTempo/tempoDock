package caddy

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/access"
)

// accessCookie holds a visitor's session on a login-protected hostname.
const accessCookie = access.CookieName

// AccessHandler is the access check for routes that need a Dock login
// (ADR 0030). The server mounts it on its loopback-only internal listener
// at Options.AccessURL's path, and Caddy asks it before every request to
// such a route (loginAccess), sending the route's key and login URL along.
// So it needs nothing stored, and works on a node whose control plane is
// away; all it keeps is the grants traded in the last minute (spentGrants).
//
//   - A valid session cookie: 204 with the visitor in access.UserHeader.
//     Dock's uptime probes carry a one-minute one naming
//     access.ProbeUser (access.ProbeCookie).
//   - access.CallbackPath with a valid grant, in the browser it was made
//     for and for the first time: sets the cookie and redirects to the
//     page the visitor asked for.
//   - Otherwise a browser page load is redirected to Dock's /access page
//     (the login URL) with a nonce left in a cookie and its hash sent
//     along, which the grant comes back bound to; anything else gets a
//     401.
//
// For a password-protected route (accessProbeHeader, probeAccess) it
// passes one thing only: a valid session naming access.ProbeUser, Dock's
// uptime probe. Any other session, or the callback, gets a 401: a
// visitor's Dock login never stands in for the password.
func AccessHandler() http.Handler {
	spent := &spentGrants{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		key, err := base64.RawURLEncoding.DecodeString(r.Header.Get(accessKeyHeader))
		uri, uerr := url.ParseRequestURI(r.Header.Get(forwardedURI))
		if err != nil || len(key) == 0 || uerr != nil {
			http.Error(w, "The access check got an incomplete request.", http.StatusBadRequest)
			return
		}
		host, now := hostOnly(r.Host), time.Now()
		if r.Header.Get(accessProbeHeader) != "" {
			if c, err := r.Cookie(accessCookie); err == nil {
				if user, ok := access.Verify(key, c.Value, access.Session, host, now); ok && user == access.ProbeUser {
					w.Header().Set(access.UserHeader, user)
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			http.Error(w, "This address asks for a password.", http.StatusUnauthorized)
			return
		}
		if uri.Path == access.CallbackPath {
			signIn(w, r, spent, key, host, uri.Query(), now)
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
			q := url.Values{"host": {host}, "to": {uri.RequestURI()}, access.NonceParam: {access.NonceHash(browserNonce(w, r))}}
			redirectTo(w, r.Header.Get(accessLoginHeader)+"?"+q.Encode())
			return
		}
		http.Error(w, "Sign in first: open https://"+host+"/ in a browser.", http.StatusUnauthorized)
	})
}

// secure reports whether the visitor reached Caddy over HTTPS.
func secure(r *http.Request) bool { return r.Header.Get("X-Forwarded-Proto") == "https" }

// nonceCookie is the name of the browser's nonce cookie for this request:
// the __Host- one over HTTPS.
func nonceCookie(r *http.Request) string {
	if secure(r) {
		return access.SecureNonceCookie
	}
	return access.NonceCookie
}

// browserNonce is the nonce of the visitor's browser on this hostname,
// for a page load on its way to Dock: the one its cookie holds, so
// several tabs signing in at once share it, or a new one. Either way the
// cookie is set again, for access.NonceTTL.
func browserNonce(w http.ResponseWriter, r *http.Request) string {
	name, nonce := nonceCookie(r), ""
	if c, err := r.Cookie(name); err == nil && access.ValidNonce(c.Value) {
		nonce = c.Value
	} else {
		nonce = access.NewNonce()
	}
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: nonce, Path: "/", MaxAge: int(access.NonceTTL.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure(r),
	})
	return nonce
}

// signIn trades a grant from Dock for a session cookie on this hostname
// and sends the visitor on to where they were going. The grant has to be
// a bound one, made for the nonce this browser holds, and not traded
// before: a sign-in link works once, in the browser that asked for it.
func signIn(w http.ResponseWriter, r *http.Request, spent *spentGrants, key []byte, host string, q url.Values, now time.Time) {
	// A browser that is signed in already (another tab traded the same
	// link a moment ago) just goes on.
	if c, err := r.Cookie(accessCookie); err == nil {
		if user, ok := access.Verify(key, c.Value, access.Session, host, now); ok && user != access.ProbeUser {
			redirectTo(w, access.LocalPath(q.Get("to")))
			return
		}
	}
	token := q.Get("grant")
	grant, ok := access.VerifyBound(key, token, host, now)
	if !ok {
		http.Error(w, "This sign-in link has expired or is not for this address. Open the address again to sign in.", http.StatusForbidden)
		return
	}
	name := nonceCookie(r)
	if c, err := r.Cookie(name); err != nil || !grant.Holds(c.Value) {
		http.Error(w, "This sign-in link was made for another browser or was already used. Open the address again to sign in.", http.StatusForbidden)
		return
	}
	switch spent.spend(token, grant.Expires, now.Unix()) {
	case grantUsed:
		http.Error(w, "This sign-in link was made for another browser or was already used. Open the address again to sign in.", http.StatusForbidden)
		return
	case grantsFull:
		http.Error(w, "Too many people are signing in to this server right now. Try again in a minute.", http.StatusServiceUnavailable)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: accessCookie, Value: access.Sign(key, access.Session, host, grant.User, now.Add(access.SessionTTL)),
		Path: "/", MaxAge: int(access.SessionTTL.Seconds()), HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: secure(r),
	})
	// The nonce has done its work; the next sign-in gets a new one.
	http.SetCookie(w, &http.Cookie{Name: name, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure(r)})
	// The grant stays out of the next page's Referer.
	w.Header().Set("Referrer-Policy", "no-referrer")
	redirectTo(w, access.LocalPath(q.Get("to")))
}

// maxSpentGrants bounds spentGrants. A full set means that many sign-ins
// on this server within one minute.
const maxSpentGrants = 4096

// What spentGrants.spend found.
const (
	grantFresh = iota
	grantUsed
	grantsFull
)

// spentGrants remembers the grants traded on this server until they
// expire, so each works once. It stays small without a timer: only a
// grant that Dock signed, that hasn't expired (one minute) and that came
// from the browser it was made for gets in, expired ones are dropped when
// a new one finds the set grown, and it never holds more than
// maxSpentGrants. A restart forgets it; a grant is still bound to its
// browser then.
type spentGrants struct {
	mu sync.Mutex
	// until maps a grant's SHA-256 to its expiry (Unix seconds).
	until map[[sha256.Size]byte]int64
	// sweepAt is the size at which the next spend drops expired entries.
	sweepAt int
}

// spend records a verified grant expiring at exp as traded at now, and
// reports whether it was fresh, already traded, or the set is full.
func (s *spentGrants) spend(token string, exp, now int64) int {
	id := sha256.Sum256([]byte(token))
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, used := s.until[id]; used {
		return grantUsed
	}
	if s.until == nil {
		s.until = map[[sha256.Size]byte]int64{}
	}
	if len(s.until) >= max(s.sweepAt, 64) {
		for k, e := range s.until {
			if e <= now {
				delete(s.until, k)
			}
		}
		// Sweep again only once it has doubled, so sweeps stay amortized.
		s.sweepAt = min(2*len(s.until), maxSpentGrants)
		if len(s.until) >= maxSpentGrants {
			return grantsFull
		}
	}
	s.until[id] = exp
	return grantFresh
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
