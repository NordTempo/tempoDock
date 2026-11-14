package api

import (
	"log/slog"
	"net/http"
	"net/http/pprof"

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

// Internal returns the handler for loopback-only endpoints, served on
// config.InternalListen and never on the API listener:
//
//	GET /internal/caddy/ask?domain=x  Caddy's on-demand TLS check: 200 if the
//	                                  hostname belongs to a service, else 404.
//	GET /internal/caddy/access        The access check for login-protected
//	                                  routes (caddy.AccessHandler, ADR 0030).
//	GET /internal/caddy/wake          The wake check of an asleep
//	                                  environment's routes
//	                                  (caddy.WakeHandler, ADR 0070).
//	GET|POST /internal/registry/token The registry's token endpoint
//	                                  (registry.Signer.Handler), when
//	                                  registryToken is not nil.
//	POST /internal/update             The updater wrote its result (ADR 0028):
//	                                  read and audit it.
//	GET /debug/pprof/...              Go runtime profiles (net/http/pprof), e.g.
//	                                  curl 127.0.0.1:7071/debug/pprof/heap,
//	                                  only with pprof = true in the config.
func Internal(c *core.Core, log *slog.Logger, registryToken, accessCheck, wakeCheck http.Handler, pprofOn bool) http.Handler {
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
	mux.Handle("GET "+WakePath, wakeCheck)
	if registryToken != nil {
		mux.Handle(RegistryTokenPath, registryToken)
	}
	// Anyone on the machine may call it; it only makes the daemon read a
	// file that only dock and root can write.
	mux.HandleFunc("POST "+update.NotifyPath, func(w http.ResponseWriter, r *http.Request) {
		c.UpdateFinished(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
	if pprofOn {
		mux.HandleFunc("GET /debug/pprof/", pprof.Index) // also serves heap, goroutine, allocs, ...
		mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	}
	return mux
}
