// Package tbdocs embeds tempoBase's user guide (the Markdown pages in
// this directory) so its MCP endpoint can serve them to agents as
// tempobase://docs/<page> resources (ADR 0024), like Dock's docs/guide.
package tbdocs

import "embed"

// Pages holds every guide page (*.md) at its file name.
//
//go:embed *.md
var Pages embed.FS
