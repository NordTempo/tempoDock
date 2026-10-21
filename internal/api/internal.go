package api

import (
	"log/slog"
	"net/http"

	"github.com/NordTempo/tempoDock/internal/core"
)

// Internal returns the handler for loopback-only endpoints, served on
// config.InternalListen and never on the API listener:
//
//	GET /internal/caddy/ask?domain=x  Caddy's on-demand TLS check: 200 if the
//	                                  hostname belongs to a service, else 404.
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
	return mux
}
