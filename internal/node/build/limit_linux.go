package build

import "syscall"

// limitMemory caps this process's data segment, which on Linux counts every
// private writable mapping: the Go heap, and anything its children (mise)
// allocate, since they inherit the limit. Allocating past it fails, which
// crashes a Go program with "out of memory".
func limitMemory(n uint64) {
	// If the hard limit is already lower, setting fails and the lower one
	// stays in effect.
	_ = syscall.Setrlimit(syscall.RLIMIT_DATA, &syscall.Rlimit{Cur: n, Max: n})
}
