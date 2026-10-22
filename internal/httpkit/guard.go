package httpkit

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// UICSP is the web UI's Content-Security-Policy. The Vite builds have no
// inline scripts; inline styles are allowed for React style attributes.
// An API docs page (Huma's) sets its own policy, since it loads a CDN
// script.
const UICSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
	"font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

// Guard adds security headers to every response and refuses cross-site
// requests that could ride on a session cookie (CSRF; see
// CheckBrowserRequest). rawBody, when set, allows non-JSON bodies on some
// routes (Dock's source uploads).
func Guard(next http.Handler, rawBody RawBody) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			// Responses carry tokens and secret values; never cache them.
			h.Set("Cache-Control", "no-store")
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		} else {
			h.Set("Content-Security-Policy", UICSP)
		}
		if err := CheckBrowserRequest(r, rawBody); err != nil {
			writeForbidden(w, err.Error())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RawBody reports whether a request may send a body of media type mt
// other than JSON.
type RawBody func(r *http.Request, mt string) bool

// CheckBrowserRequest guards state-changing requests that don't carry a
// bearer token, i.e. those a browser authenticates with the session cookie
// (and login, which sets it). SameSite alone isn't enough: it ignores
// ports, so any page on the server's host is same-site with the dashboard.
//
//   - Sec-Fetch-Site, when sent (all current browsers), must be
//     same-origin. Otherwise Origin (or Referer) must match the Host.
//   - The body must be JSON, or what rawBody allows. HTML forms can only
//     send form or text bodies.
//
// Bearer requests are exempt: a cross-site page can't set Authorization
// without a CORS preflight, which the servers never allow.
func CheckBrowserRequest(r *http.Request, rawBody RawBody) error {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return nil
	}
	if r.Header.Get("Authorization") != "" {
		return nil
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		if site != "same-origin" {
			return errors.New("cross-site request refused (Sec-Fetch-Site: " + site + ")")
		}
	} else {
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = r.Header.Get("Referer")
		}
		if origin != "" {
			if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
				return errors.New("cross-origin request refused (origin " + origin + ")")
			}
		}
	}
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		if r.ContentLength != 0 {
			return errors.New("request body needs a Content-Type of application/json")
		}
		return nil
	}
	mt, _, err := mime.ParseMediaType(ct)
	switch {
	case err != nil:
	case mt == "application/json" || strings.HasPrefix(mt, "application/") && strings.HasSuffix(mt, "+json"):
		return nil
	case rawBody != nil && rawBody(r, mt):
		return nil
	}
	return errors.New("unsupported Content-Type " + ct + "; send application/json")
}

// writeForbidden answers 403 with a problem body.
func writeForbidden(w http.ResponseWriter, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(apitypes.Error{
		Status: http.StatusForbidden, Title: http.StatusText(http.StatusForbidden), Detail: detail, Code: apitypes.CodeForbidden,
	})
}
