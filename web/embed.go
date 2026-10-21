// Package web embeds the built UI (web/dist) into the binary.
package web

// The API spec the UI's typed client is generated from (pnpm gen:api).
//go:generate go run ../cmd/platform openapi -o openapi.json

import "embed"

// Dist holds the Vite build output. It only contains .gitkeep until
// `pnpm build` has run, in which case the daemon serves a placeholder page.
//
//go:embed all:dist
var Dist embed.FS
