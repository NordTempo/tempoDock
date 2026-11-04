//go:build unix

package cli

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

// watchResize calls send with the terminal fd's size every time it
// changes (SIGWINCH) until the returned stop is called. `dock run -t`
// passes resizes on to the command's terminal this way.
func watchResize(fd int, send func(cols, rows uint16)) (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ch:
				if cols, rows, err := term.GetSize(fd); err == nil {
					send(termDim(cols), termDim(rows))
				}
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}

// termDim clamps a terminal dimension to a frame's uint16.
func termDim(n int) uint16 { return uint16(min(max(n, 0), 1<<16-1)) }
