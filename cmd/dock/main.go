// Command dock is both the CLI and the server daemon.
package main

import (
	"os"

	"github.com/NordTempo/platform/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
