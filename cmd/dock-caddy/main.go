// Command dock-caddy is the Caddy Dock installs on its servers (ADR 0106):
// Caddy's standard build, as `caddy` from its releases, plus Dock's own
// handlers. It is its own module so Caddy's dependencies stay out of the
// main module's go.mod; the release ships it as dock-caddy-linux-<arch>
// and install.sh puts it at /usr/local/bin/caddy.
package main

import (
	_ "time/tzdata"

	caddycmd "github.com/caddyserver/caddy/v2/cmd"
	_ "github.com/caddyserver/caddy/v2/modules/standard"

	_ "github.com/NordTempo/tempoDock/cmd/dock-caddy/inject"
)

func main() {
	caddycmd.Main()
}
