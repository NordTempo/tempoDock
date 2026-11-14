// Command dock is both the CLI and the server daemon.
package main

import (
	"os"

	"github.com/NordTempo/tempoDock/internal/cli"
)

func main() {
	withoutTHP()
	os.Exit(cli.Execute())
}
