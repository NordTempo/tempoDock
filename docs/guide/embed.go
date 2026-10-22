// Package guide embeds the user guide (the Markdown pages in this
// directory) so the daemon's MCP endpoint can serve them to agents as
// dock://docs/<page> resources (ADR 0017). The pages are embedded as they
// are, so there is no copy to keep in sync.
package guide

import "embed"

// Pages holds every guide page (*.md) at its file name.
//
//go:embed *.md
var Pages embed.FS
