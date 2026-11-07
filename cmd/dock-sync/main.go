//go:build linux

// Command dock-sync copies a volume to another server in passes, while
// the service using it may still run (ADR 0062). dock embeds it
// (internal/node/probe), writes it to its data dir and bind-mounts it
// read-only into the two job containers of a pass:
//
//	dock-sync recv ROOT                     on the target
//	dock-sync send [--live|--check] ROOT    on the source
//	dock-sync version
//
// The control plane joins them: recv's standard output into send's
// standard input, and send's standard output into recv's. Both print one
// report line on standard error (volsync.Report). --check compares only
// and exits 3 when the trees differ, naming the first paths.
package main

import (
	"fmt"
	"os"

	"github.com/NordTempo/tempoDock/internal/volsync"
)

// version is what `dock-sync version` prints; dock checks the binary can
// run on the host with it before using it.
const version = "dock-sync 1"

func main() {
	os.Exit(run(os.Args[1:]))
}

// run runs one command and returns the exit status.
func run(args []string) int {
	if len(args) == 1 && args[0] == "version" {
		fmt.Println(version)
		return 0
	}
	if len(args) == 2 && args[0] == "recv" {
		rep, err := volsync.Recv(args[1], os.Stdin, os.Stdout)
		fmt.Fprintln(os.Stderr, rep)
		if err != nil {
			fmt.Fprintln(os.Stderr, "dock-sync: "+err.Error())
			return 1
		}
		return 0
	}
	if len(args) >= 2 && args[0] == "send" {
		mode := volsync.Final
		switch {
		case len(args) == 3 && args[1] == "--live":
			mode = volsync.Live
		case len(args) == 3 && args[1] == "--check":
			mode = volsync.Check
		case len(args) != 2:
			return usage()
		}
		rep, err := volsync.Send(args[len(args)-1], os.Stdin, os.Stdout, mode)
		fmt.Fprintln(os.Stderr, rep)
		if err != nil {
			fmt.Fprintln(os.Stderr, "dock-sync: "+err.Error())
			return 1
		}
		if mode == volsync.Check && rep.Changed > 0 {
			for _, p := range rep.Differences {
				fmt.Fprintln(os.Stderr, "dock-sync: differs: "+p)
			}
			return 3
		}
		return 0
	}
	return usage()
}

func usage() int {
	fmt.Fprintln(os.Stderr, "usage: dock-sync recv ROOT | send [--live|--check] ROOT | version")
	return 2
}
