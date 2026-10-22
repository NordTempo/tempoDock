package tbhttp

import (
	"encoding/json"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	tbweb "github.com/NordTempo/tempoDock/web/tempobase"
)

// Pending ADR 0024 A.2: a copy of Dock's guard (internal/api/guard.go)
// and a plain UI handler until httpkit has Guard and UIHandler; this file
// is then deleted and New uses httpkit's.

// uiCSP is the web UI's Content-Security-Policy.
const uiCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
	"font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

// guard adds security headers to every response and refuses cross-site
// requests that could ride on the session cookie (CSRF).
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		} else {
			h.Set("Content-Security-Policy", uiCSP)
		}
		if err := checkBrowserRequest(r); err != nil {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(apitypes.Error{
				Status: http.StatusForbidden, Title: http.StatusText(http.StatusForbidden), Detail: err.Error(), Code: apitypes.CodeForbidden,
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// checkBrowserRequest guards state-changing requests without a bearer
// token: Sec-Fetch-Site (or Origin, or Referer) must be same-origin, and
// the body JSON. Bearer requests are exempt: a cross-site page can't set
// Authorization without a CORS preflight, which is never allowed.
func checkBrowserRequest(r *http.Request) error {
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
	if err == nil && (mt == "application/json" || strings.HasPrefix(mt, "application/") && strings.HasSuffix(mt, "+json")) {
		return nil
	}
	return errors.New("unsupported Content-Type " + ct + "; send application/json")
}

// uiHandler serves the embedded UI (web/tempobase/dist): real files when
// they exist, index.html for client-side routes, and a placeholder when
// the UI hasn't been built. httpkit.UIHandler adds compression and cache
// headers.
func uiHandler() http.Handler {
	dist, _ := fs.Sub(tbweb.Dist, "dist")
	files := http.FileServerFS(dist)
	if _, err := fs.Stat(dist, "index.html"); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(placeholderPage))
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if st, err := fs.Stat(dist, name); name == "" || err != nil || st.IsDir() {
			http.ServeFileFS(w, r, dist, "index.html") // a client-side route
			return
		}
		files.ServeHTTP(w, r)
	})
}

const placeholderPage = `<!doctype html><meta charset="utf-8"><title>tempoBase</title>
<body style="background:#000;color:#fff;font:14px ui-monospace,monospace;padding:2rem">
tempoBase is running. The web UI has not been built into this binary.`
