package api

import (
	"io/fs"
	"net/http"

	"github.com/NordTempo/tempoDock/internal/httpkit"
	"github.com/NordTempo/tempoDock/web"
)

// uiHandler serves Dock's embedded SPA (httpkit.UIHandler).
func uiHandler() http.Handler {
	dist, _ := fs.Sub(web.Dist, "dist")
	return httpkit.UIHandler(dist, placeholderPage)
}

const placeholderPage = `<!doctype html><meta charset="utf-8"><title>Tempo Dock</title>
<body style="background:#000;color:#fff;font:14px ui-monospace,monospace;padding:2rem">
Tempo Dock is running. The web UI has not been built into this binary.`
