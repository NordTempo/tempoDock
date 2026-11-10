package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"strings"

	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/update"
)

// RegistryTokenPath is the image registry's token endpoint on the internal
// listener: the realm the registry's auth challenges name (ADR 0014
// section 4).
const RegistryTokenPath = "/internal/registry/token"

// WakePath is where Caddy asks the wake check of an asleep environment's
// routes (node.Route.Wake) on the internal listener.
const WakePath = "/internal/caddy/wake"

// WakeHandler is the wake check: Caddy asks it before proxying a request
// to an asleep environment, with the route's key in X-Dock-Wake and the
// request's method and URI. It holds the request while the environment
// wakes (core.WakeForRequest), then lets it on (204), or answers a page
// load with the waking page, which reloads itself, and anything else
// with a 503 to retry. Neither names more than the environment's key.
func WakeHandler(c *core.Core) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := pageLoad(r)
		ans := c.WakeForRequest(r.Context(), r.Header.Get("X-Dock-Wake"), r.Header.Get("X-Forwarded-Method"), r.Header.Get("X-Forwarded-Uri"), r.UserAgent(), page)
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		switch {
		case ans == core.WakeGo:
			w.WriteHeader(http.StatusNoContent)
		case ans == core.WakeWaking && page:
			h.Set("Content-Type", "text/html; charset=utf-8")
			h.Set("Refresh", "2")
			h.Set("Retry-After", "2")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, wakingPage)
		case ans == core.WakeWaking:
			h.Set("Retry-After", "5")
			http.Error(w, "Waking up; try again in a few seconds.", http.StatusServiceUnavailable)
		default:
			h.Set("Retry-After", "60")
			http.Error(w, "Asleep.", http.StatusServiceUnavailable)
		}
	})
}

// pageLoad reports whether a request the wake check holds is a browser
// loading a page (it gets the waking page) rather than a program.
func pageLoad(r *http.Request) bool {
	if m := r.Header.Get("Sec-Fetch-Mode"); m != "" {
		return m == "navigate"
	}
	return r.Header.Get("X-Forwarded-Method") == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html")
}

// wakingPage is what a page load of an asleep environment sees while it
// wakes: the look of Caddy's stopped and 404 pages, nothing that moves.
// The Refresh header reloads it.
const wakingPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Waking up</title>
<style>
html,body{margin:0;height:100%;background:#000;color:#fff;font:14px/1.5 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
body{display:flex;align-items:center;justify-content:center}
p{margin:0}
.m{color:#a1a1aa}
</style>
</head>
<body>
<div><p>503. Waking up.</p><p class="m">This page reloads when it is ready.</p></div>
</body>
</html>
`

// Internal returns the handler for loopback-only endpoints, served on
// config.InternalListen and never on the API listener:
//
//	GET /internal/caddy/ask?domain=x  Caddy's on-demand TLS check: 200 if the
//	                                  hostname belongs to a service, else 404.
//	GET /internal/caddy/access        The access check for login-protected
//	                                  routes (caddy.AccessHandler, ADR 0030).
//	GET /internal/caddy/wake          The wake check of an asleep
//	                                  environment's routes (WakeHandler).
//	GET|POST /internal/registry/token The registry's token endpoint
//	                                  (registry.Signer.Handler), when
//	                                  registryToken is not nil.
//	POST /internal/update             The updater wrote its result (ADR 0028):
//	                                  read and audit it.
//	GET /debug/pprof/...              Go runtime profiles (net/http/pprof), e.g.
//	                                  curl 127.0.0.1:7071/debug/pprof/heap.
func Internal(c *core.Core, log *slog.Logger, registryToken, accessCheck http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/caddy/ask", func(w http.ResponseWriter, r *http.Request) {
		ok, err := c.AllowCertificate(r.Context(), r.URL.Query().Get("domain"))
		switch {
		case err != nil:
			log.Error("caddy ask", "err", err)
			http.Error(w, "error", http.StatusInternalServerError)
		case ok:
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	})
	mux.Handle("GET /internal/caddy/access", accessCheck)
	mux.Handle("GET "+WakePath, WakeHandler(c))
	if registryToken != nil {
		mux.Handle(RegistryTokenPath, registryToken)
	}
	// Anyone on the machine may call it; it only makes the daemon read a
	// file that only dock and root can write.
	mux.HandleFunc("POST "+update.NotifyPath, func(w http.ResponseWriter, r *http.Request) {
		c.UpdateFinished(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /debug/pprof/", pprof.Index) // also serves heap, goroutine, allocs, ...
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	return mux
}
