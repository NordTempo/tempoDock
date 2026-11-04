package logarchive

import "syscall"

// diskFree reads the free (for unprivileged writers) and total bytes of
// the filesystem holding path.
func diskFree(path string) (free, total uint64, ok bool) {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return 0, 0, false
	}
	bs := uint64(st.Bsize) //nolint:gosec // block sizes are positive
	return st.Bavail * bs, st.Blocks * bs, true
}
