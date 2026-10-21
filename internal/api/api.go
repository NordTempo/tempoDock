// Package api is the daemon's HTTP API (Huma, OpenAPI 3.1) under /api/v1,
// plus the embedded web UI. Handlers are thin: they authenticate, call
// internal/core and map results to JSON.
package api

import (
	"context"
	"io/fs"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/NordTempo/platform/internal/version"
	"github.com/NordTempo/platform/web"
)

// New returns the daemon's root handler.
func New() http.Handler {
	mux := http.NewServeMux()
	cfg := huma.DefaultConfig("platform", version.Version)
	cfg.OpenAPIPath = "/api/v1/openapi"
	cfg.DocsPath = "/api/v1/docs"
	cfg.SchemasPath = "/api/v1/schemas"
	a := humago.New(mux, cfg)

	registerHealth(a)
	mux.Handle("/", uiHandler())
	return mux
}

// HealthOutput reports that the daemon is up and which version it runs.
type HealthOutput struct {
	Body struct {
		Status  string `json:"status" example:"ok"`
		Version string `json:"version" example:"v0.1.0"`
	}
}

func registerHealth(a huma.API) {
	huma.Register(a, huma.Operation{
		OperationID: "health",
		Method:      http.MethodGet,
		Path:        "/api/v1/health",
		Summary:     "Daemon health and version",
		Tags:        []string{"system"},
	}, func(context.Context, *struct{}) (*HealthOutput, error) {
		out := &HealthOutput{}
		out.Body.Status = "ok"
		out.Body.Version = version.Version
		return out, nil
	})
}

// uiHandler serves the embedded SPA: real files when they exist, index.html
// for client-side routes, and a placeholder when the UI hasn't been built.
func uiHandler() http.Handler {
	dist, _ := fs.Sub(web.Dist, "dist")
	files := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		index, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(placeholderPage))
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path != "" {
			if _, err := fs.Stat(dist, path); err == nil {
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
}

const placeholderPage = `<!doctype html><meta charset="utf-8"><title>platform</title>
<body style="background:#000;color:#fff;font:14px ui-monospace,monospace;padding:2rem">
platform daemon is running. The web UI has not been built into this binary.`
