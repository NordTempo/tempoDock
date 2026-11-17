//go:build !dockcli

package cli

import (
	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/node/build"
	"github.com/NordTempo/tempoDock/internal/walarchive"
)

// The full binary is the CLI, the daemon and the node runtime. Building with
// `-tags dockcli` (make build-cli) leaves out everything below, and with it
// the daemon's dependencies (Docker, BuildKit, Caddy, SQLite, Huma): see
// cli_only.go.

// cliOnly is false in this build: `dock self-update` refuses to swap the
// daemon for a dock-cli, which can't serve.
const cliOnly = false

// serverCommands are the "Run a server" commands.
func serverCommands() []*cobra.Command {
	return []*cobra.Command{newServeCmd(), newNodeCmd(), newAdminCmd(), newOpenAPICmd()}
}

// helperMain runs a subprocess the daemon starts from its own binary, and
// reports whether args named one: the daemon re-runs itself to plan
// Railpack builds in a capped process, and runs itself in a database's
// WAL archiver and its base backup and restore jobs (ADR 0093).
func helperMain(args []string) (int, bool) {
	if len(args) > 1 && args[1] == build.PlanCommand {
		return build.PlanMain(), true
	}
	if len(args) > 1 && args[1] == walarchive.Command {
		return walarchive.Main(args[2:]), true
	}
	return 0, false
}
