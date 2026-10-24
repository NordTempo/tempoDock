package stats

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io/fs"
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

// eachDir makes dir the current directory and calls fn with the name of
// each directory in it. A name is valid during its call only.
func (r *Reader) eachDir(dir string, fn func(name []byte)) error {
	r.setDir(dir)
	r.path = append(r.path[:r.dirLen], 0)
	fd, errno := open(r.path)
	if errno != 0 {
		return r.pathError("open", errno)
	}
	defer func() { _ = syscall.Close(fd) }()
	for {
		n, err := syscall.ReadDirent(fd, r.dirBuf)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return &fs.PathError{Op: "readdirent", Path: dir, Err: err}
		}
		if n <= 0 {
			return nil
		}
		// linux_dirent64: ino (8), off (8), reclen (2), type (1), name.
		for b := r.dirBuf[:n]; len(b) > 19; {
			reclen := int(binary.NativeEndian.Uint16(b[16:18]))
			if reclen <= 19 || reclen > len(b) {
				break
			}
			typ, name := b[18], b[19:reclen]
			if i := bytes.IndexByte(name, 0); i >= 0 {
				name = name[:i]
			}
			b = b[reclen:]
			if (typ == syscall.DT_DIR || typ == syscall.DT_UNKNOWN) && string(name) != "." && string(name) != ".." {
				fn(name)
			}
		}
	}
}
