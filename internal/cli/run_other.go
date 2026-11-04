//go:build !unix

package cli

// watchResize does nothing where there is no SIGWINCH: the command's
// terminal keeps the size it started with.
func watchResize(int, func(cols, rows uint16)) (stop func()) { return func() {} }
