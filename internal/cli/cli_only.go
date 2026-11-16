//go:build dockcli

package cli

import "github.com/spf13/cobra"

// The CLI-only build (`-tags dockcli`, make build-cli) has no server
// commands: it is a quarter of the size and starts faster. See
// server_cmds.go for the full binary.

// cliOnly is true in this build: `dock self-update` may replace it with a
// release's dock-cli.
const cliOnly = true

// serverCommands are the "Run a server" commands: none in this build.
func serverCommands() []*cobra.Command { return nil }

// helperMain runs daemon subprocesses: none in this build.
func helperMain([]string) (int, bool) { return 0, false }
