//go:build !unix

package build

import "os"

// ownedByUs is always true where files have no Unix owner; builds only
// run on Unix servers.
func ownedByUs(os.FileInfo) bool { return true }
