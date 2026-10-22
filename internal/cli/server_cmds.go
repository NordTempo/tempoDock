//go:build !dockcli

package cli

import (
	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/node/build"
)

// The full binary is the CLI, the daemon and the node runtime. Building with
// `-tags dockcli` (make build-cli) leaves out everything below, and with it
// the daemon's dependencies (Docker, BuildKit, Caddy, SQLite, Huma): see
// cli_only.go.

// serverCommands are the "Run a server" commands.
func serverCommands() []*cobra.Command {
	return []*cobra.Command{newServeCmd(), newAdminCmd(), newOpenAPICmd()}
}

// helperMain runs a subprocess the daemon starts from its own binary, and
// reports whether args named one: the daemon re-runs itself to plan
// Railpack builds in a capped process.
func helperMain(args []string) (int, bool) {
	if len(args) > 1 && args[1] == build.PlanCommand {
		return build.PlanMain(), true
	}
	return 0, false
}
