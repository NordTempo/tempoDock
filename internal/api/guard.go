package api

import (
	"net/http"
	"strings"

	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/httpkit"
)

// guard adds security headers and the CSRF check to every response
// (httpkit.Guard). Source uploads may send their tar or gzip body, and
// the GitHub install's confirm page its form.
func guard(next http.Handler) http.Handler {
	return httpkit.Guard(next, rawBody, overHTTPS)
}

// rawBody allows a tar or gzip body on the source upload route and a
// form on the GitHub install's confirm route. Both still pass the
// same-origin check.
func rawBody(r *http.Request, mt string) bool {
	switch mt {
	case "application/x-tar", "application/gzip":
		return strings.HasSuffix(r.URL.Path, "/releases/source")
	case "application/x-www-form-urlencoded":
		return r.URL.Path == core.GitHubInstallConfirmPath
	}
	return false
}
