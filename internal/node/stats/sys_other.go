//go:build !linux

package stats

import (
	"errors"
	"syscall"

	"github.com/NordTempo/tempoDock/internal/node"
)

// open opens the NUL-terminated path read-only. Only Linux servers run
// apps; elsewhere this just keeps the package building.
func open(path []byte) (int, syscall.Errno) {
	fd, err := syscall.Open(string(path[:len(path)-1]), syscall.O_RDONLY, 0)
	if errno := syscall.Errno(0); errors.As(err, &errno) {
		return fd, errno
	}
	return fd, 0
}

// disks isn't reported outside Linux.
func disks([]string) []node.Disk { return nil }

// eachDir isn't implemented outside Linux, which has no cgroup v2 tree.
func (r *Reader) eachDir(string, func([]byte)) error { return errors.ErrUnsupported }
