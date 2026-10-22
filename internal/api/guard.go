package api

import (
	"net/http"
	"strings"

	"github.com/NordTempo/tempoDock/internal/httpkit"
)

// guard adds security headers and the CSRF check to every response
// (httpkit.Guard). Source uploads may send their tar or gzip body.
func guard(next http.Handler) http.Handler {
	return httpkit.Guard(next, sourceUpload)
}

// sourceUpload allows a tar or gzip body on the source upload route.
func sourceUpload(r *http.Request, mt string) bool {
	return strings.HasSuffix(r.URL.Path, "/releases/source") && (mt == "application/x-tar" || mt == "application/gzip")
}
