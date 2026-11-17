package walarchive

import "syscall"

// diskSpace is the size and free space of the file system holding path;
// zeros when it can't be read.
func diskSpace(path string) (size, free int64) {
	var fs syscall.Statfs_t
	if syscall.Statfs(path, &fs) != nil {
		return 0, 0
	}
	return int64(fs.Blocks) * fs.Bsize, int64(fs.Bavail) * fs.Bsize
}
