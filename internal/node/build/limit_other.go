//go:build !linux

package build

// limitMemory is a no-op outside Linux, where only builder servers run
// builds; the soft limit and the timeout still apply.
func limitMemory(uint64) {}
