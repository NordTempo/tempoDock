package main

import (
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// withoutTHP starts the daemon and the node agent over with transparent
// huge pages off for the whole process. On a THP=always kernel their
// small, sparse memory otherwise sits partly on 2 MB pages, which adds up
// to several MB of RSS that come and go with the kernel's free memory
// (docs/performance.md). The flag only covers memory touched after it is
// set, and package init has touched plenty by the time main runs, so it
// is set and the binary executed again: same PID, arguments and
// environment, and every page small from the first. If exec fails it runs
// on with the flag set. Children inherit the flag; the host processes the
// daemon starts are all short-lived (the build plan child, dock-probe's
// check, iptables), and Caddy, cloudflared and the stores run under
// systemd or Docker, which don't inherit it.
func withoutTHP() {
	if !longRunning(os.Args) {
		return
	}
	if off, err := unix.PrctlRetInt(unix.PR_GET_THP_DISABLE, 0, 0, 0, 0); err != nil || off == 1 {
		return // an old kernel, or already the second start
	}
	if err := unix.Prctl(unix.PR_SET_THP_DISABLE, 1, 0, 0, 0); err != nil {
		return
	}
	// By its path, not /proc/self/exe, so the process keeps its name
	// (ps, top and journald's identifier say dock, not exe).
	exe, err := os.Executable()
	if err != nil {
		return
	}
	_ = unix.Exec(exe, os.Args, os.Environ())
}

// longRunning reports whether args run the daemon or a node: `dock serve`
// and `dock node` without a subcommand, as their systemd units do.
func longRunning(args []string) bool {
	if len(args) < 2 {
		return false
	}
	switch args[1] {
	case "serve":
		return true
	case "node":
		return len(args) == 2 || strings.HasPrefix(args[2], "-")
	}
	return false
}
