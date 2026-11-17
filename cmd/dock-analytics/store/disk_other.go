//go:build !linux

package store

// diskBytes is the size of the filesystem dir is on: not told off Linux,
// where the store only runs in tests.
func diskBytes(string) int64 { return 0 }
