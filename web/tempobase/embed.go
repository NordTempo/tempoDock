// Package tbweb embeds tempoBase's built UI (web/tempobase/dist) into the
// tempobase binary (ADR 0024). Dock's binary never imports it.
package tbweb

import "embed"

// Dist holds the Vite build output of the tempoBase entry
// (web/vite.tempobase.config.ts). It only contains .gitkeep until the UI
// is built, in which case the server serves a placeholder page.
//
//go:embed all:dist
var Dist embed.FS
