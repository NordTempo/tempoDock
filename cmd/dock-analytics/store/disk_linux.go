package store

import "syscall"

// diskBytes is the size of the filesystem dir is on, 0 if it can't be
// told.
func diskBytes(dir string) int64 {
	var st syscall.Statfs_t
	if syscall.Statfs(dir, &st) != nil {
		return 0
	}
	return int64(st.Blocks) * st.Bsize
}
