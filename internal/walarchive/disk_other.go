//go:build !linux

package walarchive

// diskSpace is unknown off Linux, where the archiver never runs.
func diskSpace(string) (size, free int64) { return 0, 0 }
