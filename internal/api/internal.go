package api

import (
	"log/slog"
	"net/http"
	"net/http/pprof"

	"github.com/NordTempo/tempoDock/internal/core"
)

// Internal returns the handler for loopback-only endpoints, served on
// config.InternalListen and never on the API listener:
//
//	GET /internal/caddy/ask?domain=x  Caddy's on-demand TLS check: 200 if the
//	                                  hostname belongs to a service, else 404.
//	GET /debug/pprof/...              Go runtime profiles (net/http/pprof), e.g.
//	                                  curl 127.0.0.1:7071/debug/pprof/heap.
func Internal(c *core.Core, log *slog.Logger) http.Handler {
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
	mux.HandleFunc("GET /debug/pprof/", pprof.Index) // also serves heap, goroutine, allocs, ...
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	return mux
}
