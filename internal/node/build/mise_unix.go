//go:build unix

package build

import (
	"os"
	"syscall"
)

// ownedByUs reports whether a file belongs to the user this process runs
// as.
func ownedByUs(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}
