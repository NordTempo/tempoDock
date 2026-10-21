package stats

import (
	"syscall"
	"unsafe"

	"github.com/NordTempo/tempoDock/internal/node"
)

// atFDCWD is AT_FDCWD, a variable so it converts to uintptr at run time.
var atFDCWD = -100

// open opens the NUL-terminated path read-only without allocating
// (syscall.Open copies its string argument on every call).
func open(path []byte) (int, syscall.Errno) {
	fd, _, errno := syscall.Syscall6(syscall.SYS_OPENAT, uintptr(atFDCWD),
		uintptr(unsafe.Pointer(&path[0])), //nolint:gosec // NUL-terminated and alive for the call
		uintptr(syscall.O_RDONLY|syscall.O_CLOEXEC), 0, 0, 0)
	return int(fd), errno //nolint:gosec // file descriptors fit an int
}

// disks reports the filesystems of paths, once per filesystem. Paths that
// don't exist are skipped.
func disks(paths []string) []node.Disk {
	var out []node.Disk
	var ids []syscall.Fsid
	for _, p := range paths {
		var st syscall.Statfs_t
		if syscall.Statfs(p, &st) != nil {
			continue
		}
		bs := uint64(st.Bsize) //nolint:gosec // block sizes are positive
		if i := indexFsid(ids, st.Fsid); i >= 0 {
			out[i].Paths = append(out[i].Paths, p)
			continue
		}
		ids = append(ids, st.Fsid)
		out = append(out, node.Disk{Paths: []string{p}, Total: st.Blocks * bs, Used: (st.Blocks - st.Bfree) * bs})
	}
	return out
}

func indexFsid(ids []syscall.Fsid, id syscall.Fsid) int {
	for i, x := range ids {
		if x == id {
			return i
		}
	}
	return -1
}
