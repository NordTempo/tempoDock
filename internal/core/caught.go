package core

import (
	"fmt"
	"log/slog"
	"runtime/debug"
)

// caught runs fn and turns a panic in it into an error: work a background
// worker does on what a repository or a tenant sent (a pushed dock.toml, a
// source archive) fails as that piece of work, never as the daemon, which
// has no one above a worker's goroutine to recover it. The panic and its
// stack are logged once; the error's text holds no internals. what names
// the work, as in "deploying a push".
func caught[T any](log *slog.Logger, what string, fn func() (T, error)) (out T, err error) {
	defer func() {
		if p := recover(); p != nil {
			log.Error("panic in a background worker", "work", what, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			var zero T
			out, err = zero, fmt.Errorf("%s failed on a bug in Dock", what)
		}
	}()
	return fn()
}
