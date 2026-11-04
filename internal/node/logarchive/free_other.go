//go:build !linux

package logarchive

// diskFree is unknown off Linux, where servers don't run: no floor.
func diskFree(string) (free, total uint64, ok bool) { return 0, 0, false }
