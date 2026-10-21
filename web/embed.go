// Package web embeds the built UI (web/dist) into the binary.
package web

import "embed"

// Dist holds the Vite build output. It only contains .gitkeep until
// `pnpm build` has run, in which case the daemon serves a placeholder page.
//
//go:embed all:dist
var Dist embed.FS
